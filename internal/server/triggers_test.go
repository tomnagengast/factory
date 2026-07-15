package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/triggerscheduler"
)

type scheduleStatusStub struct{ values []triggerscheduler.Status }

func (s scheduleStatusStub) Statuses(time.Time) []triggerscheduler.Status { return s.values }

func TestTriggerAPIUpdatesRegistryAndReturnsOperationalProjection(t *testing.T) {
	t.Parallel()
	handler, policy, _ := testTriggerHandler(t)
	get := authenticatedRequest(t, handler, "/api/triggers")
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"protectedRoutes"`) {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	candidate := policy.RegistrySnapshot()
	candidate.Rules = append(candidate.Rules, triggerregistry.Rule{
		ID: "service-failure", Name: "Service failure", Enabled: true,
		Filter:     triggerregistry.Filter{Source: eventwire.SourceFactory, Type: "service", Action: "failure"},
		WorkflowID: settings.DefaultWorkflowID,
		Target:     triggerregistry.TargetPolicy{Kind: triggerregistry.TargetFixedIssue, Value: "ENG-40"},
		MaxHop:     triggerregistry.DefaultMaxHop, MaxOutstanding: triggerregistry.DefaultMaxOutstanding,
		AdmissionsHour: triggerregistry.DefaultAdmissionsHour,
	})
	put := authenticatedTriggerRequest(t, handler, candidate)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
	}
	updated := policy.RegistrySnapshot()
	if updated.Revision != 1 {
		t.Fatalf("updated registry = %#v", updated)
	}
	rule, found := updated.Rule("service-failure")
	if !found || rule.Revision != 1 {
		t.Fatalf("new rule = %#v, found=%t", rule, found)
	}
	stale := authenticatedTriggerRequest(t, handler, candidate)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"revision":1`) {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
}

func TestTriggerAPIUsesArraysForEmptyOperationalState(t *testing.T) {
	t.Parallel()
	handler, _, _ := testTriggerHandler(t)
	response := authenticatedRequest(t, handler, "/api/triggers")
	if response.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", response.Code, response.Body.String())
	}
	for _, field := range []string{"workflows", "observedSources", "ruleStatus", "scheduleStatus", "recentInvocations"} {
		if strings.Contains(response.Body.String(), `"`+field+`":null`) {
			t.Fatalf("%s must be an array: %s", field, response.Body.String())
		}
	}
}

