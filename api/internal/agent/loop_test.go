package agent

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/workflow"
)

type fakeAgent struct {
	prompts []string
	output  string
}

func (f *fakeAgent) Run(_ context.Context, prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	return f.output, nil
}

type fakeWorkflows struct {
	definitions []workflow.Definition
	runs        []struct{ directory, name, source string }
}

func (f *fakeWorkflows) List(context.Context) ([]workflow.Definition, error) {
	return f.definitions, nil
}

func (f *fakeWorkflows) Run(_ context.Context, directory, name, source string, _ any) (string, error) {
	f.runs = append(f.runs, struct{ directory, name, source string }{directory, name, source})
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
	if !worked || len(runner.prompts) != 1 || !strings.Contains(runner.prompts[0], "Build a review panel") {
		t.Fatalf("workflow was not authored: %#v", runner.prompts)
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
	view, _ := state.ProjectEvents(wire.Events(0))
	if !view.RunStarted(triggerEvent.ID, source.ID) {
		t.Fatal("run marker missing")
	}
}

func TestLoopRunsTaskTriggersInProjectPath(t *testing.T) {
	for _, eventType := range []string{state.TaskCreated, state.TaskUpdated, state.TaskDeleted} {
		t.Run(eventType, func(t *testing.T) {
			wire := openWire(t)
			defer wire.Close()
			path := t.TempDir()
			project, _ := wire.Publish(state.ProjectCreated, state.ProjectData{Name: "Factory", Path: &path})
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
