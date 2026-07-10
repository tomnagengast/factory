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
	"syscall"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/activity"
	"github.com/tomnagengast/network/apps/factory/internal/agentrun"
	"github.com/tomnagengast/network/apps/factory/internal/server"
)

const (
	defaultPort              = "8092"
	activityEventLimit       = 250
	agentRunLimit            = 100
	defaultMaxConcurrentRuns = 3
	defaultRepoURL           = "git@github.com:tomnagengast/network.git"
	defaultTmuxSocket        = "factory-agents"
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
	if os.Getenv("LINEAR_API_KEY") == "" {
		return errors.New("LINEAR_API_KEY is required for agent runs")
	}
	triggerActorID := os.Getenv("LINEAR_TRIGGER_ACTOR_ID")
	if triggerActorID == "" {
		return errors.New("LINEAR_TRIGGER_ACTOR_ID is required for agent runs")
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
	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Factory binary: %w", err)
	}
	launcher, err := agentrun.NewTmuxLauncher(agentrun.LauncherConfig{
		RepoURL:    envOr("FACTORY_REPO_URL", defaultRepoURL),
		RepoPath:   envOr("FACTORY_REPO_PATH", filepath.Join(stateRoot, "workspace", "network")),
		StateRoot:  stateRoot,
		BinaryPath: binaryPath,
		GitPath:    requiredCommand("git"),
		TmuxPath:   requiredCommand("tmux"),
		TmuxSocket: envOr("FACTORY_TMUX_SOCKET", defaultTmuxSocket),
	})
	if err != nil {
		return err
	}
	manager, err := agentrun.NewManager(
		runStore,
		launcher,
		stateRoot,
		envInt("FACTORY_MAX_AGENTS", defaultMaxConcurrentRuns),
		5*time.Second,
		slog.Default(),
		time.Now,
	)
	if err != nil {
		return err
	}
	go manager.Run(ctx)

	handler, err := server.New(web, activityStore, runStore, manager, []byte(secret), triggerActorID, time.Now)
	if err != nil {
		return err
	}
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
		return httpServer.Shutdown(shutdownCtx)
	}
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
