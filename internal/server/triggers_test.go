package server

import (
	"bytes"
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

func TestTriggerAPIRejectsCrossOriginAndWorkflowRemoval(t *testing.T) {
	t.Parallel()
	handler, policy, configurationStore := testTriggerHandler(t)
	candidate := policy.RegistrySnapshot()
	body, _ := json.Marshal(candidate)
	request := httptest.NewRequest(http.MethodPut, "/api/triggers", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	request.SetBasicAuth("factory", testViewerPassword)
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
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth("factory", testViewerPassword)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
