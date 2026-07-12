package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/activity"
	"github.com/tomnagengast/network/apps/factory/internal/agentrun"
	"github.com/tomnagengast/network/apps/factory/internal/eventwire"
	"github.com/tomnagengast/network/apps/factory/internal/githubhook"
	"github.com/tomnagengast/network/apps/factory/internal/linearhook"
	"github.com/tomnagengast/network/apps/factory/internal/server"
	"github.com/tomnagengast/network/apps/factory/internal/viewerauth"
)

const (
	defaultPort              = "8092"
	activityEventLimit       = 250
	agentRunLimit            = 100
	githubEventLimit         = 1000
	linearCommentEventLimit  = 500
	systemEventLimit         = 10_000
	defaultMaxConcurrentRuns = 3
	defaultRepoURL           = "git@github.com:tomnagengast/network.git"
	defaultTmuxSocket        = "factory-agents"
	serviceHeartbeatInterval = 30 * time.Second
	googleRedirectURL        = "https://factory.nags.cloud/auth/google/callback"
	viewerUsername           = "factory"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if code, handled := runAgentCommand(ctx, os.Args[1:]); handled {
		os.Exit(code)
	}
	if err := serve(ctx); err != nil {
		slog.Error("factory stopped", "error", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context) error {
	port := envOr("PORT", defaultPort)
	web := os.DirFS("frontend/dist")
	if _, err := fs.Stat(web, "index.html"); err != nil {
		return fmt.Errorf("frontend is not built (run bun run build in frontend): %w", err)
	}
	secret := os.Getenv("LINEAR_WEBHOOK_SECRET")
	if secret == "" {
		return errors.New("LINEAR_WEBHOOK_SECRET is required")
	}
	githubSecret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	if githubSecret == "" {
		return errors.New("GITHUB_WEBHOOK_SECRET is required")
	}
	linearAPIKey := os.Getenv("LINEAR_API_KEY")
	if linearAPIKey == "" {
		return errors.New("LINEAR_API_KEY is required for agent runs")
	}
	triggerActorID := os.Getenv("LINEAR_TRIGGER_ACTOR_ID")
	if triggerActorID == "" {
		return errors.New("LINEAR_TRIGGER_ACTOR_ID is required for agent runs")
	}
	viewerPassword := os.Getenv("FACTORY_VIEWER_PASSWORD")
	if viewerPassword == "" {
		return errors.New("FACTORY_VIEWER_PASSWORD is required for agent inspection")
	}
	googleClientID := os.Getenv("FACTORY_GOOGLE_CLIENT_ID")
	googleClientSecret := os.Getenv("FACTORY_GOOGLE_CLIENT_SECRET")
	allowedEmails := splitList(os.Getenv("FACTORY_GOOGLE_ALLOWED_EMAILS"))
	sessionKey := os.Getenv("FACTORY_SESSION_KEY")
	viewerAuth, err := viewerauth.New(viewerauth.Config{
		ClientID:      googleClientID,
		ClientSecret:  googleClientSecret,
		RedirectURL:   googleRedirectURL,
		AllowedEmails: allowedEmails,
		SessionKey:    []byte(sessionKey),
		BasicUsername: viewerUsername,
		BasicPassword: viewerPassword,
		Now:           time.Now,
	})
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	stateRoot := filepath.Join(home, ".local", "share", "factory")
	dataRoot := filepath.Join(stateRoot, "data")
	activityStore, err := activity.Open(filepath.Join(dataRoot, "linear-activity.json"), activityEventLimit)
	if err != nil {
		return err
	}
	runStore, err := agentrun.Open(filepath.Join(dataRoot, "agent-runs.json"), agentRunLimit)
	if err != nil {
		return err
	}
	githubEvents, err := githubhook.Open(filepath.Join(dataRoot, "github-events.json"), githubEventLimit)
	if err != nil {
		return err
	}
	linearComments, err := linearhook.Open(filepath.Join(dataRoot, "linear-comments.json"), linearCommentEventLimit)
	if err != nil {
		return err
	}
	eventJournal, err := eventwire.Open(filepath.Join(dataRoot, "system-events.jsonl"), systemEventLimit, map[string]uint64{
		githubhook.WireChannel: githubEvents.Total(),
		linearhook.WireChannel: linearComments.Total(),
	})
	if err != nil {
		return err
	}
	events, err := eventwire.New(eventJournal)
	if err != nil {
		return err
	}
	collector, err := agentrun.NewCollector(
		runStore,
		events,
		stateRoot,
		filepath.Join(dataRoot, "agent-event-offsets.json"),
	)
	if err != nil {
		return err
	}
	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Factory binary: %w", err)
	}
	tmuxPath := requiredCommand("tmux")
	tmuxSocket := envOr("FACTORY_TMUX_SOCKET", defaultTmuxSocket)
	launcher, err := agentrun.NewTmuxLauncher(agentrun.LauncherConfig{
		RepoURL:       envOr("FACTORY_REPO_URL", defaultRepoURL),
		RepoPath:      envOr("FACTORY_REPO_PATH", filepath.Join(stateRoot, "workspace", "network")),
		StateRoot:     stateRoot,
		BinaryPath:    binaryPath,
		GitPath:       requiredCommand("git"),
		WorktrunkPath: requiredCommand("wt"),
		TmuxPath:      tmuxPath,
		TmuxSocket:    tmuxSocket,
	})
	if err != nil {
		return err
	}
	manager, err := agentrun.NewManager(
		runStore,
		launcher,
		collector,
		stateRoot,
		envInt("FACTORY_MAX_AGENTS", defaultMaxConcurrentRuns),
		2*time.Second,
		slog.Default(),
		time.Now,
	)
	if err != nil {
		return err
	}
	observer, err := agentrun.NewObserver(
		runStore,
		tmuxPath,
		tmuxSocket,
		[]string{
			linearAPIKey,
			githubSecret,
			viewerPassword,
			googleClientSecret,
			sessionKey,
			os.Getenv("GITHUB_TOKEN"),
		},
		time.Now,
	)
	if err != nil {
		return err
	}

	handler, err := server.New(server.Config{
		Web:            web,
		ActivityStore:  activityStore,
		RunStore:       runStore,
		RunNotifier:    manager,
		AgentObserver:  observer,
		ViewerAuth:     viewerAuth,
		LinearSecret:   []byte(secret),
		GitHubSecret:   []byte(githubSecret),
		Events:         events,
		GitHubEvents:   githubEvents,
		LinearComments: linearComments,
		TriggerActor:   triggerActorID,
		Now:            time.Now,
	})
	if err != nil {
		return err
	}
	if err := events.CatchUp(ctx); err != nil {
		return fmt.Errorf("catch up Factory events: %w", err)
	}
	go manager.Run(ctx)
	serviceStartedAt := time.Now().UTC()
	if err := publishServiceEvent(ctx, events, "started", serviceStartedAt, serviceStartedAt); err != nil {
		return err
	}
	go publishServiceHeartbeats(ctx, events, serviceStartedAt, serviceHeartbeatInterval, time.Now)
	httpServer := &http.Server{
		Addr:              "127.0.0.1:" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("factory listening", "address", httpServer.Addr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := publishServiceEvent(shutdownCtx, events, "stopping", serviceStartedAt, time.Now().UTC()); err != nil {
			slog.Error("publish Factory stopping event", "error", err)
		}
		return httpServer.Shutdown(shutdownCtx)
	}
}

func publishServiceHeartbeats(
	ctx context.Context,
	publisher agentrun.EventPublisher,
	startedAt time.Time,
	interval time.Duration,
	now func() time.Time,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := publishServiceEvent(ctx, publisher, "heartbeat", startedAt, now().UTC()); err != nil && ctx.Err() == nil {
				slog.Error("publish Factory heartbeat", "error", err)
			}
		}
	}
}

func publishServiceEvent(
	ctx context.Context,
	publisher agentrun.EventPublisher,
	action string,
	startedAt time.Time,
	at time.Time,
) error {
	instance := strconv.Itoa(os.Getpid()) + ":" + strconv.FormatInt(startedAt.UnixNano(), 10)
	event := eventwire.Event{
		ID:         "factory:service:" + instance + ":" + action + ":" + strconv.FormatInt(at.UnixNano(), 10),
		Source:     eventwire.SourceFactory,
		Type:       "service",
		Action:     action,
		Subject:    "factory",
		Attributes: map[string][]string{"pid": {strconv.Itoa(os.Getpid())}, "startedAt": {startedAt.Format(time.RFC3339Nano)}, "status": {action}},
		ReceivedAt: at,
	}
	if _, err := publisher.PublishBatch(ctx, []eventwire.Event{event}); err != nil {
		return fmt.Errorf("publish Factory service %s: %w", action, err)
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func splitList(value string) []string {
	var values []string
	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
