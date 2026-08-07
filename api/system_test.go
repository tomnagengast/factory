package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
)

func TestSystemPersistsAPIWritesAcrossRestart(t *testing.T) {
	fixture := newSystemFixture(t)
	running := fixture.Start(t)

	project := decodeCLI[state.Project](t, running.runCLI(t,
		"project", "create", jsonArgument(t, state.ProjectData{
			Name: "Factory", Description: stringAddress("Persistent project"), Path: fixture.projectPath,
		}),
	))
	task := decodeCLI[state.Task](t, running.runCLI(t,
		"task", "create", jsonArgument(t, state.TaskData{
			Title: "Prove persistence", Description: stringAddress("Before restart"),
			Status: state.Todo, ProjectID: project.ID,
		}),
	))
	updated := decodeCLI[state.Task](t, running.runCLI(t,
		"task", "update", strconv.FormatInt(task.ID, 10), jsonArgument(t, state.TaskData{
			Title: "Persistence proved", Description: stringAddress("After restart"),
			Status: state.InReview, ProjectID: project.ID,
		}),
	))
	if updated.Status != state.InReview {
		t.Fatalf("updated task = %#v", updated)
	}
	if info, err := os.Stat(fixture.projectPath); err != nil || !info.IsDir() {
		t.Fatalf("project directory = %#v, %v", info, err)
	}

	running.Close(t)
	restarted := fixture.Start(t)
	projectResult := decodeCLI[struct {
		Project state.Project `json:"project"`
	}](t, restarted.runCLI(t, "project", "get", strconv.FormatInt(project.ID, 10)))
	taskResult := decodeCLI[struct {
		Task state.Task `json:"task"`
	}](t, restarted.runCLI(t, "task", "get", strconv.FormatInt(task.ID, 10)))
	if projectResult.Project.ID != project.ID || projectResult.Project.Name != "Factory" ||
		projectResult.Project.Path != fixture.projectPath {
		t.Fatalf("project after restart = %#v", projectResult.Project)
	}
	if taskResult.Task.ID != task.ID || taskResult.Task.Title != "Persistence proved" ||
		taskResult.Task.Status != state.InReview || taskResult.Task.ProjectID != project.ID ||
		taskResult.Task.Description == nil || *taskResult.Task.Description != "After restart" {
		t.Fatalf("task after restart = %#v", taskResult.Task)
	}
}

