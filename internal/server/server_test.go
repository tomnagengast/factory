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
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/activity"
	"github.com/tomnagengast/network/apps/factory/internal/agentrun"
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

func TestAgentRoutesRequireViewerAuthentication(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	for _, target := range []string{"/agents/run-123", "/agents/run-123/"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusFound {
			t.Fatalf("%s status = %d, want %d", target, recorder.Code, http.StatusFound)
		}
		if got := recorder.Header().Get("Location"); !strings.HasPrefix(got, "/auth/google/login?next=") {
			t.Fatalf("%s redirect = %q", target, got)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/agents/run-123", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("API response = %d, challenge %q", recorder.Code, recorder.Header().Get("WWW-Authenticate"))
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

func TestLinearDoCommentStartsOneRunPerActiveIssue(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier := testHandlerWithRuns(t)
	for i, deliveryID := range []string{"delivery-1", "delivery-2"} {
		body := fmt.Sprintf(
			`{"type":"Comment","action":"create","webhookTimestamp":%d,"actor":{"id":"%s"},"data":{"body":"/do eng-123"}}`,
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
	if got := snapshot.Runs[0]; got.IssueIdentifier != "ENG-123" || got.DuplicateTriggers != 1 {
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

func TestLinearOrdinaryCommentDoesNotStartRun(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier := testHandlerWithRuns(t)
	body := fmt.Sprintf(
		`{"type":"Comment","action":"create","webhookTimestamp":%d,"actor":{"id":"%s"},"data":{"body":"please /do ENG-123 when ready"}}`,
		testNow.UnixMilli(),
		testActorID,
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

func TestLinearDoCommentFromAnotherActorDoesNotStartRun(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier := testHandlerWithRuns(t)
	body := fmt.Sprintf(
		`{"type":"Comment","action":"create","webhookTimestamp":%d,"actor":{"id":"someone-else"},"data":{"body":"/do ENG-123"}}`,
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

var testSecret = []byte("linear-test-secret")

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
	handler, err := New(Config{
		Web:           testWeb(),
		ActivityStore: store,
		RunStore:      runStore,
		RunNotifier:   notifier,
		AgentObserver: &testObserver{err: agentrun.ErrRunNotFound},
		ViewerAuth:    testViewerAuth(t),
		LinearSecret:  testSecret,
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
	store, err := activity.Open(filepath.Join(t.TempDir(), "activity.json"), 10)
	if err != nil {
		t.Fatalf("open activity store: %v", err)
	}
	runStore, err := agentrun.Open(filepath.Join(t.TempDir(), "agent-runs.json"), 10)
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	handler, err := New(Config{
		Web:           testWeb(),
		ActivityStore: store,
		RunStore:      runStore,
		RunNotifier:   &testNotifier{},
		AgentObserver: observer,
		ViewerAuth:    testViewerAuth(t),
		LinearSecret:  testSecret,
		TriggerActor:  testActorID,
		Now:           func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler
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

func signedWebhookRequest(body, deliveryID string, secret []byte) *http.Request {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(body))
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/linear", strings.NewReader(body))
	request.Header.Set("Linear-Delivery", deliveryID)
	request.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func testWeb() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>Factory</h1>")},
	}
}
