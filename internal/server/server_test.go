package server

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/state"
)

func TestTaskSubmissionAndProjection(t *testing.T) {
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	server := testServer(t, wire)

	request := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"prompt":"Build the core"}`))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	body := response.Body.Bytes()
	var created state.Task
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Prompt != "Build the core" || created.Status != state.Queued {
		t.Fatalf("created task = %#v", created)
	}
	if strings.Contains(string(body), "startedAt") || strings.Contains(string(body), "finishedAt") {
		t.Fatalf("queued task contains unset lifecycle times: %s", body)
	}

	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), eventwire.TaskSubmitted) {
		t.Fatalf("events response = %d %s", response.Code, response.Body)
	}
}

func TestTaskSubmissionRequiresPrompt(t *testing.T) {
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	server := testServer(t, wire)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"prompt":" "}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestHealthIncludesNagsReleaseIdentity(t *testing.T) {
	t.Setenv("NAGS_SOURCE_COMMIT", "commit-1")
	t.Setenv("NAGS_SOURCE_TREE", "tree-1")
	t.Setenv("NAGS_BUILD_ID", "build-1")
	t.Setenv("NAGS_DEPLOYMENT_ID", "deployment-1")
	t.Setenv("NAGS_CONTRACT_VERSION", "1")
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	server := testServer(t, wire)

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	var health map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{
		"status":          "ok",
		"app":             "factory",
		"commit":          "commit-1",
		"tree":            "tree-1",
		"buildId":         "build-1",
		"deploymentId":    "deployment-1",
		"contractVersion": "1",
	} {
		if health[key] != expected {
			t.Errorf("%s = %#v, want %q", key, health[key], expected)
		}
	}
}

func TestFrontendAssetsAreServed(t *testing.T) {
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	server := testServer(t, wire)

	for _, path := range []string{"/", "/src/index.js", "/src/styles.css"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
}

func TestEventStreamConnectsBeforeAnEventExists(t *testing.T) {
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	app := testServer(t, wire)
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL+"/api/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	comment := make([]byte, len(": connected\n\n"))
	if _, err := io.ReadFull(response.Body, comment); err != nil {
		t.Fatal(err)
	}
	if string(comment) != ": connected\n\n" {
		t.Fatalf("stream opening = %q", comment)
	}
}

func testServer(t *testing.T, wire *eventwire.Wire) *Server {
	t.Helper()
	assets := fstest.MapFS{
		"frontend/index.html":     &fstest.MapFile{Data: []byte("<html></html>")},
		"frontend/src/index.js":   &fstest.MapFile{Data: []byte("export {};")},
		"frontend/src/styles.css": &fstest.MapFile{Data: []byte("body {}")},
	}
	var filesystem fs.FS = assets
	server, err := New(wire, filesystem, "fake")
	if err != nil {
		t.Fatal(err)
	}
	return server
}