func TestSystemTaskUpdateTriggerRecordsJournalAndHistory(t *testing.T) {
	fixture := newSystemFixture(t)
	definition := fixture.setWorkflow(t, "review")
	running := fixture.Start(t)
	workflow := systemWorkflow(t, running, definition.Name)

	project := decodeCLI[state.Project](t, running.runCLI(t,
		"project", "create", jsonArgument(t, state.ProjectData{Name: "Factory", Path: fixture.projectPath}),
	))
	task := decodeCLI[state.Task](t, running.runCLI(t,
		"task", "create", jsonArgument(t, state.TaskData{
			Title: "Review behavior", Status: state.Todo, ProjectID: project.ID,
		}),
	))
	trigger := decodeCLI[state.Trigger](t, running.runCLI(t,
		"trigger", "create", jsonArgument(t, state.TriggerData{
			EventType: state.TaskUpdated, WorkflowID: workflow.ID, Enabled: true,
		}),
	))
	updated := decodeCLI[state.Task](t, running.runCLI(t,
		"task", "update", strconv.FormatInt(task.ID, 10), jsonArgument(t, state.TaskData{
			Title: "Review behavior", Description: stringAddress("Run the system workflow"),
			Status: state.InReview, ProjectID: project.ID,
		}),
	))
	if updated.Status != state.InReview {
		t.Fatalf("updated task = %#v", updated)
	}

	run := waitForRunStatus(t, running, "completed", func(run state.WorkflowRun) bool {
		return run.TriggerID == trigger.ID && run.TaskID == task.ID
	})
	var detail struct {
		Run    state.WorkflowRun        `json:"run"`
		Events []state.WorkflowRunEvent `json:"events"`
	}
	running.getJSON(t, "/api/history/"+strconv.FormatInt(run.ID, 10), &detail)
	if detail.Run.SourceEventID < 1 || detail.Run.WorkflowID != workflow.ID ||
		detail.Run.WorkflowName != definition.Name || detail.Run.Status != "completed" ||
		detail.Run.Output != "system complete" {
		t.Fatalf("completed run = %#v", detail.Run)
	}
	wantTypes := []string{"runtime.started", "phase.started", "log", "runtime.completed"}
	gotTypes := make([]string, 0, len(detail.Events))
	for index, event := range detail.Events {
		gotTypes = append(gotTypes, event.Type)
		if event.Sequence != int64(index+1) || event.RunID != run.ID {
			t.Fatalf("history event %d = %#v", index, event)
		}
	}
	if !slices.Equal(gotTypes, wantTypes) ||
		!strings.Contains(string(detail.Events[2].Raw), `"extension":{"kept":true}`) {
		t.Fatalf("history events = %#v", detail.Events)
	}

	events := systemEvents(t, running)
	sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })
	var started eventwire.Event
	var startedData state.WorkflowRunData
	var recorded []eventwire.Event
	var completedID, sideEffectID int64
	for _, event := range events {
		switch event.Type {
		case state.WorkflowRunStarted:
			var data state.WorkflowRunData
			if json.Unmarshal(event.Data, &data) == nil && data.SourceEventID == run.SourceEventID {
				started, startedData = event, data
			}
		case state.WorkflowRunEventRecorded:
			var data state.WorkflowRunEventData
			if json.Unmarshal(event.Data, &data) == nil && data.RunID == run.ID {
				recorded = append(recorded, event)
			}
		case state.WorkflowRunCompleted:
			var data state.WorkflowRunData
			if json.Unmarshal(event.Data, &data) == nil && data.SourceEventID == run.SourceEventID {
				completedID = event.ID
			}
		case "workflow.side-effect":
			sideEffectID = event.ID
		}
	}
	if started.ID != run.ID || startedData.TriggerID != trigger.ID || startedData.TaskID != task.ID ||
		startedData.Directory != fixture.projectPath || startedData.Source != definition.Path ||
		startedData.Settings == nil || !reflect.DeepEqual(*startedData.Settings, state.DefaultSettings()) {
		t.Fatalf("workflow start = %#v, %#v", started, startedData)
	}
	var arguments struct {
		Event   eventwire.Event `json:"event"`
		Trigger state.Trigger   `json:"trigger"`
		RunID   int64           `json:"runId"`
	}
	var taskUpdate state.TaskData
	if json.Unmarshal(startedData.Arguments, &arguments) != nil ||
		json.Unmarshal(arguments.Event.Data, &taskUpdate) != nil ||
		arguments.RunID != run.ID || arguments.Event.ID != run.SourceEventID ||
		arguments.Event.Type != state.TaskUpdated || arguments.Trigger.ID != trigger.ID ||
		taskUpdate.Status != state.InReview || taskUpdate.ProjectID != project.ID {
		t.Fatalf("workflow arguments = %s", startedData.Arguments)
	}
	if len(recorded) != len(wantTypes) || !(started.ID < recorded[0].ID && recorded[3].ID < completedID) ||
		sideEffectID < 1 {
		t.Fatalf("wire lifecycle start=%d recorded=%#v complete=%d side-effect=%d", started.ID, recorded, completedID, sideEffectID)
	}

	invocations, err := invocationDirectories(fixture.invocationsPath)
	if err != nil || len(invocations) != 1 {
		t.Fatalf("workflow invocations = %#v, %v", invocations, err)
	}
	invocation := invocations[0]
	assertFileText(t, filepath.Join(invocation, "pwd"), fixture.projectPath)
	assertFileText(t, filepath.Join(invocation, "directory"), fixture.projectPath)
	assertFileText(t, filepath.Join(invocation, "source"), definition.Path)
	assertFileText(t, filepath.Join(invocation, "backend"), state.Codex)
	assertFileText(t, filepath.Join(invocation, "model"), state.DefaultSettings().Model)
	assertFileText(t, filepath.Join(invocation, "codex-bin"), fixture.codexCommand)
	assertFileText(t, filepath.Join(invocation, "factory-url"), running.url)
	assertFileText(t, filepath.Join(invocation, "factory-cli"), fixture.factoryCommand)
	argv, err := os.ReadFile(filepath.Join(invocation, "argv"))
	if err != nil || !strings.Contains(string(argv), "--codex-bin\n"+fixture.codexCommand+"\n") ||
		!strings.Contains(string(argv), "--codex-arg\nmodel_reasoning_effort=\"low\"\n") {
		t.Fatalf("workflow argv = %q, %v", argv, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.projectPath, "workflow.marker")); err != nil {
		t.Fatalf("workflow marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.projectPath, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workflow source appeared in project: %v", err)
	}
}

