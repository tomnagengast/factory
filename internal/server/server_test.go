package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/activity"
	"github.com/tomnagengast/network/apps/factory/internal/agentrun"
	"github.com/tomnagengast/network/apps/factory/internal/githubhook"
	"github.com/tomnagengast/network/apps/factory/internal/viewerauth"
)

var testNow = time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

const testActorID = "actor-tom"

const testViewerPassword = "viewer-test-password"

func TestHealthz(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var got healthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := healthResponse{Status: "ok", App: "factory"}
	if got != want {
		t.Fatalf("response = %#v, want %#v", got, want)
	}
}

func TestFrontendFallsBackToIndex(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/future-route", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got, want := recorder.Body.String(), "<h1>Factory</h1>"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestUnknownAPIIsNotFound(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/missing", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestPrivateActivityAndAgentRoutesRequireViewerAuthentication(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	for _, target := range []string{
		"/agents/run-123",
		"/agents/run-123/",
		"/activity/linear",
		"/activity/agents",
		"/activity/agents/ENG-23/1783714439062/run",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusFound {
			t.Fatalf("%s status = %d, want %d", target, recorder.Code, http.StatusFound)
		}
		if got := recorder.Header().Get("Location"); !strings.HasPrefix(got, "/auth/google/login?next=") {
			t.Fatalf("%s redirect = %q", target, got)
		}
	}

	for _, target := range []string{
		"/api/agents/run-123",
		"/api/activity/linear",
		"/api/activity/linear/" + strings.Repeat("a", 64),
		"/api/activity/agents",
		"/api/activity/agents/ENG-23/1783714439062/run",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("%s API response = %d, challenge %q", target, recorder.Code, recorder.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestAuthenticatedAgentPageServesFrontend(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/agents/run-123", nil)
	request.SetBasicAuth("factory", testViewerPassword)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got, want := recorder.Body.String(), "<h1>Factory</h1>"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestAuthenticatedAgentAPIReturnsObservedRun(t *testing.T) {
	t.Parallel()

	observer := &testObserver{view: agentrun.AgentView{
		ID:              "run-123",
		IssueIdentifier: "ENG-123",
		State:           agentrun.StateRunning,
		Live:            true,
		Windows:         []agentrun.WindowView{{ID: "@1", Name: "principal", Output: "working"}},
	}}
	handler := testHandlerWithObserver(t, observer)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/agents/run-123", nil)
	request.SetBasicAuth("factory", testViewerPassword)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var got agentrun.AgentView
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if got.ID != "run-123" || got.IssueIdentifier != "ENG-123" || len(got.Windows) != 1 {
		t.Fatalf("view = %#v", got)
	}
	if observer.lastID != "run-123" {
		t.Fatalf("observer ID = %q, want run-123", observer.lastID)
	}
}

func TestAuthenticatedAgentAPINotFound(t *testing.T) {
	t.Parallel()

	handler := testHandlerWithObserver(t, &testObserver{err: agentrun.ErrRunNotFound})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/agents/run-missing", nil)
	request.SetBasicAuth("factory", testViewerPassword)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestCloudflareBeacon(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/cdn-cgi/rum", nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestLinearWebhookAcceptsAndDeduplicatesSignedDelivery(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	body := fmt.Sprintf(
		`{"type":"Issue","action":"update","webhookTimestamp":%d,"data":{"title":"not persisted"}}`,
		testNow.UnixMilli(),
	)
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedWebhookRequest(body, "delivery-1", testSecret))
		if recorder.Code != http.StatusOK {
			t.Fatalf("webhook status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/activity", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("activity status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var got activityResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode activity: %v", err)
	}
	if got.Status != "listening" || got.Total != 1 || len(got.Events) != 1 {
		t.Fatalf("activity = %#v", got)
	}
	want := activity.Event{Type: "Issue", Action: "update", ReceivedAt: testNow}
	if got.Events[0] != want {
		t.Fatalf("event = %#v, want %#v", got.Events[0], want)
	}
}

func TestAuthenticatedLinearActivityPagesRawPayload(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	body := fmt.Sprintf(
		`{"type":"Issue","action":"update","webhookTimestamp":%d,"data":{"identifier":"ENG-23","private":"winery roadmap"}}`,
		testNow.UnixMilli(),
	)
	webhook := httptest.NewRecorder()
	handler.ServeHTTP(webhook, signedWebhookRequest(body, "delivery-private", testSecret))
	if webhook.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, want %d", webhook.Code, http.StatusOK)
	}

	pageRecorder := authenticatedRequest(t, handler, "/api/activity/linear?page=1&pageSize=25")
	var page activity.LinearPage
	if err := json.NewDecoder(pageRecorder.Body).Decode(&page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if pageRecorder.Code != http.StatusOK || page.Total != 1 || len(page.Events) != 1 || !page.Events[0].PayloadAvailable {
		t.Fatalf("page response = %d %#v", pageRecorder.Code, page)
	}
	if len(page.TypeCounts) != 1 || page.TypeCounts[0] != (activity.Count{Label: "Issue", Count: 1}) {
		t.Fatalf("type counts = %#v", page.TypeCounts)
	}

	detailRecorder := authenticatedRequest(t, handler, "/api/activity/linear/"+page.Events[0].ID)
	var detail activity.EventDetail
	if err := json.NewDecoder(detailRecorder.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detailRecorder.Code != http.StatusOK || !strings.Contains(string(detail.Payload), "winery roadmap") {
		t.Fatalf("detail response = %d %#v", detailRecorder.Code, detail)
	}

	publicRecorder := httptest.NewRecorder()
	handler.ServeHTTP(publicRecorder, httptest.NewRequest(http.MethodGet, "/api/activity", nil))
	if publicBody := publicRecorder.Body.String(); strings.Contains(publicBody, "winery roadmap") || strings.Contains(publicBody, "ENG-23") {
		t.Fatalf("public activity leaked raw payload: %s", publicBody)
	}
}

func TestLinearActivityAPIRejectsInvalidPaginationAndEventIDs(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	for _, target := range []string{
		"/api/activity/linear?page=0",
		"/api/activity/linear?pageSize=101",
		"/api/activity/linear/not-a-hash",
	} {
		recorder := authenticatedRequest(t, handler, target)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want %d", target, recorder.Code, http.StatusBadRequest)
		}
	}
	recorder := authenticatedRequest(t, handler, "/api/activity/linear/"+strings.Repeat("a", 64))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing event status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestAuthenticatedAgentActivityAndReference(t *testing.T) {
	t.Parallel()

	observer := &testObserver{}
	handler, store := testHandlerWithObserverAndStore(t, observer)
	run, _, err := store.Claim(agentrun.Trigger{DeliveryID: "delivery-agent", IssueIdentifier: "ENG-23", Kind: "test"}, testNow)
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-23", t.TempDir(), testNow); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := store.MarkRunning(run.ID, 1, testNow); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	observer.view = agentrun.AgentView{ID: run.ID, IssueIdentifier: "ENG-23", State: agentrun.StateRunning, Live: true}

	summaryRecorder := authenticatedRequest(t, handler, "/api/activity/agents")
	var summary agentrun.ActivitySnapshot
	if err := json.NewDecoder(summaryRecorder.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summaryRecorder.Code != http.StatusOK || summary.Total != 1 || summary.Active != 1 || summary.Runs[0].IssueIdentifier != "ENG-23" {
		t.Fatalf("summary response = %d %#v", summaryRecorder.Code, summary)
	}

	target := fmt.Sprintf("/api/activity/agents/eng-23/%d/run", testNow.UnixMilli())
	detailRecorder := authenticatedRequest(t, handler, target)
	var detail agentrun.AgentView
	if err := json.NewDecoder(detailRecorder.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detailRecorder.Code != http.StatusOK || detail.ID != run.ID || observer.lastID != run.ID {
		t.Fatalf("detail response = %d %#v, observer ID %q", detailRecorder.Code, detail, observer.lastID)
	}

	for _, invalidTarget := range []string{
		"/api/activity/agents/not-an-issue/123/run",
		"/api/activity/agents/ENG-23/not-a-time/run",
	} {
		if recorder := authenticatedRequest(t, handler, invalidTarget); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want %d", invalidTarget, recorder.Code, http.StatusBadRequest)
		}
	}
	if recorder := authenticatedRequest(t, handler, "/api/activity/agents/ENG-24/"+strconv.FormatInt(testNow.UnixMilli(), 10)+"/run"); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing run status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestLinearWebhookRejectsInvalidSignature(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	body := fmt.Sprintf(
		`{"type":"Issue","action":"create","webhookTimestamp":%d}`,
		testNow.UnixMilli(),
	)
	recorder := httptest.NewRecorder()
	request := signedWebhookRequest(body, "delivery-1", []byte("wrong-secret"))
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestLinearWebhookRejectsStalePayload(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	body := fmt.Sprintf(
		`{"type":"Issue","action":"create","webhookTimestamp":%d}`,
		testNow.Add(-2*time.Minute).UnixMilli(),
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedWebhookRequest(body, "delivery-1", testSecret))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestLinearFactoryLabelStartsOneRunPerActiveIssue(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier := testHandlerWithRuns(t)
	for i, deliveryID := range []string{"delivery-1", "delivery-2"} {
		body := fmt.Sprintf(
			`{"type":"Issue","action":"update","webhookTimestamp":%d,"actor":{"id":"%s"},"data":{"identifier":"ENG-123","labelIds":["label-other","label-factory"],"labels":[{"id":"label-other","name":"other"},{"id":"label-factory","name":"Factory"}]},"updatedFrom":{"labelIds":["label-other"]}}`,
			testNow.Add(time.Duration(i)*time.Millisecond).UnixMilli(),
			testActorID,
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedWebhookRequest(body, deliveryID, testSecret))
		if recorder.Code != http.StatusOK {
			t.Fatalf("webhook %d status = %d, want %d", i, recorder.Code, http.StatusOK)
		}
	}

	snapshot := runStore.Snapshot()
	if snapshot.Total != 1 || snapshot.Active != 1 || len(snapshot.Runs) != 1 {
		t.Fatalf("run snapshot = %#v", snapshot)
	}
	if got := snapshot.Runs[0]; got.IssueIdentifier != "ENG-123" || got.TriggerKind != "linear-label" || got.DuplicateTriggers != 1 {
		t.Fatalf("run = %#v", got)
	}
	if got := notifier.count.Load(); got != 1 {
		t.Fatalf("notifications = %d, want 1", got)
	}
	activityRecorder := httptest.NewRecorder()
	handler.ServeHTTP(activityRecorder, httptest.NewRequest(http.MethodGet, "/api/activity", nil))
	if body := activityRecorder.Body.String(); strings.Contains(body, "ENG-123") || strings.Contains(body, testActorID) {
		t.Fatalf("public activity leaked private Linear context: %s", body)
	}
}

func TestLinearNonTriggerEventsDoNotStartRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "old comment command",
			body: `{"type":"Comment","action":"create","actor":{"id":"actor-tom"},"data":{"body":"/do ENG-123"}}`,
		},
		{
			name: "Factory label already present",
			body: `{"type":"Issue","action":"update","actor":{"id":"actor-tom"},"data":{"identifier":"ENG-123","labelIds":["label-factory","label-other"],"labels":[{"id":"label-factory","name":"Factory"},{"id":"label-other","name":"other"}]},"updatedFrom":{"labelIds":["label-factory"]}}`,
		},
		{
			name: "Factory label removed",
			body: `{"type":"Issue","action":"update","actor":{"id":"actor-tom"},"data":{"identifier":"ENG-123","labelIds":["label-other"],"labels":[{"id":"label-other","name":"other"}]},"updatedFrom":{"labelIds":["label-other","label-factory"]}}`,
		},
		{
			name: "unrelated issue update",
			body: `{"type":"Issue","action":"update","actor":{"id":"actor-tom"},"data":{"identifier":"ENG-123","labelIds":["label-other"],"labels":[{"id":"label-other","name":"other"}]},"updatedFrom":{"title":"Old title"}}`,
		},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, runStore, notifier := testHandlerWithRuns(t)
			body := fmt.Sprintf(`{"webhookTimestamp":%d,%s`, testNow.UnixMilli(), strings.TrimPrefix(test.body, "{"))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, signedWebhookRequest(body, fmt.Sprintf("delivery-%d", i), testSecret))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if snapshot := runStore.Snapshot(); snapshot.Total != 0 {
				t.Fatalf("run snapshot = %#v", snapshot)
			}
			if got := notifier.count.Load(); got != 0 {
				t.Fatalf("notifications = %d, want 0", got)
			}
		})
	}
}

func TestLinearFactoryLabelFromAnotherActorDoesNotStartRun(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier := testHandlerWithRuns(t)
	body := fmt.Sprintf(
		`{"type":"Issue","action":"update","webhookTimestamp":%d,"actor":{"id":"someone-else"},"data":{"identifier":"ENG-123","labelIds":["label-factory"],"labels":[{"id":"label-factory","name":"Factory"}]},"updatedFrom":{"labelIds":[]}}`,
		testNow.UnixMilli(),
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedWebhookRequest(body, "delivery-1", testSecret))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if snapshot := runStore.Snapshot(); snapshot.Total != 0 {
		t.Fatalf("run snapshot = %#v", snapshot)
	}
	if got := notifier.count.Load(); got != 0 {
		t.Fatalf("notifications = %d, want 0", got)
	}
}

func TestGitHubWebhookPersistsAndDeduplicatesSignedDelivery(t *testing.T) {
	t.Parallel()

	handler, journalPath := testHandlerWithGitHub(t)
	body := `{"action":"completed","repository":{"full_name":"tomnagengast/network"},"check_run":{"status":"completed","conclusion":"success","head_sha":"abc","pull_requests":[{"number":42}],"check_suite":{"head_branch":"eng-42-fix"}}}`
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedGitHubWebhookRequest(body, "github-delivery-1", "check_run", testGitHubSecret))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}

	batch, err := githubhook.Read(journalPath, githubhook.Filter{Repository: "tomnagengast/network", PullRequest: 42}, 0)
	if err != nil {
		t.Fatalf("read GitHub journal: %v", err)
	}
	if batch.Cursor != 1 || len(batch.Events) != 1 || batch.Events[0].Conclusion != "success" {
		t.Fatalf("batch = %#v", batch)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/activity", nil))
	if !strings.Contains(recorder.Body.String(), `"type":"github/check_run"`) {
		t.Fatalf("activity missing GitHub event: %s", recorder.Body.String())
	}
}

func TestGitHubWebhookRejectsInvalidSignature(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	body := `{"repository":{"full_name":"tomnagengast/network"}}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedGitHubWebhookRequest(body, "github-delivery-1", "ping", []byte("wrong")))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestGitHubSignatureMatchesPublishedVector(t *testing.T) {
	t.Parallel()
	secret := []byte("It's a Secret to Everybody")
	signature := "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if !validGitHubSignature(secret, []byte("Hello, World!"), signature) {
		t.Fatal("published GitHub signature did not validate")
	}
}

var testSecret = []byte("linear-test-secret")
var testGitHubSecret = []byte("github-test-secret")

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, _, _ := testHandlerWithRuns(t)
	return handler
}

type testNotifier struct {
	count atomic.Int32
}

type testObserver struct {
	view   agentrun.AgentView
	err    error
	lastID string
}

func (o *testObserver) Observe(_ context.Context, id string) (agentrun.AgentView, error) {
	o.lastID = id
	return o.view, o.err
}

func (n *testNotifier) Notify() {
	n.count.Add(1)
}

func testHandlerWithRuns(t *testing.T) (http.Handler, *agentrun.Store, *testNotifier) {
	t.Helper()

	store, err := activity.Open(filepath.Join(t.TempDir(), "activity.json"), 10)
	if err != nil {
		t.Fatalf("open activity store: %v", err)
	}
	runStore, err := agentrun.Open(filepath.Join(t.TempDir(), "agent-runs.json"), 10)
	if err != nil {
		t.Fatalf("open agent run store: %v", err)
	}
	notifier := &testNotifier{}
	githubEvents, err := githubhook.Open(filepath.Join(t.TempDir(), "github-events.json"), 10)
	if err != nil {
		t.Fatalf("open GitHub journal: %v", err)
	}
	handler, err := New(Config{
		Web:           testWeb(),
		ActivityStore: store,
		RunStore:      runStore,
		RunNotifier:   notifier,
		AgentObserver: &testObserver{err: agentrun.ErrRunNotFound},
		ViewerAuth:    testViewerAuth(t),
		LinearSecret:  testSecret,
		GitHubSecret:  testGitHubSecret,
		GitHubEvents:  githubEvents,
		TriggerActor:  testActorID,
		Now:           func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler, runStore, notifier
}

func testHandlerWithObserver(t *testing.T, observer AgentObserver) http.Handler {
	t.Helper()
	handler, _ := testHandlerWithObserverAndStore(t, observer)
	return handler
}

func testHandlerWithObserverAndStore(t *testing.T, observer AgentObserver) (http.Handler, *agentrun.Store) {
	t.Helper()
	store, err := activity.Open(filepath.Join(t.TempDir(), "activity.json"), 10)
	if err != nil {
		t.Fatalf("open activity store: %v", err)
	}
	runStore, err := agentrun.Open(filepath.Join(t.TempDir(), "agent-runs.json"), 10)
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	githubEvents, err := githubhook.Open(filepath.Join(t.TempDir(), "github-events.json"), 10)
	if err != nil {
		t.Fatalf("open GitHub journal: %v", err)
	}
	handler, err := New(Config{
		Web:           testWeb(),
		ActivityStore: store,
		RunStore:      runStore,
		RunNotifier:   &testNotifier{},
		AgentObserver: observer,
		ViewerAuth:    testViewerAuth(t),
		LinearSecret:  testSecret,
		GitHubSecret:  testGitHubSecret,
		GitHubEvents:  githubEvents,
		TriggerActor:  testActorID,
		Now:           func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler, runStore
}

func testHandlerWithGitHub(t *testing.T) (http.Handler, string) {
	t.Helper()
	directory := t.TempDir()
	activityStore, err := activity.Open(filepath.Join(directory, "activity.json"), 10)
	if err != nil {
		t.Fatalf("open activity store: %v", err)
	}
	runStore, err := agentrun.Open(filepath.Join(directory, "agent-runs.json"), 10)
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	journalPath := filepath.Join(directory, "github-events.json")
	journal, err := githubhook.Open(journalPath, 10)
	if err != nil {
		t.Fatalf("open GitHub journal: %v", err)
	}
	handler, err := New(Config{
		Web:           testWeb(),
		ActivityStore: activityStore,
		RunStore:      runStore,
		RunNotifier:   &testNotifier{},
		AgentObserver: &testObserver{err: agentrun.ErrRunNotFound},
		ViewerAuth:    testViewerAuth(t),
		LinearSecret:  testSecret,
		GitHubSecret:  testGitHubSecret,
		GitHubEvents:  journal,
		TriggerActor:  testActorID,
		Now:           func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler, journalPath
}

func testViewerAuth(t *testing.T) *viewerauth.Authenticator {
	t.Helper()
	auth, err := viewerauth.New(viewerauth.Config{
		ClientID:      "google-client",
		ClientSecret:  "google-secret",
		RedirectURL:   "https://factory.example/auth/google/callback",
		AllowedEmails: []string{"tom@example.com"},
		SessionKey:    bytes.Repeat([]byte("s"), 32),
		BasicUsername: "factory",
		BasicPassword: testViewerPassword,
		Now:           func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("new viewer auth: %v", err)
	}
	return auth
}

func authenticatedRequest(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.SetBasicAuth("factory", testViewerPassword)
	handler.ServeHTTP(recorder, request)
	return recorder
}

func signedWebhookRequest(body, deliveryID string, secret []byte) *http.Request {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(body))
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/linear", strings.NewReader(body))
	request.Header.Set("Linear-Delivery", deliveryID)
	request.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func signedGitHubWebhookRequest(body, deliveryID, eventType string, secret []byte) *http.Request {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(body))
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(body))
	request.Header.Set("X-GitHub-Delivery", deliveryID)
	request.Header.Set("X-GitHub-Event", eventType)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func testWeb() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>Factory</h1>")},
	}
}
