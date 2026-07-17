package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/migration"
	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/server"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerscheduler"
	"github.com/tomnagengast/factory/internal/workflow"
)

type RuntimeConfig struct {
	Generation          *migration.SelectedGeneration
	Web                 fs.FS
	ViewerAuth          server.ViewerAuthenticator
	LinearSecret        []byte
	GitHubSecret        []byte
	LinearAPIKey        string
	TriggerActorID      string
	ProviderLabel       string
	ProviderCoordinator projectsetup.ProviderCoordinator
	ProjectParser       *projectsetup.Parser
	StateRoot           string
	BinaryPath          string
	GitPath             string
	GitHubPath          string
	WorktrunkPath       string
	TmuxPath            string
	TmuxSocket          string
	NagsPath            string
	TaskEndpoint        string
	ProviderRepository  string
	Build               server.BuildIdentity
	Redactions          []string
	InstallHandler      func(http.Handler)
	Now                 func() time.Time
	Logger              *slog.Logger
	RunPoll             time.Duration
	MergePoll           time.Duration
	ProjectPoll         time.Duration
	SchedulePoll        time.Duration
	HeartbeatPoll       time.Duration
	RecoveryPoll        time.Duration
}

type scheduleRegistry struct{ policy *PolicyAdapter }

func (r scheduleRegistry) Snapshot() triggerregistry.Snapshot { return r.policy.RegistrySnapshot() }

type disabledLegacyResolver struct{}

func (disabledLegacyResolver) Resolve(context.Context, string) (agentrun.RepositoryConfig, error) {
	return agentrun.RepositoryConfig{}, errors.New("canonical admission owns repository resolution")
}

