package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	validateErr error
	validations []string
	mu          sync.Mutex
	runs        []struct {
		directory, source string
		settings          state.Settings
		args              any
	}
	active, maxActive int
	started, finished chan struct{}
	release           chan struct{}
}

func newFakeWorkflows() *fakeWorkflows {
	return &fakeWorkflows{
		started:  make(chan struct{}, state.MaxWorkflowCapacity+1),
		finished: make(chan struct{}, state.MaxWorkflowCapacity+1),
	}
}

func (f *fakeWorkflows) List(context.Context) ([]workflow.Definition, error) {
	return f.definitions, nil
}

func (f *fakeWorkflows) Validate(_ context.Context, source string) error {
	f.validations = append(f.validations, source)
	return f.validateErr
}

func (f *fakeWorkflows) Run(
	ctx context.Context,
	directory, source string,
	settings state.Settings,
	args any,
	emit func(workflow.Event) error,
) (string, error) {
	f.mu.Lock()
	f.runs = append(f.runs, struct {
		directory, source string
		settings          state.Settings
		args              any
	}{directory, source, settings, args})
	f.active++
	f.maxActive = max(f.maxActive, f.active)
	f.mu.Unlock()
	f.started <- struct{}{}
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
		f.finished <- struct{}{}
	}()
	if f.release != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-f.release:
		}
	}
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

func (f *fakeWorkflows) snapshot() (runs int, maxActive int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs), f.maxActive
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
	workflows := newFakeWorkflows()
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
		!strings.Contains(runner.prompts[0], "github.com/tomnagengast/workflow") ||
		!strings.Contains(runner.prompts[0], `workflow validate "/workflows/workflow-1.js"`) {
		t.Fatalf("workflow was not authored: %#v", runner.prompts)
	}
	if len(workflows.validations) != 1 || workflows.validations[0] != workflows.LocalPath(created.ID) {
		t.Fatalf("workflow validations = %#v", workflows.validations)
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

func TestLoopRejectsInvalidAuthoredWorkflow(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	created, err := wire.Publish(state.WorkflowCreated, state.WorkflowData{Name: "Draft"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: created.ID, Author: "user", Content: "Build it",
	}); err != nil {
		t.Fatal(err)
	}
	workflows := newFakeWorkflows()
	workflows.validateErr = errors.New("parse error near mktemp")
	loop, err := NewLoop(wire, &fakeAgent{output: "Updated and validated."}, workflows)
	if err != nil {
		t.Fatal(err)
	}
	worked, _, err := loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("authoring step = %v, %v", worked, err)
	}
	if eventTypeCount(wire.Events(0), authoringCompleted) != 0 ||
		eventTypeCount(wire.Events(0), authoringFailed) != 1 {
		t.Fatalf("authoring events = %#v", wire.Events(0))
	}
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	comments := view.CommentsFor("workflow", created.ID)
	if len(comments) != 2 ||
		!strings.Contains(comments[1].Content, "parse error near mktemp") {
		t.Fatalf("authoring failure comment = %#v", comments)
	}
}

