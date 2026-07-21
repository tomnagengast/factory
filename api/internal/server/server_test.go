package server

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/quiescence"
	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/store"
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

func TestCommentDeleteCascadesAndSurvivesReplay(t *testing.T) {
	wirePath := filepath.Join(t.TempDir(), "factory.db")
	wire := openStoreAt(t, wirePath)
	defer func() { _ = wire.Close() }()
	handler := testServer(t, wire).Handler()
	projectPath := filepath.Join(t.TempDir(), "factory")
	project := requestJSON(t, handler, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"name":"Factory","path":%q}`, projectPath))
	if project.Code != http.StatusCreated {
		t.Fatalf("project status = %d, body = %s", project.Code, project.Body)
	}
	createdTask := requestJSON(t, handler, http.MethodPost, "/api/tasks",
		`{"title":"Delete a thread","status":"todo","projectId":1}`)
	var task state.Task
	if createdTask.Code != http.StatusCreated || json.Unmarshal(createdTask.Body.Bytes(), &task) != nil {
		t.Fatalf("task = %d %s", createdTask.Code, createdTask.Body)
	}
	createComment := func(content string, parentID *int64) state.Comment {
		t.Helper()
		body := fmt.Sprintf(`{"content":%q}`, content)
		if parentID != nil {
			body = fmt.Sprintf(`{"content":%q,"parentCommentId":%d}`, content, *parentID)
		}
		response := requestJSON(t, handler, http.MethodPost,
			fmt.Sprintf("/api/tasks/%d/comments", task.ID), body)
		var comment state.Comment
		if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &comment) != nil {
			t.Fatalf("comment %q = %d %s", content, response.Code, response.Body)
		}
		return comment
	}
	root := createComment("Root", nil)
	child := createComment("Child", &root.ID)
	grandchild := createComment("Grandchild", &child.ID)
	sibling := createComment("Sibling", &root.ID)

	beforeDelete := wire.Events(0)
	deleted := requestJSON(t, handler, http.MethodDelete, fmt.Sprintf("/api/comments/%d", child.ID), "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleted.Code, deleted.Body)
	}
	afterDelete := wire.Events(0)
	if len(afterDelete) != len(beforeDelete)+1 {
		t.Fatalf("delete appended %d events, want 1", len(afterDelete)-len(beforeDelete))
	}
	deleteEvent := afterDelete[len(afterDelete)-1]
	var deleteData state.IDData
	if deleteEvent.Type != state.CommentDeleted || json.Unmarshal(deleteEvent.Data, &deleteData) != nil || deleteData.ID != child.ID {
		t.Fatalf("delete event = %#v, data = %#v", deleteEvent, deleteData)
	}

	assertTaskComments := func(handler http.Handler) {
		t.Helper()
		detail := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/tasks/%d", task.ID), "")
		var result struct {
			Comments []state.Comment `json:"comments"`
		}
		if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &result) != nil {
			t.Fatalf("task detail = %d %s", detail.Code, detail.Body)
		}
		ids := make([]int64, len(result.Comments))
		for index, comment := range result.Comments {
			ids[index] = comment.ID
		}
		if !slices.Equal(ids, []int64{root.ID, sibling.ID}) {
			t.Fatalf("active task comments = %v", ids)
		}
	}
	assertTaskComments(handler)

	childDetail := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/comments/%d", child.ID), "")
	var childResult struct {
		Comment state.Comment   `json:"comment"`
		Replies []state.Comment `json:"replies"`
	}
	if childDetail.Code != http.StatusOK || json.Unmarshal(childDetail.Body.Bytes(), &childResult) != nil ||
		childResult.Comment.DeletedAt == nil || childResult.Replies == nil || len(childResult.Replies) != 0 {
		t.Fatalf("deleted child detail = %d %s", childDetail.Code, childDetail.Body)
	}
	grandchildDetail := requestJSON(t, handler, http.MethodGet,
		fmt.Sprintf("/api/comments/%d", grandchild.ID), "")
	var grandchildResult struct {
		Comment state.Comment `json:"comment"`
	}
	if grandchildDetail.Code != http.StatusOK || json.Unmarshal(grandchildDetail.Body.Bytes(), &grandchildResult) != nil ||
		grandchildResult.Comment.DeletedAt == nil {
		t.Fatalf("deleted grandchild detail = %d %s", grandchildDetail.Code, grandchildDetail.Body)
	}

	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}
	wire = openStoreAt(t, wirePath)
	handler = testServer(t, wire).Handler()
	assertTaskComments(handler)
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

func TestTaskListSummariesProjectCommentsAndWorkflowRuns(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	project, _ := wire.Publish(state.ProjectCreated, state.ProjectData{
		Name: "Factory", Path: t.TempDir(),
	})
	task, _ := wire.Publish(state.TaskCreated, state.TaskData{
		Title: "Improve the list", Status: state.InProgress, ProjectID: project.ID,
	})
	emptyTask, _ := wire.Publish(state.TaskCreated, state.TaskData{
		Title: "No activity", Status: state.Backlog, ProjectID: project.ID,
	})

	root, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: task.ID, Author: "user", Content: "Root",
	})
	wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: task.ID, ParentCommentID: &root.ID,
		Author: "user", Content: "Reply",
	})
	wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: task.ID, Author: "agent", Content: "Gate prompt",
	})
	deleted, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: task.ID, Author: "user", Content: "Remove me",
	})
	wire.Publish(state.CommentDeleted, state.IDData{ID: deleted.ID})

	review, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	verify, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "verify"})
	createdRun, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 50, WorkflowID: review.ID, WorkflowName: "review", SourceEventID: task.ID,
	})
	wire.Publish(state.WorkflowRunCompleted, state.WorkflowRunData{
		TriggerID: 50, SourceEventID: task.ID, Output: "done",
	})

	readList := func(path string) struct {
		Project           state.Project  `json:"project"`
		Tasks             []taskListItem `json:"tasks"`
		CheckpointEventID int64          `json:"checkpointEventId"`
	} {
		t.Helper()
		response := requestJSON(t, handler, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, response.Code, response.Body)
		}
		var result struct {
			Project           state.Project  `json:"project"`
			Tasks             []taskListItem `json:"tasks"`
			CheckpointEventID int64          `json:"checkpointEventId"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	findTask := func(tasks []taskListItem, id int64) taskListItem {
		t.Helper()
		for _, item := range tasks {
			if item.ID == id {
				return item
			}
		}
		t.Fatalf("task %d missing from %#v", id, tasks)
		return taskListItem{}
	}

	terminal := findTask(readList("/api/tasks").Tasks, task.ID)
	if len(terminal.WorkflowRuns) != 1 || terminal.WorkflowRuns[0].RunID != createdRun.ID ||
		terminal.WorkflowRuns[0].Status != "completed" {
		t.Fatalf("completed fallback = %#v", terminal.WorkflowRuns)
	}

	firstUpdate, _ := wire.Publish(state.TaskUpdated, state.TaskData{
		ID: task.ID, Title: "Improve the list", Status: state.InProgress, ProjectID: project.ID,
	})
	firstActive, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 50, WorkflowID: review.ID, WorkflowName: "review", SourceEventID: firstUpdate.ID,
	})
	secondUpdate, _ := wire.Publish(state.TaskUpdated, state.TaskData{
		ID: task.ID, Title: "Improve the list", Status: state.InProgress, ProjectID: project.ID,
	})
	secondActive, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 50, WorkflowID: review.ID, WorkflowName: "review", SourceEventID: secondUpdate.ID,
	})
	wire.Publish(state.WorkflowRunWaiting, state.WorkflowRunStateData{RunID: secondActive.ID})
	verifyRun, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 60, WorkflowID: verify.ID, WorkflowName: "verify",
		SourceEventID: secondUpdate.ID, TaskID: task.ID,
	})
	wire.Publish(state.WorkflowRunFailed, state.WorkflowRunData{
		TriggerID: 60, SourceEventID: secondUpdate.ID, Error: "failed",
	})
	custom, _ := wire.Publish("release.ready", map[string]int64{"taskId": task.ID})
	wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 70, WorkflowID: review.ID, WorkflowName: "review", SourceEventID: custom.ID,
	})

	global := readList("/api/tasks")
	if global.CheckpointEventID != wire.LastID() {
		t.Fatalf("global checkpoint = %d, want %d", global.CheckpointEventID, wire.LastID())
	}
	if len(global.Tasks) != 2 || global.Tasks[0].ID != emptyTask.ID || global.Tasks[1].ID != task.ID {
		t.Fatalf("global task order = %#v", global.Tasks)
	}
	active := findTask(global.Tasks, task.ID)
	if active.CommentCount != 3 {
		t.Fatalf("comment count = %d, want 3", active.CommentCount)
	}
	wantRuns := []taskWorkflowRun{
		{RunID: firstActive.ID, TriggerID: 50, WorkflowID: review.ID, WorkflowName: "review", Status: "running"},
		{RunID: secondActive.ID, TriggerID: 50, WorkflowID: review.ID, WorkflowName: "review", Status: "waiting"},
		{RunID: verifyRun.ID, TriggerID: 60, WorkflowID: verify.ID, WorkflowName: "verify", Status: "failed"},
	}
	if !reflect.DeepEqual(active.WorkflowRuns, wantRuns) {
		t.Fatalf("active workflow runs = %#v, want %#v", active.WorkflowRuns, wantRuns)
	}
	empty := findTask(global.Tasks, emptyTask.ID)
	if empty.CommentCount != 0 || empty.WorkflowRuns == nil || len(empty.WorkflowRuns) != 0 {
		t.Fatalf("empty task summary = %#v", empty)
	}

	projectDetail := readList(fmt.Sprintf("/api/projects/%d", project.ID))
	if projectDetail.CheckpointEventID != global.CheckpointEventID || projectDetail.Project.ID != project.ID {
		t.Fatalf("project detail = %#v", projectDetail)
	}
	projectTask := findTask(projectDetail.Tasks, task.ID)
	if projectTask.CommentCount != active.CommentCount || !reflect.DeepEqual(projectTask.WorkflowRuns, active.WorkflowRuns) {
		t.Fatalf("project task summary = %#v, global = %#v", projectTask, active)
	}
	if len(projectDetail.Tasks) != 2 || projectDetail.Tasks[0].ID != task.ID ||
		projectDetail.Tasks[1].ID != emptyTask.ID {
		t.Fatalf("project task order = %#v", projectDetail.Tasks)
	}

	wire.Publish(state.WorkflowRunResumed, state.WorkflowRunStateData{RunID: secondActive.ID})
	resumed := findTask(readList("/api/tasks").Tasks, task.ID)
	if len(resumed.WorkflowRuns) < 2 || resumed.WorkflowRuns[1].Status != "running" {
		t.Fatalf("resumed workflow runs = %#v", resumed.WorkflowRuns)
	}
	wire.Publish(state.WorkflowRunCompleted, state.WorkflowRunData{
		TriggerID: 50, SourceEventID: firstUpdate.ID, Output: "done",
	})
	remaining := findTask(readList("/api/tasks").Tasks, task.ID)
	if len(remaining.WorkflowRuns) < 1 || remaining.WorkflowRuns[0].RunID != secondActive.ID ||
		remaining.WorkflowRuns[0].Status != "running" {
		t.Fatalf("remaining active workflow runs = %#v", remaining.WorkflowRuns)
	}
	wire.Publish(state.WorkflowRunFailed, state.WorkflowRunData{
		TriggerID: 50, SourceEventID: secondUpdate.ID, Error: "failed",
	})
	failed := findTask(readList("/api/tasks").Tasks, task.ID)
	if len(failed.WorkflowRuns) < 1 || failed.WorkflowRuns[0].RunID != secondActive.ID ||
		failed.WorkflowRuns[0].Status != "failed" {
		t.Fatalf("failed terminal fallback = %#v", failed.WorkflowRuns)
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

func TestReactionAPIUpdatesTasksRootCommentsRepliesAndGatePrompts(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	task, _ := wire.Publish(state.TaskCreated, state.TaskData{
		Title: "Review reactions", Status: state.InReview, ProjectID: 99,
	})
	root, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: task.ID, Author: "user", Content: "Root",
	})
	reply, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: task.ID, ParentCommentID: &root.ID,
		Author: "user", Content: "Reply",
	})
	gate, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: task.ID, Author: "agent", Content: "Approve it?",
	})
	workflowComment, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: 88, Author: "user", Content: "Revise it",
	})
	handler := testServer(t, wire).Handler()

	createdComments := eventCount(wire.Events(0), state.CommentCreated)
	first := requestJSON(t, handler, http.MethodPut, "/api/tasks/1/reactions", `{"emoji":"❤️","active":true}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first task reaction = %d %s", first.Code, first.Body)
	}
	var firstTask state.Task
	if err := json.Unmarshal(first.Body.Bytes(), &firstTask); err != nil ||
		!slices.Equal(firstTask.Reactions, []string{"❤️"}) {
		t.Fatalf("first task reaction = %#v, %v", firstTask, err)
	}
	firstUpdatedAt := firstTask.UpdatedAt

	repeated := requestJSON(t, handler, http.MethodPut, "/api/tasks/1/reactions", `{"emoji":"❤️","active":true}`)
	var repeatedTask state.Task
	if repeated.Code != http.StatusOK || json.Unmarshal(repeated.Body.Bytes(), &repeatedTask) != nil ||
		!repeatedTask.UpdatedAt.After(firstUpdatedAt) || !slices.Equal(repeatedTask.Reactions, []string{"❤️"}) {
		t.Fatalf("repeated task reaction = %d %s", repeated.Code, repeated.Body)
	}
	cleared := requestJSON(t, handler, http.MethodPut, "/api/tasks/1/reactions", `{"emoji":"❤️","active":false}`)
	var clearedTask state.Task
	if cleared.Code != http.StatusOK || json.Unmarshal(cleared.Body.Bytes(), &clearedTask) != nil ||
		clearedTask.Reactions == nil || len(clearedTask.Reactions) != 0 {
		t.Fatalf("cleared task reaction = %d %s", cleared.Code, cleared.Body)
	}

	for _, target := range []struct {
		id    int64
		emoji string
	}{
		{root.ID, "👍"}, {reply.ID, "🎉"}, {gate.ID, "👀"},
	} {
		response := requestJSON(t, handler, http.MethodPut,
			fmt.Sprintf("/api/comments/%d/reactions", target.id),
			fmt.Sprintf(`{"emoji":%q,"active":true}`, target.emoji))
		if response.Code != http.StatusOK {
			t.Fatalf("comment %d reaction = %d %s", target.id, response.Code, response.Body)
		}
	}
	last := wire.Events(0)[len(wire.Events(0))-1]
	var payload state.ReactionUpdatedData
	if last.Type != state.ReactionUpdated || json.Unmarshal(last.Data, &payload) != nil ||
		payload != (state.ReactionUpdatedData{TargetType: "comment", TargetID: gate.ID, Emoji: "👀", Active: true}) {
		t.Fatalf("last reaction event = %#v, payload = %#v", last, payload)
	}
	if eventCount(wire.Events(0), state.CommentCreated) != createdComments {
		t.Fatal("reaction request created a comment")
	}

	if response := requestJSON(t, handler, http.MethodDelete, "/api/tasks/1", ""); response.Code != http.StatusNoContent {
		t.Fatalf("delete task = %d %s", response.Code, response.Body)
	}
	if response := requestJSON(t, handler, http.MethodDelete, "/api/comments/2", ""); response.Code != http.StatusNoContent {
		t.Fatalf("delete root = %d %s", response.Code, response.Body)
	}
	beforeRejectedReaction := wire.LastID()
	cascaded := requestJSON(t, handler, http.MethodPut, "/api/comments/3/reactions", `{"emoji":"😂","active":true}`)
	if cascaded.Code != http.StatusNotFound || wire.LastID() != beforeRejectedReaction {
		t.Fatalf("cascaded reply reaction = %d %s, last ID = %d", cascaded.Code, cascaded.Body, wire.LastID())
	}

	taskDetail := requestJSON(t, handler, http.MethodGet, "/api/tasks/1", "")
	var taskEnvelope struct {
		Task     state.Task      `json:"task"`
		Comments []state.Comment `json:"comments"`
	}
	if taskDetail.Code != http.StatusOK || json.Unmarshal(taskDetail.Body.Bytes(), &taskEnvelope) != nil ||
		taskEnvelope.Task.Reactions == nil || len(taskEnvelope.Comments) != 1 || taskEnvelope.Comments[0].ID != gate.ID {
		t.Fatalf("task detail reactions = %d %s", taskDetail.Code, taskDetail.Body)
	}
	commentDetail := requestJSON(t, handler, http.MethodGet, "/api/comments/3", "")
	var commentEnvelope struct {
		Comment state.Comment   `json:"comment"`
		Replies []state.Comment `json:"replies"`
	}
	if commentDetail.Code != http.StatusOK || json.Unmarshal(commentDetail.Body.Bytes(), &commentEnvelope) != nil ||
		!slices.Equal(commentEnvelope.Comment.Reactions, []string{"🎉"}) ||
		commentEnvelope.Comment.DeletedAt == nil || commentEnvelope.Replies == nil || len(commentEnvelope.Replies) != 0 {
		t.Fatalf("comment detail reactions = %d %s", commentDetail.Code, commentDetail.Body)
	}

	lastID := wire.LastID()
	unsupported := requestJSON(t, handler, http.MethodPut,
		fmt.Sprintf("/api/comments/%d/reactions", workflowComment.ID), `{"emoji":"👍","active":true}`)
	if unsupported.Code != http.StatusBadRequest || wire.LastID() != lastID {
		t.Fatalf("workflow reaction = %d %s, last ID = %d", unsupported.Code, unsupported.Body, wire.LastID())
	}
}

func TestReactionAPIRejectsInvalidRequestsWithoutAppendingEvents(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	activeTask, _ := wire.Publish(state.TaskCreated, state.TaskData{Title: "Active", Status: state.Todo, ProjectID: 99})
	deletedTask, _ := wire.Publish(state.TaskCreated, state.TaskData{Title: "Deleted", Status: state.Todo, ProjectID: 99})
	activeComment, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: activeTask.ID, Author: "user", Content: "Active",
	})
	deletedComment, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: activeTask.ID, Author: "user", Content: "Deleted",
	})
	workflowComment, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: 88, Author: "user", Content: "Workflow",
	})
	wire.Publish(state.TaskDeleted, state.IDData{ID: deletedTask.ID})
	wire.Publish(state.CommentDeleted, state.IDData{ID: deletedComment.ID})
	handler := testServer(t, wire).Handler()

	tests := []struct {
		name, path, body string
		status           int
	}{
		{"invalid task ID", "/api/tasks/nope/reactions", `{"emoji":"👍","active":true}`, http.StatusBadRequest},
		{"invalid comment ID", "/api/comments/0/reactions", `{"emoji":"👍","active":true}`, http.StatusBadRequest},
		{"unknown field", fmt.Sprintf("/api/tasks/%d/reactions", activeTask.ID), `{"emoji":"👍","active":true,"actor":"user"}`, http.StatusBadRequest},
		{"missing active", fmt.Sprintf("/api/tasks/%d/reactions", activeTask.ID), `{"emoji":"👍"}`, http.StatusBadRequest},
		{"null active", fmt.Sprintf("/api/tasks/%d/reactions", activeTask.ID), `{"emoji":"👍","active":null}`, http.StatusBadRequest},
		{"skin tone", fmt.Sprintf("/api/tasks/%d/reactions", activeTask.ID), `{"emoji":"👍🏻","active":true}`, http.StatusBadRequest},
		{"heart without variation selector", fmt.Sprintf("/api/tasks/%d/reactions", activeTask.ID), `{"emoji":"❤","active":true}`, http.StatusBadRequest},
		{"whitespace", fmt.Sprintf("/api/comments/%d/reactions", activeComment.ID), `{"emoji":" 👍 ","active":true}`, http.StatusBadRequest},
		{"missing task", "/api/tasks/999/reactions", `{"emoji":"👍","active":true}`, http.StatusNotFound},
		{"deleted task", fmt.Sprintf("/api/tasks/%d/reactions", deletedTask.ID), `{"emoji":"👍","active":true}`, http.StatusNotFound},
		{"missing comment", "/api/comments/999/reactions", `{"emoji":"👍","active":true}`, http.StatusNotFound},
		{"deleted comment", fmt.Sprintf("/api/comments/%d/reactions", deletedComment.ID), `{"emoji":"👍","active":true}`, http.StatusNotFound},
		{"workflow comment", fmt.Sprintf("/api/comments/%d/reactions", workflowComment.ID), `{"emoji":"👍","active":true}`, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lastID := wire.LastID()
			response := requestJSON(t, handler, http.MethodPut, test.path, test.body)
			if response.Code != test.status || wire.LastID() != lastID {
				t.Fatalf("response = %d %s, last ID = %d, want status %d and ID %d",
					response.Code, response.Body, wire.LastID(), test.status, lastID)
			}
		})
	}
}

func TestReactionAndDeletionAreOrderedByTheWire(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		wire := openWire(t)
		task, _ := wire.Publish(state.TaskCreated, state.TaskData{
			Title: "Concurrent", Status: state.Todo, ProjectID: 99,
		})
		handler := testServer(t, wire).Handler()
		start := make(chan struct{})
		responses := make(chan *httptest.ResponseRecorder, 2)
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			request := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/tasks/%d/reactions", task.ID),
				strings.NewReader(`{"emoji":"👍","active":true}`))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			responses <- response
		}()
		go func() {
			defer workers.Done()
			<-start
			request := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/tasks/%d", task.ID), nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			responses <- response
		}()
		close(start)
		workers.Wait()
		close(responses)

		codes := make(map[int]int)
		for response := range responses {
			codes[response.Code]++
		}
		if codes[http.StatusNoContent] != 1 || codes[http.StatusOK]+codes[http.StatusNotFound] != 1 {
			t.Fatalf("iteration %d response codes = %#v", iteration, codes)
		}
		reactionIndex, deleteIndex := -1, -1
		for index, event := range wire.Events(0) {
			switch event.Type {
			case state.ReactionUpdated:
				reactionIndex = index
			case state.TaskDeleted:
				deleteIndex = index
			}
		}
		if deleteIndex < 0 || reactionIndex > deleteIndex ||
			(reactionIndex >= 0) != (codes[http.StatusOK] == 1) {
			t.Fatalf("iteration %d event order reaction=%d delete=%d, codes=%#v, events=%#v",
				iteration, reactionIndex, deleteIndex, codes, wire.Events(0))
		}
		wire.Close()
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
	view, _, err := wire.Snapshot()
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

func TestWorkflowDetailReplaysOrderedAuthoringSteps(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	workflowEvent, _ := wire.Publish(state.WorkflowCreated, state.WorkflowData{Name: "Draft"})
	user, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: workflowEvent.ID, Author: "user", Content: "Build it",
	})
	intermediate := false
	final := true
	for _, data := range []state.CommentData{
		{
			RelationType: "workflow", RelationID: workflowEvent.ID, ParentCommentID: &user.ID,
			Author: "agent", Kind: "reasoning", Label: "codex", Final: &intermediate,
			Content: "Inspecting the request.",
		},
		{
			RelationType: "workflow", RelationID: workflowEvent.ID, ParentCommentID: &user.ID,
			Author: "agent", Kind: "tool-use", Label: "command", Final: &intermediate,
			Content: "workflow validate review.js",
		},
		{
			RelationType: "workflow", RelationID: workflowEvent.ID, ParentCommentID: &user.ID,
			Author: "agent", Kind: "tool-output", Label: "command", Final: &intermediate,
			Content: "valid\n",
		},
		{
			RelationType: "workflow", RelationID: workflowEvent.ID, ParentCommentID: &user.ID,
			Author: "agent", Kind: "message", Final: &final, Content: "Created the workflow.",
		},
	} {
		if _, err := wire.Publish(state.CommentCreated, data); err != nil {
			t.Fatal(err)
		}
	}
	handler := testServer(t, wire).Handler()
	var details [2]struct {
		Comments []state.Comment `json:"comments"`
	}
	for index := range details {
		response := requestJSON(t, handler, http.MethodGet,
			fmt.Sprintf("/api/workflows/%d", workflowEvent.ID), "")
		if response.Code != http.StatusOK {
			t.Fatalf("detail status = %d, body = %s", response.Code, response.Body)
		}
		if err := json.Unmarshal(response.Body.Bytes(), &details[index]); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(details[0].Comments, details[1].Comments) {
		t.Fatalf("workflow replay changed: %#v != %#v", details[0].Comments, details[1].Comments)
	}
	comments := details[0].Comments
	if len(comments) != 5 || comments[0].ID != user.ID || comments[0].Final ||
		comments[1].Kind != "reasoning" || comments[1].Label != "codex" || comments[1].Final ||
		comments[2].Kind != "tool-use" || comments[2].Label != "command" || comments[2].Final ||
		comments[3].Kind != "tool-output" || comments[3].Content != "valid\n" || comments[3].Final ||
		comments[4].Kind != "message" || !comments[4].Final {
		t.Fatalf("workflow comments = %#v", comments)
	}
	for _, comment := range comments[1:] {
		if comment.ParentCommentID == nil || *comment.ParentCommentID != user.ID {
			t.Fatalf("step parent = %#v", comment)
		}
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
	path := filepath.Join(t.TempDir(), "factory.db")
	wire := openStoreAt(t, path)
	response := requestJSON(t, testServer(t, wire).Handler(), http.MethodPost,
		"/api/ingest?source=linear", `{"type":"Issue","action":"update"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openStoreAt(t, path)
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
	if health["workflowActive"] != float64(0) || health["workflowQuiescing"] != false {
		t.Fatalf("workflow health = %#v", health)
	}
}

func TestHealthTracksRunningWorkflowLifecycle(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	readHealth := func(wantRunning int) {
		t.Helper()
		response := requestJSON(t, handler, http.MethodGet, "/api/health", "")
		var health struct {
			WorkflowRunning   int   `json:"workflowRunning"`
			WorkflowActive    int   `json:"workflowActive"`
			CheckpointEventID int64 `json:"checkpointEventId"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &health) != nil {
			t.Fatalf("health = %d %s", response.Code, response.Body)
		}
		if health.WorkflowRunning != wantRunning || health.CheckpointEventID != wire.LastID() {
			t.Fatalf("health = %#v, want %d running at checkpoint %d",
				health, wantRunning, wire.LastID())
		}
		if health.WorkflowActive != 0 {
			t.Fatalf("projected runs changed workflow admission: %#v", health)
		}
	}

	readHealth(0)
	workflow, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	firstSource, _ := wire.Publish("release.first", map[string]bool{"ready": true})
	secondSource, _ := wire.Publish("release.second", map[string]bool{"ready": true})
	readHealth(0)
	firstRun, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 10, WorkflowID: workflow.ID, WorkflowName: "review", SourceEventID: firstSource.ID,
	})
	readHealth(1)
	wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 11, WorkflowID: workflow.ID, WorkflowName: "review", SourceEventID: secondSource.ID,
	})
	readHealth(2)
	wire.Publish(state.WorkflowRunWaiting, state.WorkflowRunStateData{RunID: firstRun.ID})
	readHealth(1)
	wire.Publish(state.WorkflowRunResumed, state.WorkflowRunStateData{RunID: firstRun.ID})
	readHealth(2)
	wire.Publish(state.WorkflowRunCompleted, state.WorkflowRunData{
		TriggerID: 11, SourceEventID: secondSource.ID, Output: "done",
	})
	readHealth(1)
	wire.Publish(state.WorkflowRunFailed, state.WorkflowRunData{
		TriggerID: 10, SourceEventID: firstSource.ID, Error: "failed",
	})
	readHealth(0)
}

func TestQuiescenceAPIStopsAdmissionDrainsAndReleases(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	admission := quiescence.New()
	handler := testServerWithAdmission(t, wire, admission).Handler()
	if !admission.TryStart() {
		t.Fatal("active workflow admission was blocked")
	}

	acquired := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		acquired <- requestJSON(t, handler, http.MethodPost, "/api/quiescence", "")
	}()
	waitForServerCondition(t, func() bool { return !admission.Accepting() }, "admission to stop")
	health := requestJSON(t, handler, http.MethodGet, "/api/health", "")
	if !strings.Contains(health.Body.String(), `"workflowActive":1`) ||
		!strings.Contains(health.Body.String(), `"workflowQuiescing":true`) {
		t.Fatalf("draining health = %s", health.Body)
	}
	if response := requestJSON(t, handler, http.MethodPost, "/api/quiescence", ""); response.Code != http.StatusConflict {
		t.Fatalf("concurrent acquire = %d %s", response.Code, response.Body)
	}
	select {
	case <-acquired:
		t.Fatal("quiescence returned before active work drained")
	default:
	}

	admission.Done(nil)
	response := receiveServerResponse(t, acquired, "quiescence response")
	if response.Code != http.StatusOK {
		t.Fatalf("acquire = %d %s", response.Code, response.Body)
	}
	var lease quiescence.Lease
	if err := json.Unmarshal(response.Body.Bytes(), &lease); err != nil || lease.Token == "" {
		t.Fatalf("lease = %#v, %v", lease, err)
	}
	if response := requestJSON(t, handler, http.MethodDelete, "/api/quiescence/wrong", ""); response.Code != http.StatusNotFound {
		t.Fatalf("wrong release = %d %s", response.Code, response.Body)
	}
	if response := requestJSON(t, handler, http.MethodDelete, "/api/quiescence/"+lease.Token, ""); response.Code != http.StatusOK {
		t.Fatalf("release = %d %s", response.Code, response.Body)
	}
	health = requestJSON(t, handler, http.MethodGet, "/api/health", "")
	if !strings.Contains(health.Body.String(), `"workflowActive":0`) ||
		!strings.Contains(health.Body.String(), `"workflowQuiescing":false`) {
		t.Fatalf("released health = %s", health.Body)
	}
}

func TestQuiescenceAPIRejectsFailedDrain(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	admission := quiescence.New()
	handler := testServerWithAdmission(t, wire, admission).Handler()
	if !admission.TryStart() {
		t.Fatal("active workflow admission was blocked")
	}
	acquired := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		acquired <- requestJSON(t, handler, http.MethodPost, "/api/quiescence", "")
	}()
	waitForServerCondition(t, func() bool { return !admission.Accepting() }, "admission to stop")
	admission.Done(errors.New("terminal wire event could not be recorded"))
	response := receiveServerResponse(t, acquired, "failed quiescence response")
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "failed while draining") {
		t.Fatalf("failed drain = %d %s", response.Code, response.Body)
	}
	if admission.Accepting() {
		t.Fatal("failed coordinator resumed admission")
	}
}

func TestTriggerEnabledStateDefaultsPersistsAndRequiresExplicitUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.db")
	wire := openStoreAt(t, path)
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
	reopened := openStoreAt(t, path)
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

func TestEventStreamReplaysEventAfterTaskListCheckpoint(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	project, _ := wire.Publish(state.ProjectCreated, state.ProjectData{Name: "Factory", Path: t.TempDir()})
	task, _ := wire.Publish(state.TaskCreated, state.TaskData{
		Title: "Watch comments", Status: state.Todo, ProjectID: project.ID,
	})
	list := requestJSON(t, handler, http.MethodGet, "/api/tasks", "")
	var snapshot struct {
		CheckpointEventID int64 `json:"checkpointEventId"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &snapshot) != nil {
		t.Fatalf("task list = %d %s", list.Code, list.Body)
	}
	intervening, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: task.ID, Author: "user", Content: "New comment",
	})

	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		fmt.Sprintf("%s/api/events/stream?after=%d", server.URL, snapshot.CheckpointEventID), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for _, expected := range []string{": connected\n", "\n"} {
		line, err := reader.ReadString('\n')
		if err != nil || line != expected {
			t.Fatalf("stream opening = %q, %v; want %q", line, err, expected)
		}
	}
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: ") {
		t.Fatalf("stream event = %q, %v", line, err)
	}
	var event eventwire.Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &event); err != nil {
		t.Fatal(err)
	}
	if event.ID != intervening.ID || event.Type != state.CommentCreated {
		t.Fatalf("replayed event = %#v, want %#v", event, intervening)
	}
}

