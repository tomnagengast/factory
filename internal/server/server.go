package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/linearidentity"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/triggerscheduler"
	"github.com/tomnagengast/factory/internal/workflow"
)

const (
	maxLinearWebhookBody  = 1 << 20
	maxGitHubWebhookBody  = 25 << 20
	replayWindow          = time.Minute
	defaultWirePage       = 1
	defaultWirePageSize   = 25
	maxWirePage           = 1_000_000
	maxWirePageSize       = 100
	attributeDeliveryID   = "deliveryId"
	attributeTriggerKind  = "triggerKind"
	attributeIssue        = "issueIdentifier"
	attributeProjectSetup = "projectSetup"
	maxSettingsBody       = 64 << 10
)

type EventStore interface {
	Add(deliveryID string, event activity.Event) (bool, error)
	StagePayload(deliveryID string, payload []byte) error
	AddStaged(deliveryID string, event activity.Event) (bool, error)
	StagedPayload(deliveryID string) ([]byte, error)
	Snapshot() activity.Snapshot
}

type ProjectSetupController interface {
	Enqueue(context.Context, projectsetup.Request) error
	PublicSnapshot() projectsetup.PublicSnapshot
}

type RunStore interface {
	Claim(trigger agentrun.Trigger, now time.Time) (agentrun.Run, bool, error)
	ClaimContinuation(claim agentrun.ContinuationClaim, now time.Time) (agentrun.Run, bool, error)
	PublicSnapshot() agentrun.PublicSnapshot
	ActivitySnapshot() agentrun.ActivitySnapshot
	FindObserverRun(source taskmodel.Source, taskIdentifier string, startedUnixMilli int64) (agentrun.Run, bool)
	Find(string) (agentrun.Run, bool)
	SchedulePullRequestReconcile(repository string, pullRequest int, headBranch, deliveryID string, cursor uint64, remediation bool, now time.Time) (bool, error)
}

type RunNotifier interface {
	Notify()
}

type GitHubEventStore interface {
	Add(githubhook.Event) (bool, error)
	AddAt(uint64, githubhook.Event) (bool, error)
	Total() uint64
}

type LinearCommentStore interface {
	Add(linearhook.Event) (bool, error)
	AddAt(uint64, linearhook.Event) (bool, error)
	Total() uint64
}

type AgentObserver interface {
	Observe(context.Context, string) (agentrun.AgentView, error)
}

type SettingsStore interface {
	Snapshot() settings.Snapshot
	Update(uint64, settings.Snapshot, time.Time) (settings.Snapshot, error)
}

type WorkflowDraftStore interface {
	Snapshot() workflow.DraftSnapshot
	Draft(string) (workflow.Draft, bool)
	Create(workflow.Draft) (workflow.Draft, error)
	Save(string, uint64, uint64, workflow.Draft, time.Time) (workflow.Draft, error)
	Materialize(workflow.Draft, uint64) (workflow.Draft, error)
	Discard(string, uint64, uint64) error
	AdvanceBase(string, uint64, uint64, uint64, time.Time) (workflow.Draft, error)
}

type EventWire interface {
	Handle(eventwire.Filter, eventwire.Handler) error
	Publish(context.Context, eventwire.Event) (eventwire.Record, bool, error)
	Status() eventwire.Status
	Query(eventwire.Query) (eventwire.Page, error)
	Record(uint64) (eventwire.Record, bool)
}

type TriggerPolicy interface {
	RegistrySnapshot() triggerregistry.Snapshot
	SettingsSnapshot() settings.Snapshot
	RoutingSnapshot() triggerrouter.Snapshot
	UpdateRegistry(uint64, uint64, triggerregistry.Snapshot, time.Time) (triggerregistry.Snapshot, error)
	UpdateSettings(uint64, settings.Snapshot, time.Time) (settings.Snapshot, error)
	UpdateAgentSettings(uint64, settings.AgentSettings, settings.RuntimeSettings, time.Time) (settings.Snapshot, error)
	PublishWorkflow(uint64, uint64, workflow.Definition, time.Time) (settings.Snapshot, error)
	DeleteWorkflow(uint64, uint64, string, time.Time) (settings.Snapshot, error)
	UpdateProtectedFeedback(uint64, string, time.Time) (settings.Snapshot, error)
}

type ScheduleStatus interface {
	Statuses(time.Time) []triggerscheduler.Status
}

type TaskController interface {
	Projects() []taskservice.ProjectChoice
	Control() taskcontrol.Snapshot
	SetProject(uint64, string, bool) (taskcontrol.Snapshot, error)
	List(string, int) (taskstore.TaskPage, error)
	Detail(string, uint64, int) (taskservice.Detail, error)
	Create(context.Context, taskservice.CreateRequest) (taskstore.Result, error)
	Update(context.Context, taskstore.UpdateCommand) (taskstore.Result, error)
	Message(context.Context, taskstore.MessageCommand) (taskstore.Result, error)
	Link(context.Context, taskstore.LinkCommand) (taskstore.Result, error)
	Gate(context.Context, taskstore.GateCommand) (taskstore.Result, error)
	Decide(context.Context, taskstore.DecisionCommand) (taskstore.Result, error)
	State(context.Context, taskstore.StateCommand) (taskstore.Result, error)
	Start(context.Context, taskservice.StartRequest) (taskservice.StartResult, error)
}