func TestLoopRunsMatchingEventTrigger(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	sourcePath := "/workflows/review.js"
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{
		Name: "review", Path: &sourcePath,
	})
	triggerEvent, _ := wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	source, _ := wire.Publish("release.ready", map[string]string{"version": "1.0"})

	workflows := newFakeWorkflows()
	loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
	worked, _, err := loop.step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, workflows.started, "workflow start")
	waitForSignal(t, workflows.finished, "workflow finish")
	waitForActiveCount(t, loop, 0)
	if !worked {
		t.Fatal("trigger was not dispatched")
	}
	workflows.mu.Lock()
	if len(workflows.runs) != 1 || workflows.runs[0].source != sourcePath || workflows.runs[0].directory != "" {
		t.Fatalf("trigger did not run: %#v", workflows.runs)
	}
	if workflows.runs[0].settings != state.DefaultSettings {
		t.Fatalf("trigger settings = %#v", workflows.runs[0].settings)
	}
	args, ok := workflows.runs[0].args.(map[string]any)
	triggerArg, triggerOK := args["trigger"].(state.Trigger)
	runID, runOK := args["runId"].(int64)
	if !ok || !triggerOK || !triggerArg.Enabled || !runOK || runID < 1 {
		t.Fatalf("trigger args = %#v", workflows.runs[0].args)
	}
	workflows.mu.Unlock()
	view, _ := state.ProjectEvents(wire.Events(0))
	if !view.RunStarted(triggerEvent.ID, source.ID) {
		t.Fatal("run marker missing")
	}
	if len(view.Runs) != 1 || view.Runs[0].Status != "completed" || view.Runs[0].WorkflowName != "review" {
		t.Fatalf("run history missing: %#v", view.Runs)
	}
	if runID != view.Runs[0].ID {
		t.Fatalf("workflow runId = %d, history ID = %d", runID, view.Runs[0].ID)
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

func TestLoopSkipsAWorkflowOwnTerminalEvents(t *testing.T) {
	for _, eventType := range []string{state.WorkflowRunCompleted, state.WorkflowRunFailed} {
		t.Run(eventType, func(t *testing.T) {
			wire := openWire(t)
			defer wire.Close()
			summary, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "summary"})
			other, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "other"})
			trigger, _ := wire.Publish(state.TriggerCreated, state.TriggerData{
				EventType: eventType, WorkflowID: summary.ID, Enabled: true,
			})
			wire.Publish(eventType, state.WorkflowRunData{
				TriggerID: 10, WorkflowID: summary.ID, SourceEventID: 11,
			})

			events := wire.Events(0)
			view, err := state.ProjectEvents(events)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, found := pendingTrigger(view, events); found {
				t.Fatal("workflow matched its own terminal event")
			}

			source, _ := wire.Publish(eventType, state.WorkflowRunData{
				TriggerID: 20, WorkflowID: other.ID, SourceEventID: 21,
			})
			events = wire.Events(0)
			view, err = state.ProjectEvents(events)
			if err != nil {
				t.Fatal(err)
			}
			selected, matched, found := pendingTrigger(view, events)
			if !found || selected.ID != trigger.ID || matched.ID != source.ID {
				t.Fatalf("other workflow terminal event did not match: %#v, %#v, %v", selected, matched, found)
			}
		})
	}
}

func TestLoopRecoversInterruptedRuns(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{
		Name: "review", Phases: []string{"Review"},
	})
	triggerEvent, _ := wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	source, _ := wire.Publish("release.ready", map[string]string{"version": "1.0"})
	started, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: triggerEvent.ID, WorkflowID: workflowEvent.ID,
		WorkflowName: "review", WorkflowPhases: []string{"Review"}, SourceEventID: source.ID,
	})

	loop, _ := NewLoop(wire, &fakeAgent{}, newFakeWorkflows())
	if err := loop.recoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	run, found := view.Run(started.ID)
	if !found || run.Status != "failed" || run.Error != interruptedRun {
		t.Fatalf("interrupted run was not recovered: %#v, %v", run, found)
	}
	eventCount := len(wire.Events(0))
	if err := loop.recoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	if len(wire.Events(0)) != eventCount {
		t.Fatal("interrupted run recovery was not idempotent")
	}
}

