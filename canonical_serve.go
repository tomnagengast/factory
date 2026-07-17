package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/activation"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/app"
	"github.com/tomnagengast/factory/internal/migration"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/server"
	"github.com/tomnagengast/factory/internal/viewerauth"
)

func serveCanonicalConfigured(ctx context.Context, options serveOptions) error {
	serviceStartedAt := time.Now().UTC()
	contractVersion, err := strconv.Atoi(buildContractVersion)
	if err != nil || contractVersion != agentrun.LifecycleContractVersion {
		return fmt.Errorf("build contract version %q does not match lifecycle contract %d", buildContractVersion, agentrun.LifecycleContractVersion)
	}
	repository := envOr("FACTORY_REPOSITORY", defaultRepository)
	baseBranch := envOr("FACTORY_BASE_BRANCH", defaultBaseBranch)
	if repository != defaultRepository || baseBranch != defaultBaseBranch {
		return fmt.Errorf("Factory lifecycle supports only %s on %s", defaultRepository, defaultBaseBranch)
	}
	web := os.DirFS("frontend/dist")
	if _, err := fs.Stat(web, "index.html"); err != nil {
		return fmt.Errorf("frontend is not built (run bun run build in frontend): %w", err)
	}
	linearSecret := os.Getenv("LINEAR_WEBHOOK_SECRET")
	githubSecret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	linearAPIKey := os.Getenv("LINEAR_API_KEY")
	triggerActorID := os.Getenv("LINEAR_TRIGGER_ACTOR_ID")
	if linearSecret == "" || githubSecret == "" || linearAPIKey == "" || triggerActorID == "" {
		return errors.New("Linear/GitHub webhook secrets, Linear API key, and trigger actor are required")
	}

	viewerAuth, err := canonicalViewerAuth(options)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	stateRoot := filepath.Join(home, ".local", "share", "factory")
	dataRoot := filepath.Join(stateRoot, "data")
	apiToken := strings.TrimSpace(os.Getenv("FACTORY_API_TOKEN"))
	if apiToken == "" {
		apiToken, err = viewerauth.LoadOrCreateToken(filepath.Join(dataRoot, "api-token"))
		if err != nil {
			return err
		}
	}
	viewerAuth, err = viewerauth.NewToken(viewerAuth, apiToken)
	if err != nil {
		return err
	}

	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Factory binary: %w", err)
	}
	tmuxPath := requiredCommand("tmux")
	gitPath := requiredCommand("git")
	githubPath := requiredCommand("gh")
	worktrunkPath := requiredCommand("wt")
	nagsPath := requiredCommand(filepath.Join(home, ".local", "bin", "nags"))
	tmuxSocket := envOr("FACTORY_TMUX_SOCKET", defaultTmuxSocket)
	repoPath := envOr("FACTORY_REPO_PATH", filepath.Join(stateRoot, "workspace", "network"))
	managedRepositoryRoot := filepath.Join(home, "repos", managedGitHubOwner)
	compiled := canonicalCompiledRepositories(home, stateRoot, repoPath, managedRepositoryRoot, options.address.URL())
	existing := make([]projectsetup.ExistingRepository, len(compiled))
	for index, source := range compiled {
		existing[index] = projectsetup.ExistingRepository{Repository: source.Repository, ProjectPath: source.ProjectPath}
	}
	projectParser, err := projectsetup.NewParser(managedGitHubOwner, managedRepositoryRoot, existing)
	if err != nil {
		return err
	}
	providerCoordinator, err := projectsetup.NewLinearProviderCoordinator(
		linearGraphQLURL, linearAPIKey, providerProjectName, []string{"Factory", "Yolo"}, &http.Client{Timeout: 10 * time.Second},
	)
	if err != nil {
		return err
	}

	generationService, err := app.NewGenerationService(app.GenerationConfig{
		DataRoot: dataRoot, GenerationsRoot: filepath.Join(stateRoot, "generations"),
		Migration: migration.Options{TriggerActorID: triggerActorID, CompiledRepositories: compiled, Now: serviceStartedAt},
		Finalizer: activation.FinalizerConfig{
			Home: home, StateRoot: stateRoot, DataRoot: dataRoot,
			ReceiptPath: filepath.Join(stateRoot, "deployments", "current.json"), CurrentPath: filepath.Join(stateRoot, "current"),
			ExecutablePath: binaryPath,
			RuntimeArtifacts: []string{
				filepath.Join(home, ".local", "bin", "factory-run"),
				filepath.Join(home, "Library", "LaunchAgents", "com.nags.factory.plist"),
			},
			Identity: activation.BuildIdentity{
				Commit: buildCommit, Tree: buildTree, BuildID: buildID, DeploymentID: buildDeploymentID, ContractVersion: contractVersion,
			},
			Now: time.Now,
		},
		RetryInterval: 2 * time.Second, Logger: slog.Default(),
	})
	if err != nil {
		return err
	}
	if err := generationService.Prepare(ctx); err != nil {
		return fmt.Errorf("prepare canonical generation: %w", err)
	}

	build := server.BuildIdentity{
		Commit: buildCommit, Tree: buildTree, BuildID: buildID, DeploymentID: buildDeploymentID,
		ContractVersion: buildContractVersion, StartedAt: serviceStartedAt,
	}
	switcher, err := app.NewHandlerSwitch(canonicalBootstrapHandler(build))
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr: options.address.NetworkAddress(), Handler: switcher,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	listener, err := net.Listen("tcp", httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", httpServer.Addr, err)
	}
	defer listener.Close()

	httpComponent := app.Component{Name: "http", Run: func(componentContext context.Context) error {
		serveResult := make(chan error, 1)
		go func() {
			slog.Info("factory listening", "address", httpServer.Addr, "generation", generationService.StagedPath())
			serveResult <- httpServer.Serve(listener)
		}()
		select {
		case serveErr := <-serveResult:
			if errors.Is(serveErr, http.ErrServerClosed) {
				return componentContext.Err()
			}
			return serveErr
		case <-componentContext.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(shutdownContext); err != nil {
				return err
			}
			serveErr := <-serveResult
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				return serveErr
			}
			return componentContext.Err()
		}
	}}
	runtimeComponent := app.Component{Name: "generation-runtime", Run: func(componentContext context.Context) error {
		return generationService.Run(componentContext, func(runtimeContext context.Context, selected *migration.SelectedGeneration) error {
			return app.RunSelectedRuntime(runtimeContext, app.RuntimeConfig{
				Generation: selected, Web: web, ViewerAuth: viewerAuth,
				LinearSecret: []byte(linearSecret), GitHubSecret: []byte(githubSecret), LinearAPIKey: linearAPIKey,
				TriggerActorID: triggerActorID, ProviderLabel: "Factory", ProviderCoordinator: providerCoordinator,
				ProjectParser: projectParser, StateRoot: stateRoot, BinaryPath: binaryPath,
				GitPath: gitPath, GitHubPath: githubPath, WorktrunkPath: worktrunkPath,
				TmuxPath: tmuxPath, TmuxSocket: tmuxSocket, NagsPath: nagsPath,
				TaskEndpoint:       "http://127.0.0.1:" + strconv.Itoa(options.address.Port) + "/api/agent/task",
				ProviderRepository: defaultRepository, Build: build,
				Redactions: []string{
					linearAPIKey, githubSecret, os.Getenv("FACTORY_GOOGLE_CLIENT_SECRET"),
					os.Getenv("FACTORY_SESSION_KEY"), os.Getenv("GITHUB_TOKEN"),
				},
				InstallHandler: switcher.Install, Now: time.Now, Logger: slog.Default(),
				RunPoll: 2 * time.Second, MergePoll: mergeReconcileInterval, ProjectPoll: projectSetupRetryPoll,
				SchedulePoll: 30 * time.Second, HeartbeatPoll: serviceHeartbeatInterval, RecoveryPoll: 5 * time.Second,
			})
		})
	}}
	supervisor, err := app.NewSupervisor(httpComponent, runtimeComponent)
	if err != nil {
		return err
	}
	return supervisor.Run(ctx)
}