type LinearTaskController interface {
	Detail(context.Context, string) (taskservice.LinearIssue, error)
	Comment(context.Context, string, string, string, string, string) (taskservice.LinearIssue, error)
	Link(context.Context, string, string, string) (taskservice.LinearIssue, error)
	State(context.Context, string, string) (taskservice.LinearIssue, error)
	Gate(context.Context, string, string, string, string, string) (taskservice.LinearIssue, error)
}

type LinearIdentityBinder interface {
	Bind(identifier, uuid string) (bool, error)
}

type ViewerAuthenticator interface {
	Page(http.Handler) http.Handler
	API(http.Handler) http.Handler
	Login(http.ResponseWriter, *http.Request)
	Callback(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
	Actor(*http.Request) (taskstore.Actor, bool)
}

type Config struct {
	Web                fs.FS
	ActivityStore      EventStore
	RunStore           RunStore
	RunNotifier        RunNotifier
	AgentObserver      AgentObserver
	Settings           SettingsStore
	WorkflowDrafts     WorkflowDraftStore
	WorkflowDraftError string
	ViewerAuth         ViewerAuthenticator
	LinearSecret       []byte
	GitHubSecret       []byte
	Events             EventWire
	GitHubEvents       GitHubEventStore
	LinearComments     LinearCommentStore
	TriggerActor       string
	RepositoryResolver agentrun.RepositoryResolver
	ProjectSetups      ProjectSetupController
	Now                func() time.Time
	Build              BuildIdentity
	GenericTriggers    bool
	TriggerPolicy      TriggerPolicy
	ScheduleStatus     ScheduleStatus
	Tasks              TaskController
	LinearTasks        LinearTaskController
	LinearIdentities   LinearIdentityBinder
	TaskStatus         func() taskstore.Status
	Ready              func() bool
}

type appServer struct {
	activityStore      EventStore
	runStore           RunStore
	runNotifier        RunNotifier
	agentObserver      AgentObserver
	settings           SettingsStore
	workflowDrafts     WorkflowDraftStore
	workflowDraftError string
	workflowLocks      keyedWorkflowLocks
	viewerAuth         ViewerAuthenticator
	linearSecret       []byte
	githubSecret       []byte
	events             EventWire
	githubEvents       GitHubEventStore
	linearComments     LinearCommentStore
	triggerActor       string
	repositoryResolver agentrun.RepositoryResolver
	projectSetups      ProjectSetupController
	now                func() time.Time
	build              BuildIdentity
	genericTriggers    bool
	triggerPolicy      TriggerPolicy
	scheduleStatus     ScheduleStatus
	tasks              TaskController
	linearTasks        LinearTaskController
	linearIdentities   LinearIdentityBinder
	taskStatus         func() taskstore.Status
	ready              func() bool
}

type BuildIdentity struct {
	Commit          string    `json:"commit"`
	Tree            string    `json:"tree"`
	BuildID         string    `json:"buildId"`
	DeploymentID    string    `json:"deploymentId"`
	ContractVersion string    `json:"contractVersion"`
	StartedAt       time.Time `json:"startedAt"`
}

type legacyRepositoryResolver struct{}

func (legacyRepositoryResolver) Resolve(context.Context, string) (agentrun.RepositoryConfig, error) {
	return agentrun.RepositoryConfig{
		App:            "network",
		Repository:     "tomnagengast/network",
		RepoURL:        "git@github.com:tomnagengast/network.git",
		RepoPath:       "/tmp/factory-network",
		ManagedRoot:    "/tmp",
		ProjectPath:    "/tmp/factory-network",
		BaseBranch:     "main",
		ReceiptPath:    "/tmp/factory-receipt",
		PendingReceipt: "/tmp/factory-pending",
		HealthURL:      "http://127.0.0.1:8090/healthz",
	}, nil
}

type healthResponse struct {
	Status        string                      `json:"status"`
	App           string                      `json:"app"`
	Wire          wireHealthStatus            `json:"wire"`
	Tasks         taskstore.Status            `json:"tasks"`
	ProjectSetups projectsetup.PublicSnapshot `json:"projectSetups"`
	BuildIdentity
}

type wireHealthStatus struct {
	Total         uint64               `json:"total"`
	Dispatched    uint64               `json:"dispatched"`
	Pending       uint64               `json:"pending"`
	RejectedTotal uint64               `json:"rejectedTotal"`
	LastRejection *wireHealthRejection `json:"lastRejection,omitempty"`
}

type wireHealthRejection struct {
	Sequence   uint64           `json:"sequence"`
	EventID    string           `json:"eventId"`
	Source     eventwire.Source `json:"source"`
	Type       string           `json:"type"`
	Action     string           `json:"action"`
	RejectedAt time.Time        `json:"rejectedAt"`
}

type linearPayload struct {
	Type             string `json:"type"`
	Action           string `json:"action"`
	URL              string `json:"url"`
	WebhookTimestamp int64  `json:"webhookTimestamp"`
	Actor            struct {
		ID string `json:"id"`
	} `json:"actor"`
	Data struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Body        string   `json:"body"`
		IssueID     string   `json:"issueId"`
		ParentID    string   `json:"parentId"`
		Identifier  string   `json:"identifier"`
		LabelIDs    []string `json:"labelIds"`
		Labels      []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"labels"`
		Issue struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
		} `json:"issue"`
	} `json:"data"`
	UpdatedFrom struct {
		LabelIDs json.RawMessage `json:"labelIds"`
	} `json:"updatedFrom"`
}

type homeResponse struct {
	Status         string                  `json:"status"`
	Total          uint64                  `json:"total"`
	LastReceivedAt *time.Time              `json:"lastReceivedAt"`
	Events         []activity.Event        `json:"events"`
	AgentRuns      agentrun.PublicSnapshot `json:"agentRuns"`
}

type wireDetailResponse struct {
	Record           eventwire.Record `json:"record"`
	PayloadAvailable bool             `json:"payloadAvailable"`
	Payload          json.RawMessage  `json:"payload,omitempty"`
}

type settingsResponse struct {
	Revision  uint64                   `json:"revision"`
	UpdatedAt time.Time                `json:"updatedAt,omitempty"`
	Agents    settings.AgentSettings   `json:"agents"`
	Runtime   settings.RuntimeSettings `json:"runtime"`
}

func New(config Config) (http.Handler, error) {
	if config.ActivityStore == nil {
		return nil, errors.New("server: activity store is required")
	}
	if len(config.LinearSecret) == 0 {
		return nil, errors.New("server: Linear webhook secret is required")
	}
	if len(config.GitHubSecret) == 0 {
		return nil, errors.New("server: GitHub webhook secret is required")
	}
	if config.Events == nil {
		return nil, errors.New("server: event wire is required")
	}
	if config.GitHubEvents == nil {
		return nil, errors.New("server: GitHub event store is required")
	}
	if config.LinearComments == nil {
		return nil, errors.New("server: Linear comment store is required")
	}
	if config.LinearIdentities == nil {
		return nil, errors.New("server: Linear identity binder is required")
	}
	if config.RunStore == nil {
		return nil, errors.New("server: agent run store is required")
	}
	if config.RunNotifier == nil {
		return nil, errors.New("server: agent run notifier is required")
	}
	if config.AgentObserver == nil {
		return nil, errors.New("server: agent observer is required")
	}
	if config.Settings == nil {
		return nil, errors.New("server: settings store is required")
	}
	if config.ViewerAuth == nil {
		return nil, errors.New("server: viewer authenticator is required")
	}
	if config.TriggerActor == "" {
		return nil, errors.New("server: Linear trigger actor is required")
	}
	if config.ProjectSetups == nil {
		return nil, errors.New("server: project setup controller is required")
	}
	if config.Now == nil {
		return nil, errors.New("server: clock is required")
	}
	if config.Build.Commit == "" || config.Build.Tree == "" || config.Build.BuildID == "" || config.Build.DeploymentID == "" || config.Build.ContractVersion == "" || config.Build.StartedAt.IsZero() {
		return nil, errors.New("server: build identity is required")
	}
	if config.RepositoryResolver == nil {
		config.RepositoryResolver = legacyRepositoryResolver{}
	}
	if config.Ready == nil {
		config.Ready = func() bool { return true }
	}
	if config.TaskStatus == nil {
		config.TaskStatus = func() taskstore.Status { return taskstore.Status{Healthy: true} }
	}
	if config.GenericTriggers && (config.TriggerPolicy == nil || config.ScheduleStatus == nil) {
		return nil, errors.New("server: generic trigger policy and schedule status are required")
	}
	app := &appServer{
		activityStore:      config.ActivityStore,
		runStore:           config.RunStore,
		runNotifier:        config.RunNotifier,
		agentObserver:      config.AgentObserver,
		settings:           config.Settings,
		workflowDrafts:     config.WorkflowDrafts,
		workflowDraftError: config.WorkflowDraftError,
		workflowLocks:      newKeyedWorkflowLocks(),
		viewerAuth:         config.ViewerAuth,
		linearSecret:       config.LinearSecret,
		githubSecret:       config.GitHubSecret,
		events:             config.Events,
		githubEvents:       config.GitHubEvents,
		linearComments:     config.LinearComments,
		triggerActor:       config.TriggerActor,
		repositoryResolver: config.RepositoryResolver,
		projectSetups:      config.ProjectSetups,
		now:                config.Now,
		build:              config.Build,
		genericTriggers:    config.GenericTriggers,
		triggerPolicy:      config.TriggerPolicy,
		scheduleStatus:     config.ScheduleStatus,
		tasks:              config.Tasks,
		linearTasks:        config.LinearTasks,
		linearIdentities:   config.LinearIdentities,
		taskStatus:         config.TaskStatus,
		ready:              config.Ready,
	}
	if err := app.events.Handle(eventwire.Filter{Source: eventwire.SourceLinear}, app.dispatchLinear); err != nil {
		return nil, err
	}
	if err := app.events.Handle(eventwire.Filter{Source: eventwire.SourceGitHub}, app.dispatchGitHub); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", app.healthz)
	mux.HandleFunc("POST /api/agent/task", app.agentTask)
	mux.HandleFunc("GET /api/home", app.home)
	mux.Handle("GET /api/wire", app.viewerAuth.API(http.HandlerFunc(app.wire)))
	mux.Handle("GET /api/wire/{sequence}", app.viewerAuth.API(http.HandlerFunc(app.wireEvent)))
	mux.Handle("GET /api/agents", app.viewerAuth.API(http.HandlerFunc(app.agents)))
	mux.Handle("GET /api/agents/{identifier}/{started}/run", canonicalAgentReference(app.viewerAuth.API(http.HandlerFunc(app.agentByReference))))
	mux.Handle("GET /api/settings", app.viewerAuth.API(http.HandlerFunc(app.getSettings)))
	mux.Handle("PUT /api/settings", app.viewerAuth.API(http.HandlerFunc(app.putSettings)))
	mux.Handle("GET /api/triggers", app.viewerAuth.API(http.HandlerFunc(app.getTriggers)))
	mux.Handle("PUT /api/triggers", app.viewerAuth.API(http.HandlerFunc(app.putTriggers)))
	mux.Handle("PUT /api/triggers/protected/linear-feedback", app.viewerAuth.API(http.HandlerFunc(app.putProtectedFeedback)))
	mux.Handle("GET /api/workflows", app.viewerAuth.API(http.HandlerFunc(app.getWorkflows)))
	mux.Handle("GET /api/tasks", app.viewerAuth.API(http.HandlerFunc(app.getTasks)))
	mux.Handle("GET /api/task-projects", app.viewerAuth.API(http.HandlerFunc(app.getTaskProjects)))
	mux.Handle("PUT /api/task-projects/{id}", app.viewerAuth.API(http.HandlerFunc(app.putTaskProject)))
	mux.Handle("POST /api/tasks", app.viewerAuth.API(http.HandlerFunc(app.postTask)))
	mux.Handle("GET /api/tasks/{provider}/{id}", app.viewerAuth.API(http.HandlerFunc(app.getTask)))
	mux.Handle("PATCH /api/tasks/{provider}/{id}", app.viewerAuth.API(http.HandlerFunc(app.patchTask)))
	mux.Handle("POST /api/tasks/{provider}/{id}/messages", app.viewerAuth.API(http.HandlerFunc(app.postTaskMessage)))
	mux.Handle("POST /api/tasks/{provider}/{id}/links", app.viewerAuth.API(http.HandlerFunc(app.postTaskLink)))
	mux.Handle("POST /api/tasks/{provider}/{id}/gates", app.viewerAuth.API(http.HandlerFunc(app.postTaskGate)))
	mux.Handle("POST /api/tasks/{provider}/{id}/gates/{gateID}/decision", app.viewerAuth.API(http.HandlerFunc(app.postTaskGateDecision)))
	mux.Handle("POST /api/tasks/{provider}/{id}/state", app.viewerAuth.API(http.HandlerFunc(app.postTaskState)))
	mux.Handle("POST /api/tasks/{provider}/{id}/start", app.viewerAuth.API(http.HandlerFunc(app.postTaskStart)))
	mux.Handle("POST /api/workflow-drafts", app.viewerAuth.API(http.HandlerFunc(app.postWorkflowDraft)))
	mux.Handle("PUT /api/workflow-drafts/{id}", app.viewerAuth.API(http.HandlerFunc(app.putWorkflowDraft)))
	mux.Handle("DELETE /api/workflow-drafts/{id}", app.viewerAuth.API(http.HandlerFunc(app.deleteWorkflowDraft)))
	mux.Handle("POST /api/workflow-drafts/{id}/publish", app.viewerAuth.API(http.HandlerFunc(app.publishWorkflowDraft)))
	mux.Handle("DELETE /api/workflows/{id}", app.viewerAuth.API(http.HandlerFunc(app.deleteWorkflow)))
	mux.HandleFunc("POST /api/webhooks/linear", app.linearWebhook)
	mux.HandleFunc("POST /api/webhooks/github", app.githubWebhook)
	mux.HandleFunc("POST /cdn-cgi/rum", cloudflareBeacon)
	mux.HandleFunc("GET /auth/google/login", app.viewerAuth.Login)
	mux.HandleFunc("GET /auth/google/callback", app.viewerAuth.Callback)
	mux.HandleFunc("GET /auth/logout", app.viewerAuth.Logout)
	page := frontendPage(config.Web)
	mux.Handle("GET /{$}", page)
	mux.Handle("GET /home", page)
	mux.Handle("GET /wire", app.viewerAuth.Page(page))
	mux.Handle("GET /agents", app.viewerAuth.Page(page))
	mux.Handle("GET /tasks", app.viewerAuth.Page(page))
	mux.Handle("GET /tasks/{provider}/{id}", app.viewerAuth.Page(page))
	mux.Handle("GET /agents/{identifier}/{started}/run", canonicalAgentReference(app.viewerAuth.Page(page)))
	mux.Handle("GET /settings", app.viewerAuth.Page(page))
	mux.Handle("GET /triggers", app.viewerAuth.Page(page))
	mux.Handle("GET /workflows", app.viewerAuth.Page(page))
	mux.Handle("GET /{asset...}", http.FileServerFS(config.Web))
	return canonicalPaths(mux), nil
}

func (s *appServer) healthz(w http.ResponseWriter, _ *http.Request) {
	status := s.events.Status()
	wire := wireHealthStatus{
		Total: status.Total, Dispatched: status.Dispatched, Pending: status.Pending,
		RejectedTotal: status.RejectedTotal,
	}
	if status.LastRejection != nil {
		wire.LastRejection = &wireHealthRejection{
			Sequence: status.LastRejection.Sequence, EventID: status.LastRejection.EventID,
			Source: status.LastRejection.Source, Type: status.LastRejection.Type,
			Action: status.LastRejection.Action, RejectedAt: status.LastRejection.RejectedAt,
		}
	}
	setups := s.projectSetups.PublicSnapshot()
	tasks := s.taskStatus()
	health := healthResponse{Status: "ok", App: "factory", Wire: wire, Tasks: tasks, ProjectSetups: setups, BuildIdentity: s.build}
	httpStatus := http.StatusOK
	if !s.ready() || status.Pending > 0 || setups.Failed > 0 || !tasks.Healthy {
		health.Status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}
	writeJSON(w, httpStatus, health)
}

func cloudflareBeacon(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *appServer) home(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.activityStore.Snapshot()
	response := homeResponse{
		Status:    "listening",
		Total:     snapshot.Total,
		Events:    snapshot.Events,
		AgentRuns: s.runStore.PublicSnapshot(),
	}
	if len(snapshot.Events) > 0 {
		response.LastReceivedAt = &snapshot.Events[0].ReceivedAt
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *appServer) wire(w http.ResponseWriter, r *http.Request) {
	page, err := queryInt(r, "page", defaultWirePage, 1, maxWirePage)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	pageSize, err := queryInt(r, "pageSize", defaultWirePageSize, 1, maxWirePageSize)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	source := eventwire.Source(r.URL.Query().Get("source"))
	if source != "" && !eventwire.ValidSource(source) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	eventType := r.URL.Query().Get("type")
	if len(eventType) > 256 || eventType != strings.TrimSpace(eventType) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	snapshot, err := s.events.Query(eventwire.Query{Source: source, Type: eventType, Page: page, PageSize: pageSize})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *appServer) wireEvent(w http.ResponseWriter, r *http.Request) {
	sequence, err := strconv.ParseUint(r.PathValue("sequence"), 10, 64)
	if err != nil || sequence < 1 {
		http.NotFound(w, r)
		return
	}
	event, found := s.events.Record(sequence)
	if !found {
		http.NotFound(w, r)
		return
	}
	response := wireDetailResponse{Record: event}
	if event.Event.Source == eventwire.SourceLinear {
		deliveryIDs := event.Event.Values(attributeDeliveryID)
		if len(deliveryIDs) > 0 {
			payload, err := s.activityStore.StagedPayload(deliveryIDs[0])
			if err == nil {
				response.PayloadAvailable = true
				response.Payload = json.RawMessage(payload)
			} else if !errors.Is(err, os.ErrNotExist) {
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *appServer) agents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.runStore.ActivitySnapshot())
}

func (s *appServer) agentByReference(w http.ResponseWriter, r *http.Request) {
	taskIdentifier := strings.ToUpper(r.PathValue("identifier"))
	if !taskmodel.ValidDisplayIdentifier(taskIdentifier) {
		http.NotFound(w, r)
		return
	}
	started, err := strconv.ParseInt(r.PathValue("started"), 10, 64)
	if err != nil || started < 1 {
		http.NotFound(w, r)
		return
	}
	source, ok := observerTaskSource(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	run, found := s.runStore.FindObserverRun(source, taskIdentifier, started)
	if !found {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	s.writeAgent(w, r, run.ID)
}

func (s *appServer) writeAgent(w http.ResponseWriter, r *http.Request, id string) {
	view, err := s.agentObserver.Observe(r.Context(), id)
	if errors.Is(err, agentrun.ErrRunNotFound) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("observe agent run", "run_id", id, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *appServer) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, publicSettings(s.settings.Snapshot()))
}

func (s *appServer) putSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireReady(w) {
		return
	}
	if !sameOrigin(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody))
	decoder.DisallowUnknownFields()
	var candidate settingsResponse
	if err := decoder.Decode(&candidate); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	var updated settings.Snapshot
	validation := s.settings.Snapshot()
	validation.Agents = candidate.Agents
	validation.Runtime = candidate.Runtime
	if err := validation.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.triggerPolicy != nil {
		updated, err = s.triggerPolicy.UpdateAgentSettings(candidate.Revision, candidate.Agents, candidate.Runtime, s.now())
	} else {
		updated, err = s.settings.Update(candidate.Revision, validation, s.now())
	}
	if errors.Is(err, settings.ErrRevisionConflict) {
		writeJSON(w, http.StatusConflict, publicSettings(updated))
		return
	}
	if errors.Is(err, triggerrouter.ErrPolicyValidation) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if errors.Is(err, triggerrouter.ErrPolicyPending) {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		slog.Error("update settings", "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, publicSettings(updated))
}

func publicSettings(snapshot settings.Snapshot) settingsResponse {
	return settingsResponse{Revision: snapshot.Revision, UpdatedAt: snapshot.UpdatedAt, Agents: snapshot.Agents, Runtime: snapshot.Runtime}
}

func sameOrigin(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" &&
		strings.EqualFold(parsed.Host, r.Host)
}

func (s *appServer) linearWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.requireReady(w) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLinearWebhookBody))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}
	if !validSignature(s.linearSecret, body, r.Header.Get("Linear-Signature")) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	var payload linearPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if payload.Type == "" || len(payload.Type) > 64 || payload.Action == "" || len(payload.Action) > 64 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	deliveryID := r.Header.Get("Linear-Delivery")
	if deliveryID == "" || len(deliveryID) > 128 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	now := s.now().UTC()
	sentAt := time.UnixMilli(payload.WebhookTimestamp)
	if sentAt.Before(now.Add(-replayWindow)) || sentAt.After(now.Add(replayWindow)) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	identifier, uuid, hasIdentity, err := linearPayloadIdentity(payload)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if hasIdentity {
		if _, err := s.linearIdentities.Bind(identifier, uuid); err != nil {
			if errors.Is(err, linearidentity.ErrConflict) {
				http.Error(w, http.StatusText(http.StatusConflict), http.StatusConflict)
				return
			}
			slog.Error("bind Linear webhook identity", "identifier", identifier, "error", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
	}
	wake, hasWake := commentWake(payload, deliveryID, s.triggerActor, now)
	configuration := s.settings.Snapshot()
	trigger, hasTrigger := agentTrigger(payload, deliveryID, s.triggerActor, configuration.Triggers.LinearLabel)
	setupProject := payload.Type == "Project" && (payload.Action == "create" || payload.Action == "update") && payload.Actor.ID == s.triggerActor
	event := linearWireEvent(payload, deliveryID, wake, hasWake, trigger, hasTrigger, setupProject, now)
	if err := s.activityStore.StagePayload(deliveryID, body); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if _, _, err := s.events.Publish(r.Context(), event); err != nil {
		slog.Error("publish Linear event", "delivery_id", deliveryID, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func linearPayloadIdentity(payload linearPayload) (string, string, bool, error) {
	identifier, uuid := "", ""
	switch payload.Type {
	case "Issue":
		identifier, uuid = payload.Data.Identifier, payload.Data.ID
	case "Comment":
		identifier = payload.Data.Issue.Identifier
		uuid = payload.Data.IssueID
		if uuid == "" {
			uuid = payload.Data.Issue.ID
		} else if payload.Data.Issue.ID != "" && payload.Data.Issue.ID != uuid {
			return "", "", false, errors.New("Linear webhook issue UUID fields conflict")
		}
	default:
		return "", "", false, nil
	}
	identifier = strings.ToUpper(strings.TrimSpace(identifier))
	uuid = strings.TrimSpace(uuid)
	if identifier == "" && uuid == "" {
		return "", "", false, nil
	}
	if identifier == "" || uuid == "" {
		return "", "", false, nil
	}
	return identifier, uuid, true, nil
}

func commentWake(payload linearPayload, deliveryID, allowedActorID string, now time.Time) (linearhook.Event, bool) {
	if payload.Type != "Comment" || payload.Action != "create" || payload.Actor.ID != allowedActorID || linearhook.FactoryAuthored(payload.Data.Body) {
		return linearhook.Event{}, false
	}
	issueID := payload.Data.IssueID
	if issueID == "" {
		issueID = payload.Data.Issue.ID
	}
	identifier := strings.ToUpper(strings.TrimSpace(payload.Data.Issue.Identifier))
	if !agentrun.ValidIssueIdentifier(identifier) {
		identifier = ""
	}
	if payload.Data.ID == "" || issueID == "" || identifier == "" {
		return linearhook.Event{}, false
	}
	return linearhook.Event{
		DeliveryID:      deliveryID,
		CommentID:       payload.Data.ID,
		IssueID:         issueID,
		IssueIdentifier: identifier,
		ParentID:        payload.Data.ParentID,
		URL:             payload.URL,
		ReceivedAt:      now,
	}, true
}

func (s *appServer) githubWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.requireReady(w) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitHubWebhookBody))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}
	if !validGitHubSignature(s.githubSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	deliveryID := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	eventType := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	if deliveryID == "" || len(deliveryID) > 128 || eventType == "" || len(eventType) > 64 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	now := s.now().UTC()
	event, err := githubhook.Parse(deliveryID, eventType, body, now)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	normalized := githubhook.ToWire(event)
	if _, ok := githubhook.FromWire(normalized); !ok {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if _, _, err := s.events.Publish(r.Context(), normalized); err != nil {
		slog.Error("publish GitHub event", "delivery_id", deliveryID, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *appServer) requireReady(w http.ResponseWriter) bool {
	if s.ready() {
		return true
	}
	w.Header().Set("Retry-After", "5")
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
	return false
}

func linearWireEvent(
	payload linearPayload,
	deliveryID string,
	wake linearhook.Event,
	hasWake bool,
	trigger agentrun.Trigger,
	hasTrigger bool,
	setupProject bool,
	now time.Time,
) eventwire.Event {
	identifier := strings.ToUpper(strings.TrimSpace(payload.Data.Identifier))
	if identifier == "" {
		identifier = strings.ToUpper(strings.TrimSpace(payload.Data.Issue.Identifier))
	}
	if identifier == "" && payload.Type == "Project" {
		identifier = strings.TrimSpace(payload.Data.ID)
	}
	event := eventwire.Event{
		ID:         "linear:" + deliveryID,
		Source:     eventwire.SourceLinear,
		Type:       payload.Type,
		Action:     payload.Action,
		Subject:    identifier,
		Attributes: map[string][]string{attributeDeliveryID: {deliveryID}},
		ReceivedAt: now,
	}
	event.Attributes[triggerregistry.AttributeActorID] = []string{payload.Actor.ID}
	event.Attributes[triggerregistry.AttributeProducer] = []string{"linear-webhook"}
	provenance := "human"
	if linearhook.FactoryAuthored(payload.Data.Body) {
		provenance = "factory"
	}
	event.Attributes[triggerregistry.AttributeProvenance] = []string{provenance}
	addedLabelIDs, addedLabelNames := addedLabels(payload)
	if len(addedLabelIDs) > 0 {
		event.Attributes["addedLabelId"] = addedLabelIDs
		event.Attributes[triggerregistry.AttributeAddedLabel] = addedLabelNames
	}
	if hasWake {
		metadata := event.Attributes
		event = linearhook.ToWire(wake)
		for key, values := range metadata {
			event.Attributes[key] = values
		}
		event.Attributes[attributeIssue] = []string{wake.IssueIdentifier}
		event.Attributes[attributeTriggerKind] = []string{agentrun.TriggerKindComment}
	}
	if hasTrigger {
		event.Attributes[attributeTriggerKind] = []string{trigger.Kind}
		event.Attributes[attributeIssue] = []string{trigger.IssueIdentifier}
	}
	if setupProject {
		event.Attributes[attributeProjectSetup] = []string{"true"}
	}
	return event
}

func (s *appServer) dispatchLinear(ctx context.Context, record eventwire.Record) error {
	deliveryID := firstAttribute(record.Event, attributeDeliveryID)
	if deliveryID == "" {
		return errors.New("server: Linear event delivery ID is missing")
	}
	if _, err := s.activityStore.AddStaged(deliveryID, activity.Event{
		Type:       record.Event.Type,
		Action:     record.Event.Action,
		ReceivedAt: record.Event.ReceivedAt,
	}); err != nil {
		return fmt.Errorf("server: project Linear activity: %w", err)
	}
	if firstAttribute(record.Event, attributeProjectSetup) == "true" {
		payloadBody, err := s.activityStore.StagedPayload(deliveryID)
		if err != nil {
			return fmt.Errorf("server: read Linear project payload: %w", err)
		}
		var payload linearPayload
		if err := json.Unmarshal(payloadBody, &payload); err != nil {
			return fmt.Errorf("server: decode Linear project payload: %w", err)
		}
		if payload.Type != record.Event.Type || payload.Action != record.Event.Action || payload.Data.ID != record.Event.Subject {
			return errors.New("server: normalized Linear project event does not match its staged payload")
		}
		if err := s.projectSetups.Enqueue(ctx, projectsetup.Request{
			ProjectID: payload.Data.ID, ProjectName: payload.Data.Name, Description: payload.Data.Description,
		}); err != nil {
			return fmt.Errorf("server: enqueue Linear project setup: %w", err)
		}
	}

	if event, ok := linearhook.FromWire(record.Event); ok {
		if sequence := record.ChannelSequences[linearhook.WireChannel]; sequence > s.linearComments.Total() {
			if _, err := s.linearComments.AddAt(sequence, event); err != nil {
				return fmt.Errorf("server: project Linear comment: %w", err)
			}
		}
		if firstAttribute(record.Event, attributeTriggerKind) == agentrun.TriggerKindComment {
			configuration := s.settings.Snapshot()
			definition, err := configuration.WorkflowForTrigger(agentrun.TriggerKindComment)
			if err != nil {
				return fmt.Errorf("server: select Linear continuation workflow: %w", err)
			}
			pinned := workflow.Pin(definition)
			digest, err := pinned.Digest()
			if err != nil {
				return fmt.Errorf("server: digest Linear continuation workflow: %w", err)
			}
			if marker, ok := s.settings.(interface {
				MarkWorkflowRollbackIncompatible(time.Time) (settings.Snapshot, error)
			}); ok {
				configuration, err = marker.MarkWorkflowRollbackIncompatible(record.Event.ReceivedAt)
				if err != nil {
					return fmt.Errorf("server: mark workflow rollback boundary: %w", err)
				}
			}
			trigger, err := s.repositoryTrigger(ctx, deliveryID, event.IssueIdentifier, agentrun.TriggerKindComment)
			if err != nil {
				return fmt.Errorf("server: resolve Linear continuation repository: %w", err)
			}
			run, created, err := s.runStore.ClaimContinuation(agentrun.ContinuationClaim{
				Trigger: trigger, Workflow: pinned, WorkflowDigest: digest, PolicyRevision: configuration.Revision,
			}, record.Event.ReceivedAt)
			if err != nil {
				return fmt.Errorf("server: claim Linear continuation: %w", err)
			}
			if created || (run.State == agentrun.StatePending && run.ResumeCount > 0) {
				s.runNotifier.Notify()
			}
		}
	}
	if !s.genericTriggers && firstAttribute(record.Event, attributeTriggerKind) == agentrun.TriggerKindLabel {
		trigger, err := s.repositoryTrigger(ctx, deliveryID, firstAttribute(record.Event, attributeIssue), agentrun.TriggerKindLabel)
		if err != nil {
			return fmt.Errorf("server: resolve Linear run repository: %w", err)
		}
		_, created, err := s.runStore.Claim(trigger, record.Event.ReceivedAt)
		if err != nil {
			return fmt.Errorf("server: claim Linear run: %w", err)
		}
		if created {
			s.runNotifier.Notify()
		}
	}

	return nil
}

func addedLabels(payload linearPayload) ([]string, []string) {
	var previous []string
	if len(payload.UpdatedFrom.LabelIDs) == 0 || json.Unmarshal(payload.UpdatedFrom.LabelIDs, &previous) != nil {
		return nil, nil
	}
	previousSet := make(map[string]bool, len(previous))
	for _, id := range previous {
		previousSet[id] = true
	}
	current := make(map[string]bool, len(payload.Data.LabelIDs))
	for _, id := range payload.Data.LabelIDs {
		current[id] = true
	}
	var ids []string
	var names []string
	for _, label := range payload.Data.Labels {
		if label.ID == "" || !current[label.ID] || previousSet[label.ID] {
			continue
		}
		ids = append(ids, label.ID)
		names = append(names, triggerregistry.CanonicalFold(label.Name))
	}
	return ids, names
}

func (s *appServer) repositoryTrigger(ctx context.Context, deliveryID, issueIdentifier, kind string) (agentrun.Trigger, error) {
	config, err := s.repositoryResolver.Resolve(ctx, issueIdentifier)
	if err != nil {
		return agentrun.Trigger{}, err
	}
	return agentrun.Trigger{
		DeliveryID:      deliveryID,
		IssueIdentifier: issueIdentifier,
		Kind:            kind,
		Repository:      config.Repository,
		RepositoryURL:   config.RepoURL,
		RepositoryPath:  config.RepoPath,
		ManagedRoot:     config.ManagedRoot,
		BaseBranch:      config.BaseBranch,
		Bootstrap:       config.Bootstrap,
		CloudURL:        config.CloudURL,
	}, nil
}

func (s *appServer) dispatchGitHub(_ context.Context, record eventwire.Record) error {
	event, ok := githubhook.FromWire(record.Event)
	if !ok {
		return errors.New("server: invalid normalized GitHub event")
	}
	if sequence := record.ChannelSequences[githubhook.WireChannel]; sequence > s.githubEvents.Total() {
		if _, err := s.githubEvents.AddAt(sequence, event); err != nil {
			return fmt.Errorf("server: project GitHub wake: %w", err)
		}
	}
	if _, err := s.activityStore.Add("github:"+event.DeliveryID, activity.Event{
		Type:       "github/" + event.Type,
		Action:     event.Action,
		ReceivedAt: event.ReceivedAt,
	}); err != nil {
		return fmt.Errorf("server: project GitHub activity: %w", err)
	}
	remediation := githubWakeRequiresRemediation(event)
	pullRequests := event.PullRequests
	if len(pullRequests) == 0 && event.HeadBranch != "" {
		pullRequests = []int{0}
	}
	for _, pullRequest := range pullRequests {
		scheduled, err := s.runStore.SchedulePullRequestReconcile(event.Repository, pullRequest, event.HeadBranch, event.DeliveryID, record.Sequence, remediation, event.ReceivedAt)
		if err != nil {
			return fmt.Errorf("server: schedule pull request reconciliation: %w", err)
		}
		if scheduled {
			s.runNotifier.Notify()
		}
	}
	return nil
}

func githubWakeRequiresRemediation(event githubhook.Event) bool {
	switch event.Type {
	case "issue_comment", "pull_request_review", "pull_request_review_comment":
		return event.Action == "created" || event.Action == "edited" || event.Action == "submitted"
	case "pull_request_review_thread":
		return event.Action == "unresolved"
	case "pull_request":
		return slices.Contains([]string{"converted_to_draft", "edited", "ready_for_review", "reopened", "review_requested", "synchronize"}, event.Action)
	case "check_run", "check_suite", "workflow_run":
		return slices.Contains([]string{"action_required", "cancelled", "failure", "stale", "startup_failure", "timed_out"}, strings.ToLower(event.Conclusion))
	case "status":
		return strings.EqualFold(event.Status, "error") || strings.EqualFold(event.Status, "failure")
	default:
		return false
	}
}

func firstAttribute(event eventwire.Event, key string) string {
	values := event.Values(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func agentTrigger(payload linearPayload, deliveryID, allowedActorID string, trigger settings.LinearLabelTrigger) (agentrun.Trigger, bool) {
	if !trigger.Enabled || payload.Type != "Issue" || payload.Action != "update" || payload.Actor.ID != allowedActorID {
		return agentrun.Trigger{}, false
	}
	factoryLabelID := ""
	for _, label := range payload.Data.Labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), trigger.Label) {
			factoryLabelID = label.ID
			break
		}
	}
	if factoryLabelID == "" || !slices.Contains(payload.Data.LabelIDs, factoryLabelID) || len(payload.UpdatedFrom.LabelIDs) == 0 {
		return agentrun.Trigger{}, false
	}
	identifier := strings.ToUpper(strings.TrimSpace(payload.Data.Identifier))
	if !agentrun.ValidIssueIdentifier(identifier) {
		return agentrun.Trigger{}, false
	}
	var previousLabelIDs []string
	if err := json.Unmarshal(payload.UpdatedFrom.LabelIDs, &previousLabelIDs); err != nil || slices.Contains(previousLabelIDs, factoryLabelID) {
		return agentrun.Trigger{}, false
	}
	return agentrun.Trigger{
		DeliveryID:      deliveryID,
		IssueIdentifier: identifier,
		Kind:            agentrun.TriggerKindLabel,
	}, true
}

func validSignature(secret, body []byte, signature string) bool {
	provided, err := hex.DecodeString(signature)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), provided)
}

func validGitHubSignature(secret, body []byte, signature string) bool {
	encoded, found := strings.CutPrefix(signature, "sha256=")
	if !found {
		return false
	}
	provided, err := hex.DecodeString(encoded)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), provided)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func queryInt(r *http.Request, name string, fallback, minimum, maximum int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New("invalid query parameter")
	}
	return parsed, nil
}

func canonicalPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && (strings.HasSuffix(r.URL.Path, "/") || path.Clean(r.URL.Path) != r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func canonicalAgentReference(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taskIdentifier := strings.ToUpper(r.PathValue("identifier"))
		started, err := strconv.ParseInt(r.PathValue("started"), 10, 64)
		_, sourceOK := observerTaskSource(r)
		if !taskmodel.ValidDisplayIdentifier(taskIdentifier) || err != nil || started < 1 || !sourceOK {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func observerTaskSource(r *http.Request) (taskmodel.Source, bool) {
	values, present := r.URL.Query()["source"]
	if !present {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	source := taskmodel.Source(values[0])
	if source != taskmodel.SourceFactory && source != taskmodel.SourceLinear {
		return "", false
	}
	return source, true
}

func frontendPage(web fs.FS) http.Handler {
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		indexRequest := r.Clone(r.Context())
		indexURL := *r.URL
		indexURL.Path = "/"
		indexRequest.URL = &indexURL
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, indexRequest)
	})
}