func TestLoopRunsTaskTriggersInProjectPath(t *testing.T) {
	for _, eventType := range []string{state.TaskCreated, state.TaskUpdated, state.TaskDeleted} {
		t.Run(eventType, func(t *testing.T) {
			wire := openWire(t)
			defer wire.Close()
			path := t.TempDir()
			project, _ := wire.Publish(state.ProjectCreated, state.ProjectData{Name: "Factory", Path: path})
			sourcePath := "/workflows/review.js"
			workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{
				Name: "review", Path: &sourcePath,
			})
			wire.Publish(state.TriggerCreated, state.TriggerData{
				EventType: eventType, WorkflowID: workflowEvent.ID, Enabled: true,
			})
			task, _ := wire.Publish(state.TaskCreated, state.TaskData{
				Title: "Ship it", Status: state.Backlog, ProjectID: project.ID,
			})
			switch eventType {
			case state.TaskUpdated:
				wire.Publish(eventType, state.TaskData{
					ID: task.ID, Title: "Ship it", Status: state.InReview, ProjectID: project.ID,
				})
			case state.TaskDeleted:
				wire.Publish(eventType, state.IDData{ID: task.ID})
			}

			workflows := newFakeWorkflows()
			loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
			worked, _, err := loop.step(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			waitForSignal(t, workflows.started, "workflow start")
			waitForSignal(t, workflows.finished, "workflow finish")
			waitForActiveCount(t, loop, 0)
			workflows.mu.Lock()
			defer workflows.mu.Unlock()
			if !worked || len(workflows.runs) != 1 ||
				workflows.runs[0].directory != path || workflows.runs[0].source != sourcePath {
				t.Fatalf("runs = %#v, want project path %q and source %q", workflows.runs, path, sourcePath)
			}
			if eventType == state.TaskUpdated {
				args, ok := workflows.runs[0].args.(map[string]any)
				event, eventOK := args["event"].(eventwire.Event)
				var data state.TaskData
				if !ok || !eventOK || json.Unmarshal(event.Data, &data) != nil || data.Status != state.InReview {
					t.Fatalf("task update args = %#v", workflows.runs[0].args)
				}
			}
		})
	}
}

func TestLoopRunsTriggersUpToWorkflowCapacity(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	settings := state.DefaultSettings
	settings.WorkflowCapacity = 2
	wire.Publish(state.SettingsUpdated, settings)
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	for version := 1; version <= 3; version++ {
		wire.Publish("release.ready", map[string]int{"version": version})
	}

	workflows := newFakeWorkflows()
	workflows.release = make(chan struct{})
	loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
	for range settings.WorkflowCapacity {
		worked, _, err := loop.step(context.Background())
		if err != nil || !worked {
			t.Fatalf("dispatch = %v, %v", worked, err)
		}
		waitForSignal(t, workflows.started, "workflow start")
	}
	worked, _, err := loop.step(context.Background())
	if err != nil || worked {
		t.Fatalf("capacity did not stop dispatch: %v, %v", worked, err)
	}
	select {
	case <-workflows.started:
		t.Fatal("third workflow started above capacity")
	default:
	}
	if runs, maxActive := workflows.snapshot(); runs != 2 || maxActive != 2 {
		t.Fatalf("runs = %d, max active = %d", runs, maxActive)
	}

	settings.WorkflowCapacity = 1
	wire.Publish(state.SettingsUpdated, settings)
	workflows.release <- struct{}{}
	waitForSignal(t, workflows.finished, "first workflow finish")
	waitForActiveCount(t, loop, 1)
	worked, _, err = loop.step(context.Background())
	if err != nil || worked {
		t.Fatalf("lower capacity did not hold dispatch: %v, %v", worked, err)
	}
	select {
	case <-workflows.started:
		t.Fatal("third workflow started before active count fell below the new capacity")
	default:
	}

	workflows.release <- struct{}{}
	waitForSignal(t, workflows.finished, "second workflow finish")
	waitForActiveCount(t, loop, 0)
	worked, _, err = loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("dispatch after capacity freed = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "third workflow start")
	workflows.release <- struct{}{}
	waitForSignal(t, workflows.finished, "third workflow finish")
	waitForActiveCount(t, loop, 0)
	if runs, maxActive := workflows.snapshot(); runs != 3 || maxActive != 2 {
		t.Fatalf("runs = %d, max active = %d", runs, maxActive)
	}
}

func TestLoopPausesTriggerDispatchAtZeroCapacity(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	settings := state.DefaultSettings
	settings.WorkflowCapacity = 0
	wire.Publish(state.SettingsUpdated, settings)
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	wire.Publish("release.ready", map[string]int{"version": 1})

	workflows := newFakeWorkflows()
	loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
	worked, _, err := loop.step(context.Background())
	if err != nil || worked {
		t.Fatalf("zero-capacity step = %v, %v", worked, err)
	}
	select {
	case <-workflows.started:
		t.Fatal("workflow started at zero capacity")
	default:
	}

	settings.WorkflowCapacity = 1
	wire.Publish(state.SettingsUpdated, settings)
	worked, _, err = loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("resumed dispatch = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "resumed workflow start")
	waitForSignal(t, workflows.finished, "resumed workflow finish")
	waitForActiveCount(t, loop, 0)
}

func TestLoopDiscardsDisabledEventsAndRunsNewEventAfterEnable(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	triggerEvent, _ := wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: false,
	})
	wire.Publish("release.ready", map[string]int{"version": 1})

	workflows := newFakeWorkflows()
	loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
	worked, _, err := loop.step(context.Background())
	if err != nil || worked || eventTypeCount(wire.Events(0), state.WorkflowRunStarted) != 0 {
		t.Fatalf("disabled dispatch = %v, %v, events = %#v", worked, err, wire.Events(0))
	}

	wire.Publish(state.TriggerUpdated, state.TriggerData{
		ID: triggerEvent.ID, EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	worked, _, err = loop.step(context.Background())
	if err != nil || worked {
		t.Fatalf("disabled-interval event was replayed: %v, %v", worked, err)
	}
	newSource, _ := wire.Publish("release.ready", map[string]int{"version": 2})
	worked, _, err = loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("new event was not dispatched: %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "enabled workflow start")
	waitForSignal(t, workflows.finished, "enabled workflow finish")
	waitForActiveCount(t, loop, 0)
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Runs) != 1 || view.Runs[0].SourceEventID != newSource.ID {
		t.Fatalf("runs = %#v", view.Runs)
	}
}

func TestDisabledCronIsNeitherScheduledNorDispatched(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	schedule := "* * * * *"
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	triggerEvent, _ := wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: state.CronFired, Schedule: &schedule,
		WorkflowID: workflowEvent.ID, Enabled: false,
	})
	events := wire.Events(0)
	view, err := state.ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if due, next := nextCron(view, time.Now().UTC().Add(time.Hour)); due != nil || !next.IsZero() {
		t.Fatalf("disabled cron = %#v, %v", due, next)
	}
	wire.Publish(state.CronFired, state.CronData{TriggerID: triggerEvent.ID})
	events = wire.Events(0)
	view, err = state.ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found := pendingTrigger(view, events); found {
		t.Fatal("targeted cron event matched a disabled trigger")
	}
}

