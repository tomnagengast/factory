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
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/activity"
	"github.com/tomnagengast/network/apps/factory/internal/agentrun"
	"github.com/tomnagengast/network/apps/factory/internal/eventwire"
	"github.com/tomnagengast/network/apps/factory/internal/githubhook"
	"github.com/tomnagengast/network/apps/factory/internal/linearhook"
	"github.com/tomnagengast/network/apps/factory/internal/viewerauth"
)

const (
	maxLinearWebhookBody = 1 << 20
	maxGitHubWebhookBody = 25 << 20
	replayWindow         = time.Minute
	triggerLabel         = "Factory"
	defaultActivityPage  = 1
	defaultActivityLimit = 25
	maxActivityPage      = 1_000_000
	maxActivityLimit     = 100
	attributeDeliveryID  = "deliveryId"
	attributeTriggerKind = "triggerKind"
	attributeIssue       = "issueIdentifier"
)

type EventStore interface {
	Add(deliveryID string, event activity.Event) (bool, error)
	StagePayload(deliveryID string, payload []byte) error
	AddStaged(deliveryID string, event activity.Event) (bool, error)
	Snapshot() activity.Snapshot
	LinearPage(page, pageSize int) (activity.LinearPage, error)
	LinearEvent(id string) (activity.EventDetail, bool, error)
}

