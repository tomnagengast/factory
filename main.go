package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/app"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/linearidentity"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/server"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/triggerscheduler"
	"github.com/tomnagengast/factory/internal/viewerauth"
	"github.com/tomnagengast/factory/internal/workflow"
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
	defaultRepository        = "tomnagengast/network"
	defaultBaseBranch        = "main"
	defaultTmuxSocket        = "factory-agents"
	serviceHeartbeatInterval = 30 * time.Second
	mergeReconcileInterval   = 60 * time.Second
	projectSetupRetryPoll    = 15 * time.Second
	managedGitHubOwner       = "tomnagengast"
	linearGraphQLURL         = "https://api.linear.app/graphql"
	providerProjectName      = "Network"
	googleRedirectURL        = "https://factory.nags.cloud/auth/google/callback"
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
	address, err := resolveManagementAddress(managementFlags{}, nil)
	if err != nil {
		return fmt.Errorf("server address: %w", err)
	}
	return serveConfigured(ctx, serveOptions{address: address})
}

type serveOptions struct {
	address    managementAddress
	localStart bool
	output     io.Writer
}

func serveConfigured(ctx context.Context, options serveOptions) error {
	serviceStartedAt := time.Now().UTC()
	if buildContractVersion != strconv.Itoa(agentrun.LifecycleContractVersion) {
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
	googleClientID := os.Getenv("FACTORY_GOOGLE_CLIENT_ID")
	googleClientSecret := os.Getenv("FACTORY_GOOGLE_CLIENT_SECRET")
	allowedEmails := splitList(os.Getenv("FACTORY_GOOGLE_ALLOWED_EMAILS"))
	sessionKey := os.Getenv("FACTORY_SESSION_KEY")
	var viewerAuth server.ViewerAuthenticator
	var err error
	if options.localStart && isLoopbackManagementHost(options.address.Host) {
		viewerAuth, err = viewerauth.NewLocal(options.address.Host, options.address.Port)
	} else {
		redirectURL := googleRedirectURL
		if options.localStart {
			redirectURL = os.Getenv("FACTORY_GOOGLE_REDIRECT_URL")
		}
		viewerAuth, err = viewerauth.New(viewerauth.Config{
			ClientID:      googleClientID,
			ClientSecret:  googleClientSecret,
			RedirectURL:   redirectURL,
			AllowedEmails: allowedEmails,
			SessionKey:    []byte(sessionKey),
			Now:           time.Now,
		})
	}
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
	linearIdentities, err := linearidentity.Open(filepath.Join(dataRoot, "linear-task-identities.json"))
	if err != nil {
		return err
	}
	maxConcurrentRuns := envInt("FACTORY_MAX_AGENTS", defaultMaxConcurrentRuns)
	settingsStore, err := settings.Open(filepath.Join(dataRoot, "settings.json"), settings.Defaults(maxConcurrentRuns))
	if err != nil {
		return err
	}
	draftStore, draftErr := workflow.OpenDraftStore(filepath.Join(dataRoot, "workflow-drafts.json"))
	draftError := ""
	if draftErr != nil {
		draftError = draftErr.Error()
		slog.Error("workflow authoring unavailable", "error", draftErr)
	}
	activityStore, err := activity.Open(filepath.Join(dataRoot, "linear-activity.json"), activityEventLimit)
	if err != nil {
		return err
	}
	runStore, err := agentrun.Open(filepath.Join(dataRoot, "agent-runs.json"), agentRunLimit)
	if err != nil {
		return err
	}
	projectSetupStore, err := projectsetup.Open(filepath.Join(dataRoot, "project-setups.json"), time.Now())
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
	rawEvents, err := eventwire.New(eventJournal)
	if err != nil {
		return err
	}
	registryStore, err := triggerregistry.Open(
		filepath.Join(dataRoot, "triggers.json"),
		triggerregistry.Defaults(settingsStore.Snapshot(), triggerActorID),
		settingsStore.Snapshot(),
	)
	if err != nil {
		return err
	}
	routingStore, err := triggerrouter.Open(filepath.Join(dataRoot, "trigger-routing.jsonl"))
	if err != nil {
		return err
	}
	events, err := triggerrouter.NewCoordinatedWire(rawEvents, registryStore, settingsStore, routingStore, time.Now)
	if err != nil {
		return err
	}
	taskStorePath := filepath.Join(dataRoot, "native-tasks.jsonl")
	nativeTasks, err := taskstore.Open(taskStorePath)
	if err != nil {
		return err
	}
	taskStager, err := taskstore.NewStager(filepath.Join(dataRoot, "task-operations"), taskStorePath)
	if err != nil {
		return err
	}
	_, dispatchedEvents, _, retainedEvents := eventJournal.Snapshot()
	if err := taskStager.Recover(dispatchedEvents, retainedEvents); err != nil {
		return fmt.Errorf("recover staged task operations: %w", err)
	}
	taskDispatcher, err := taskstore.NewDispatcher(nativeTasks, taskStager)
	if err != nil {
		return err
	}
	if err := events.Handle(eventwire.Filter{Source: eventwire.SourceFactory, Type: taskstore.StagedEventType}, func(ctx context.Context, record eventwire.Record) error {
		_, err := taskDispatcher.Apply(ctx, record)
		return err
	}); err != nil {
		return err
	}
	taskCoordinator, err := taskstore.NewCoordinator(nativeTasks, taskStager, events)
	if err != nil {
		return err
	}
	taskCompleter, err := taskservice.NewCompleter(nativeTasks, taskCoordinator, time.Now)
	if err != nil {
		return err
	}
	nativeTaskControl, err := taskcontrol.Open(filepath.Join(dataRoot, "native-task-control.json"))
	if err != nil {
		return err
	}
	cursorStore, err := triggerscheduler.Open(filepath.Join(dataRoot, "trigger-cursors.json"))
	if err != nil {
		return err
	}
	scheduler, err := triggerscheduler.New(registryStore, cursorStore, events, slog.Default(), time.Now)
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
	gitPath := requiredCommand("git")
	githubPath := requiredCommand("gh")
	worktrunkPath := requiredCommand("wt")
	nagsPath := requiredCommand(filepath.Join(home, ".local", "bin", "nags"))
	tmuxSocket := envOr("FACTORY_TMUX_SOCKET", defaultTmuxSocket)
	repoPath := envOr("FACTORY_REPO_PATH", filepath.Join(stateRoot, "workspace", "network"))
	managedRepositoryRoot := filepath.Join(home, "repos", managedGitHubOwner)
	staticRepositoryConfigs := []agentrun.RepositoryConfig{
		{
			App: "network", Repository: "tomnagengast/network",
			RepoURL:  "git@github.com:tomnagengast/network.git",
			RepoPath: repoPath, ManagedRoot: filepath.Dir(repoPath), ProjectPath: "/Volumes/T9/Repos/tomnagengast/network",
			BaseBranch: "main", SourcePath: "apps/network",
			ReceiptPath:    filepath.Join(home, ".local", "share", "network", "deployments", "current.json"),
			PendingReceipt: filepath.Join(home, ".local", "share", "network", "deployments", "pending.json"),
			HealthURL:      "http://127.0.0.1:8090/healthz",
		},
		{
			App: "notebook", Repository: "tomnagengast/notebook",
			RepoURL:        "git@github.com:tomnagengast/notebook.git",
			RepoPath:       filepath.Join(managedRepositoryRoot, "notebook"),
			ManagedRoot:    managedRepositoryRoot,
			ProjectPath:    filepath.Join(managedRepositoryRoot, "notebook"),
			BaseBranch:     "main",
			ReceiptPath:    filepath.Join(home, ".local", "share", "notebook", "deployments", "current.json"),
			PendingReceipt: filepath.Join(home, ".local", "share", "notebook", "deployments", "pending.json"),
			HealthURL:      "http://127.0.0.1:8091/healthz",
		},
		{
			App: "factory", Repository: "tomnagengast/factory",
			RepoURL:        "git@github.com:tomnagengast/factory.git",
			RepoPath:       filepath.Join(managedRepositoryRoot, "factory"),
			ManagedRoot:    managedRepositoryRoot,
			ProjectPath:    filepath.Join(managedRepositoryRoot, "factory"),
			BaseBranch:     "main",
			ReceiptPath:    filepath.Join(stateRoot, "deployments", "current.json"),
			PendingReceipt: filepath.Join(stateRoot, "deployments", "pending.json"),
			HealthURL:      options.address.URL() + "/api/healthz",
		},
		{
			App: "artifacts", Repository: "tomnagengast/artifacts",
			RepoURL:     "git@github.com:tomnagengast/artifacts.git",
			RepoPath:    filepath.Join(managedRepositoryRoot, "artifacts"),
			ManagedRoot: managedRepositoryRoot,
			ProjectPath: filepath.Join(managedRepositoryRoot, "artifacts"),
			BaseBranch:  "main", Bootstrap: true,
		},
	}
	var providerRepositoryConfig agentrun.RepositoryConfig
	for _, config := range staticRepositoryConfigs {
		if strings.EqualFold(config.Repository, defaultRepository) {
			providerRepositoryConfig = config
			break
		}
	}
	if providerRepositoryConfig.Repository == "" {
		return fmt.Errorf("provider repository %s is not configured", defaultRepository)
	}
	existingRepositories := make([]projectsetup.ExistingRepository, 0, len(staticRepositoryConfigs))
	for _, config := range staticRepositoryConfigs {
		existingRepositories = append(existingRepositories, projectsetup.ExistingRepository{
			Repository: config.Repository, ProjectPath: config.ProjectPath,
		})
	}
	projectParser, err := projectsetup.NewParser(managedGitHubOwner, managedRepositoryRoot, existingRepositories)
	if err != nil {
		return err
	}
	projectSpecs := projectSetupStore.RepositorySpecs()
	for _, spec := range projectSpecs {
		if err := projectParser.Validate(spec); err != nil {
			return fmt.Errorf("validate persisted project setup %s: %w", spec.ProjectID, err)
		}
	}
	repositoryConfigs, err := repositoryConfigsWithSetups(staticRepositoryConfigs, projectSpecs)
	if err != nil {
		return err
	}
	repositoryCatalog, err := agentrun.NewRepositoryCatalog(repositoryConfigs)
	if err != nil {
		return err
	}
	repositoryResolver, err := agentrun.NewLinearRepositoryResolver(
		linearGraphQLURL,
		linearAPIKey,
		&http.Client{Timeout: 10 * time.Second},
		repositoryCatalog,
	)
	if err != nil {
		return err
	}
	factoryRepositoryResolver, err := agentrun.NewFactoryRepositoryResolver(nativeTasks, repositoryCatalog)
	if err != nil {
		return err
	}
	taskRepositoryResolver, err := agentrun.NewCompositeTaskRepositoryResolver(repositoryResolver, factoryRepositoryResolver)
	if err != nil {
		return err
	}
	launcherConfig := agentrun.LauncherConfig{
		Repository:    defaultRepository,
		RepoURL:       envOr("FACTORY_REPO_URL", defaultRepoURL),
		RepoPath:      repoPath,
		ManagedRoot:   filepath.Dir(repoPath),
		BaseBranch:    baseBranch,
		StateRoot:     stateRoot,
		BinaryPath:    binaryPath,
		GitPath:       gitPath,
		GitHubPath:    githubPath,
		WorktrunkPath: worktrunkPath,
		TmuxPath:      tmuxPath,
		TmuxSocket:    tmuxSocket,
		TaskEndpoint:  "http://127.0.0.1:" + strconv.Itoa(options.address.Port) + "/api/agent/task",
		Repositories:  repositoryCatalog.Snapshot,
	}
	launcher, err := agentrun.NewTmuxLauncher(launcherConfig)
	if err != nil {
		return err
	}
	pullRequests, err := agentrun.NewGitHubCLI(githubPath, repoPath)
	if err != nil {
		return err
	}
	readerOptions := completionReaderOptions{
		linearURL: linearGraphQLURL, linearAPIKey: linearAPIKey,
		gitPath: gitPath, worktrunkPath: worktrunkPath,
		httpClient:     &http.Client{Timeout: 10 * time.Second},
		taskCompletion: taskCompleter,
	}
	completionReaders, err := buildCompletionReaders(repositoryConfigs, readerOptions)
	if err != nil {
		return err
	}
	completionEvidence, err := agentrun.NewRepositoryCompletionEvidence(completionReaders)
	if err != nil {
		return err
	}
	projectRegistrar := &repositoryRegistrar{
		staticConfigs: staticRepositoryConfigs, catalog: repositoryCatalog,
		evidence: completionEvidence, readerOptions: readerOptions,
	}
	terminalValidator, err := agentrun.NewMechanicalCompletionValidator(pullRequests, completionEvidence, repository, time.Now)
	if err != nil {
		return err
	}
	manager, err := agentrun.NewManager(
		runStore,
		launcher,
		collector,
		pullRequests,
		terminalValidator,
		agentrun.LifecycleConfig{
			Repository: repository,
			BaseBranch: baseBranch,
		},
		stateRoot,
		func() int { return settingsStore.Snapshot().Runtime.MaxConcurrentRuns },
		2*time.Second,
		mergeReconcileInterval,
		slog.Default(),
		time.Now,
	)
	if err != nil {
		return err
	}
	triggerManager, err := triggerrouter.NewManager(routingStore, runStore, events, taskRepositoryResolver, manager, slog.Default(), time.Now)
	if err != nil {
		return err
	}
	nativeTaskService, err := taskservice.New(
		nativeTaskControl, projectSetupStore, repositoryCatalog, nativeTasks, taskCoordinator,
		events, routingStore, triggerManager, time.Now,
	)
	if err != nil {
		return err
	}
	linearTaskProvider, err := taskservice.NewLinearProvider(linearGraphQLURL, linearAPIKey, &http.Client{Timeout: 10 * time.Second}, linearIdentities)
	if err != nil {
		return err
	}
	if err := manager.SetInvocationStartGate(routingStore); err != nil {
		return err
	}
	providerCoordinator, err := projectsetup.NewLinearProviderCoordinator(
		linearGraphQLURL,
		linearAPIKey,
		providerProjectName,
		[]string{"Factory", "Yolo"},
		&http.Client{Timeout: 10 * time.Second},
	)
	if err != nil {
		return err
	}
	providerStarter, err := newProviderAgentStarter(
		providerCoordinator,
		runStore,
		manager,
		providerRepositoryConfig,
		time.Now,
	)
	if err != nil {
		return err
	}
	projectProvisioner := &repositoryProvisioner{
		launcherConfig: launcherConfig,
		nagsPath:       nagsPath,
		provider:       providerStarter,
	}
	projectManager, err := projectsetup.NewManager(
		projectSetupStore, projectParser, projectRegistrar, projectProvisioner,
		projectSetupRetryPoll, slog.Default(), time.Now,
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
			googleClientSecret,
			sessionKey,
			os.Getenv("GITHUB_TOKEN"),
		},
		time.Now,
	)
	if err != nil {
		return err
	}

	var ready atomic.Bool
	handler, err := server.New(server.Config{
		Web:                web,
		ActivityStore:      activityStore,
		RunStore:           runStore,
		RunNotifier:        manager,
		AgentObserver:      observer,
		Settings:           settingsStore,
		WorkflowDrafts:     draftStore,
		WorkflowDraftError: draftError,
		ViewerAuth:         viewerAuth,
		LinearSecret:       []byte(secret),
		GitHubSecret:       []byte(githubSecret),
		Events:             events,
		GitHubEvents:       githubEvents,
		LinearComments:     linearComments,
		TriggerActor:       triggerActorID,
		RepositoryResolver: repositoryResolver,
		ProjectSetups:      projectManager,
		Now:                time.Now,
		Build: server.BuildIdentity{
			Commit:          buildCommit,
			Tree:            buildTree,
			BuildID:         buildID,
			DeploymentID:    buildDeploymentID,
			ContractVersion: buildContractVersion,
			StartedAt:       serviceStartedAt,
		},
		GenericTriggers:  true,
		TriggerPolicy:    events,
		ScheduleStatus:   scheduler,
		Tasks:            nativeTaskService,
		LinearTasks:      linearTaskProvider,
		LinearIdentities: linearIdentities,
		TaskStatus: func() taskstore.Status {
			status := nativeTasks.Status()
			staging := taskStager.Status()
			status.PendingStages = staging.PendingStages
			status.Healthy = status.Healthy && staging.Healthy && staging.PendingStages == 0
			return status
		},
		Ready: ready.Load,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              options.address.NetworkAddress(),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	listener, err := net.Listen("tcp", httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", httpServer.Addr, err)
	}
	defer listener.Close()
	if options.localStart {
		record := newLocalRuntimeRecord(options.address, binaryPath, serviceStartedAt)
		if err := publishLocalRuntimeRecord(record); err != nil {
			return err
		}
		defer removeOwnedLocalRuntimeRecord(record)
		if options.output != nil {
			fmt.Fprintf(options.output, "Factory running at %s (press Ctrl-C to stop)\n", options.address.URL())
		}
	}

	advancing := make(chan struct{})
	recovery := app.Component{Name: "event-recovery", Run: func(componentContext context.Context) error {
		return superviseEventWireRecovery(componentContext, events, 5*time.Second, func(ctx context.Context) error {
			return triggerManager.ReconcileExisting(ctx)
		}, func() error {
			if _, err := events.ReconcileCompiledDefaults(settingsStore.Snapshot().Revision, time.Now()); err != nil {
				return fmt.Errorf("reconcile compiled default workflows: %w", err)
			}
			projectManager.Reconcile(componentContext)
			if err := triggerManager.Reconcile(componentContext); err != nil {
				return err
			}
			if err := publishServiceEvent(componentContext, events, "started", serviceStartedAt, serviceStartedAt); err != nil {
				return err
			}
			ready.Store(true)
			close(advancing)
			return nil
		}, slog.Default())
	}}
	waitAndRun := func(run func(context.Context)) func(context.Context) error {
		return func(componentContext context.Context) error {
			if err := app.WaitForReady(componentContext, advancing); err != nil {
				return err
			}
			run(componentContext)
			return componentContext.Err()
		}
	}
	supervisor, err := app.NewSupervisor(
		app.Component{Name: "http", Run: func(componentContext context.Context) error {
			serveResult := make(chan error, 1)
			go func() {
				slog.Info("factory listening", "address", httpServer.Addr)
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
		}},
		recovery,
		app.Component{Name: "repository-onboarding", Run: waitAndRun(projectManager.Run)},
		app.Component{Name: "run-manager", Run: waitAndRun(manager.Run)},
		app.Component{Name: "trigger-manager", Run: waitAndRun(func(componentContext context.Context) { triggerManager.Run(componentContext, 2*time.Second) })},
		app.Component{Name: "scheduler", Run: waitAndRun(func(componentContext context.Context) { scheduler.Run(componentContext, 30*time.Second) })},
		app.Component{Name: "heartbeat", Run: waitAndRun(func(componentContext context.Context) {
			publishServiceHeartbeats(componentContext, events, serviceStartedAt, serviceHeartbeatInterval, time.Now)
		})},
	)
	if err != nil {
		return err
	}
	err = supervisor.Run(ctx)
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if publishErr := publishServiceEvent(shutdownContext, events, "stopping", serviceStartedAt, time.Now().UTC()); publishErr != nil {
		slog.Error("publish Factory stopping event", "error", publishErr)
	}
	return err
}

type recoverableEventWire interface {
	CatchUp(context.Context) error
}

func recoverEventWire(
	ctx context.Context,
	events recoverableEventWire,
	retryInterval time.Duration,
	beforeCatchUp func(context.Context) error,
	onReady func() error,
	logger *slog.Logger,
) {
	for {
		err := beforeCatchUp(ctx)
		if err == nil {
			err = events.CatchUp(ctx)
		}
		if err == nil {
			err = onReady()
		}
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		logger.Warn("Factory event wire recovery pending", "error", err, "retry_in", retryInterval)
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func superviseEventWireRecovery(
	ctx context.Context,
	events recoverableEventWire,
	retryInterval time.Duration,
	beforeCatchUp func(context.Context) error,
	onReady func() error,
	logger *slog.Logger,
) error {
	for {
		err := beforeCatchUp(ctx)
		if err == nil {
			err = events.CatchUp(ctx)
		}
		if err == nil {
			err = onReady()
		}
		if err == nil {
			<-ctx.Done()
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warn("Factory event wire recovery pending", "error", err, "retry_in", retryInterval)
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
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
		ID:      "factory:service:" + instance + ":" + action + ":" + strconv.FormatInt(at.UnixNano(), 10),
		Source:  eventwire.SourceFactory,
		Type:    "service",
		Action:  action,
		Subject: "factory",
		Attributes: map[string][]string{
			"pid": {strconv.Itoa(os.Getpid())}, "startedAt": {startedAt.Format(time.RFC3339Nano)}, "status": {action},
			eventwire.AttributeProducer: {"factory-service"}, eventwire.AttributeProvenance: {"factory"},
		},
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

func repositoryRemoteURLs(repository string) []string {
	return []string{
		"git@github.com:" + repository + ".git",
		"https://github.com/" + repository,
		"https://github.com/" + repository + ".git",
	}
}