func TestSystemWorkflowCapacity(t *testing.T) {
	fixture := newSystemFixture(t)
	fixture.setWorkflowMode(t, "blocking")
	definition := fixture.setWorkflow(t, "review")
	running := fixture.Start(t)
	workflow := systemWorkflow(t, running, definition.Name)
	settings := state.DefaultSettings()
	settings.WorkflowCapacity = 2
	running.requestJSON(t, http.MethodPut, "/api/settings", settings, nil)
	trigger := decodeCLI[state.Trigger](t, running.runCLI(t,
		"trigger", "create", jsonArgument(t, state.TriggerData{
			EventType: "release.ready", WorkflowID: workflow.ID, Enabled: true,
		}),
	))
	sources := []eventwire.Event{
		createSystemEvent(t, running, "release.ready", map[string]int{"version": 1}),
		createSystemEvent(t, running, "release.ready", map[string]int{"version": 2}),
		createSystemEvent(t, running, "release.ready", map[string]int{"version": 3}),
	}

	waitForSystemCondition(t, "two admitted workflow processes", func() (bool, error) {
		invocations, err := startedInvocations(fixture.invocationsPath)
		return len(invocations) == 2, err
	})
	waitForHistoryCounts(t, running, map[string]int{"running": 2, "completed": 0})
	invocations, _ := startedInvocations(fixture.invocationsPath)
	if len(invocations) != 2 || activeInvocationCount(invocations) != 2 {
		t.Fatalf("initial invocations = %#v", invocations)
	}

	settings.WorkflowCapacity = 1
	running.requestJSON(t, http.MethodPut, "/api/settings", settings, nil)
	first := invocationForSource(t, invocations, sources[0].ID)
	if err := releaseInvocation(first); err != nil {
		t.Fatal(err)
	}
	waitForHistoryCounts(t, running, map[string]int{"running": 1, "completed": 1})
	if current, err := invocationDirectories(fixture.invocationsPath); err != nil || len(current) != 2 {
		t.Fatalf("invocations after lowering capacity = %#v, %v", current, err)
	} else if activeInvocationCount(current) != 1 {
		t.Fatalf("active invocations after lowering capacity = %d, want 1", activeInvocationCount(current))
	}

	second := invocationForSource(t, invocations, sources[1].ID)
	if err := releaseInvocation(second); err != nil {
		t.Fatal(err)
	}
	waitForSystemCondition(t, "third workflow process", func() (bool, error) {
		current, err := startedInvocations(fixture.invocationsPath)
		return len(current) == 3, err
	})
	waitForHistoryCounts(t, running, map[string]int{"running": 1, "completed": 2})
	allInvocations, _ := startedInvocations(fixture.invocationsPath)
	if activeInvocationCount(allInvocations) != 1 {
		t.Fatalf("active invocations after capacity freed = %d, want 1", activeInvocationCount(allInvocations))
	}
	third := invocationForSource(t, allInvocations, sources[2].ID)
	if err := releaseInvocation(third); err != nil {
		t.Fatal(err)
	}
	waitForHistoryCounts(t, running, map[string]int{"running": 0, "completed": 3})

	runs := systemHistory(t, running, "completed")
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	gotSources := make([]int64, 0, len(runs))
	for _, run := range runs {
		if run.TriggerID == trigger.ID {
			gotSources = append(gotSources, run.SourceEventID)
		}
	}
	wantSources := []int64{sources[0].ID, sources[1].ID, sources[2].ID}
	if !slices.Equal(gotSources, wantSources) {
		t.Fatalf("completed source order = %v, want %v", gotSources, wantSources)
	}
}