func TestCronResumesAfterEnableWithoutCatchUp(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	schedule := "* * * * *"
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	triggerEvent, _ := wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: state.CronFired, Schedule: &schedule,
		WorkflowID: workflowEvent.ID, Enabled: true,
	})
	wire.Publish(state.TriggerUpdated, state.TriggerData{
		ID: triggerEvent.ID, EventType: state.CronFired, Schedule: &schedule,
		WorkflowID: workflowEvent.ID, Enabled: false,
	})
	wire.Publish(state.TriggerUpdated, state.TriggerData{
		ID: triggerEvent.ID, EventType: state.CronFired, Schedule: &schedule,
		WorkflowID: workflowEvent.ID, Enabled: true,
	})
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	reenabled, _ := view.Trigger(triggerEvent.ID)
	if due, next := nextCron(view, reenabled.UpdatedAt); due != nil || next.IsZero() || !next.After(reenabled.UpdatedAt) {
		t.Fatalf("cron catch-up = %#v, next = %v, update = %v", due, next, reenabled.UpdatedAt)
	} else if dueAtTick, _ := nextCron(view, next); dueAtTick == nil || dueAtTick.ID != triggerEvent.ID {
		t.Fatalf("first post-enable tick was not due: %#v", dueAtTick)
	}

	wire.Publish(state.CronFired, state.CronData{TriggerID: triggerEvent.ID})
	workflows := newFakeWorkflows()
	loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
	worked, _, err := loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("post-enable cron dispatch = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "cron workflow start")
	waitForSignal(t, workflows.finished, "cron workflow finish")
	waitForActiveCount(t, loop, 0)
}

