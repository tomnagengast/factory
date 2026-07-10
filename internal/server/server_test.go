package server

import (
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
)

var testNow = time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

const testActorID = "actor-tom"

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
	handler, err := New(
		testWeb(),
		store,
		runStore,
		notifier,
		testSecret,
		testActorID,
		func() time.Time { return testNow },
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler, runStore, notifier
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
