package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/workflow"
)

const maxTaskRequestBody = 96 << 10

type taskSummary struct {
	Ref          taskmodel.TaskRef           `json:"ref"`
	Title        string                      `json:"title"`
	ProjectID    string                      `json:"projectId,omitempty"`
	ApprovalMode string                      `json:"approvalMode,omitempty"`
	State        string                      `json:"state"`
	Revision     uint64                      `json:"revision,omitempty"`
	UpdatedAt    time.Time                   `json:"updatedAt,omitempty"`
	LatestRun    *agentrun.ActivityRun       `json:"latestRun,omitempty"`
	ReadOnly     bool                        `json:"readOnly"`
	ExternalURL  string                      `json:"externalUrl,omitempty"`
	Description  string                      `json:"description,omitempty"`
	ProjectName  string                      `json:"projectName,omitempty"`
	StateName    string                      `json:"stateName,omitempty"`
	Messages     []taskservice.LinearMessage `json:"messages,omitempty"`
}

type tasksResponse struct {
	Tasks      []taskSummary `json:"tasks"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type nativeTaskDetailResponse struct {
	taskservice.Detail
	LatestRun *agentrun.ActivityRun `json:"latestRun,omitempty"`
}

func (s *appServer) getTaskProjects(w http.ResponseWriter, _ *http.Request) {
	if !s.tasksAvailable(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": s.tasks.Projects(), "control": s.tasks.Control()})
}

func (s *appServer) putTaskProject(w http.ResponseWriter, r *http.Request) {
	if !s.taskMutationReady(w, r) {
		return
	}
	var request struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		Enabled          bool   `json:"enabled"`
	}
	if !decodeTaskJSON(w, r, &request) {
		return
	}
	snapshot, err := s.tasks.SetProject(request.ExpectedRevision, r.PathValue("id"), request.Enabled)
	if errors.Is(err, taskcontrol.ErrRevisionConflict) {
		writeJSON(w, http.StatusConflict, snapshot)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *appServer) getTasks(w http.ResponseWriter, r *http.Request) {
	if !s.tasksAvailable(w) {
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	page, err := s.tasks.List(r.URL.Query().Get("cursor"), limit)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider != "" && provider != string(taskmodel.SourceFactory) && provider != string(taskmodel.SourceLinear) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	approval := strings.TrimSpace(r.URL.Query().Get("approval"))
	activity := strings.TrimSpace(r.URL.Query().Get("activity"))
	if activity != "" && activity != "active" && activity != "inactive" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	latestRuns, activeTasks := s.taskActivityIndex()
	response := tasksResponse{}
	if provider == "" || provider == string(taskmodel.SourceFactory) {
		response.NextCursor = page.NextCursor
	}
	if provider == "" || provider == string(taskmodel.SourceFactory) {
		for _, task := range page.Tasks {
			key := task.Ref.OwnershipKey()
			if projectID != "" && task.ProjectID != projectID || state != "" && task.State != state || approval != "" && task.ApprovalMode != approval ||
				activity == "active" && !activeTasks[key] || activity == "inactive" && activeTasks[key] {
				continue
			}
			response.Tasks = append(response.Tasks, taskSummary{
				Ref: task.Ref, Title: task.Title, ProjectID: task.ProjectID, ApprovalMode: task.ApprovalMode,
				State: task.State, Revision: task.Revision, UpdatedAt: task.UpdatedAt, LatestRun: latestRuns[key],
			})
		}
	}
	if (provider == "" || provider == string(taskmodel.SourceLinear)) && r.URL.Query().Get("cursor") == "" && projectID == "" && approval == "" {
		response.Tasks = append(response.Tasks, s.managedLinearTasks(state, activity, latestRuns, activeTasks)...)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *appServer) taskActivityIndex() (map[string]*agentrun.ActivityRun, map[string]bool) {
	latestRuns := make(map[string]*agentrun.ActivityRun)
	active := make(map[string]bool)
	for _, run := range s.runStore.ActivitySnapshot().Runs {
		key := run.Task.OwnershipKey()
		if current := latestRuns[key]; current == nil || run.UpdatedAt.After(current.UpdatedAt) {
			value := run
			latestRuns[key] = &value
		}
		if run.State.Active() {
			active[key] = true
		}
	}
	if s.triggerPolicy != nil {
		for _, invocation := range s.triggerPolicy.RoutingSnapshot().Invocations {
			if invocation.Nonterminal() {
				active[invocation.Task.OwnershipKey()] = true
			}
		}
	}
	return latestRuns, active
}

func (s *appServer) managedLinearTasks(state, activity string, latestRuns map[string]*agentrun.ActivityRun, activeTasks map[string]bool) []taskSummary {
	byKey := make(map[string]taskSummary)
	for key, run := range latestRuns {
		if run.Task.Source != taskmodel.SourceLinear || state != "" && string(run.State) != state {
			continue
		}
		summary := taskSummary{
			Ref: run.Task, Title: run.Task.Identifier, State: string(run.State), UpdatedAt: run.UpdatedAt,
			LatestRun: run, ReadOnly: true, ExternalURL: "https://linear.app/issue/" + strings.ToLower(run.Task.Identifier),
		}
		byKey[key] = summary
	}
	if s.triggerPolicy != nil {
		for _, invocation := range s.triggerPolicy.RoutingSnapshot().Invocations {
			if invocation.Task.Source != taskmodel.SourceLinear {
				continue
			}
			key := invocation.Task.OwnershipKey()
			if _, found := byKey[key]; !found {
				byKey[key] = taskSummary{
					Ref: invocation.Task, Title: invocation.Task.Identifier, State: invocation.State,
					UpdatedAt: invocation.UpdatedAt, ReadOnly: true,
					ExternalURL: "https://linear.app/issue/" + strings.ToLower(invocation.Task.Identifier),
				}
			}
		}
	}
	result := make([]taskSummary, 0, len(byKey))
	for key, summary := range byKey {
		if activity == "active" && !activeTasks[key] || activity == "inactive" && activeTasks[key] {
			continue
		}
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.After(result[j].UpdatedAt)
		}
		return result[i].Ref.Identifier < result[j].Ref.Identifier
	})
	return result
}

func (s *appServer) getTask(w http.ResponseWriter, r *http.Request) {
	if !s.tasksAvailable(w) {
		return
	}
	provider := r.PathValue("provider")
	if provider == string(taskmodel.SourceLinear) {
		identifier := strings.ToUpper(r.PathValue("id"))
		var managed *taskSummary
		latestRuns, activeTasks := s.taskActivityIndex()
		for _, summary := range s.managedLinearTasks("", "", latestRuns, activeTasks) {
			if summary.Ref.Identifier == identifier || summary.Ref.ProviderID == identifier {
				value := summary
				managed = &value
				break
			}
		}
		if managed == nil {
			http.NotFound(w, r)
			return
		}
		if s.linearTasks == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		issue, err := s.linearTasks.Detail(r.Context(), identifier)
		if errors.Is(err, taskservice.ErrLinearTaskNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			slog.Warn("read managed Linear task", "identifier", identifier, "error", err)
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		managed.Title, managed.Description = issue.Title, issue.Description
		managed.ProjectID, managed.ProjectName = issue.ProjectID, issue.ProjectName
		managed.State, managed.StateName, managed.UpdatedAt = issue.State, issue.StateName, issue.UpdatedAt
		managed.ExternalURL, managed.Messages = issue.ExternalURL, issue.Messages
		writeJSON(w, http.StatusOK, *managed)
		return
	}
	if provider != string(taskmodel.SourceFactory) {
		http.NotFound(w, r)
		return
	}
	after, err := queryUint(r, "after", 0)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	detail, err := s.tasks.Detail(r.PathValue("id"), after, 200)
	if errors.Is(err, taskstore.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	latestRuns, _ := s.taskActivityIndex()
	writeJSON(w, http.StatusOK, nativeTaskDetailResponse{Detail: detail, LatestRun: latestRuns[detail.Task.Ref.OwnershipKey()]})
}

func (s *appServer) postTask(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.taskActor(w, r)
	if !ok || !s.taskMutationReady(w, r) {
		return
	}
	if taskIdempotencyKey(r) == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	var request struct {
		Title        string `json:"title"`
		Description  string `json:"description"`
		ProjectID    string `json:"projectId"`
		ApprovalMode string `json:"approvalMode"`
	}
	if !decodeTaskJSON(w, r, &request) {
		return
	}
	result, err := s.tasks.Create(r.Context(), taskservice.CreateRequest{
		Actor: actor, Title: request.Title, Description: request.Description, ProjectID: request.ProjectID,
		ApprovalMode: request.ApprovalMode, IdempotencyKey: taskIdempotencyKey(r),
	})
	if s.writeTaskError(w, r, result, err) {
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *appServer) patchTask(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.nativeTaskMutation(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		Title            string `json:"title"`
		Description      string `json:"description"`
		ApprovalMode     string `json:"approvalMode"`
	}
	if !decodeTaskJSON(w, r, &request) {
		return
	}
	result, err := s.tasks.Update(r.Context(), taskstore.UpdateCommand{
		Actor: actor, TaskID: r.PathValue("id"), ExpectedRevision: request.ExpectedRevision,
		Title: request.Title, Description: request.Description, ApprovalMode: request.ApprovalMode,
		IdempotencyKey: taskIdempotencyKey(r),
	})
	if !s.writeTaskError(w, r, result, err) {
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *appServer) postTaskMessage(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.nativeTaskMutation(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		ParentID         string `json:"parentId"`
		Body             string `json:"body"`
	}
	if !decodeTaskJSON(w, r, &request) {
		return
	}
	result, err := s.tasks.Message(r.Context(), taskstore.MessageCommand{Actor: actor, TaskID: r.PathValue("id"), ExpectedRevision: request.ExpectedRevision, ParentID: request.ParentID, Body: request.Body, IdempotencyKey: taskIdempotencyKey(r)})
	if !s.writeTaskError(w, r, result, err) {
		writeJSON(w, http.StatusCreated, result)
	}
}

func (s *appServer) postTaskLink(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.nativeTaskMutation(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		Label            string `json:"label"`
		URL              string `json:"url"`
	}
	if !decodeTaskJSON(w, r, &request) {
		return
	}
	result, err := s.tasks.Link(r.Context(), taskstore.LinkCommand{Actor: actor, TaskID: r.PathValue("id"), ExpectedRevision: request.ExpectedRevision, Label: request.Label, URL: request.URL, IdempotencyKey: taskIdempotencyKey(r)})
	if !s.writeTaskError(w, r, result, err) {
		writeJSON(w, http.StatusCreated, result)
	}
}

func (s *appServer) postTaskGate(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.nativeTaskMutation(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		Kind             string `json:"kind"`
		Mode             string `json:"mode"`
		ArtifactURL      string `json:"artifactUrl"`
	}
	if !decodeTaskJSON(w, r, &request) {
		return
	}
	result, err := s.tasks.Gate(r.Context(), taskstore.GateCommand{Actor: actor, TaskID: r.PathValue("id"), ExpectedRevision: request.ExpectedRevision, Kind: request.Kind, Mode: request.Mode, ArtifactURL: request.ArtifactURL, IdempotencyKey: taskIdempotencyKey(r)})
	if !s.writeTaskError(w, r, result, err) {
		writeJSON(w, http.StatusCreated, result)
	}
}

func (s *appServer) postTaskGateDecision(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.nativeTaskMutation(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		Action           string `json:"action"`
		Reason           string `json:"reason"`
	}
	if !decodeTaskJSON(w, r, &request) {
		return
	}
	result, err := s.tasks.Decide(r.Context(), taskstore.DecisionCommand{Actor: actor, TaskID: r.PathValue("id"), GateID: r.PathValue("gateID"), ExpectedRevision: request.ExpectedRevision, Action: request.Action, Reason: request.Reason, IdempotencyKey: taskIdempotencyKey(r)})
	if !s.writeTaskError(w, r, result, err) {
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *appServer) postTaskState(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.nativeTaskMutation(w, r)
	if !ok {
		return
	}
	var request struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		State            string `json:"state"`
	}
	if !decodeTaskJSON(w, r, &request) {
		return
	}
	result, err := s.tasks.State(r.Context(), taskstore.StateCommand{Actor: actor, TaskID: r.PathValue("id"), ExpectedRevision: request.ExpectedRevision, State: request.State, IdempotencyKey: taskIdempotencyKey(r)})
	if !s.writeTaskError(w, r, result, err) {
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *appServer) postTaskStart(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.nativeTaskMutation(w, r)
	if !ok {
		return
	}
	if taskIdempotencyKey(r) == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	if !decodeTaskJSON(w, r, &struct{}{}) {
		return
	}
	result, err := s.tasks.Start(r.Context(), taskservice.StartRequest{Actor: actor, TaskID: r.PathValue("id")})
	if err != nil {
		s.writeTaskError(w, r, taskstore.Result{}, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *appServer) nativeTaskMutation(w http.ResponseWriter, r *http.Request) (taskstore.Actor, bool) {
	if r.PathValue("provider") != string(taskmodel.SourceFactory) {
		http.Error(w, "task provider is read-only", http.StatusMethodNotAllowed)
		return taskstore.Actor{}, false
	}
	actor, ok := s.taskActor(w, r)
	if !ok || !s.taskMutationReady(w, r) {
		return taskstore.Actor{}, false
	}
	return actor, true
}

func (s *appServer) taskActor(w http.ResponseWriter, r *http.Request) (taskstore.Actor, bool) {
	actor, ok := s.viewerAuth.Actor(r)
	if !ok {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
	}
	return actor, ok
}

func (s *appServer) tasksAvailable(w http.ResponseWriter) bool {
	if s.tasks != nil {
		return true
	}
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
	return false
}

func (s *appServer) taskMutationReady(w http.ResponseWriter, r *http.Request) bool {
	if !s.tasksAvailable(w) || !s.requireReady(w) {
		return false
	}
	if !sameOrigin(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	return true
}

func decodeTaskJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTaskRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return false
	}
	return true
}

func taskIdempotencyKey(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(value) > 128 {
		return ""
	}
	return value
}

func (s *appServer) writeTaskError(w http.ResponseWriter, r *http.Request, result taskstore.Result, err error) bool {
	if err == nil {
		return false
	}
	var conflict taskstore.RevisionConflict
	if errors.As(err, &conflict) {
		writeJSON(w, http.StatusConflict, conflict.Current)
		return true
	}
	if errors.Is(err, taskstore.ErrNotFound) {
		http.NotFound(w, r)
		return true
	}
	if errors.Is(err, taskservice.ErrDisabled) || errors.Is(err, taskservice.ErrWorkflowUnavailable) || errors.Is(err, taskservice.ErrRoutingConflict) {
		http.Error(w, err.Error(), http.StatusConflict)
		return true
	}
	if errors.Is(err, taskstore.ErrIdempotencyConflict) {
		http.Error(w, err.Error(), http.StatusConflict)
		return true
	}
	if result.Task.Ref.ProviderID != "" {
		writeJSON(w, http.StatusBadRequest, result)
		return true
	}
	slog.Warn("task API request rejected", "error", err)
	http.Error(w, err.Error(), http.StatusBadRequest)
	return true
}

func queryUint(r *http.Request, name string, fallback uint64) (uint64, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

type agentTaskRequest struct {
	Operation      string `json:"operation"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	After          uint64 `json:"after,omitempty"`
	Revision       uint64 `json:"revision,omitempty"`
	Body           string `json:"body,omitempty"`
	ParentID       string `json:"parentId,omitempty"`
	Label          string `json:"label,omitempty"`
	URL            string `json:"url,omitempty"`
	State          string `json:"state,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Mode           string `json:"mode,omitempty"`
	ArtifactURL    string `json:"artifactUrl,omitempty"`
}

func (s *appServer) agentTask(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if !s.requireReady(w) || !s.tasksAvailable(w) {
		return
	}
	runID := strings.TrimSpace(r.Header.Get("X-Factory-Run-ID"))
	run, found := s.runStore.Find(runID)
	if !found || !run.State.HasWorker() || run.RunDirectory == "" || run.PinnedWorkflowDigest != workflow.ProviderNeutralDigest() {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	capability, err := agentrun.ReadTaskCapability(run.RunDirectory)
	token := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	if err != nil || !capability.Authorizes(run, token) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	var request agentTaskRequest
	if !decodeTaskJSON(w, r, &request) {
		return
	}
	if run.Task.Source != taskmodel.SourceFactory {
		s.linearAgentTask(w, r, run, request)
		return
	}
	detail, err := s.tasks.Detail(run.Task.ProviderID, request.After, 500)
	if err != nil {
		s.writeTaskError(w, r, taskstore.Result{}, err)
		return
	}
	if request.Operation == "activity" && len(detail.Messages.Messages) == 0 && detail.Task.Revision <= request.Revision {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Operation == "show" || request.Operation == "messages" || request.Operation == "activity" {
		writeJSON(w, http.StatusOK, detail)
		return
	}
	actor := taskstore.Actor{ID: "run:" + run.ID, Kind: taskstore.AuthorAgent}
	key := "helper:" + run.ID + ":" + request.IdempotencyKey
	if request.IdempotencyKey == "" || len(key) > 256 {
		http.Error(w, "idempotency key is required", http.StatusBadRequest)
		return
	}
	var result taskstore.Result
	switch request.Operation {
	case "comment", "reply":
		result, err = s.tasks.Message(r.Context(), taskstore.MessageCommand{Actor: actor, TaskID: run.Task.ProviderID, ExpectedRevision: detail.Task.Revision, ParentID: request.ParentID, Body: request.Body, IdempotencyKey: key})
	case "link":
		result, err = s.tasks.Link(r.Context(), taskstore.LinkCommand{Actor: actor, TaskID: run.Task.ProviderID, ExpectedRevision: detail.Task.Revision, Label: request.Label, URL: request.URL, IdempotencyKey: key})
	case "state":
		result, err = s.tasks.State(r.Context(), taskstore.StateCommand{Actor: actor, TaskID: run.Task.ProviderID, ExpectedRevision: detail.Task.Revision, State: request.State, IdempotencyKey: key})
	case "gate-open":
		result, err = s.tasks.Gate(r.Context(), taskstore.GateCommand{Actor: actor, TaskID: run.Task.ProviderID, ExpectedRevision: detail.Task.Revision, Kind: request.Kind, Mode: request.Mode, ArtifactURL: request.ArtifactURL, IdempotencyKey: key})
	default:
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !s.writeTaskError(w, r, result, err) {
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *appServer) linearAgentTask(w http.ResponseWriter, r *http.Request, run agentrun.Run, request agentTaskRequest) {
	if run.Task.Source != taskmodel.SourceLinear || s.linearTasks == nil {
		http.Error(w, "task provider helper is unavailable", http.StatusServiceUnavailable)
		return
	}
	issue, err := s.linearTasks.Detail(r.Context(), run.Task.Identifier)
	if err != nil {
		s.writeLinearTaskError(w, err)
		return
	}
	filtered := issue
	filtered.Messages = nil
	for _, message := range issue.Messages {
		if message.Ordinal > request.After {
			filtered.Messages = append(filtered.Messages, message)
		}
	}
	if request.Operation == "activity" && len(filtered.Messages) == 0 && issue.Revision <= request.Revision {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Operation == "show" || request.Operation == "messages" || request.Operation == "activity" {
		writeJSON(w, http.StatusOK, filtered)
		return
	}
	if request.IdempotencyKey == "" {
		http.Error(w, "idempotency key is required", http.StatusBadRequest)
		return
	}
	switch request.Operation {
	case "comment", "reply":
		issue, err = s.linearTasks.Comment(r.Context(), run.Task.Identifier, request.ParentID, request.Body, request.Operation, "helper:"+run.ID+":"+request.IdempotencyKey)
	case "link":
		issue, err = s.linearTasks.Link(r.Context(), run.Task.Identifier, request.Label, request.URL)
	case "state":
		issue, err = s.linearTasks.State(r.Context(), run.Task.Identifier, request.State)
	case "gate-open":
		issue, err = s.linearTasks.Gate(r.Context(), run.Task.Identifier, request.Kind, request.Mode, request.ArtifactURL, "helper:"+run.ID+":"+request.IdempotencyKey)
	default:
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err != nil {
		s.writeLinearTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, issue)
}

func (s *appServer) writeLinearTaskError(w http.ResponseWriter, err error) {
	if errors.Is(err, taskservice.ErrLinearTaskNotFound) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	slog.Warn("Linear task helper request rejected", "error", err)
	http.Error(w, err.Error(), http.StatusBadRequest)
}
