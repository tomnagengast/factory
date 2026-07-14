package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/viewerauth"
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
	want := healthResponse{Status: "ok", App: "factory", Wire: wireHealthStatus{}, BuildIdentity: testBuildIdentity()}
	if got != want {
		t.Fatalf("response = %#v, want %#v", got, want)
	}
}

func TestHealthzReportsPendingWireAsDegraded(t *testing.T) {
	t.Parallel()
	handler, _, _, _, wire := testHandlerWithRunsAndSettingsAndWire(t)
	if err := wire.Handle(eventwire.Filter{Source: eventwire.SourceFactory}, func(context.Context, eventwire.Record) error {
		return errors.New("temporary projection failure")
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	event := eventwire.Event{
		ID: "factory:test:pending", Source: eventwire.SourceFactory, Type: "service",
		Action: "heartbeat", Subject: "factory", ReceivedAt: testNow,
	}
	if _, _, err := wire.Publish(context.Background(), event); err == nil {
		t.Fatal("pending event published without transient error")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	var got healthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "degraded" || got.Wire.Pending != 1 || got.Wire.Total != 1 || got.Wire.Dispatched != 0 {
		t.Fatalf("response = %#v", got)
	}
}

func TestUnknownFrontendRouteIsNotFound(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/future-route", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
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

func TestPrivateCanonicalRoutesRequireViewerAuthentication(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	for _, target := range []string{
		"/wire",
		"/agents",
		"/agents/ENG-23/1783714439062/run",
		"/settings",
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
		"/api/wire",
		"/api/wire/1",
		"/api/agents",
		"/api/agents/ENG-23/1783714439062/run",
		"/api/settings",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Basic ZmFjdG9yeTp2aWV3ZXItdGVzdC1wYXNzd29yZA==")
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("%s API response = %d, challenge %q", target, recorder.Code, recorder.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestPublicHomePageServesFrontendWithoutAuthentication(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/home", nil))

	if recorder.Code != http.StatusOK || recorder.Body.String() != "<h1>Factory</h1>" {
		t.Fatalf("home page = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestDeprecatedAndMalformedRoutesAreNotFoundWithoutRedirects(t *testing.T) {
	t.Parallel()
	handler := testHandler(t)
	for _, target := range []string{
		"/activity", "/activity/linear", "/activity/agents", "/agents/run-123",
		"/home/", "/wire/", "/agents/", "/settings/", "/agents/ENG-23/1783714439062/run/",
		"/api/activity", "/api/activity/linear", "/api/activity/agents", "/api/agents/run-123",
		"//wire", "/wire/../home",
	} {
		recorder := authenticatedRequest(t, handler, target)
		if recorder.Code != http.StatusNotFound || recorder.Header().Get("Location") != "" {
			t.Fatalf("%s response = %d, location %q", target, recorder.Code, recorder.Header().Get("Location"))
		}
	}
}

func TestMalformedAgentReferencesAreNotFoundBeforeAuthentication(t *testing.T) {
	t.Parallel()
	handler := testHandler(t)
	for _, target := range []string{
		"/agents/not-an-issue/123/run",
		"/agents/ENG-23/not-a-time/run",
		"/api/agents/not-an-issue/123/run",
		"/api/agents/ENG-23/not-a-time/run",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound || recorder.Header().Get("Location") != "" {
			t.Fatalf("%s response = %d, location %q", target, recorder.Code, recorder.Header().Get("Location"))
		}
	}
}

func TestAuthenticatedSettingsPageAndAPI(t *testing.T) {
	t.Parallel()

	handler, _, _, store := testHandlerWithRunsAndSettings(t)
	page := authenticatedRequest(t, handler, "/settings")
	if page.Code != http.StatusOK || page.Body.String() != "<h1>Factory</h1>" {
		t.Fatalf("settings page = %d %q", page.Code, page.Body.String())
	}

	get := authenticatedRequest(t, handler, "/api/settings")
	var current settings.Snapshot
	if err := json.NewDecoder(get.Body).Decode(&current); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if get.Code != http.StatusOK || current.Revision != 0 || current.Triggers.LinearLabel.Label != "Factory" {
		t.Fatalf("settings response = %d %#v", get.Code, current)
	}

	current.Triggers.LinearLabel.Label = "Build"
	put := authenticatedSettingsRequest(t, handler, current, "")
	var updated settings.Snapshot
	if err := json.NewDecoder(put.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated settings: %v", err)
	}
	if put.Code != http.StatusOK || updated.Revision != 1 || updated.Triggers.LinearLabel.Label != "Build" {
		t.Fatalf("update response = %d %#v", put.Code, updated)
	}
	if got := store.Snapshot(); got.Revision != 1 || got.Triggers.LinearLabel.Label != "Build" {
		t.Fatalf("persisted settings = %#v", got)
	}
}

func TestSettingsAPIRejectsUnsafeAndStaleWrites(t *testing.T) {
	t.Parallel()

	handler, _, _, store := testHandlerWithRunsAndSettings(t)
	candidate := store.Snapshot()

	crossOrigin := authenticatedSettingsRequest(t, handler, candidate, "https://attacker.example")
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", crossOrigin.Code, http.StatusForbidden)
	}

	malformed := httptest.NewRecorder()
	malformedRequest := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"schema":1,"unknown":true}`))
	malformedRequest.AddCookie(viewerSessionCookie(t, handler))
	malformedRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(malformed, malformedRequest)
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field status = %d, want %d", malformed.Code, http.StatusBadRequest)
	}

	tooLarge := httptest.NewRecorder()
	tooLargeRequest := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(strings.Repeat(" ", maxSettingsBody+1)))
	tooLargeRequest.AddCookie(viewerSessionCookie(t, handler))
	tooLargeRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(tooLarge, tooLargeRequest)
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want %d", tooLarge.Code, http.StatusRequestEntityTooLarge)
	}

	first := authenticatedSettingsRequest(t, handler, candidate, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first update status = %d", first.Code)
	}
	stale := authenticatedSettingsRequest(t, handler, candidate, "")
	var conflict settings.Snapshot
	if err := json.NewDecoder(stale.Body).Decode(&conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if stale.Code != http.StatusConflict || conflict.Revision != 1 || store.Snapshot().Revision != 1 {
		t.Fatalf("stale response = %d %#v, state %#v", stale.Code, conflict, store.Snapshot())
	}
}

func TestLinearLabelSettingsControlNewRunsWithoutDroppingActivity(t *testing.T) {
	t.Parallel()

	handler, runStore, _, configuration := testHandlerWithRunsAndSettings(t)
	candidate := configuration.Snapshot()
	candidate.Triggers.LinearLabel.Enabled = false
	if _, err := configuration.Update(candidate.Revision, candidate, testNow); err != nil {
		t.Fatalf("disable label trigger: %v", err)
	}
	body := fmt.Sprintf(
		`{"type":"Issue","action":"update","webhookTimestamp":%d,"actor":{"id":"%s"},"data":{"identifier":"ENG-123","labelIds":["label-factory"],"labels":[{"id":"label-factory","name":"Factory"}]},"updatedFrom":{"labelIds":[]}}`,
		testNow.UnixMilli(),
		testActorID,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedWebhookRequest(body, "disabled-label", testSecret))
	if recorder.Code != http.StatusOK || runStore.Snapshot().Total != 0 {
		t.Fatalf("disabled trigger response = %d, runs %#v", recorder.Code, runStore.Snapshot())
	}
	activityRecorder := httptest.NewRecorder()
	handler.ServeHTTP(activityRecorder, httptest.NewRequest(http.MethodGet, "/api/home", nil))
	var activity homeResponse
	if err := json.NewDecoder(activityRecorder.Body).Decode(&activity); err != nil {
		t.Fatalf("decode activity: %v", err)
	}
	if activity.Total != 1 {
		t.Fatalf("activity total = %d, want 1", activity.Total)
	}
}

func TestLinearCommentSettingsKeepJournalWithoutStartingContinuation(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier, journalPath, configuration := testHandlerWithLinearCommentsAndSettings(t)
	prior, _, err := runStore.Claim(agentrun.Trigger{
		DeliveryID: "label-delivery", IssueIdentifier: "ENG-123", Kind: agentrun.TriggerKindLabel,
	}, testNow.Add(-time.Minute))
	if err != nil {
		t.Fatalf("seed prior run: %v", err)
	}
	if err := runStore.Finish(prior.ID, agentrun.StateSucceeded, 1, "done", testNow.Add(-time.Second)); err != nil {
		t.Fatalf("finish prior run: %v", err)
	}
	candidate := configuration.Snapshot()
	candidate.Triggers.LinearComment.Enabled = false
	if _, err := configuration.Update(candidate.Revision, candidate, testNow); err != nil {
		t.Fatalf("disable comment trigger: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedWebhookRequest(testLinearCommentBody("comment-disabled", "Please continue"), "comment-disabled-delivery", testSecret))
	if recorder.Code != http.StatusOK || runStore.Snapshot().Total != 1 || runStore.Snapshot().Active != 0 {
		t.Fatalf("disabled continuation response = %d, runs %#v", recorder.Code, runStore.Snapshot())
	}
	if notifier.count.Load() != 0 {
		t.Fatalf("notifications = %d, want 0", notifier.count.Load())
	}
	batch, err := linearhook.Read(journalPath, linearhook.Filter{IssueIdentifier: "ENG-123"}, 0)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].CommentID != "comment-disabled" {
		t.Fatalf("comment journal = %#v, %v", batch, err)
	}
}

func TestAuthenticatedAgentPageServesFrontend(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	recorder := authenticatedRequest(t, handler, "/agents/ENG-23/1783714439062/run")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got, want := recorder.Body.String(), "<h1>Factory</h1>"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
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
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/home", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("activity status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var got homeResponse
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

func TestAuthenticatedWirePagesAllSourcesAndReadsLinearPayload(t *testing.T) {
	t.Parallel()

	handler, _, _, _, wire := testHandlerWithRunsAndSettingsAndWire(t)
	body := fmt.Sprintf(
		`{"type":"Issue","action":"update","webhookTimestamp":%d,"data":{"identifier":"ENG-23","private":"winery roadmap"}}`,
		testNow.UnixMilli(),
	)
	webhook := httptest.NewRecorder()
	handler.ServeHTTP(webhook, signedWebhookRequest(body, "delivery-private", testSecret))
	if webhook.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, want %d", webhook.Code, http.StatusOK)
	}
	if _, _, err := wire.Publish(context.Background(), eventwire.Event{
		ID: "telemetry:future", Source: eventwire.Source("telemetry"), Type: "future-kind", Action: "observed", ReceivedAt: testNow.Add(time.Second),
	}); err != nil {
		t.Fatalf("publish future event: %v", err)
	}

	pageRecorder := authenticatedRequest(t, handler, "/api/wire?page=1&pageSize=25")
	var page eventwire.Page
	if err := json.NewDecoder(pageRecorder.Body).Decode(&page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if pageRecorder.Code != http.StatusOK || page.Retained != 2 || page.Matching != 2 || len(page.Records) != 2 {
		t.Fatalf("page response = %d %#v", pageRecorder.Code, page)
	}
	if len(page.SourceCounts) != 2 {
		t.Fatalf("source counts = %#v", page.SourceCounts)
	}
	if len(page.TypeCounts) != 2 {
		t.Fatalf("type counts = %#v", page.TypeCounts)
	}

	linearRecord := page.Records[1]
	if linearRecord.Event.Source != eventwire.SourceLinear {
		t.Fatalf("linear record = %#v", linearRecord)
	}
	detailRecorder := authenticatedRequest(t, handler, "/api/wire/"+strconv.FormatUint(linearRecord.Sequence, 10))
	var detail wireDetailResponse
	if err := json.NewDecoder(detailRecorder.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detailRecorder.Code != http.StatusOK || !detail.PayloadAvailable || !strings.Contains(string(detail.Payload), "winery roadmap") {
		t.Fatalf("detail response = %d %#v", detailRecorder.Code, detail)
	}

	filteredRecorder := authenticatedRequest(t, handler, "/api/wire?source=telemetry&type=future-kind")
	var filtered eventwire.Page
	if err := json.NewDecoder(filteredRecorder.Body).Decode(&filtered); err != nil {
		t.Fatalf("decode filtered wire: %v", err)
	}
	if filteredRecorder.Code != http.StatusOK || filtered.Matching != 1 || len(filtered.Records) != 1 || filtered.Records[0].Event.Type != "future-kind" {
		t.Fatalf("filtered wire = %d %#v", filteredRecorder.Code, filtered)
	}

	publicRecorder := httptest.NewRecorder()
	handler.ServeHTTP(publicRecorder, httptest.NewRequest(http.MethodGet, "/api/home", nil))
	if publicBody := publicRecorder.Body.String(); strings.Contains(publicBody, "winery roadmap") || strings.Contains(publicBody, "ENG-23") {
		t.Fatalf("public activity leaked raw payload: %s", publicBody)
	}
}

func TestWireAPIRejectsInvalidQueriesAndSequences(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	for _, target := range []string{
		"/api/wire?page=0",
		"/api/wire?pageSize=101",
		"/api/wire?source=Unknown",
		"/api/wire?type=%20Issue",
	} {
		recorder := authenticatedRequest(t, handler, target)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want %d", target, recorder.Code, http.StatusBadRequest)
		}
	}
	recorder := authenticatedRequest(t, handler, "/api/wire/not-a-sequence")
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

	summaryRecorder := authenticatedRequest(t, handler, "/api/agents")
	var summary agentrun.ActivitySnapshot
	if err := json.NewDecoder(summaryRecorder.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summaryRecorder.Code != http.StatusOK || summary.Total != 1 || summary.Active != 1 || summary.Runs[0].IssueIdentifier != "ENG-23" {
		t.Fatalf("summary response = %d %#v", summaryRecorder.Code, summary)
	}

	target := fmt.Sprintf("/api/agents/eng-23/%d/run", testNow.UnixMilli())
	detailRecorder := authenticatedRequest(t, handler, target)
	var detail agentrun.AgentView
	if err := json.NewDecoder(detailRecorder.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detailRecorder.Code != http.StatusOK || detail.ID != run.ID || observer.lastID != run.ID {
		t.Fatalf("detail response = %d %#v, observer ID %q", detailRecorder.Code, detail, observer.lastID)
	}

	for _, invalidTarget := range []string{
		"/api/agents/not-an-issue/123/run",
		"/api/agents/ENG-23/not-a-time/run",
	} {
		if recorder := authenticatedRequest(t, handler, invalidTarget); recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", invalidTarget, recorder.Code, http.StatusNotFound)
		}
	}
	if recorder := authenticatedRequest(t, handler, "/api/agents/ENG-24/"+strconv.FormatInt(testNow.UnixMilli(), 10)+"/run"); recorder.Code != http.StatusNotFound {
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

func TestLinearProjectWebhookEnqueuesSetupFromProtectedPayload(t *testing.T) {
	t.Parallel()
	setups := &testProjectSetups{}
	handler, journalPath := testHandlerWithProjectSetups(t, setups)
	description := "New app setup.\nGitHub Repo: tomnagengast/cellar\nLocal Path: /Users/tom/repos/tomnagengast/cellar\nCloud URL: https://cellar.nags.cloud"
	body := fmt.Sprintf(
		`{"type":"Project","action":"create","webhookTimestamp":%d,"actor":{"id":%q},"data":{"id":"project-cellar","name":"Cellar","description":%q}}`,
		testNow.UnixMilli(), testActorID, description,
	)
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedWebhookRequest(body, "project-delivery", testSecret))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}
	requests := setups.Requests()
	if len(requests) != 1 || requests[0] != (projectsetup.Request{
		ProjectID: "project-cellar", ProjectName: "Cellar", Description: description,
	}) {
		t.Fatalf("requests = %#v", requests)
	}
	wireData, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read wire: %v", err)
	}
	if strings.Contains(string(wireData), description) || strings.Contains(string(wireData), "/Users/tom/repos") {
		t.Fatalf("wire leaked project description: %s", wireData)
	}

	otherActor := strings.Replace(body, testActorID, "other-actor", 1)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedWebhookRequest(otherActor, "project-other-actor", testSecret))
	if recorder.Code != http.StatusOK || len(setups.Requests()) != 1 {
		t.Fatalf("other actor response = %d, requests = %#v", recorder.Code, setups.Requests())
	}
}

func TestHealthzReportsFailedProjectSetupAsDegraded(t *testing.T) {
	t.Parallel()
	setups := &testProjectSetups{snapshot: projectsetup.PublicSnapshot{Total: 1, Failed: 1}}
	handler, _ := testHandlerWithProjectSetups(t, setups)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	var got healthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "degraded" || got.ProjectSetups.Failed != 1 {
		t.Fatalf("response = %#v", got)
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
	handler.ServeHTTP(activityRecorder, httptest.NewRequest(http.MethodGet, "/api/home", nil))
	if body := activityRecorder.Body.String(); strings.Contains(body, "ENG-123") || strings.Contains(body, testActorID) {
		t.Fatalf("public activity leaked private Linear context: %s", body)
	}
}

func TestPermanentRepositoryRoutingFailureDoesNotBlockLaterRun(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	activityStore, err := activity.Open(filepath.Join(directory, "activity.json"), 10)
	if err != nil {
		t.Fatalf("open activity store: %v", err)
	}
	runStore, err := agentrun.Open(filepath.Join(directory, "agent-runs.json"), 10)
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	githubEvents, err := githubhook.Open(filepath.Join(directory, "github-events.json"), 10)
	if err != nil {
		t.Fatalf("open GitHub journal: %v", err)
	}
	linearComments, err := linearhook.Open(filepath.Join(directory, "linear-comments.json"), 10)
	if err != nil {
		t.Fatalf("open Linear comments: %v", err)
	}
	wire := testEventWire(t, githubEvents.Total(), linearComments.Total())
	handler, err := New(Config{
		Web:            testWeb(),
		ActivityStore:  activityStore,
		RunStore:       runStore,
		RunNotifier:    &testNotifier{},
		AgentObserver:  &testObserver{err: agentrun.ErrRunNotFound},
		Settings:       testSettingsStore(t),
		ViewerAuth:     testViewerAuth(t),
		LinearSecret:   testSecret,
		GitHubSecret:   testGitHubSecret,
		Events:         wire,
		GitHubEvents:   githubEvents,
		LinearComments: linearComments,
		RepositoryResolver: testRepositoryResolver{
			"ENG-404": {err: eventwire.Permanent(errors.New("repository is not allowlisted"))},
			"ENG-123": {config: agentrun.RepositoryConfig{
				App: "factory", Repository: "tomnagengast/network", RepoURL: "git@github.com:tomnagengast/network.git",
				RepoPath: "/Users/tom/repos/tomnagengast/network", ProjectPath: "/Users/tom/repos/tomnagengast/network",
				ManagedRoot: "/Users/tom/repos/tomnagengast", BaseBranch: "main", CloudURL: "https://network.nags.cloud",
			}},
		},
		ProjectSetups: &testProjectSetups{},
		TriggerActor:  testActorID,
		Now:           func() time.Time { return testNow },
		Build:         testBuildIdentity(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	for _, issue := range []string{"ENG-404", "ENG-123"} {
		body := fmt.Sprintf(
			`{"type":"Issue","action":"update","webhookTimestamp":%d,"actor":{"id":"%s"},"data":{"identifier":"%s","labelIds":["label-factory"],"labels":[{"id":"label-factory","name":"Factory"}]},"updatedFrom":{"labelIds":[]}}`,
			testNow.UnixMilli(), testActorID, issue,
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedWebhookRequest(body, "delivery-"+strings.ToLower(issue), testSecret))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", issue, recorder.Code, http.StatusOK)
		}
	}

	snapshot := runStore.Snapshot()
	if snapshot.Total != 1 || snapshot.Runs[0].IssueIdentifier != "ENG-123" || snapshot.Runs[0].CloudURL != "https://network.nags.cloud" {
		t.Fatalf("runs = %#v", snapshot)
	}
	status := wire.Status()
	if status.Pending != 0 || status.RejectedTotal != 1 || status.LastRejection == nil || status.LastRejection.EventID != "linear:delivery-eng-404" {
		t.Fatalf("wire status = %#v", status)
	}
}

func TestLinearWebhookReplaysStagedPayloadWithoutProviderRedelivery(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	activityStore, err := activity.Open(filepath.Join(directory, "activity.json"), 10)
	if err != nil {
		t.Fatalf("open activity: %v", err)
	}
	flaky := &flakyActivityStore{Store: activityStore, failNext: true}
	runStore, err := agentrun.Open(filepath.Join(directory, "runs.json"), 10)
	if err != nil {
		t.Fatalf("open runs: %v", err)
	}
	githubEvents, err := githubhook.Open(filepath.Join(directory, "github-events.json"), 10)
	if err != nil {
		t.Fatalf("open GitHub events: %v", err)
	}
	linearComments, err := linearhook.Open(filepath.Join(directory, "linear-comments.json"), 10)
	if err != nil {
		t.Fatalf("open Linear comments: %v", err)
	}
	journalPath := filepath.Join(directory, "system-events.jsonl")
	journal, err := eventwire.Open(journalPath, 100, nil)
	if err != nil {
		t.Fatalf("open wire journal: %v", err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	notifier := &testNotifier{}
	handler, err := New(Config{
		Web:            testWeb(),
		ActivityStore:  flaky,
		RunStore:       runStore,
		RunNotifier:    notifier,
		AgentObserver:  &testObserver{err: agentrun.ErrRunNotFound},
		Settings:       testSettingsStore(t),
		ViewerAuth:     testViewerAuth(t),
		LinearSecret:   testSecret,
		GitHubSecret:   testGitHubSecret,
		Events:         wire,
		GitHubEvents:   githubEvents,
		LinearComments: linearComments,
		ProjectSetups:  &testProjectSetups{},
		TriggerActor:   testActorID,
		Now:            func() time.Time { return testNow },
		Build:          testBuildIdentity(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	body := fmt.Sprintf(
		`{"type":"Issue","action":"update","webhookTimestamp":%d,"private":"must not enter wire","actor":{"id":"%s"},"data":{"identifier":"ENG-123","labelIds":["label-factory"],"labels":[{"id":"label-factory","name":"Factory"}]},"updatedFrom":{"labelIds":[]}}`,
		testNow.UnixMilli(),
		testActorID,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedWebhookRequest(body, "delivery-replay", testSecret))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if len(journal.Pending()) != 1 {
		t.Fatalf("pending records = %#v", journal.Pending())
	}
	if err := wire.CatchUp(context.Background()); err != nil {
		t.Fatalf("internal catch-up: %v", err)
	}
	if len(journal.Pending()) != 0 || activityStore.Snapshot().Total != 1 || runStore.Snapshot().Total != 1 {
		t.Fatalf("replayed state: pending=%#v activity=%#v runs=%#v", journal.Pending(), activityStore.Snapshot(), runStore.Snapshot())
	}
	if notifier.count.Load() != 1 {
		t.Fatalf("notifications = %d, want 1", notifier.count.Load())
	}
	wireData, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read wire: %v", err)
	}
	if strings.Contains(string(wireData), "must not enter wire") {
		t.Fatal("wire contains staged private payload")
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
			name: "Factory label with invalid issue identifier",
			body: `{"type":"Issue","action":"update","actor":{"id":"actor-tom"},"data":{"identifier":"not-an-issue","labelIds":["label-factory"],"labels":[{"id":"label-factory","name":"Factory"}]},"updatedFrom":{"labelIds":[]}}`,
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

func TestLinearCommentsJournalUnmanagedIssuesWithoutStartingRuns(t *testing.T) {
	t.Parallel()
	handler, runStore, notifier, journalPath := testHandlerWithLinearComments(t)
	comments := []struct {
		deliveryID string
		commentID  string
		parentID   string
	}{
		{deliveryID: "comment-delivery-1", commentID: "comment-1"},
		{deliveryID: "comment-delivery-2", commentID: "comment-2", parentID: "comment-1"},
	}
	for _, comment := range comments {
		body := fmt.Sprintf(
			`{"type":"Comment","action":"create","url":"https://linear.example/comment/%s","webhookTimestamp":%d,"actor":{"id":"%s"},"data":{"id":"%s","body":"Please handle this","issueId":"issue-123","parentId":%q,"issue":{"id":"issue-123","identifier":"ENG-123"}}}`,
			comment.commentID,
			testNow.UnixMilli(),
			testActorID,
			comment.commentID,
			comment.parentID,
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedWebhookRequest(body, comment.deliveryID, testSecret))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}

	batch, err := linearhook.Read(journalPath, linearhook.Filter{IssueIdentifier: "eng-123"}, 0)
	if err != nil {
		t.Fatalf("read Linear comment journal: %v", err)
	}
	if batch.Cursor != 2 || len(batch.Events) != 2 || batch.Events[1].ParentID != "comment-1" {
		t.Fatalf("batch = %#v", batch)
	}
	if snapshot := runStore.Snapshot(); snapshot.Total != 0 || snapshot.Active != 0 {
		t.Fatalf("run snapshot = %#v", snapshot)
	}
	if got := notifier.count.Load(); got != 0 {
		t.Fatalf("notifications = %d, want 0", got)
	}
}

func TestLinearCommentStartsOneContinuationAfterTerminalRun(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier, journalPath := testHandlerWithLinearComments(t)
	prior, _, err := runStore.Claim(agentrun.Trigger{
		DeliveryID:      "label-delivery",
		IssueIdentifier: "ENG-123",
		Kind:            agentrun.TriggerKindLabel,
	}, testNow.Add(-2*time.Second))
	if err != nil {
		t.Fatalf("seed prior run: %v", err)
	}
	if err := runStore.Finish(prior.ID, agentrun.StateSucceeded, 1, "done", testNow.Add(-time.Second)); err != nil {
		t.Fatalf("finish prior run: %v", err)
	}

	body := testLinearCommentBody("comment-1", "Please continue")
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedWebhookRequest(body, "comment-delivery", testSecret))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}

	snapshot := runStore.Snapshot()
	if snapshot.Total != 2 || snapshot.Active != 1 || len(snapshot.Runs) != 2 {
		t.Fatalf("run snapshot = %#v", snapshot)
	}
	continuation := snapshot.Runs[0]
	if continuation.ID == prior.ID || continuation.State != agentrun.StatePending || continuation.TriggerKind != agentrun.TriggerKindComment {
		t.Fatalf("continuation = %#v", continuation)
	}
	if got := notifier.count.Load(); got != 1 {
		t.Fatalf("notifications = %d, want 1", got)
	}
	batch, err := linearhook.Read(journalPath, linearhook.Filter{IssueIdentifier: "ENG-123"}, 0)
	if err != nil || len(batch.Events) != 1 {
		t.Fatalf("batch = %#v, %v", batch, err)
	}
}

func TestLinearCommentCoalescesIntoActiveRunWithoutNotification(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier, _ := testHandlerWithLinearComments(t)
	active, _, err := runStore.Claim(agentrun.Trigger{
		DeliveryID:      "label-delivery",
		IssueIdentifier: "ENG-123",
		Kind:            agentrun.TriggerKindLabel,
	}, testNow.Add(-time.Second))
	if err != nil {
		t.Fatalf("seed active run: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedWebhookRequest(testLinearCommentBody("comment-1", "Please continue"), "comment-delivery", testSecret))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	snapshot := runStore.Snapshot()
	if snapshot.Total != 1 || snapshot.Active != 1 || len(snapshot.Runs) != 1 || snapshot.Runs[0].ID != active.ID || snapshot.Runs[0].DuplicateTriggers != 1 {
		t.Fatalf("run snapshot = %#v", snapshot)
	}
	if got := notifier.count.Load(); got != 0 {
		t.Fatalf("notifications = %d, want 0", got)
	}
}

func TestLinearCommentResumesParkedRunAndNotifiesManager(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier, _ := testHandlerWithLinearComments(t)
	run, _, err := runStore.Claim(agentrun.Trigger{
		DeliveryID:      "label-delivery",
		IssueIdentifier: "ENG-123",
		Kind:            agentrun.TriggerKindLabel,
	}, testNow.Add(-2*time.Second))
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := runStore.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), testNow.Add(-2*time.Second)); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	checkpoint := agentrun.ReadyCheckpoint{
		ContractVersion: agentrun.LifecycleContractVersion,
		RunID:           run.ID,
		Repository:      "tomnagengast/network",
		PullRequest:     8,
		BaseBranch:      "main",
		HeadBranch:      "eng-123-fix",
		VerifiedHeadOID: "08c1c678a0b23bbe8e2dc2da1e398583d7e4c416",
		CreatedAt:       testNow.Add(-time.Second),
	}
	if err := runStore.MarkAwaitingMerge(run.ID, checkpoint, testNow.Add(time.Hour), 1, testNow.Add(-time.Second)); err != nil {
		t.Fatalf("mark awaiting: %v", err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedWebhookRequest(testLinearCommentBody("comment-1", "Please revise"), "comment-delivery", testSecret))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	resumed, ok := runStore.Find(run.ID)
	if !ok || resumed.State != agentrun.StatePending || resumed.TriggerKind != agentrun.TriggerKindComment || resumed.ResumeCount != 1 {
		t.Fatalf("resumed = %#v, found=%t", resumed, ok)
	}
	if got := notifier.count.Load(); got != 1 {
		t.Fatalf("notifications = %d, want 1", got)
	}
}

func TestLinearCommentWakeFiltersFactoryAndUnsupportedComments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		action string
		actor  string
		body   string
	}{
		{name: "Factory signature", action: "create", actor: testActorID, body: "Done.\n\n🐘"},
		{name: "Factory marker", action: "create", actor: testActorID, body: "Done.\n\n🐘 `codex-do:ENG-123:plan-gate:r1`"},
		{name: "other actor", action: "create", actor: "someone-else", body: "Please change this"},
		{name: "comment update", action: "update", actor: testActorID, body: "Please change this"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, runStore, notifier, journalPath := testHandlerWithLinearComments(t)
			prior, _, err := runStore.Claim(agentrun.Trigger{DeliveryID: "label-delivery", IssueIdentifier: "ENG-123", Kind: agentrun.TriggerKindLabel}, testNow.Add(-2*time.Second))
			if err != nil {
				t.Fatalf("seed prior run: %v", err)
			}
			if err := runStore.Finish(prior.ID, agentrun.StateSucceeded, 1, "done", testNow.Add(-time.Second)); err != nil {
				t.Fatalf("finish prior run: %v", err)
			}
			body := fmt.Sprintf(
				`{"type":"Comment","action":%q,"webhookTimestamp":%d,"actor":{"id":%q},"data":{"id":"comment-1","body":%q,"issueId":"issue-123","issue":{"identifier":"ENG-123"}}}`,
				test.action,
				testNow.UnixMilli(),
				test.actor,
				test.body,
			)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, signedWebhookRequest(body, "delivery-1", testSecret))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			batch, err := linearhook.Read(journalPath, linearhook.Filter{IssueIdentifier: "ENG-123"}, 0)
			if err != nil || len(batch.Events) != 0 {
				t.Fatalf("batch = %#v, %v", batch, err)
			}
			if snapshot := runStore.Snapshot(); snapshot.Total != 1 || snapshot.Active != 0 {
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
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/home", nil))
	if !strings.Contains(recorder.Body.String(), `"type":"github/check_run"`) {
		t.Fatalf("activity missing GitHub event: %s", recorder.Body.String())
	}
}

func TestGitHubBranchOnlyFailureWakeSchedulesParkedRun(t *testing.T) {
	t.Parallel()

	handler, runStore, notifier := testHandlerWithRuns(t)
	run, _, err := runStore.Claim(agentrun.Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: agentrun.TriggerKindLabel}, testNow.Add(-2*time.Second))
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if err := runStore.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), testNow.Add(-2*time.Second)); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	checkpoint := agentrun.ReadyCheckpoint{
		ContractVersion: agentrun.LifecycleContractVersion,
		RunID:           run.ID,
		Repository:      "tomnagengast/network",
		PullRequest:     8,
		BaseBranch:      "main",
		HeadBranch:      "eng-123-fix",
		VerifiedHeadOID: "08c1c678a0b23bbe8e2dc2da1e398583d7e4c416",
		CreatedAt:       testNow.Add(-time.Second),
	}
	if err := runStore.MarkAwaitingMerge(run.ID, checkpoint, testNow.Add(time.Hour), 1, testNow.Add(-time.Second)); err != nil {
		t.Fatalf("mark awaiting: %v", err)
	}
	body := `{"state":"failure","sha":"08c1c678a0b23bbe8e2dc2da1e398583d7e4c416","branches":[{"name":"eng-123-fix"}],"repository":{"full_name":"tomnagengast/network"}}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, signedGitHubWebhookRequest(body, "status-1", "status", testGitHubSecret))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	parked, _ := runStore.Find(run.ID)
	if !parked.RemediationRequested || parked.LastGitHubCursor == 0 || parked.NextReconcileAt == nil {
		t.Fatalf("parked = %#v", parked)
	}
	if got := notifier.count.Load(); got != 1 {
		t.Fatalf("notifications = %d, want 1", got)
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

func TestGitHubWakeRequiresRemediation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event githubhook.Event
		want  bool
	}{
		{name: "review comment", event: githubhook.Event{Type: "pull_request_review_comment", Action: "created"}, want: true},
		{name: "requested changes", event: githubhook.Event{Type: "pull_request_review", Action: "submitted"}, want: true},
		{name: "failed check", event: githubhook.Event{Type: "check_run", Conclusion: "failure"}, want: true},
		{name: "synchronized pull request", event: githubhook.Event{Type: "pull_request", Action: "synchronize"}, want: true},
		{name: "successful check", event: githubhook.Event{Type: "check_run", Conclusion: "success"}},
		{name: "labeled pull request", event: githubhook.Event{Type: "pull_request", Action: "labeled"}},
		{name: "closed pull request", event: githubhook.Event{Type: "pull_request", Action: "closed"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := githubWakeRequiresRemediation(test.event); got != test.want {
				t.Fatalf("githubWakeRequiresRemediation(%#v) = %t, want %t", test.event, got, test.want)
			}
		})
	}
}

func TestGitHubWebhookRejectsUnprojectableDeliveryBeforeWire(t *testing.T) {
	t.Parallel()

	handler := testHandler(t)
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, signedGitHubWebhookRequest(`{"action":"completed"}`, "github-invalid", "check_run", testGitHubSecret))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want %d", invalid.Code, http.StatusBadRequest)
	}

	valid := httptest.NewRecorder()
	handler.ServeHTTP(valid, signedGitHubWebhookRequest(`{"repository":{"full_name":"tomnagengast/network"}}`, "github-valid", "ping", testGitHubSecret))
	if valid.Code != http.StatusOK {
		t.Fatalf("valid status after rejection = %d, want %d", valid.Code, http.StatusOK)
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

type testProjectSetups struct {
	mu       sync.Mutex
	requests []projectsetup.Request
	snapshot projectsetup.PublicSnapshot
	err      error
}

func (s *testProjectSetups) Enqueue(_ context.Context, request projectsetup.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	return s.err
}

func (s *testProjectSetups) PublicSnapshot() projectsetup.PublicSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

func (s *testProjectSetups) Requests() []projectsetup.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]projectsetup.Request(nil), s.requests...)
}

type testRepositoryResolution struct {
	config agentrun.RepositoryConfig
	err    error
}

type testRepositoryResolver map[string]testRepositoryResolution

func (r testRepositoryResolver) Resolve(_ context.Context, issueIdentifier string) (agentrun.RepositoryConfig, error) {
	resolution, ok := r[issueIdentifier]
	if !ok {
		return agentrun.RepositoryConfig{}, errors.New("unexpected issue identifier")
	}
	return resolution.config, resolution.err
}

type flakyActivityStore struct {
	*activity.Store
	failNext bool
}

func (s *flakyActivityStore) AddStaged(deliveryID string, event activity.Event) (bool, error) {
	if s.failNext {
		s.failNext = false
		return false, errors.New("temporary projection failure")
	}
	return s.Store.AddStaged(deliveryID, event)
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
	handler, runStore, notifier, _ := testHandlerWithRunsAndSettings(t)
	return handler, runStore, notifier
}

func testHandlerWithRunsAndSettings(t *testing.T) (http.Handler, *agentrun.Store, *testNotifier, *settings.Store) {
	t.Helper()
	handler, runStore, notifier, configuration, _ := testHandlerWithRunsAndSettingsAndWire(t)
	return handler, runStore, notifier, configuration
}

func testHandlerWithRunsAndSettingsAndWire(t *testing.T) (http.Handler, *agentrun.Store, *testNotifier, *settings.Store, *eventwire.Wire) {
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
	configuration := testSettingsStore(t)
	githubEvents, err := githubhook.Open(filepath.Join(t.TempDir(), "github-events.json"), 10)
	if err != nil {
		t.Fatalf("open GitHub journal: %v", err)
	}
	linearComments, err := linearhook.Open(filepath.Join(t.TempDir(), "linear-comments.json"), 10)
	if err != nil {
		t.Fatalf("open Linear comment journal: %v", err)
	}
	wire := testEventWire(t, githubEvents.Total(), linearComments.Total())
	handler, err := New(Config{
		Web:            testWeb(),
		ActivityStore:  store,
		RunStore:       runStore,
		RunNotifier:    notifier,
		AgentObserver:  &testObserver{err: agentrun.ErrRunNotFound},
		Settings:       configuration,
		ViewerAuth:     testViewerAuth(t),
		LinearSecret:   testSecret,
		GitHubSecret:   testGitHubSecret,
		Events:         wire,
		GitHubEvents:   githubEvents,
		LinearComments: linearComments,
		ProjectSetups:  &testProjectSetups{},
		TriggerActor:   testActorID,
		Now:            func() time.Time { return testNow },
		Build:          testBuildIdentity(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler, runStore, notifier, configuration, wire
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
	linearComments, err := linearhook.Open(filepath.Join(t.TempDir(), "linear-comments.json"), 10)
	if err != nil {
		t.Fatalf("open Linear comment journal: %v", err)
	}
	handler, err := New(Config{
		Web:            testWeb(),
		ActivityStore:  store,
		RunStore:       runStore,
		RunNotifier:    &testNotifier{},
		AgentObserver:  observer,
		Settings:       testSettingsStore(t),
		ViewerAuth:     testViewerAuth(t),
		LinearSecret:   testSecret,
		GitHubSecret:   testGitHubSecret,
		Events:         testEventWire(t, githubEvents.Total(), linearComments.Total()),
		GitHubEvents:   githubEvents,
		LinearComments: linearComments,
		ProjectSetups:  &testProjectSetups{},
		TriggerActor:   testActorID,
		Now:            func() time.Time { return testNow },
		Build:          testBuildIdentity(),
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
	linearComments, err := linearhook.Open(filepath.Join(directory, "linear-comments.json"), 10)
	if err != nil {
		t.Fatalf("open Linear comment journal: %v", err)
	}
	handler, err := New(Config{
		Web:            testWeb(),
		ActivityStore:  activityStore,
		RunStore:       runStore,
		RunNotifier:    &testNotifier{},
		AgentObserver:  &testObserver{err: agentrun.ErrRunNotFound},
		Settings:       testSettingsStore(t),
		ViewerAuth:     testViewerAuth(t),
		LinearSecret:   testSecret,
		GitHubSecret:   testGitHubSecret,
		Events:         testEventWire(t, journal.Total(), linearComments.Total()),
		GitHubEvents:   journal,
		LinearComments: linearComments,
		ProjectSetups:  &testProjectSetups{},
		TriggerActor:   testActorID,
		Now:            func() time.Time { return testNow },
		Build:          testBuildIdentity(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler, journalPath
}

func testHandlerWithLinearComments(t *testing.T) (http.Handler, *agentrun.Store, *testNotifier, string) {
	t.Helper()
	handler, runStore, notifier, journalPath, _ := testHandlerWithLinearCommentsAndSettings(t)
	return handler, runStore, notifier, journalPath
}

func testHandlerWithLinearCommentsAndSettings(t *testing.T) (http.Handler, *agentrun.Store, *testNotifier, string, *settings.Store) {
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
	githubEvents, err := githubhook.Open(filepath.Join(directory, "github-events.json"), 10)
	if err != nil {
		t.Fatalf("open GitHub journal: %v", err)
	}
	journalPath := filepath.Join(directory, "linear-comments.json")
	linearComments, err := linearhook.Open(journalPath, 10)
	if err != nil {
		t.Fatalf("open Linear comment journal: %v", err)
	}
	notifier := &testNotifier{}
	configuration := testSettingsStore(t)
	handler, err := New(Config{
		Web:            testWeb(),
		ActivityStore:  activityStore,
		RunStore:       runStore,
		RunNotifier:    notifier,
		AgentObserver:  &testObserver{err: agentrun.ErrRunNotFound},
		Settings:       configuration,
		ViewerAuth:     testViewerAuth(t),
		LinearSecret:   testSecret,
		GitHubSecret:   testGitHubSecret,
		Events:         testEventWire(t, githubEvents.Total(), linearComments.Total()),
		GitHubEvents:   githubEvents,
		LinearComments: linearComments,
		ProjectSetups:  &testProjectSetups{},
		TriggerActor:   testActorID,
		Now:            func() time.Time { return testNow },
		Build:          testBuildIdentity(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler, runStore, notifier, journalPath, configuration
}

func testHandlerWithProjectSetups(t *testing.T, setups ProjectSetupController) (http.Handler, string) {
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
	githubEvents, err := githubhook.Open(filepath.Join(directory, "github-events.json"), 10)
	if err != nil {
		t.Fatalf("open GitHub journal: %v", err)
	}
	linearComments, err := linearhook.Open(filepath.Join(directory, "linear-comments.json"), 10)
	if err != nil {
		t.Fatalf("open Linear comment journal: %v", err)
	}
	wirePath := filepath.Join(directory, "system-events.jsonl")
	journal, err := eventwire.Open(wirePath, 100, map[string]uint64{
		githubhook.WireChannel: githubEvents.Total(), linearhook.WireChannel: linearComments.Total(),
	})
	if err != nil {
		t.Fatalf("open event wire: %v", err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new event wire: %v", err)
	}
	handler, err := New(Config{
		Web: testWeb(), ActivityStore: activityStore, RunStore: runStore,
		RunNotifier: &testNotifier{}, AgentObserver: &testObserver{err: agentrun.ErrRunNotFound},
		Settings: testSettingsStore(t), ViewerAuth: testViewerAuth(t),
		LinearSecret: testSecret, GitHubSecret: testGitHubSecret,
		Events: wire, GitHubEvents: githubEvents, LinearComments: linearComments,
		ProjectSetups: setups, TriggerActor: testActorID,
		Now: func() time.Time { return testNow }, Build: testBuildIdentity(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return handler, wirePath
}

func testViewerAuth(t *testing.T) *viewerauth.Authenticator {
	t.Helper()
	auth, err := viewerauth.New(viewerauth.Config{
		ClientID:      "google-client",
		ClientSecret:  "google-secret",
		RedirectURL:   "https://factory.example/auth/google/callback",
		AllowedEmails: []string{"tom@example.com"},
		SessionKey:    bytes.Repeat([]byte("s"), 32),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return testOAuthResponse(`{"access_token":"access-token"}`), nil
			case "https://openidconnect.googleapis.com/v1/userinfo":
				if got := request.Header.Get("Authorization"); got != "Bearer access-token" {
					t.Fatalf("OAuth userinfo authorization = %q", got)
				}
				return testOAuthResponse(`{"sub":"google-subject","email":"tom@example.com","email_verified":true}`), nil
			default:
				t.Fatalf("unexpected OAuth request: %s", request.URL)
				return nil, nil
			}
		})},
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("new viewer auth: %v", err)
	}
	return auth
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testOAuthResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testSettingsStore(t *testing.T) *settings.Store {
	t.Helper()
	store, err := settings.Open(filepath.Join(t.TempDir(), "settings.json"), settings.Defaults(3))
	if err != nil {
		t.Fatalf("open settings store: %v", err)
	}
	return store
}

func testBuildIdentity() BuildIdentity {
	return BuildIdentity{
		Commit:          "08c1c678a0b23bbe8e2dc2da1e398583d7e4c416",
		Tree:            "4236dfd6f63c814726d34887e24659e231fde7a5",
		BuildID:         "test-build",
		DeploymentID:    "test-deployment",
		ContractVersion: "1",
		StartedAt:       testNow,
	}
}

func testEventWire(t *testing.T, githubTotal, linearTotal uint64) *eventwire.Wire {
	t.Helper()
	journal, err := eventwire.Open(
		filepath.Join(t.TempDir(), "system-events.jsonl"),
		100,
		map[string]uint64{
			githubhook.WireChannel: githubTotal,
			linearhook.WireChannel: linearTotal,
		},
	)
	if err != nil {
		t.Fatalf("open event wire: %v", err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new event wire: %v", err)
	}
	return wire
}

func authenticatedRequest(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(viewerSessionCookie(t, handler))
	handler.ServeHTTP(recorder, request)
	return recorder
}

func authenticatedSettingsRequest(t *testing.T, handler http.Handler, candidate settings.Snapshot, origin string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("encode settings: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	request.AddCookie(viewerSessionCookie(t, handler))
	request.Header.Set("Content-Type", "application/json")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func viewerSessionCookie(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/auth/google/login?next=%2Fhome", nil))
	if login.Code != http.StatusFound {
		t.Fatalf("OAuth login status = %d, body %q", login.Code, login.Body.String())
	}
	location, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse OAuth login redirect: %v", err)
	}
	state := location.Query().Get("state")
	if state == "" {
		t.Fatal("OAuth login redirect is missing state")
	}
	stateCookie := responseCookie(t, login, "__Host-factory_oauth_state")

	callback := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(
		http.MethodGet,
		"/auth/google/callback?state="+url.QueryEscape(state)+"&code=google-code",
		nil,
	)
	callbackRequest.AddCookie(stateCookie)
	handler.ServeHTTP(callback, callbackRequest)
	if callback.Code != http.StatusFound || callback.Header().Get("Location") != "/home" {
		t.Fatalf("OAuth callback = %d, location %q, body %q", callback.Code, callback.Header().Get("Location"), callback.Body.String())
	}
	return responseCookie(t, callback, "__Host-factory_session")
}

func responseCookie(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name && cookie.MaxAge >= 0 {
			return cookie
		}
	}
	t.Fatalf("response cookie %q not found", name)
	return nil
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

func testLinearCommentBody(commentID, body string) string {
	return fmt.Sprintf(
		`{"type":"Comment","action":"create","url":"https://linear.example/comment/%s","webhookTimestamp":%d,"actor":{"id":"%s"},"data":{"id":"%s","body":%q,"issueId":"issue-123","issue":{"id":"issue-123","identifier":"ENG-123"}}}`,
		commentID,
		testNow.UnixMilli(),
		testActorID,
		commentID,
		body,
	)
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