type RunStore interface {
	Claim(trigger agentrun.Trigger, now time.Time) (agentrun.Run, bool, error)
	ClaimContinuation(trigger agentrun.Trigger, now time.Time) (agentrun.Run, bool, error)
	PublicSnapshot() agentrun.PublicSnapshot
	ActivitySnapshot() agentrun.ActivitySnapshot
	FindStarted(issueIdentifier string, startedUnixMilli int64) (agentrun.Run, bool)
	SchedulePullRequestReconcile(repository string, pullRequest int, headBranch, deliveryID string, cursor uint64, now time.Time) (bool, error)
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

type Config struct {
	Web            fs.FS
	ActivityStore  EventStore
	RunStore       RunStore
	RunNotifier    RunNotifier
	AgentObserver  AgentObserver
	ViewerAuth     *viewerauth.Authenticator
	LinearSecret   []byte
	GitHubSecret   []byte
	Events         *eventwire.Wire
	GitHubEvents   GitHubEventStore
	LinearComments LinearCommentStore
	TriggerActor   string
	Now            func() time.Time
	Build          BuildIdentity
}

type appServer struct {
	activityStore  EventStore
	runStore       RunStore
	runNotifier    RunNotifier
	agentObserver  AgentObserver
	viewerAuth     *viewerauth.Authenticator
	linearSecret   []byte
	githubSecret   []byte
	events         *eventwire.Wire
	githubEvents   GitHubEventStore
	linearComments LinearCommentStore
	triggerActor   string
	now            func() time.Time
	build          BuildIdentity
}

type BuildIdentity struct {
	Commit          string    `json:"commit"`
	Tree            string    `json:"tree"`
	BuildID         string    `json:"buildId"`
	DeploymentID    string    `json:"deploymentId"`
	ContractVersion string    `json:"contractVersion"`
	StartedAt       time.Time `json:"startedAt"`
}

type healthResponse struct {
	Status string `json:"status"`
	App    string `json:"app"`
	BuildIdentity
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
		ID         string   `json:"id"`
		Body       string   `json:"body"`
		IssueID    string   `json:"issueId"`
		ParentID   string   `json:"parentId"`
		Identifier string   `json:"identifier"`
		LabelIDs   []string `json:"labelIds"`
		Labels     []struct {
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

type activityResponse struct {
	Status         string                  `json:"status"`
	Total          uint64                  `json:"total"`
	LastReceivedAt *time.Time              `json:"lastReceivedAt"`
	Events         []activity.Event        `json:"events"`
	AgentRuns      agentrun.PublicSnapshot `json:"agentRuns"`
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
	if config.RunStore == nil {
		return nil, errors.New("server: agent run store is required")
	}
	if config.RunNotifier == nil {
		return nil, errors.New("server: agent run notifier is required")
	}
	if config.AgentObserver == nil {
		return nil, errors.New("server: agent observer is required")
	}
	if config.ViewerAuth == nil {
		return nil, errors.New("server: viewer authenticator is required")
	}
	if config.TriggerActor == "" {
		return nil, errors.New("server: Linear trigger actor is required")
	}
	if config.Now == nil {
		return nil, errors.New("server: clock is required")
	}
	if config.Build.Commit == "" || config.Build.Tree == "" || config.Build.BuildID == "" || config.Build.DeploymentID == "" || config.Build.ContractVersion == "" || config.Build.StartedAt.IsZero() {
		return nil, errors.New("server: build identity is required")
	}

	app := &appServer{
		activityStore:  config.ActivityStore,
		runStore:       config.RunStore,
		runNotifier:    config.RunNotifier,
		agentObserver:  config.AgentObserver,
		viewerAuth:     config.ViewerAuth,
		linearSecret:   config.LinearSecret,
		githubSecret:   config.GitHubSecret,
		events:         config.Events,
		githubEvents:   config.GitHubEvents,
		linearComments: config.LinearComments,
		triggerActor:   config.TriggerActor,
		now:            config.Now,
		build:          config.Build,
	}
	if err := app.events.Handle(eventwire.Filter{Source: eventwire.SourceLinear}, app.dispatchLinear); err != nil {
		return nil, err
	}
	if err := app.events.Handle(eventwire.Filter{Source: eventwire.SourceGitHub}, app.dispatchGitHub); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", app.healthz)
	mux.HandleFunc("GET /api/activity", app.activity)
	mux.Handle("GET /api/activity/linear", app.viewerAuth.API(http.HandlerFunc(app.linearActivity)))
	mux.Handle("GET /api/activity/linear/{id}", app.viewerAuth.API(http.HandlerFunc(app.linearEvent)))
	mux.Handle("GET /api/activity/agents", app.viewerAuth.API(http.HandlerFunc(app.agentActivity)))
	mux.Handle("GET /api/activity/agents/{issue}/{started}/run", app.viewerAuth.API(http.HandlerFunc(app.agentByReference)))
	mux.Handle("GET /api/agents/{id}", app.viewerAuth.API(http.HandlerFunc(app.agent)))
	mux.HandleFunc("POST /api/webhooks/linear", app.linearWebhook)
	mux.HandleFunc("POST /api/webhooks/github", app.githubWebhook)
	mux.HandleFunc("POST /cdn-cgi/rum", cloudflareBeacon)
	mux.HandleFunc("GET /auth/google/login", app.viewerAuth.Login)
	mux.HandleFunc("GET /auth/google/callback", app.viewerAuth.Callback)
	mux.HandleFunc("GET /auth/logout", app.viewerAuth.Logout)
	mux.Handle("GET /activity/linear", app.viewerAuth.Page(frontend(config.Web)))
	mux.Handle("GET /activity/linear/", app.viewerAuth.Page(frontend(config.Web)))
	mux.Handle("GET /activity/agents", app.viewerAuth.Page(frontend(config.Web)))
	mux.Handle("GET /activity/agents/", app.viewerAuth.Page(frontend(config.Web)))
	mux.Handle("GET /agents", app.viewerAuth.Page(frontend(config.Web)))
	mux.Handle("GET /agents/", app.viewerAuth.Page(frontend(config.Web)))
	mux.Handle("/", frontend(config.Web))
	return mux, nil
}

func (s *appServer) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok", App: "factory", BuildIdentity: s.build})
}

func cloudflareBeacon(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *appServer) activity(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.activityStore.Snapshot()
	response := activityResponse{
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

func (s *appServer) linearActivity(w http.ResponseWriter, r *http.Request) {
	page, err := queryInt(r, "page", defaultActivityPage, 1, maxActivityPage)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	pageSize, err := queryInt(r, "pageSize", defaultActivityLimit, 1, maxActivityLimit)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	snapshot, err := s.activityStore.LinearPage(page, pageSize)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *appServer) linearEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validEventID(id) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	event, found, err := s.activityStore.LinearEvent(id)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (s *appServer) agentActivity(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.runStore.ActivitySnapshot())
}

func (s *appServer) agentByReference(w http.ResponseWriter, r *http.Request) {
	issueIdentifier := strings.ToUpper(r.PathValue("issue"))
	if !agentrun.ValidIssueIdentifier(issueIdentifier) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	started, err := strconv.ParseInt(r.PathValue("started"), 10, 64)
	if err != nil || started < 1 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	run, found := s.runStore.FindStarted(issueIdentifier, started)
	if !found {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	s.writeAgent(w, r, run.ID)
}

func (s *appServer) agent(w http.ResponseWriter, r *http.Request) {
	s.writeAgent(w, r, r.PathValue("id"))
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

func (s *appServer) linearWebhook(w http.ResponseWriter, r *http.Request) {
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
	wake, hasWake := commentWake(payload, deliveryID, s.triggerActor, now)
	trigger, hasTrigger := agentTrigger(payload, deliveryID, s.triggerActor)
	event := linearWireEvent(payload, deliveryID, wake, hasWake, trigger, hasTrigger, now)
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

func linearWireEvent(
	payload linearPayload,
	deliveryID string,
	wake linearhook.Event,
	hasWake bool,
	trigger agentrun.Trigger,
	hasTrigger bool,
	now time.Time,
) eventwire.Event {
	identifier := strings.ToUpper(strings.TrimSpace(payload.Data.Identifier))
	if identifier == "" {
		identifier = strings.ToUpper(strings.TrimSpace(payload.Data.Issue.Identifier))
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
	if hasWake {
		event = linearhook.ToWire(wake)
		event.Attributes[attributeTriggerKind] = []string{agentrun.TriggerKindComment}
		event.Attributes[attributeIssue] = []string{wake.IssueIdentifier}
	}
	if hasTrigger {
		event.Attributes[attributeTriggerKind] = []string{trigger.Kind}
		event.Attributes[attributeIssue] = []string{trigger.IssueIdentifier}
	}
	return event
}

func (s *appServer) dispatchLinear(_ context.Context, record eventwire.Record) error {
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

	if event, ok := linearhook.FromWire(record.Event); ok {
		if sequence := record.ChannelSequences[linearhook.WireChannel]; sequence > s.linearComments.Total() {
			if _, err := s.linearComments.AddAt(sequence, event); err != nil {
				return fmt.Errorf("server: project Linear comment: %w", err)
			}
		}
		run, created, err := s.runStore.ClaimContinuation(agentrun.Trigger{
			DeliveryID:      deliveryID,
			IssueIdentifier: event.IssueIdentifier,
			Kind:            agentrun.TriggerKindComment,
		}, record.Event.ReceivedAt)
		if err != nil {
			return fmt.Errorf("server: claim Linear continuation: %w", err)
		}
		if created || (run.State == agentrun.StatePending && run.ResumeCount > 0) {
			s.runNotifier.Notify()
		}
	}

	if firstAttribute(record.Event, attributeTriggerKind) == agentrun.TriggerKindLabel {
		_, created, err := s.runStore.Claim(agentrun.Trigger{
			DeliveryID:      deliveryID,
			IssueIdentifier: firstAttribute(record.Event, attributeIssue),
			Kind:            agentrun.TriggerKindLabel,
		}, record.Event.ReceivedAt)
		if err != nil {
			return fmt.Errorf("server: claim Linear run: %w", err)
		}
		if created {
			s.runNotifier.Notify()
		}
	}
	return nil
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
	for _, pullRequest := range event.PullRequests {
		scheduled, err := s.runStore.SchedulePullRequestReconcile(event.Repository, pullRequest, event.HeadBranch, event.DeliveryID, record.Sequence, event.ReceivedAt)
		if err != nil {
			return fmt.Errorf("server: schedule pull request reconciliation: %w", err)
		}
		if scheduled {
			s.runNotifier.Notify()
		}
	}
	return nil
}

func firstAttribute(event eventwire.Event, key string) string {
	values := event.Values(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func agentTrigger(payload linearPayload, deliveryID, allowedActorID string) (agentrun.Trigger, bool) {
	if payload.Type != "Issue" || payload.Action != "update" || payload.Actor.ID != allowedActorID {
		return agentrun.Trigger{}, false
	}
	factoryLabelID := ""
	for _, label := range payload.Data.Labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), triggerLabel) {
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

func validEventID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func frontend(web fs.FS) http.Handler {
	files := http.FileServerFS(web)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." {
			name = "index.html"
		}
		if _, err := fs.Stat(web, name); err == nil {
			files.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(name, "api/") {
			http.NotFound(w, r)
			return
		}

		indexRequest := r.Clone(r.Context())
		indexURL := *r.URL
		indexURL.Path = "/"
		indexRequest.URL = &indexURL
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, indexRequest)
	})
}
