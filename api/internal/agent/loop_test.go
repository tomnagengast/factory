package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/workflow"
)

type fakeAgent struct {
	prompts  []string
	settings []state.Settings
	output   string
}

func (f *fakeAgent) Run(_ context.Context, settings state.Settings, prompt string) (string, error) {
	f.settings = append(f.settings, settings)
	f.prompts = append(f.prompts, prompt)
	return f.output, nil
}

type fakeWorkflows struct {
	definitions []workflow.Definition
	runs        []struct {
		directory, name, source string
		settings                state.Settings
	}
}

func (f *fakeWorkflows) List(context.Context) ([]workflow.Definition, error) {
	return f.definitions, nil
}

func (f *fakeWorkflows) Run(
	_ context.Context,
	directory, name, source string,
	settings state.Settings,
	_ any,
	emit func(workflow.Event) error,
) (string, error) {
	f.runs = append(f.runs, struct {
		directory, name, source string
		settings                state.Settings
	}{directory, name, source, settings})
	if emit != nil {
		events := []string{
			`{"sequence":1,"at":"2026-07-17T12:00:00Z","type":"step.started","workflow":"review","phase":"Review","stepId":1,"kind":"agent","message":"Review it"}`,
			`{"sequence":2,"at":"2026-07-17T12:00:01Z","type":"step.completed","workflow":"review","phase":"Review","stepId":1,"kind":"agent","result":"approved","extension":{"kept":true}}`,
		}
		for _, raw := range events {
			var event workflow.Event
			json.Unmarshal([]byte(raw), &event)
			event.Raw = json.RawMessage(raw)
			if err := emit(event); err != nil {
				return "", err
			}
		}
	}
	return "complete", nil
}

func (f *fakeWorkflows) LocalPath(id int64) string {
	return filepath.Join("/workflows", "workflow-"+strconv.FormatInt(id, 10)+".js")
}

func TestLoopAnswersWorkflowConversation(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	created, err := wire.Publish(state.WorkflowCreated, state.WorkflowData{Name: "Draft"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: created.ID, Author: "user", Content: "Build a review panel",
	}); err != nil {
		t.Fatal(err)
	}
	selected := state.Settings{Harness: state.Claude, Model: "sonnet", Reasoning: "high"}
	if _, err := wire.Publish(state.SettingsUpdated, selected); err != nil {
		t.Fatal(err)
	}
	runner := &fakeAgent{output: "Created the review panel."}
	workflows := &fakeWorkflows{}
	loop, err := NewLoop(wire, runner, workflows)
	if err != nil {
		t.Fatal(err)
	}
	worked, _, err := loop.step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !worked || len(runner.prompts) != 1 ||
		!strings.Contains(runner.prompts[0], "Build a review panel") ||
		!strings.Contains(runner.prompts[0], "$FACTORY_CLI") ||
		!strings.Contains(runner.prompts[0], "github.com/tomnagengast/workflow") {
		t.Fatalf("workflow was not authored: %#v", runner.prompts)
	}
	if len(runner.settings) != 1 || runner.settings[0] != selected {
		t.Fatalf("authoring settings = %#v", runner.settings)
	}
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	comments := view.CommentsFor("workflow", created.ID)
	if len(comments) != 2 || comments[1].Author != "agent" {
		t.Fatalf("agent reply missing: %#v", comments)
	}
	authored, _ := view.Workflow(created.ID)
	if authored.Path == nil || *authored.Path != workflows.LocalPath(created.ID) {
		t.Fatalf("workflow path = %v, want live authoring target", authored.Path)
	}
}

func TestLoopRunsMatchingEventTrigger(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	triggerEvent, _ := wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID,
	})
	source, _ := wire.Publish("release.ready", map[string]string{"version": "1.0"})

	workflows := &fakeWorkflows{}
	loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
	worked, _, err := loop.step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !worked || len(workflows.runs) != 1 || workflows.runs[0].name != "review" || workflows.runs[0].directory != "" {
		t.Fatalf("trigger did not run: %#v", workflows.runs)
	}
	if workflows.runs[0].settings != state.DefaultSettings {
		t.Fatalf("trigger settings = %#v", workflows.runs[0].settings)
	}
	view, _ := state.ProjectEvents(wire.Events(0))
	if !view.RunStarted(triggerEvent.ID, source.ID) {
		t.Fatal("run marker missing")
	}
	if len(view.Runs) != 1 || view.Runs[0].Status != "completed" || view.Runs[0].WorkflowName != "review" {
		t.Fatalf("run history missing: %#v", view.Runs)
	}
	events := view.EventsFor(view.Runs[0].ID)
	if len(events) != 2 || events[0].Type != "step.started" ||
		string(events[1].Result) != `"approved"` {
		t.Fatalf("run events missing: %#v", events)
	}
	var recorded int
	for _, event := range wire.Events(0) {
		if event.Type == state.WorkflowRunEventRecorded {
			recorded++
			if recorded == 2 && !strings.Contains(string(event.Data), `"extension":{"kept":true}`) {
				t.Fatalf("workflow event was filtered: %s", event.Data)
			}
		}
	}
	if recorded != 2 {
		t.Fatalf("recorded workflow events = %d, want 2", recorded)
	}
}

func TestLoopRunsTaskTriggersInProjectPath(t *testing.T) {
	for _, eventType := range []string{state.TaskCreated, state.TaskUpdated, state.TaskDeleted} {
		t.Run(eventType, func(t *testing.T) {
			wire := openWire(t)
			defer wire.Close()
			path := t.TempDir()
			project, _ := wire.Publish(state.ProjectCreated, state.ProjectData{Name: "Factory", Path: path})
			workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
			wire.Publish(state.TriggerCreated, state.TriggerData{EventType: eventType, WorkflowID: workflowEvent.ID})
			task, _ := wire.Publish(state.TaskCreated, state.TaskData{
				Title: "Ship it", Status: state.Backlog, ProjectID: project.ID,
			})
			switch eventType {
			case state.TaskUpdated:
				wire.Publish(eventType, state.TaskData{
					ID: task.ID, Title: "Ship it", Status: state.Todo, ProjectID: project.ID,
				})
			case state.TaskDeleted:
				wire.Publish(eventType, state.IDData{ID: task.ID})
			}

			workflows := &fakeWorkflows{}
			loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
			worked, _, err := loop.step(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !worked || len(workflows.runs) != 1 || workflows.runs[0].directory != path {
				t.Fatalf("runs = %#v, want project path %q", workflows.runs, path)
			}
		})
	}
}

func openWire(t *testing.T) *eventwire.Wire {
	t.Helper()
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return wire
}
