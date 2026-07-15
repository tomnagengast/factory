package taskservice

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

var (
	ErrDisabled            = errors.New("task service: native tasks are disabled for this project")
	ErrWorkflowUnavailable = errors.New("task service: compiled provider-neutral workflow is unavailable")
	ErrRoutingConflict     = errors.New("task service: task route conflicts with current project authority")
)

type Control interface {
	Enabled(string) bool
}

type Projects interface {
	ResolveSucceeded(string) (projectsetup.Spec, error)
}

type Catalog interface {
	ResolveRepository(string) (agentrun.RepositoryConfig, error)
}

type Tasks interface {
	Find(string) (taskstore.Task, bool)
}

type Mutator interface {
	Execute(context.Context, taskstore.CommandEnvelope, time.Time) (taskstore.Result, error)
}

type Policy interface {
	SettingsSnapshot() settings.Snapshot
	RegistrySnapshot() triggerregistry.Snapshot
}

type Admitter interface {
	AdmitNative(triggerrouter.NativeAdmission) (triggerrouter.Invocation, bool, error)
}

type Reconciler interface {
	Reconcile(context.Context) error
}

type Service struct {
	control    Control
	projects   Projects
	catalog    Catalog
	tasks      Tasks
	mutator    Mutator
	policy     Policy
	admitter   Admitter
	reconciler Reconciler
	now        func() time.Time
}

type CreateRequest struct {
	Actor          taskstore.Actor
	Title          string
	Description    string
	ProjectID      string
	ApprovalMode   string
	IdempotencyKey string
}

type StartRequest struct {
	Actor  taskstore.Actor
	TaskID string
}

type StartResult struct {
	Task       taskstore.Task
	Invocation triggerrouter.Invocation
	Admitted   bool
}

func New(control Control, projects Projects, catalog Catalog, tasks Tasks, mutator Mutator, policy Policy, admitter Admitter, reconciler Reconciler, now func() time.Time) (*Service, error) {
	if control == nil || projects == nil || catalog == nil || tasks == nil || mutator == nil || policy == nil || admitter == nil || reconciler == nil || now == nil {
		return nil, errors.New("task service: dependencies are required")
	}
	return &Service{control: control, projects: projects, catalog: catalog, tasks: tasks, mutator: mutator, policy: policy, admitter: admitter, reconciler: reconciler, now: now}, nil
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (taskstore.Result, error) {
	if !s.control.Enabled(request.ProjectID) {
		return taskstore.Result{}, ErrDisabled
	}
	if _, err := s.projects.ResolveSucceeded(request.ProjectID); err != nil {
		return taskstore.Result{}, fmt.Errorf("task service: resolve project: %w", err)
	}
	return s.mutator.Execute(ctx, taskstore.CreateEnvelope(taskstore.CreateCommand{
		Actor: request.Actor, Title: request.Title, Description: request.Description,
		ProjectID: request.ProjectID, ApprovalMode: request.ApprovalMode,
		IdempotencyKey: request.IdempotencyKey,
	}), s.now())
}

func (s *Service) Start(ctx context.Context, request StartRequest) (StartResult, error) {
	task, found := s.tasks.Find(request.TaskID)
	if !found {
		return StartResult{}, taskstore.ErrNotFound
	}
	if !s.control.Enabled(task.ProjectID) {
		return StartResult{}, ErrDisabled
	}
	if task.State != taskstore.StateOpen && task.State != taskstore.StateInProgress {
		return StartResult{}, fmt.Errorf("task service: task state %q cannot be started", task.State)
	}

	spec, err := s.projects.ResolveSucceeded(task.ProjectID)
	if err != nil {
		return StartResult{}, fmt.Errorf("task service: resolve project: %w", err)
	}
	repository, err := s.catalog.ResolveRepository(spec.Repository)
	if err != nil {
		return StartResult{}, fmt.Errorf("task service: resolve repository: %w", err)
	}
	if repository.Repository != spec.Repository {
		return StartResult{}, ErrRoutingConflict
	}

	configuration := s.policy.SettingsSnapshot()
	definition, found := configuration.Workflow(workflow.ProviderNeutralID)
	if !found || !definition.Enabled {
		return StartResult{}, ErrWorkflowUnavailable
	}
	digest, err := workflow.Digest(definition)
	if err != nil || digest != workflow.ProviderNeutralDigest() {
		return StartResult{}, ErrWorkflowUnavailable
	}
	pin := workflow.Pin(definition)
	now := s.now().UTC()
	route := taskstore.RoutingSnapshot{
		ProjectID: task.ProjectID, Repository: repository.Repository,
		RepositoryURL: repository.RepoURL, RepositoryPath: repository.RepoPath,
		ManagedRoot: repository.ManagedRoot, BaseBranch: repository.BaseBranch,
		Bootstrap: repository.Bootstrap, CloudURL: repository.CloudURL,
		WorkflowID: definition.ID, WorkflowDigest: digest, AdmittedAt: now,
	}
	if task.Routing == nil {
		result, err := s.mutator.Execute(ctx, taskstore.RoutingEnvelope(taskstore.RoutingCommand{
			Actor: request.Actor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision,
			Routing: route, IdempotencyKey: "native-start-route-v1",
		}), now)
		if err != nil {
			return StartResult{}, err
		}
		task = result.Task
	} else if !sameRoute(*task.Routing, route) {
		return StartResult{}, ErrRoutingConflict
	}

	registry := s.policy.RegistrySnapshot()
	invocation, admitted, err := s.admitter.AdmitNative(triggerrouter.NativeAdmission{
		Task: task.Ref, Workflow: pin, WorkflowDigest: digest,
		PolicyRevision: configuration.Revision, RegistryRevision: registry.Revision,
		AdmittedAt: task.Routing.AdmittedAt,
	})
	if err != nil {
		return StartResult{}, err
	}
	if err := s.reconciler.Reconcile(ctx); err != nil {
		return StartResult{}, fmt.Errorf("task service: reconcile admitted task: %w", err)
	}
	return StartResult{Task: task, Invocation: invocation, Admitted: admitted}, nil
}

func sameRoute(left, right taskstore.RoutingSnapshot) bool {
	left.AdmittedAt = time.Time{}
	right.AdmittedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}
