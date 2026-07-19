package server

import (
	"bytes"
	"encoding/base64"
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

func TestTaskDetailIncludesNullOptionalFields(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	projectPath := filepath.Join(t.TempDir(), "factory")
	requestJSON(t, handler, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"name":"Factory","path":%q}`, projectPath))
	requestJSON(t, handler, http.MethodPost, "/api/tasks",
		`{"title":"Root task","status":"todo","projectId":1}`)

	detail := requestJSON(t, handler, http.MethodGet, "/api/tasks/2", "")
	if detail.Code != http.StatusOK {
		t.Fatalf("task detail status = %d, body = %s", detail.Code, detail.Body)
	}
	var result struct {
		Task map[string]any `json:"task"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"description", "parentTaskId"} {
		value, found := result.Task[field]
		if !found || value != nil {
			t.Errorf("task %s = %#v, present = %v; want explicit null", field, value, found)
		}
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

func TestTaskInReviewStatusPersistsAcrossCreateUpdateAndReplay(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	projectPath := filepath.Join(t.TempDir(), "factory")
	requestJSON(t, handler, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"name":"Factory","path":%q}`, projectPath))

	created := requestJSON(t, handler, http.MethodPost, "/api/tasks",
		`{"title":"Review the change","status":"in review","projectId":1}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body)
	}
	var createdTask state.Task
	if err := json.Unmarshal(created.Body.Bytes(), &createdTask); err != nil {
		t.Fatal(err)
	}
	if createdTask.Status != state.InReview {
		t.Fatalf("created task status = %q", createdTask.Status)
	}

	updated := requestJSON(t, handler, http.MethodPut, "/api/tasks/2",
		`{"title":"Review the finished change","status":"in review","projectId":1}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body)
	}
	var updatedTask state.Task
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedTask); err != nil {
		t.Fatal(err)
	}
	if updatedTask.Title != "Review the finished change" || updatedTask.Status != state.InReview {
		t.Fatalf("updated task = %#v", updatedTask)
	}

	var taskEvents []state.TaskData
	for _, event := range wire.Events(0) {
		if event.Type != state.TaskCreated && event.Type != state.TaskUpdated {
			continue
		}
		var data state.TaskData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		taskEvents = append(taskEvents, data)
	}
	if len(taskEvents) != 2 || taskEvents[0].Status != state.InReview ||
		taskEvents[1].Status != state.InReview {
		t.Fatalf("task events = %#v", taskEvents)
	}

	detail := requestJSON(t, handler, http.MethodGet, "/api/tasks/2", "")
	var result struct {
		Task state.Task `json:"task"`
	}
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &result) != nil ||
		result.Task.Status != state.InReview {
		t.Fatalf("task detail = %d %s", detail.Code, detail.Body)
	}

	lastID := wire.LastID()
	rejected := requestJSON(t, handler, http.MethodPost, "/api/tasks",
		`{"title":"Use an alias","status":"review","projectId":1}`)
	if rejected.Code != http.StatusBadRequest || wire.LastID() != lastID {
		t.Fatalf("alias status = %d, body = %s, last ID = %d", rejected.Code, rejected.Body, wire.LastID())
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

func TestWorkflowListIncludesProjectedUsage(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	for _, name := range []string{"Alpha", "Charlie", "Zulu"} {
		if _, err := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := wire.Publish(state.TaskCreated, state.TaskData{
		Title: "Used twice", Status: state.Todo, ProjectID: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	starts := []state.WorkflowRunData{
		{TriggerID: 10, WorkflowID: 1, SourceEventID: 100},
		{TriggerID: 11, WorkflowID: 3, SourceEventID: task.ID, TaskID: task.ID},
		{TriggerID: 12, WorkflowID: 3, SourceEventID: task.ID, TaskID: task.ID},
	}
	for _, start := range starts {
		if _, err := wire.Publish(state.WorkflowRunStarted, start); err != nil {
			t.Fatal(err)
		}
	}

	response := requestJSON(t, testServer(t, wire).Handler(), http.MethodGet, "/api/workflows", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	var result struct {
		Workflows []map[string]any `json:"workflows"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Workflows) != 3 {
		t.Fatalf("workflows = %#v", result.Workflows)
	}
	want := []struct {
		name  string
		runs  float64
		tasks float64
	}{
		{name: "Alpha", runs: 1, tasks: 0},
		{name: "Charlie", runs: 0, tasks: 0},
		{name: "Zulu", runs: 2, tasks: 1},
	}
	for index, expected := range want {
		workflow := result.Workflows[index]
		if workflow["name"] != expected.name || workflow["runCount"] != expected.runs ||
			workflow["taskCount"] != expected.tasks {
			t.Errorf("workflow %d = %#v, want %s with %v runs and %v tasks",
				index, workflow, expected.name, expected.runs, expected.tasks)
		}
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
	project, _ := wire.Publish(state.ProjectCreated, state.ProjectData{Name: "Factory", Path: t.TempDir()})
	task, _ := wire.Publish(state.TaskCreated, state.TaskData{
		Title: "Review history", Status: state.InReview, ProjectID: project.ID,
	})
	source, _ := wire.Publish(state.TaskUpdated, state.TaskData{
		ID: task.ID, Title: "Review history", Status: state.InReview, ProjectID: project.ID,
	})
	started, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 2, WorkflowID: 1, WorkflowName: "review",
		WorkflowPhases: []string{"Review"}, SourceEventID: source.ID,
	})
	wire.Publish(state.WorkflowRunEventRecorded, state.WorkflowRunEventData{
		RunID: started.ID, Event: json.RawMessage(
			`{"sequence":1,"at":"2026-07-17T12:00:00Z","type":"log","workflow":"review","phase":"Review","message":"Inspecting the change"}`,
		),
	})
	custom, _ := wire.Publish("release.ready", map[string]int64{"id": task.ID})
	nonTask, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 3, WorkflowID: 1, WorkflowName: "review", SourceEventID: custom.ID,
	})
	handler := testServer(t, wire).Handler()
	list := requestJSON(t, handler, http.MethodGet, "/api/history", "")
	var listed struct {
		History []state.WorkflowRun `json:"history"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil {
		t.Fatalf("history = %d %s", list.Code, list.Body)
	}
	var listedTaskID int64
	for _, run := range listed.History {
		if run.ID == started.ID {
			listedTaskID = run.TaskID
		}
	}
	if listedTaskID != task.ID {
		t.Fatalf("history = %#v, want task ID %d", listed.History, task.ID)
	}
	detail := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/history/%d", started.ID), "")
	var decoded struct {
		Run    state.WorkflowRun        `json:"run"`
		Events []state.WorkflowRunEvent `json:"events"`
	}
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &decoded) != nil ||
		decoded.Run.TaskID != task.ID || len(decoded.Events) != 1 ||
		decoded.Events[0].Message != "Inspecting the change" {
		t.Fatalf("history detail = %d %s", detail.Code, detail.Body)
	}
	nonTaskDetail := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/history/%d", nonTask.ID), "")
	var nonTaskDecoded struct {
		Run map[string]json.RawMessage `json:"run"`
	}
	if nonTaskDetail.Code != http.StatusOK || json.Unmarshal(nonTaskDetail.Body.Bytes(), &nonTaskDecoded) != nil {
		t.Fatalf("non-task history detail = %d %s", nonTaskDetail.Code, nonTaskDetail.Body)
	}
	if _, found := nonTaskDecoded.Run["taskId"]; found {
		t.Fatalf("non-task history includes taskId: %s", nonTaskDetail.Body)
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

func TestUniversalIngress(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	tests := []struct {
		method, path, contentType, response, eventType, encoding, body string
		payload                                                        []byte
	}{
		{
			http.MethodPatch, "/api/ingest/deliveries?source=github&attempt=1",
			"application/json", "{}", "ingress.github", "utf-8",
			`{"action":"opened"}`, []byte(`{"action":"opened"}`),
		},
		{
			http.MethodPost, "/api/ingest/v1/logs?source=otel", "application/x-protobuf",
			"", "ingress.otel", "base64", base64.StdEncoding.EncodeToString([]byte{0xff, 0, 0x80}),
			[]byte{0xff, 0, 0x80},
		},
		{
			http.MethodDelete, "/api/ingest", "text/plain", "",
			"ingress.received", "utf-8", "deploy complete", []byte("deploy complete"),
		},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, bytes.NewReader(test.payload))
		request.Header.Set("Content-Type", test.contentType)
		request.Header.Set("X-Delivery", "delivery-1")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != test.response {
			t.Fatalf("%s response = %d %q", test.path, response.Code, response.Body.String())
		}
		if strings.HasPrefix(test.contentType, "application/") &&
			response.Header().Get("Content-Type") != test.contentType {
			t.Fatalf("%s response content type = %q", test.path, response.Header().Get("Content-Type"))
		}
		event := wire.Events(0)[len(wire.Events(0))-1]
		var data struct {
			Method, URL, BodyEncoding, Body string
			Headers                         http.Header
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		if event.Type != test.eventType || data.Method != test.method || data.URL != test.path ||
			data.BodyEncoding != test.encoding || data.Body != test.body ||
			data.Headers.Get("X-Delivery") != "delivery-1" {
			t.Fatalf("%s event = %#v, data = %#v", test.path, event, data)
		}
	}
}

func TestUniversalIngressSurvivesReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	wire, err := eventwire.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, testServer(t, wire).Handler(), http.MethodPost,
		"/api/ingest?source=linear", `{"type":"Issue","action":"update"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := eventwire.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events := reopened.Events(0)
	var data struct {
		Body string
	}
	if len(events) != 1 || json.Unmarshal(events[0].Data, &data) != nil ||
		events[0].Type != "ingress.linear" || data.Body != `{"type":"Issue","action":"update"}` {
		t.Fatalf("replayed events = %#v", events)
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

func TestTriggerEnabledStateDefaultsPersistsAndRequiresExplicitUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	wire, err := eventwire.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := testServer(t, wire).Handler()

	created := requestJSON(t, handler, http.MethodPost, "/api/triggers",
		`{"eventType":"release.ready","workflowId":24}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var trigger state.Trigger
	if err := json.Unmarshal(created.Body.Bytes(), &trigger); err != nil {
		t.Fatal(err)
	}
	if !trigger.Enabled {
		t.Fatalf("created trigger = %#v", trigger)
	}
	var createdData map[string]any
	if err := json.Unmarshal(wire.Events(0)[0].Data, &createdData); err != nil {
		t.Fatal(err)
	}
	if enabled, found := createdData["enabled"]; !found || enabled != true {
		t.Fatalf("creation event = %#v", createdData)
	}

	disabled := requestJSON(t, handler, http.MethodPut, "/api/triggers/1",
		`{"eventType":"release.ready","workflowId":24,"enabled":false}`)
	if disabled.Code != http.StatusOK || json.Unmarshal(disabled.Body.Bytes(), &trigger) != nil || trigger.Enabled {
		t.Fatalf("disable = %d %s", disabled.Code, disabled.Body)
	}
	list := requestJSON(t, handler, http.MethodGet, "/api/triggers", "")
	var triggers struct {
		Triggers []state.Trigger `json:"triggers"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &triggers); err != nil {
		t.Fatal(err)
	}
	if len(triggers.Triggers) != 1 || triggers.Triggers[0].Enabled {
		t.Fatalf("disabled trigger list = %#v", triggers.Triggers)
	}

	lastID := wire.LastID()
	omitted := requestJSON(t, handler, http.MethodPut, "/api/triggers/1",
		`{"eventType":"release.changed","workflowId":24}`)
	if omitted.Code != http.StatusBadRequest ||
		!strings.Contains(omitted.Body.String(), "trigger enabled is required") {
		t.Fatalf("omitted enabled = %d %s", omitted.Code, omitted.Body)
	}
	if wire.LastID() != lastID {
		t.Fatalf("omitted enabled appended event: %d -> %d", lastID, wire.LastID())
	}
	detail := requestJSON(t, handler, http.MethodGet, "/api/triggers/1", "")
	if err := json.Unmarshal(detail.Body.Bytes(), &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Enabled || trigger.EventType != "release.ready" {
		t.Fatalf("rejected update changed trigger = %#v", trigger)
	}
	health := requestJSON(t, handler, http.MethodGet, "/api/health", "")
	var healthData map[string]any
	if err := json.Unmarshal(health.Body.Bytes(), &healthData); err != nil {
		t.Fatal(err)
	}
	if healthData["triggers"] != float64(1) {
		t.Fatalf("health = %#v", healthData)
	}

	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := eventwire.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	handler = testServer(t, reopened).Handler()
	detail = requestJSON(t, handler, http.MethodGet, "/api/triggers/1", "")
	if err := json.Unmarshal(detail.Body.Bytes(), &trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Enabled {
		t.Fatalf("restarted trigger = %#v", trigger)
	}

	enabled := requestJSON(t, handler, http.MethodPut, "/api/triggers/1",
		`{"eventType":"release.ready","workflowId":24,"enabled":true}`)
	if enabled.Code != http.StatusOK || json.Unmarshal(enabled.Body.Bytes(), &trigger) != nil || !trigger.Enabled {
		t.Fatalf("enable = %d %s", enabled.Code, enabled.Body)
	}
}

func TestSettingsAPIUpdatesHarnessSelection(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	response := requestJSON(t, handler, http.MethodGet, "/api/settings", "")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"harness":"codex"`) ||
		!strings.Contains(response.Body.String(), `"workflowCapacity":6`) ||
		!strings.Contains(response.Body.String(), `"name":"Claude Code"`) {
		t.Fatalf("default settings = %d %s", response.Code, response.Body)
	}
	response = requestJSON(t, handler, http.MethodPut, "/api/settings",
		`{"harness":"claude","model":"sonnet","reasoning":"high","workflowCapacity":4}`)
	if response.Code != http.StatusOK {
		t.Fatalf("update settings = %d %s", response.Code, response.Body)
	}
	health := requestJSON(t, handler, http.MethodGet, "/api/health", "")
	if !strings.Contains(health.Body.String(), `"harness":"claude"`) ||
		!strings.Contains(health.Body.String(), `"workflowCapacity":4`) {
		t.Fatalf("health = %s", health.Body)
	}
	response = requestJSON(t, handler, http.MethodPut, "/api/settings",
		`{"harness":"claude","model":"sonnet","reasoning":"high","workflowCapacity":0}`)
	if response.Code != http.StatusOK {
		t.Fatalf("pause settings = %d %s", response.Code, response.Body)
	}
	response = requestJSON(t, handler, http.MethodPut, "/api/settings",
		`{"harness":"claude","model":"gpt-5.6-sol","reasoning":"high","workflowCapacity":4}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid settings = %d %s", response.Code, response.Body)
	}
	for _, body := range []string{
		`{"harness":"claude","model":"sonnet","reasoning":"high"}`,
		`{"harness":"claude","model":"sonnet","reasoning":"high","workflowCapacity":11}`,
	} {
		response = requestJSON(t, handler, http.MethodPut, "/api/settings", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid capacity settings = %d %s", response.Code, response.Body)
		}
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
	server, err := New(wire, filesystem, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return server
}
