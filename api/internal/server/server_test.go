package server

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
)

func TestProjectTaskCommentAndArtifactAPI(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()

	project := requestJSON(t, handler, http.MethodPost, "/api/projects", `{"name":"Factory"}`)
	if project.Code != http.StatusCreated {
		t.Fatalf("project status = %d, body = %s", project.Code, project.Body)
	}
	task := requestJSON(t, handler, http.MethodPost, "/api/tasks", `{
		"title":"Build the UI","status":"todo","projectId":1
	}`)
	if task.Code != http.StatusCreated {
		t.Fatalf("task status = %d, body = %s", task.Code, task.Body)
	}
	comment := requestJSON(t, handler, http.MethodPost, "/api/tasks/2/comments", `{"content":"Keep it small."}`)
	if comment.Code != http.StatusCreated {
		t.Fatalf("comment status = %d, body = %s", comment.Code, comment.Body)
	}
	artifact := requestJSON(t, handler, http.MethodPost, "/api/artifacts", `{
		"type":"link","content":"https://example.com","relationType":"task","relationId":2
	}`)
	if artifact.Code != http.StatusCreated {
		t.Fatalf("artifact status = %d, body = %s", artifact.Code, artifact.Body)
	}

	detail := requestJSON(t, handler, http.MethodGet, "/api/tasks/2", "")
	var result struct {
		Task      state.Task       `json:"task"`
		Comments  []state.Comment  `json:"comments"`
		Artifacts []state.Artifact `json:"artifacts"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Task.Title != "Build the UI" || len(result.Comments) != 1 || len(result.Artifacts) != 1 {
		t.Fatalf("unexpected task detail: %#v", result)
	}
}

func TestTaskListDefaultsToDescendingIDs(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	requestJSON(t, handler, http.MethodPost, "/api/tasks", `{"title":"First","status":"backlog"}`)
	requestJSON(t, handler, http.MethodPost, "/api/tasks", `{"title":"Second","status":"backlog"}`)
	response := requestJSON(t, handler, http.MethodGet, "/api/tasks", "")
	var result struct {
		Tasks []state.Task `json:"tasks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 2 || result.Tasks[0].ID < result.Tasks[1].ID {
		t.Fatalf("tasks are not descending: %#v", result.Tasks)
	}
}

func TestWorkflowCreationIsAConversation(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	response := requestJSON(t, handler, http.MethodPost, "/api/workflows", `{"message":"Build a review panel"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Workflows) != 1 || len(view.CommentsFor("workflow", view.Workflows[0].ID)) != 1 {
		t.Fatalf("workflow conversation missing: %#v", view)
	}
}

func TestWorkflowDetailIncludesLiveSource(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	path := filepath.Join(t.TempDir(), "review.js")
	source := "export const meta = { name: \"review\" };"
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{
		Name: "review", Path: &path,
	}); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, testServer(t, wire).Handler(), http.MethodGet, "/api/workflows/1", "")
	var detail struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Source != source {
		t.Fatalf("source = %q, want %q", detail.Source, source)
	}

	source = "export const meta = { name: \"review-v2\" };"
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	response = requestJSON(t, testServer(t, wire).Handler(), http.MethodGet, "/api/workflows/1", "")
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Source != source {
		t.Fatalf("refreshed source = %q, want %q", detail.Source, source)
	}
}

func TestArbitraryEventIntakeAndTypes(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	response := requestJSON(t, handler, http.MethodPost, "/api/events", `{"type":"release.ready","data":{"version":"1"}}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	types := requestJSON(t, handler, http.MethodGet, "/api/events/types", "")
	if !strings.Contains(types.Body.String(), "release.ready") || !strings.Contains(types.Body.String(), "cron") {
		t.Fatalf("event types = %s", types.Body)
	}
}

func TestHealthIncludesNagsReleaseIdentity(t *testing.T) {
	t.Setenv("NAGS_SOURCE_COMMIT", "commit-1")
	t.Setenv("NAGS_SOURCE_TREE", "tree-1")
	t.Setenv("NAGS_BUILD_ID", "build-1")
	t.Setenv("NAGS_DEPLOYMENT_ID", "deployment-1")
	t.Setenv("NAGS_CONTRACT_VERSION", "1")
	wire := openWire(t)
	defer wire.Close()
	response := requestJSON(t, testServer(t, wire).Handler(), http.MethodGet, "/api/health", "")
	var health map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{
		"status": "ok", "app": "factory", "commit": "commit-1", "tree": "tree-1",
		"buildId": "build-1", "deploymentId": "deployment-1", "contractVersion": "1",
	} {
		if health[key] != expected {
			t.Errorf("%s = %#v, want %q", key, health[key], expected)
		}
	}
}

func TestSolidAppFallbackAndAssets(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	for _, path := range []string{"/", "/tasks/12", "/assets/app.js", "/assets/styles.css"} {
		response := requestJSON(t, handler, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, response.Code, response.Body)
		}
	}
}

func TestEventStreamConnectsBeforeAnEventExists(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	server := httptest.NewServer(testServer(t, wire).Handler())
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/events/stream", nil)
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

func requestJSON(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func openWire(t *testing.T) *eventwire.Wire {
	t.Helper()
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func testServer(t *testing.T, wire *eventwire.Wire) *Server {
	t.Helper()
	assets := fstest.MapFS{
		"index.html":        &fstest.MapFile{Data: []byte("<html></html>")},
		"assets/app.js":     &fstest.MapFile{Data: []byte("export {};")},
		"assets/styles.css": &fstest.MapFile{Data: []byte("body {}")},
	}
	var filesystem fs.FS = assets
	server, err := New(wire, filesystem, "codex")
	if err != nil {
		t.Fatal(err)
	}
	return server
}