func TestSystemTriggerDisableBoundaries(t *testing.T) {
	fixture := newSystemFixture(t)
	fixture.setWorkflowMode(t, "blocking")
	definition := fixture.setWorkflow(t, "review")
	running := fixture.Start(t)
	workflow := systemWorkflow(t, running, definition.Name)
	trigger := decodeCLI[state.Trigger](t, running.runCLI(t,
		"trigger", "create", jsonArgument(t, state.TriggerData{
			EventType: "release.ready", WorkflowID: workflow.ID, Enabled: true,
		}),
	))
	firstSource := createSystemEvent(t, running, "release.ready", map[string]int{"version": 1})
	waitForSystemCondition(t, "first blocked workflow", func() (bool, error) {
		invocations, err := startedInvocations(fixture.invocationsPath)
		return len(invocations) == 1, err
	})
	waitForHistoryCounts(t, running, map[string]int{"running": 1, "completed": 0})

	disabled := state.TriggerData{
		EventType: trigger.EventType, WorkflowID: trigger.WorkflowID, Enabled: false,
	}
	disabledResult := decodeCLI[state.Trigger](t, running.runCLI(t,
		"trigger", "update", strconv.FormatInt(trigger.ID, 10), jsonArgument(t, disabled),
	))
	if disabledResult.Enabled {
		t.Fatalf("disabled trigger = %#v", disabledResult)
	}
	disabledSource := createSystemEvent(t, running, "release.ready", map[string]int{"version": 2})
	initialInvocations, _ := startedInvocations(fixture.invocationsPath)
	if err := releaseInvocation(invocationForSource(t, initialInvocations, firstSource.ID)); err != nil {
		t.Fatal(err)
	}
	waitForHistoryCounts(t, running, map[string]int{"running": 0, "completed": 1})

	enabled := disabled
	enabled.Enabled = true
	enabledResult := decodeCLI[state.Trigger](t, running.runCLI(t,
		"trigger", "update", strconv.FormatInt(trigger.ID, 10), jsonArgument(t, enabled),
	))
	if !enabledResult.Enabled {
		t.Fatalf("enabled trigger = %#v", enabledResult)
	}
	thirdSource := createSystemEvent(t, running, "release.ready", map[string]int{"version": 3})
	waitForSystemCondition(t, "post-enable workflow", func() (bool, error) {
		invocations, err := startedInvocations(fixture.invocationsPath)
		return len(invocations) == 2, err
	})
	allInvocations, _ := startedInvocations(fixture.invocationsPath)
	if err := releaseInvocation(invocationForSource(t, allInvocations, thirdSource.ID)); err != nil {
		t.Fatal(err)
	}
	waitForHistoryCounts(t, running, map[string]int{"running": 0, "completed": 2})

	runs := systemHistory(t, running, "completed")
	var sourceIDs []int64
	for _, run := range runs {
		if run.TriggerID == trigger.ID {
			sourceIDs = append(sourceIDs, run.SourceEventID)
		}
	}
	slices.Sort(sourceIDs)
	want := []int64{firstSource.ID, thirdSource.ID}
	if !slices.Equal(sourceIDs, want) || slices.Contains(sourceIDs, disabledSource.ID) {
		t.Fatalf("trigger source IDs = %v, want %v without %d", sourceIDs, want, disabledSource.ID)
	}
}