// RunSelectedRuntime composes every selected-generation owner, installs the
// full HTTP handler, recovers the wire, and supervises all advancing loops.
// The caller owns the selected generation and its activation lease.
func RunSelectedRuntime(ctx context.Context, config RuntimeConfig) error {
	if err := validateRuntimeConfig(config); err != nil {
		return err
	}
	selected := config.Generation
	wire, err := eventwire.New(selected.Wire)
	if err != nil {
		return err
	}
	coordinator, err := policy.NewCoordinator(selected.Policy, func() bool { return wire.Status().Pending > 0 })
	if err != nil {
		return err
	}
	admitter, err := runs.NewAdmitter(selected.Runs)
	if err != nil {
		return err
	}
	var runService *RunService
	eventAdmission, err := NewEventAdmission(coordinator, admitter, func() {
		if runService != nil {
			runService.Notify()
		}
	}, config.Now)
	if err != nil {
		return err
	}
	if err := eventAdmission.Register(wire); err != nil {
		return err
	}
	taskOutbox, err := taskstore.NewTaskOutbox(selected.Tasks, wire)
	if err != nil {
		return err
	}
	taskCompleter, err := taskservice.NewCompleter(selected.Tasks, taskOutbox, config.Now)
	if err != nil {
		return err
	}

	repositories, err := NewRepositoryAdapter(selected.Repositories)
	if err != nil {
		return err
	}
	defaultRepository, err := repositories.ResolveRepository(config.ProviderRepository)
	if err != nil {
		return err
	}
	launcherConfig := agentrun.LauncherConfig{
		Repository: defaultRepository.Repository, RepoURL: defaultRepository.RepoURL,
		RepoPath: defaultRepository.RepoPath, ManagedRoot: defaultRepository.ManagedRoot,
		BaseBranch: defaultRepository.BaseBranch, Bootstrap: defaultRepository.Bootstrap,
		StateRoot: config.StateRoot, BinaryPath: config.BinaryPath, GitPath: config.GitPath,
		GitHubPath: config.GitHubPath, WorktrunkPath: config.WorktrunkPath,
		TmuxPath: config.TmuxPath, TmuxSocket: config.TmuxSocket, TaskEndpoint: config.TaskEndpoint,
		Repositories: func() []agentrun.RepositoryConfig {
			values, err := repositories.Configs()
			if err != nil {
				config.Logger.Error("project canonical repository catalog", "error", err)
				return nil
			}
			return values
		},
	}
	legacyLauncher, err := agentrun.NewTmuxLauncher(launcherConfig)
	if err != nil {
		return err
	}
	launcher, err := NewRunLauncher(legacyLauncher)
	if err != nil {
		return err
	}
	linearReader, err := NewLinearGraphQLProjectReader("https://api.linear.app/graphql", config.LinearAPIKey, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		return err
	}
	resolver, err := NewRepositoryResolver(selected.Repositories, selected.Tasks, linearReader, config.ProjectParser)
	if err != nil {
		return err
	}
	completion, err := NewCompletionAuthorities(selected.Repositories, CompletionOptions{
		GitHubPath: config.GitHubPath, GitDirectory: defaultRepository.RepoPath,
		LinearURL: "https://api.linear.app/graphql", LinearAPIKey: config.LinearAPIKey,
		GitPath: config.GitPath, WorktrunkPath: config.WorktrunkPath,
		HTTPClient: &http.Client{Timeout: 10 * time.Second}, TaskCompletion: taskCompleter, Now: config.Now,
	})
	if err != nil {
		return err
	}
	outboxCollector, err := runs.NewOutboxCollector(selected.Runs, wire)
	if err != nil {
		return err
	}
	recordCollector, err := agentrun.NewRecordCollector(wire, config.StateRoot, selected.Runtime.AgentOffsets)
	if err != nil {
		return err
	}
	collector, err := NewRunCollector(outboxCollector, recordCollector)
	if err != nil {
		return err
	}
	manager, err := runs.NewManager(
		selected.Runs, wire, resolver, launcher, completion.PullRequests, completion.Validator, collector,
		config.StateRoot, func() int { return coordinator.Snapshot().Settings().Runtime.MaxConcurrentRuns },
		config.RunPoll, config.MergePoll, config.Now, config.Logger,
	)
	if err != nil {
		return err
	}
	runService, err = NewRunService(manager, config.RunPoll)
	if err != nil {
		return err
	}
	runAdapter, err := NewRunAdapter(selected.Runs, func() triggerregistry.Snapshot {
		return policy.RegistryView(coordinator.Snapshot())
	}, runService.Notify)
	if err != nil {
		return err
	}
	policyAdapter, err := NewPolicyAdapter(coordinator, runAdapter.RoutingSnapshot)
	if err != nil {
		return err
	}
	nativeAdmitter, err := NewNativeAdmitter(coordinator, admitter)
	if err != nil {
		return err
	}
	taskReconciler, err := NewTaskReconciler(taskOutbox, runService)
	if err != nil {
		return err
	}
	tasks, err := taskservice.New(
		TaskControlAdapter{Policy: policyAdapter}, repositories, repositories, selected.Tasks,
		taskOutbox, policyAdapter, nativeAdmitter, taskReconciler, config.Now,
	)
	if err != nil {
		return err
	}
	identityAdapter, err := NewLinearIdentityAdapter(selected.Tasks)
	if err != nil {
		return err
	}
	linearTasks, err := taskservice.NewLinearProvider("https://api.linear.app/graphql", config.LinearAPIKey, &http.Client{Timeout: 10 * time.Second}, identityAdapter)
	if err != nil {
		return err
	}
	activityAdapter, err := NewActivityAdapter(selected.Activity)
	if err != nil {
		return err
	}
	providerStarter, err := NewProviderAgentStarter(
		config.ProviderCoordinator, activityAdapter, wire, config.TriggerActorID, config.ProviderLabel, config.Now,
	)
	if err != nil {
		return err
	}
	provisioner, err := NewRepositoryProvisioner(launcherConfig, config.NagsPath, providerStarter)
	if err != nil {
		return err
	}
	onboarding, err := NewRepositoryOnboarding(
		selected.Repositories, config.ProjectParser, provisioner, config.ProjectPoll, config.Logger, config.Now,
	)
	if err != nil {
		return err
	}
	drafts, err := workflow.OpenDraftStore(selected.Runtime.WorkflowDrafts)
	if err != nil {
		return err
	}
	cursors, err := triggerscheduler.Open(selected.Runtime.TriggerCursors)
	if err != nil {
		return err
	}
	scheduler, err := triggerscheduler.New(scheduleRegistry{policy: policyAdapter}, cursors, wire, config.Logger, config.Now)
	if err != nil {
		return err
	}
	observer, err := agentrun.NewObserver(runAdapter, config.TmuxPath, config.TmuxSocket, config.Redactions, config.Now)
	if err != nil {
		return err
	}
	wireState := selected.Wire.State()
	projectionCursor := NewProviderProjectionCursor(wireState.ChannelTotals[githubhook.WireChannel], wireState.ChannelTotals[linearhook.WireChannel])

	var ready atomic.Bool
	handler, err := server.New(server.Config{
		Web: config.Web, ActivityStore: activityAdapter, RunStore: runAdapter, RunNotifier: runService,
		AgentObserver: observer, Settings: policyAdapter, WorkflowDrafts: drafts,
		ViewerAuth: config.ViewerAuth, LinearSecret: config.LinearSecret, GitHubSecret: config.GitHubSecret,
		Events: wire, GitHubEvents: GitHubProjection{Cursor: projectionCursor}, LinearComments: LinearProjection{Cursor: projectionCursor},
		TriggerActor: config.TriggerActorID, RepositoryResolver: disabledLegacyResolver{}, ProjectSetups: onboarding,
		Now: config.Now, Build: config.Build, GenericTriggers: true, TriggerPolicy: policyAdapter,
		ScheduleStatus: scheduler, Tasks: tasks, LinearTasks: linearTasks, LinearIdentities: identityAdapter,
		TaskStatus: selected.Tasks.Status, Ready: ready.Load, HealthReady: func() bool { return true },
	})
	if err != nil {
		return err
	}
	config.InstallHandler(handler)

	advancing := make(chan struct{})
	recovery := Component{Name: "canonical-recovery", Run: func(componentContext context.Context) error {
		return recoverSelectedRuntime(componentContext, wire, taskOutbox, onboarding, runService, func() error {
			if err := publishRuntimeServiceEvent(componentContext, wire, "started", config.Build.StartedAt, config.Now().UTC()); err != nil {
				return err
			}
			ready.Store(true)
			close(advancing)
			return nil
		}, config.RecoveryPoll, config.Logger)
	}}
	wait := func(run func(context.Context) error) func(context.Context) error {
		return func(componentContext context.Context) error {
			if err := WaitForReady(componentContext, advancing); err != nil {
				return err
			}
			return run(componentContext)
		}
	}
	supervisor, err := NewSupervisor(
		recovery,
		Component{Name: "repository-onboarding", Run: wait(onboarding.Run)},
		Component{Name: "run-manager", Run: wait(runService.Run)},
		Component{Name: "scheduler", Run: wait(func(componentContext context.Context) error {
			scheduler.Run(componentContext, config.SchedulePoll)
			return componentContext.Err()
		})},
		Component{Name: "heartbeat", Run: wait(func(componentContext context.Context) error {
			publishRuntimeHeartbeats(componentContext, wire, config.Build.StartedAt, config.HeartbeatPoll, config.Now, config.Logger)
			return componentContext.Err()
		})},
	)
	if err != nil {
		return err
	}
	err = supervisor.Run(ctx)
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if publishErr := publishRuntimeServiceEvent(shutdownContext, wire, "stopping", config.Build.StartedAt, config.Now().UTC()); publishErr != nil {
		config.Logger.Error("publish Factory stopping event", "error", publishErr)
	}
	return err
}