func TestTriggerAPIIncludesSuppressedRoutingOutcomes(t *testing.T) {
	t.Parallel()
	handler, policy, _ := testTriggerHandler(t)
	candidate := policy.RegistrySnapshot()
	candidate.Rules = []triggerregistry.Rule{{
		ID: "bounded", Name: "Bounded", Enabled: true,
		Filter:     triggerregistry.Filter{Source: eventwire.SourceFactory, Type: "test", Action: "created"},
		WorkflowID: settings.DefaultWorkflowID,
		Target:     triggerregistry.TargetPolicy{Kind: triggerregistry.TargetFixedIssue, Value: "ENG-40"},
		MaxHop:     triggerregistry.DefaultMaxHop, MaxOutstanding: 1,
		AdmissionsHour: triggerregistry.DefaultAdmissionsHour,
	}}
	if response := authenticatedTriggerRequest(t, handler, candidate); response.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
	}
	for _, id := range []string{"event-1", "event-2"} {
		if _, _, err := policy.Publish(context.Background(), eventwire.Event{
			ID: id, Source: eventwire.SourceFactory, Type: "test", Action: "created", ReceivedAt: testNow,
		}); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	response := authenticatedRequest(t, handler, "/api/triggers")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"suppressed"`) ||
		!strings.Contains(response.Body.String(), `"reason":"rule-outstanding-limit"`) {
		t.Fatalf("GET status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTriggerMutationWaitsForStartupReadiness(t *testing.T) {
	t.Parallel()
	ready := false
	handler, policy, _ := testTriggerHandlerReady(t, func() bool { return ready })
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if health.Code != http.StatusServiceUnavailable || !strings.Contains(health.Body.String(), `"status":"degraded"`) {
		t.Fatalf("not-ready health status=%d body=%s", health.Code, health.Body.String())
	}
	response := authenticatedTriggerRequest(t, handler, policy.RegistrySnapshot())
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "5" {
		t.Fatalf("not-ready PUT status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	ready = true
	response = authenticatedTriggerRequest(t, handler, policy.RegistrySnapshot())
	if response.Code != http.StatusOK {
		t.Fatalf("ready PUT status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTriggerProjectionHidesTerminalRunDetail(t *testing.T) {
	t.Parallel()
	secret := "credential-shaped terminal detail"
	if got := visibleInvocationReason(triggerrouter.Invocation{State: triggerrouter.StateBlocked, Reason: secret}); got != "" {
		t.Fatalf("terminal Run detail was visible: %q", got)
	}
	if got := visibleInvocationReason(triggerrouter.Invocation{State: triggerrouter.StateRejected, Reason: "repository-routing-rejected"}); got != "repository-routing-rejected" {
		t.Fatalf("safe routing rejection = %q", got)
	}
}

func TestTriggerAPIRejectsCrossOriginAndWorkflowRemoval(t *testing.T) {
	t.Parallel()
	handler, policy, configurationStore := testTriggerHandler(t)
	candidate := policy.RegistrySnapshot()
	body, _ := json.Marshal(candidate)
	request := httptest.NewRequest(http.MethodPut, "/api/triggers", bytes.NewReader(body))
	request.AddCookie(viewerSessionCookie(t, handler))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || policy.RegistrySnapshot().Revision != 0 {
		t.Fatalf("cross-origin status=%d registry=%#v", recorder.Code, policy.RegistrySnapshot())
	}

	configuration := configurationStore.Snapshot()
	configuration.Workflows[0].Enabled = false
	settingsRequest := authenticatedSettingsRequest(t, handler, configuration, "")
	if settingsRequest.Code != http.StatusBadRequest {
		t.Fatalf("settings status=%d body=%s", settingsRequest.Code, settingsRequest.Body.String())
	}
}

func testTriggerHandler(t *testing.T) (http.Handler, *triggerrouter.CoordinatedWire, *settings.Store) {
	return testTriggerHandlerReady(t, func() bool { return true })
}

func testTriggerHandlerReady(t *testing.T, ready func() bool) (http.Handler, *triggerrouter.CoordinatedWire, *settings.Store) {
	t.Helper()
	directory := t.TempDir()
	activityStore, _ := activity.Open(filepath.Join(directory, "activity.json"), 10)
	runStore, _ := agentrun.Open(filepath.Join(directory, "runs.json"), 10)
	configuration, _ := settings.Open(filepath.Join(directory, "settings.json"), settings.Defaults(3))
	registry, _ := triggerregistry.Open(filepath.Join(directory, "triggers.json"), triggerregistry.Defaults(configuration.Snapshot(), testActorID), configuration.Snapshot())
	routing, _ := triggerrouter.Open(filepath.Join(directory, "routing.jsonl"))
	githubEvents, _ := githubhook.Open(filepath.Join(directory, "github.json"), 10)
	linearComments, _ := linearhook.Open(filepath.Join(directory, "linear.json"), 10)
	journal, _ := eventwire.Open(filepath.Join(directory, "wire.jsonl"), 100, map[string]uint64{
		githubhook.WireChannel: 0, linearhook.WireChannel: 0,
	})
	raw, _ := eventwire.New(journal)
	policy, _ := triggerrouter.NewCoordinatedWire(raw, registry, configuration, routing, func() time.Time { return testNow })
	resolver := testRepositoryResolver{"ENG-40": {config: agentrun.RepositoryConfig{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "https://github.com/tomnagengast/factory",
		RepoPath: filepath.Join(directory, "repos", "factory"), ManagedRoot: filepath.Join(directory, "repos"), BaseBranch: "main",
	}}}
	handler, err := New(Config{
		Web: testWeb(), ActivityStore: activityStore, RunStore: runStore, RunNotifier: &testNotifier{},
		AgentObserver: &testObserver{err: agentrun.ErrRunNotFound}, Settings: configuration,
		ViewerAuth: testViewerAuth(t), LinearSecret: testSecret, GitHubSecret: testGitHubSecret,
		Events: policy, GitHubEvents: githubEvents, LinearComments: linearComments,
		ProjectSetups: &testProjectSetups{}, TriggerActor: testActorID, RepositoryResolver: resolver,
		Now: func() time.Time { return testNow }, Build: testBuildIdentity(), GenericTriggers: true,
		TriggerPolicy: policy, ScheduleStatus: scheduleStatusStub{},
		Ready: ready,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler, policy, configuration
}

func authenticatedTriggerRequest(t *testing.T, handler http.Handler, candidate triggerregistry.Snapshot) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/triggers", bytes.NewReader(body))
	request.AddCookie(viewerSessionCookie(t, handler))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
