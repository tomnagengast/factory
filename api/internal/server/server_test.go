package server

import (
	"encoding/json"
	"fmt"
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
	projectPath := filepath.Join(t.TempDir(), "factory")

	project := requestJSON(t, handler, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"name":"Factory","path":%q}`, projectPath))
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
	projectPath := filepath.Join(t.TempDir(), "factory")
	requestJSON(t, handler, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"name":"Factory","path":%q}`, projectPath))
	requestJSON(t, handler, http.MethodPost, "/api/tasks", `{"title":"First","status":"backlog","projectId":1}`)
	requestJSON(t, handler, http.MethodPost, "/api/tasks", `{"title":"Second","status":"backlog","projectId":1}`)
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

func TestProjectRequiresAndCreatesPath(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	if response := requestJSON(t, handler, http.MethodPost, "/api/projects", `{"name":"Missing"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("missing path status = %d", response.Code)
	}
	path := filepath.Join(t.TempDir(), "created")
	response := requestJSON(t, handler, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"name":"Created","path":%q}`, path))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("project path was not created: %v", err)
	}
	updatedPath := filepath.Join(t.TempDir(), "updated")
	response = requestJSON(t, handler, http.MethodPut, "/api/projects/1",
		fmt.Sprintf(`{"name":"Updated","path":%q}`, updatedPath))
	if response.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", response.Code, response.Body)
	}
	if info, err := os.Stat(updatedPath); err != nil || !info.IsDir() {
		t.Fatalf("updated project path was not created: %v", err)
	}
}

func TestTaskRequiresExistingProject(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	for _, body := range []string{
		`{"title":"Missing"}`,
		`{"title":"Unknown","projectId":99}`,
	} {
		response := requestJSON(t, handler, http.MethodPost, "/api/tasks", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body)
		}
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

func TestWorkflowHistoryListsRunsAndEvents(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	started, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 2, WorkflowID: 1, WorkflowName: "review",
		WorkflowPhases: []string{"Review"}, SourceEventID: 3,
	})
	wire.Publish(state.WorkflowRunEventRecorded, state.WorkflowRunEventData{
		RunID: started.ID, Event: json.RawMessage(
			`{"sequence":1,"at":"2026-07-17T12:00:00Z","type":"log","workflow":"review","phase":"Review","message":"Inspecting the change"}`,
		),
	})
	handler := testServer(t, wire).Handler()
	list := requestJSON(t, handler, http.MethodGet, "/api/history", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"workflowName":"review"`) {
		t.Fatalf("history = %d %s", list.Code, list.Body)
	}
	detail := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/history/%d", started.ID), "")
	if detail.Code != http.StatusOK ||
		!strings.Contains(detail.Body.String(), `"message":"Inspecting the change"`) {
		t.Fatalf("history detail = %d %s", detail.Code, detail.Body)
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

func TestHealthIncludesReleaseIdentity(t *testing.T) {
	t.Setenv("FACTORY_RELEASE_COMMIT", "commit-1")
	t.Setenv("FACTORY_RELEASE_TREE", "tree-1")
	t.Setenv("FACTORY_RELEASE_BUILD", "build-1")
	t.Setenv("FACTORY_RELEASE_DEPLOYMENT", "deployment-1")
	t.Setenv("FACTORY_RELEASE_CONTRACT", "1")
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
		"harness": state.Codex,
	} {
		if health[key] != expected {
			t.Errorf("%s = %#v, want %q", key, health[key], expected)
		}
	}
}

func TestSettingsAPIUpdatesHarnessSelection(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	response := requestJSON(t, handler, http.MethodGet, "/api/settings", "")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"harness":"codex"`) ||
		!strings.Contains(response.Body.String(), `"name":"Claude Code"`) {
		t.Fatalf("default settings = %d %s", response.Code, response.Body)
	}
	response = requestJSON(t, handler, http.MethodPut, "/api/settings",
		`{"harness":"claude","model":"sonnet","reasoning":"high"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("update settings = %d %s", response.Code, response.Body)
	}
	health := requestJSON(t, handler, http.MethodGet, "/api/health", "")
	if !strings.Contains(health.Body.String(), `"harness":"claude"`) {
		t.Fatalf("health = %s", health.Body)
	}
	response = requestJSON(t, handler, http.MethodPut, "/api/settings",
		`{"harness":"claude","model":"gpt-5.6-sol","reasoning":"high"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid settings = %d %s", response.Code, response.Body)
	}
}

func TestSolidAppFallbackAndAssets(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	for path, cacheControl := range map[string]string{
		"/":                     "no-cache, must-revalidate",
		"/tasks/12":             "no-cache, must-revalidate",
		"/assets/app-a1.js":     "public, max-age=31536000, immutable",
		"/assets/styles-b2.css": "public, max-age=31536000, immutable",
	} {
		response := requestJSON(t, handler, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, response.Code, response.Body)
		}
		if response.Header().Get("Cache-Control") != cacheControl {
			t.Fatalf("%s cache control = %q", path, response.Header().Get("Cache-Control"))
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
		"index.html":           &fstest.MapFile{Data: []byte("<html></html>")},
		"assets/app-a1.js":     &fstest.MapFile{Data: []byte("export {};")},
		"assets/styles-b2.css": &fstest.MapFile{Data: []byte("body {}")},
	}
	var filesystem fs.FS = assets
	server, err := New(wire, filesystem)
	if err != nil {
		t.Fatal(err)
	}
	return server
}