func TestDisablingTriggerDoesNotCancelActiveRun(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	triggerEvent, _ := wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	wire.Publish("release.ready", map[string]int{"version": 1})

	workflows := newFakeWorkflows()
	workflows.release = make(chan struct{})
	loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
	worked, _, err := loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("initial dispatch = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "active workflow start")
	wire.Publish(state.TriggerUpdated, state.TriggerData{
		ID: triggerEvent.ID, EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: false,
	})
	workflows.release <- struct{}{}
	waitForSignal(t, workflows.finished, "active workflow finish")
	waitForActiveCount(t, loop, 0)
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Runs) != 1 || view.Runs[0].Status != "completed" {
		t.Fatalf("active run = %#v", view.Runs)
	}
	wire.Publish("release.ready", map[string]int{"version": 2})
	worked, _, err = loop.step(context.Background())
	if err != nil || worked || eventTypeCount(wire.Events(0), state.WorkflowRunStarted) != 1 {
		t.Fatalf("disabled trigger admitted later event: %v, %v", worked, err)
	}
}

func TestDisableWinsAgainstStaleTriggerAdmission(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	triggerEvent, _ := wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	wire.Publish("release.ready", map[string]int{"version": 1})
	events := wire.Events(0)
	view, err := state.ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	trigger, source, found := pendingTrigger(view, events)
	if !found {
		t.Fatal("trigger was not selected")
	}
	snapshotID := events[len(events)-1].ID
	wire.Publish(state.TriggerUpdated, state.TriggerData{
		ID: triggerEvent.ID, EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: false,
	})

	workflows := newFakeWorkflows()
	loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
	published, err := loop.startTrigger(context.Background(), view, trigger, source, snapshotID)
	if err != nil || published || loop.active != 0 ||
		eventTypeCount(wire.Events(0), state.WorkflowRunStarted) != 0 {
		t.Fatalf("stale admission = %v, %v, events = %#v", published, err, wire.Events(0))
	}
	select {
	case <-workflows.started:
		t.Fatal("stale workflow process started")
	default:
	}
}

func TestLoopRunCancelsAndWaitsForActiveWorkflows(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	settings := state.DefaultSettings
	settings.WorkflowCapacity = 2
	wire.Publish(state.SettingsUpdated, settings)
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	for version := 1; version <= 3; version++ {
		wire.Publish("release.ready", map[string]int{"version": version})
	}

	workflows := newFakeWorkflows()
	workflows.release = make(chan struct{})
	loop, _ := NewLoop(wire, &fakeAgent{}, workflows)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- loop.Run(ctx) }()
	waitForSignal(t, workflows.started, "first workflow start")
	waitForSignal(t, workflows.started, "second workflow start")
	cancel()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("loop stop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not wait for active workflows to stop")
	}
	waitForSignal(t, workflows.finished, "first canceled workflow")
	waitForSignal(t, workflows.finished, "second canceled workflow")
	if runs, _ := workflows.snapshot(); runs != 2 {
		t.Fatalf("runs = %d, want two active workflows only", runs)
	}
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Runs) != 2 {
		t.Fatalf("run history = %#v", view.Runs)
	}
	for _, run := range view.Runs {
		if run.Status != "failed" || !strings.Contains(run.Error, "workflow canceled before completion") {
			t.Fatalf("canceled workflow remained active: %#v", run)
		}
	}
}

func waitForSignal(t *testing.T, channel <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForActiveCount(t *testing.T, loop *Loop, want int) {
	t.Helper()
	for loop.active > want {
		select {
		case err := <-loop.completed:
			loop.active--
			if err != nil {
				t.Fatalf("workflow completion error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %d active workflows", want)
		}
	}
	if loop.active != want {
		t.Fatalf("active workflows = %d, want %d", loop.active, want)
	}
}

func eventTypeCount(events []eventwire.Event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func openWire(t *testing.T) *eventwire.Wire {
	t.Helper()
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return wire
}