func TestEventStreamReplaysWorkflowTransitionAfterHealthCheckpoint(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	handler := testServer(t, wire).Handler()
	workflow, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	source, _ := wire.Publish("release.ready", map[string]bool{"ready": true})
	started, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 10, WorkflowID: workflow.ID, WorkflowName: "review", SourceEventID: source.ID,
	})
	health := requestJSON(t, handler, http.MethodGet, "/api/health", "")
	var snapshot struct {
		WorkflowRunning   int   `json:"workflowRunning"`
		CheckpointEventID int64 `json:"checkpointEventId"`
	}
	if health.Code != http.StatusOK || json.Unmarshal(health.Body.Bytes(), &snapshot) != nil ||
		snapshot.WorkflowRunning != 1 || snapshot.CheckpointEventID != started.ID {
		t.Fatalf("health = %d %s", health.Code, health.Body)
	}
	intervening, _ := wire.Publish(state.WorkflowRunWaiting, state.WorkflowRunStateData{RunID: started.ID})

	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		fmt.Sprintf("%s/api/events/stream?after=%d", server.URL, snapshot.CheckpointEventID), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for _, expected := range []string{": connected\n", "\n"} {
		line, err := reader.ReadString('\n')
		if err != nil || line != expected {
			t.Fatalf("stream opening = %q, %v; want %q", line, err, expected)
		}
	}
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: ") {
		t.Fatalf("stream event = %q, %v", line, err)
	}
	var event eventwire.Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &event); err != nil {
		t.Fatal(err)
	}
	if event.ID != intervening.ID || event.Type != state.WorkflowRunWaiting {
		t.Fatalf("replayed event = %#v, want %#v", event, intervening)
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

type testStore struct {
	*store.Store
	t *testing.T
}

func (s *testStore) Publish(eventType string, data any) (eventwire.Event, error) {
	return s.Append(eventType, data)
}

func (s *testStore) Events(after int64) []eventwire.Event {
	s.t.Helper()
	events, err := s.EventsAfter(after, 100_000)
	if err != nil {
		s.t.Fatal(err)
	}
	return events
}

func (s *testStore) Event(id int64) (eventwire.Event, bool) {
	s.t.Helper()
	event, found, err := s.Store.Event(id)
	if err != nil {
		s.t.Fatal(err)
	}
	return event, found
}

func (s *testStore) LastID() int64 {
	s.t.Helper()
	id, err := s.Store.LastID()
	if err != nil {
		s.t.Fatal(err)
	}
	return id
}

func openStoreAt(t *testing.T, path string) *testStore {
	t.Helper()
	eventStore, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return &testStore{Store: eventStore, t: t}
}

func openWire(t *testing.T) *testStore {
	t.Helper()
	return openStoreAt(t, filepath.Join(t.TempDir(), "factory.db"))
}

func testServer(t *testing.T, wire *testStore) *Server {
	t.Helper()
	return testServerWithAdmission(t, wire, quiescence.New())
}

func testServerWithAdmission(
	t *testing.T,
	wire *testStore,
	admission *quiescence.Controller,
) *Server {
	t.Helper()
	assets := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<html></html>")},
		"assets/app-a1.js":     &fstest.MapFile{Data: []byte("export {};")},
		"assets/styles-b2.css": &fstest.MapFile{Data: []byte("body {}")},
	}
	var filesystem fs.FS = assets
	server, err := New(wire.Store, filesystem, t.TempDir(), admission)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func waitForServerCondition(t *testing.T, check func() bool, label string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if check() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", label)
		case <-time.After(time.Millisecond):
		}
	}
}

func receiveServerResponse(
	t *testing.T,
	responses <-chan *httptest.ResponseRecorder,
	label string,
) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-responses:
		return response
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func eventCount(events []eventwire.Event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}