func canonicalViewerAuth(options serveOptions) (server.ViewerAuthenticator, error) {
	if options.localStart && isLoopbackManagementHost(options.address.Host) {
		return viewerauth.NewLocal(options.address.Host, options.address.Port)
	}
	redirectURL := googleRedirectURL
	if options.localStart {
		redirectURL = os.Getenv("FACTORY_GOOGLE_REDIRECT_URL")
	}
	return viewerauth.New(viewerauth.Config{
		ClientID: os.Getenv("FACTORY_GOOGLE_CLIENT_ID"), ClientSecret: os.Getenv("FACTORY_GOOGLE_CLIENT_SECRET"),
		RedirectURL: redirectURL, AllowedEmails: splitList(os.Getenv("FACTORY_GOOGLE_ALLOWED_EMAILS")),
		SessionKey: []byte(os.Getenv("FACTORY_SESSION_KEY")), Now: time.Now,
	})
}

func canonicalBootstrapHandler(build server.BuildIdentity) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/healthz" {
			writer.Header().Set("Retry-After", "2")
			http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(struct {
			Status string `json:"status"`
			App    string `json:"app"`
			server.BuildIdentity
		}{Status: "ok", App: "factory", BuildIdentity: build})
	})
}

func canonicalCompiledRepositories(home, stateRoot, repoPath, managedRoot, healthURL string) []repositories.CompiledSource {
	return []repositories.CompiledSource{
		{
			App: "network", Repository: "tomnagengast/network", RepoURL: "git@github.com:tomnagengast/network.git",
			RepoPath: repoPath, ManagedRoot: filepath.Dir(repoPath), ProjectPath: "/Volumes/T9/Repos/tomnagengast/network",
			BaseBranch: "main", SourcePath: "apps/network",
			ReceiptPath:    filepath.Join(home, ".local", "share", "network", "deployments", "current.json"),
			PendingReceipt: filepath.Join(home, ".local", "share", "network", "deployments", "pending.json"),
			HealthURL:      "http://127.0.0.1:8090/healthz",
		},
		{
			App: "notebook", Repository: "tomnagengast/notebook", RepoURL: "git@github.com:tomnagengast/notebook.git",
			RepoPath: filepath.Join(managedRoot, "notebook"), ManagedRoot: managedRoot, ProjectPath: filepath.Join(managedRoot, "notebook"),
			BaseBranch: "main", ReceiptPath: filepath.Join(home, ".local", "share", "notebook", "deployments", "current.json"),
			PendingReceipt: filepath.Join(home, ".local", "share", "notebook", "deployments", "pending.json"),
			HealthURL:      "http://127.0.0.1:8091/healthz",
		},
		{
			App: "factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git",
			RepoPath: filepath.Join(managedRoot, "factory"), ManagedRoot: managedRoot, ProjectPath: filepath.Join(managedRoot, "factory"),
			BaseBranch: "main", ReceiptPath: filepath.Join(stateRoot, "deployments", "current.json"),
			PendingReceipt: filepath.Join(stateRoot, "deployments", "pending.json"), HealthURL: healthURL,
		},
		{
			App: "artifacts", Repository: "tomnagengast/artifacts", RepoURL: "git@github.com:tomnagengast/artifacts.git",
			RepoPath: filepath.Join(managedRoot, "artifacts"), ManagedRoot: managedRoot, ProjectPath: filepath.Join(managedRoot, "artifacts"),
			BaseBranch: "main", Bootstrap: true,
		},
	}
}
