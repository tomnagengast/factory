package taskservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
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
	Snapshot() taskcontrol.Snapshot
	SetProject(uint64, string, bool, time.Time) (taskcontrol.Snapshot, error)
}

type Projects interface {
	ResolveSucceeded(string) (projectsetup.Spec, error)
	Choices() []projectsetup.Choice
}

type Catalog interface {
	ResolveRepository(string) (agentrun.RepositoryConfig, error)
}

type Tasks interface {
	Find(string) (taskstore.Task, bool)
	FindIdentifier(string) (taskstore.Task, bool)
	List(string, int) (taskstore.TaskPage, error)
	Messages(string, uint64, int) (taskstore.MessagePage, error)
	Links(string) ([]taskstore.Link, error)
	Gates(string) ([]taskstore.Gate, error)
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
	AdmitNativeContinuation(triggerrouter.NativeAdmission, string) (triggerrouter.Invocation, bool, error)
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
	Actor          taskstore.Actor
	TaskID         string
	IdempotencyKey string
}

type StartResult struct {
	Task       taskstore.Task
	Invocation triggerrouter.Invocation
	Admitted   bool
}

type ProjectChoice struct {
	projectsetup.Choice
	Enabled bool `json:"enabled"`
}

type Detail struct {
	Task     taskstore.Task        `json:"task"`
	Messages taskstore.MessagePage `json:"messages"`
	Links    []taskstore.Link      `json:"links"`
	Gates    []taskstore.Gate      `json:"gates"`
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

func (s *Service) Projects() []ProjectChoice {
	choices := s.projects.Choices()
	result := make([]ProjectChoice, len(choices))
	for index, choice := range choices {
		result[index] = ProjectChoice{Choice: choice, Enabled: s.control.Enabled(choice.ProjectID)}
	}
	return result
}

func (s *Service) Control() taskcontrol.Snapshot {
	return s.control.Snapshot()
}

func (s *Service) SetProject(expected uint64, projectID string, enabled bool) (taskcontrol.Snapshot, error) {
	if enabled {
		if _, err := s.projects.ResolveSucceeded(projectID); err != nil {
			return s.control.Snapshot(), fmt.Errorf("task service: resolve project: %w", err)
		}
	}
	return s.control.SetProject(expected, projectID, enabled, s.now())
}

func (s *Service) List(cursor string, limit int) (taskstore.TaskPage, error) {
	return s.tasks.List(cursor, limit)
}

func (s *Service) Detail(taskID string, after uint64, limit int) (Detail, error) {
	task, found := s.resolveTask(taskID)
	if !found {
		return Detail{}, taskstore.ErrNotFound
	}
	messages, err := s.tasks.Messages(task.Ref.ProviderID, after, limit)
	if err != nil {
		return Detail{}, err
	}
	links, err := s.tasks.Links(task.Ref.ProviderID)
	if err != nil {
		return Detail{}, err
	}
	gates, err := s.tasks.Gates(task.Ref.ProviderID)
	if err != nil {
		return Detail{}, err
	}
	// Empty collections stay JSON arrays, never null, for API and helper consumers.
	if messages.Messages == nil {
		messages.Messages = []taskstore.Message{}
	}
	if links == nil {
		links = []taskstore.Link{}
	}
	if gates == nil {
		gates = []taskstore.Gate{}
	}
	return Detail{Task: task, Messages: messages, Links: links, Gates: gates}, nil
}

func (s *Service) Update(ctx context.Context, command taskstore.UpdateCommand) (taskstore.Result, error) {
	return s.mutator.Execute(ctx, taskstore.UpdateEnvelope(command), s.now())
}

func (s *Service) Message(ctx context.Context, command taskstore.MessageCommand) (taskstore.Result, error) {
	result, err := s.mutator.Execute(ctx, taskstore.MessageEnvelope(command), s.now())
	if err != nil || result.Message == nil || command.Actor.Kind != taskstore.AuthorHuman || result.Task.State != taskstore.StateInProgress {
		return result, err
	}
	if _, _, err := s.continueTask(result.Task, "message:"+result.Message.ID); err != nil {
		return taskstore.Result{}, err
	}
	if err := s.reconciler.Reconcile(ctx); err != nil {
		return taskstore.Result{}, fmt.Errorf("task service: reconcile message continuation: %w", err)
	}
	return result, nil
}

func (s *Service) Link(ctx context.Context, command taskstore.LinkCommand) (taskstore.Result, error) {
	return s.mutator.Execute(ctx, taskstore.LinkEnvelope(command), s.now())
}

func (s *Service) Gate(ctx context.Context, command taskstore.GateCommand) (taskstore.Result, error) {
	return s.mutator.Execute(ctx, taskstore.GateEnvelope(command), s.now())
}

func (s *Service) Decide(ctx context.Context, command taskstore.DecisionCommand) (taskstore.Result, error) {
	result, err := s.mutator.Execute(ctx, taskstore.DecisionEnvelope(command), s.now())
	if err != nil || result.Gate == nil || command.Actor.Kind != taskstore.AuthorHuman || result.Task.State != taskstore.StateInProgress {
		return result, err
	}
	if _, _, err := s.continueTask(result.Task, "gate:"+result.Gate.ID+":"+result.Gate.Status); err != nil {
		return taskstore.Result{}, err
	}
	if err := s.reconciler.Reconcile(ctx); err != nil {
		return taskstore.Result{}, fmt.Errorf("task service: reconcile gate continuation: %w", err)
	}
	return result, nil
}

func (s *Service) State(ctx context.Context, command taskstore.StateCommand) (taskstore.Result, error) {
	return s.mutator.Execute(ctx, taskstore.StateEnvelope(command), s.now())
}

func (s *Service) Start(ctx context.Context, request StartRequest) (StartResult, error) {
	if request.IdempotencyKey == "" || request.IdempotencyKey != strings.TrimSpace(request.IdempotencyKey) || len(request.IdempotencyKey) > 128 {
		return StartResult{}, errors.New("task service: start idempotency key is invalid")
	}
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
	if err != nil {
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
		digest := sha256.Sum256([]byte(task.Ref.ProviderID + "\x00" + request.IdempotencyKey))
		result, err := s.mutator.Execute(ctx, taskstore.RoutingEnvelope(taskstore.RoutingCommand{
			Actor: request.Actor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision,
			Routing: route, IdempotencyKey: "native-start-route-v1:" + hex.EncodeToString(digest[:]),
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

func (s *Service) resolveTask(value string) (taskstore.Task, bool) {
	if task, found := s.tasks.Find(value); found {
		return task, true
	}
	return s.tasks.FindIdentifier(value)
}

func (s *Service) continueTask(task taskstore.Task, eventKey string) (triggerrouter.Invocation, bool, error) {
	if task.Routing == nil || task.Routing.WorkflowID != workflow.ProviderNeutralID {
		return triggerrouter.Invocation{}, false, ErrWorkflowUnavailable
	}
	configuration := s.policy.SettingsSnapshot()
	definition, found := configuration.Workflow(workflow.ProviderNeutralID)
	if !found || !definition.Enabled {
		return triggerrouter.Invocation{}, false, ErrWorkflowUnavailable
	}
	// Continuations pin the currently published reserved workflow, so a task
	// admitted under an older compiled revision keeps working after upgrades.
	digest, err := workflow.Digest(definition)
	if err != nil {
		return triggerrouter.Invocation{}, false, ErrWorkflowUnavailable
	}
	return s.admitter.AdmitNativeContinuation(triggerrouter.NativeAdmission{
		Task: task.Ref, Workflow: workflow.Pin(definition), WorkflowDigest: digest,
		PolicyRevision: configuration.Revision, RegistryRevision: s.policy.RegistrySnapshot().Revision,
		AdmittedAt: s.now().UTC(),
	}, eventKey)
}