func validateRuntimeConfig(config RuntimeConfig) error {
	if config.Generation == nil || config.Web == nil || config.ViewerAuth == nil || len(config.LinearSecret) == 0 || len(config.GitHubSecret) == 0 ||
		config.LinearAPIKey == "" || config.TriggerActorID == "" || config.ProviderLabel == "" || config.ProviderCoordinator == nil ||
		config.ProjectParser == nil || config.StateRoot == "" || config.BinaryPath == "" || config.GitPath == "" || config.GitHubPath == "" ||
		config.WorktrunkPath == "" || config.TmuxPath == "" || config.TmuxSocket == "" || config.NagsPath == "" || config.TaskEndpoint == "" ||
		config.ProviderRepository == "" || config.InstallHandler == nil || config.Now == nil || config.Logger == nil ||
		config.RunPoll <= 0 || config.MergePoll <= 0 || config.ProjectPoll <= 0 || config.SchedulePoll <= 0 || config.HeartbeatPoll <= 0 || config.RecoveryPoll <= 0 {
		return errors.New("app runtime: complete selected-generation authorities are required")
	}
	return nil
}

func recoverSelectedRuntime(
	ctx context.Context,
	wire *eventwire.Wire,
	tasks *taskstore.TaskOutbox,
	repositories *RepositoryOnboarding,
	runs *RunService,
	onReady func() error,
	retry time.Duration,
	logger *slog.Logger,
) error {
	for {
		err := wire.CatchUp(ctx)
		if err == nil {
			err = tasks.Reconcile(ctx)
		}
		if err == nil {
			repositories.Reconcile(ctx)
			runs.Reconcile(ctx)
			err = onReady()
		}
		if err == nil {
			<-ctx.Done()
			return ctx.Err()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warn("Factory selected runtime recovery pending", "error", err, "retry_in", retry)
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func publishRuntimeHeartbeats(ctx context.Context, publisher *eventwire.Wire, started time.Time, interval time.Duration, now func() time.Time, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := publishRuntimeServiceEvent(ctx, publisher, "heartbeat", started, now().UTC()); err != nil && ctx.Err() == nil {
				logger.Error("publish Factory heartbeat", "error", err)
			}
		}
	}
}

func publishRuntimeServiceEvent(ctx context.Context, publisher *eventwire.Wire, action string, started, at time.Time) error {
	event := eventwire.Event{
		ID:     "factory:service:" + configInstance(started) + ":" + action + ":" + fmt.Sprint(at.UnixNano()),
		Source: eventwire.SourceFactory, Type: "service", Action: action, Subject: "factory",
		Attributes: map[string][]string{
			"startedAt": {started.Format(time.RFC3339Nano)}, "status": {action},
			eventwire.AttributeProducer: {"factory-service"}, eventwire.AttributeProvenance: {"factory"},
		},
		ReceivedAt: at,
	}
	_, _, err := publisher.Publish(ctx, event)
	return err
}

func configInstance(started time.Time) string { return fmt.Sprint(started.UnixNano()) }