func stringAddress(value string) *string {
	return &value
}

func jsonArgument(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func systemWorkflow(t *testing.T, running *runningSystem, name string) state.Workflow {
	t.Helper()
	var response struct {
		Workflows []state.Workflow `json:"workflows"`
	}
	running.getJSON(t, "/api/workflows", &response)
	for _, workflow := range response.Workflows {
		if workflow.Name == name {
			return workflow
		}
	}
	t.Fatalf("workflow %q not found in %#v", name, response.Workflows)
	return state.Workflow{}
}

func systemHistory(t *testing.T, running *runningSystem, status string) []state.WorkflowRun {
	t.Helper()
	var response struct {
		History []state.WorkflowRun `json:"history"`
	}
	path := "/api/history?limit=200"
	if status != "" {
		path += "&status=" + status
	}
	running.getJSON(t, path, &response)
	return response.History
}

func waitForRunStatus(
	t *testing.T,
	running *runningSystem,
	status string,
	match func(state.WorkflowRun) bool,
) state.WorkflowRun {
	t.Helper()
	var selected state.WorkflowRun
	waitForSystemCondition(t, status+" workflow history", func() (bool, error) {
		for _, run := range systemHistory(t, running, status) {
			if match(run) {
				selected = run
				return true, nil
			}
		}
		return false, nil
	})
	return selected
}

func waitForHistoryCounts(t *testing.T, running *runningSystem, want map[string]int) {
	t.Helper()
	waitForSystemCondition(t, "workflow history counts", func() (bool, error) {
		for status, count := range want {
			if len(systemHistory(t, running, status)) != count {
				return false, nil
			}
		}
		return true, nil
	})
}

func createSystemEvent(t *testing.T, running *runningSystem, eventType string, data any) eventwire.Event {
	t.Helper()
	var event eventwire.Event
	running.requestJSON(t, http.MethodPost, "/api/events", map[string]any{
		"type": eventType, "data": data,
	}, &event)
	return event
}

func systemEvents(t *testing.T, running *runningSystem) []eventwire.Event {
	t.Helper()
	var response struct {
		Events []eventwire.Event `json:"events"`
	}
	running.getJSON(t, "/api/events?limit=500", &response)
	return response.Events
}

func startedInvocations(path string) ([]string, error) {
	invocations, err := invocationDirectories(path)
	if err != nil {
		return nil, err
	}
	started := make([]string, 0, len(invocations))
	for _, invocation := range invocations {
		if _, err := os.Stat(filepath.Join(invocation, "started")); err == nil {
			started = append(started, invocation)
		}
	}
	sort.Strings(started)
	return started, nil
}

func invocationForSource(t *testing.T, invocations []string, sourceID int64) string {
	t.Helper()
	for _, invocation := range invocations {
		data, err := os.ReadFile(filepath.Join(invocation, "args.json"))
		if err != nil {
			t.Fatal(err)
		}
		var arguments struct {
			Event eventwire.Event `json:"event"`
		}
		if json.Unmarshal(data, &arguments) == nil && arguments.Event.ID == sourceID {
			return invocation
		}
	}
	t.Fatalf("invocation for source %d not found in %#v", sourceID, invocations)
	return ""
}

func activeInvocationCount(invocations []string) int {
	count := 0
	for _, invocation := range invocations {
		if _, err := os.Stat(filepath.Join(invocation, "active")); err == nil {
			count++
		}
	}
	return count
}

func assertFileText(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
