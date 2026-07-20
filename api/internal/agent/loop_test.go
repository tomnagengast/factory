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
	"github.com/tomnagengast/factory/api/internal/quiescence"
	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/store"
	"github.com/tomnagengast/factory/api/internal/workflow"
)

type fakeAgent struct {
	prompts  []string
	settings []state.Settings
	output   string
	steps    []AgentStep
	err      error
	streamed chan struct{}
	release  chan struct{}
}

func (f *fakeAgent) Run(
	_ context.Context,
	settings state.Settings,
	prompt string,
	emit func(AgentStep) error,
) (string, error) {
	f.settings = append(f.settings, settings)
	f.prompts = append(f.prompts, prompt)
	for _, step := range f.steps {
		if err := emit(step); err != nil {
			return "", err
		}
	}
	if f.streamed != nil {
		f.streamed <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	return f.output, f.err
}

type fakeWorkflows struct {
	definitions []workflow.Definition
	listErr     error
	validateErr error
	validations []string
	mu          sync.Mutex
	runs        []struct {
		directory, source string
		settings          state.Settings
		args              any
		resume            []json.RawMessage
	}
	runEvents         [][]string
	runErrors         []error
	outputs           []string
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

func newTestLoop(
	wire *testStore,
	runner Runner,
	workflows workflow.Runner,
) (*Loop, error) {
	return NewLoop(wire.Store, runner, workflows, quiescence.New())
}

func (f *fakeWorkflows) List(context.Context) ([]workflow.Definition, error) {
	return f.definitions, f.listErr
}

func (f *fakeWorkflows) Validate(_ context.Context, source string) error {
	f.validations = append(f.validations, source)
	return f.validateErr
}

func (f *fakeWorkflows) Run(
	ctx context.Context,
	request workflow.RunRequest,
	emit func(workflow.Event) error,
) (string, error) {
	f.mu.Lock()
	runIndex := len(f.runs)
	f.runs = append(f.runs, struct {
		directory, source string
		settings          state.Settings
		args              any
		resume            []json.RawMessage
	}{
		request.Directory, request.Source, request.Settings, request.Arguments,
		request.Resume,
	})
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
		if runIndex < len(f.runEvents) {
			events = f.runEvents[runIndex]
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
	if runIndex < len(f.runErrors) && f.runErrors[runIndex] != nil {
		return "", f.runErrors[runIndex]
	}
	if runIndex < len(f.outputs) {
		return f.outputs[runIndex], nil
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
	runner := &fakeAgent{
		output: "Created the review panel.",
		steps: []AgentStep{
			{Kind: "reasoning", Label: "codex", Content: "Planning the workflow."},
			{Kind: "tool-use", Label: "command", Content: "workflow validate /workflows/workflow-1.js"},
			{Kind: "tool-output", Label: "command", Content: "valid"},
		},
	}
	workflows := newFakeWorkflows()
	loop, err := newTestLoop(wire, runner, workflows)
	if err != nil {
		t.Fatal(err)
	}
	worked, _, err := loop.step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := finishAuthoring(t, loop); err != nil {
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
	if len(comments) != 5 || comments[1].Kind != "reasoning" || comments[1].Final ||
		comments[2].Kind != "tool-use" || comments[3].Kind != "tool-output" ||
		comments[4].Author != "agent" || comments[4].Kind != "message" || !comments[4].Final {
		t.Fatalf("agent reply missing: %#v", comments)
	}
	authored, _ := view.Workflow(created.ID)
	if authored.Path == nil || *authored.Path != workflows.LocalPath(created.ID) {
		t.Fatalf("workflow path = %v, want live authoring target", authored.Path)
	}
	if _, err := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: created.ID, Author: "user", Content: "Revise the panel",
	}); err != nil {
		t.Fatal(err)
	}
	runner.steps = nil
	runner.output = "Revised the review panel."
	worked, _, err = loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("second authoring step = %v, %v", worked, err)
	}
	if err := finishAuthoring(t, loop); err != nil || len(runner.prompts) != 2 {
		t.Fatalf("second authoring step = %v, %v, prompts = %#v", worked, err, runner.prompts)
	}
	secondPrompt := runner.prompts[1]
	for _, expected := range []string{
		"user: Build a review panel", "agent: Created the review panel.", "user: Revise the panel",
	} {
		if !strings.Contains(secondPrompt, expected) {
			t.Fatalf("%q missing from second prompt: %s", expected, secondPrompt)
		}
	}
	for _, excluded := range []string{"agent: Planning the workflow.", "agent: workflow validate /workflows/workflow-1.js", "agent: valid"} {
		if strings.Contains(secondPrompt, excluded) {
			t.Fatalf("progress %q entered second prompt: %s", excluded, secondPrompt)
		}
	}
	if worked, _, err = loop.step(context.Background()); err != nil || worked {
		t.Fatalf("answered request was selected again: %v, %v", worked, err)
	}
}

func TestLoopPersistsProgressBeforeAuthoringCompletes(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	created, _ := wire.Publish(state.WorkflowCreated, state.WorkflowData{Name: "Draft"})
	user, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: created.ID, Author: "user", Content: "Build it",
	})
	runner := &fakeAgent{
		steps: []AgentStep{
			{Kind: "reasoning", Label: "codex", Content: "Inspecting the request."},
			{Kind: "tool-use", Label: "command", Content: "workflow validate review.js"},
			{Kind: "tool-output", Label: "command", Content: "valid\n"},
			{Kind: "message", Content: "The file is ready for validation."},
		},
		output: "Created the workflow.", streamed: make(chan struct{}, 1), release: make(chan struct{}),
	}
	workflows := newFakeWorkflows()
	workflows.definitions = []workflow.Definition{
		{Name: "review", Path: workflows.LocalPath(created.ID), Description: "Review work"},
	}
	loop, _ := newTestLoop(wire, runner, workflows)
	if worked, _, err := loop.step(context.Background()); err != nil || !worked {
		t.Fatalf("authoring step = %v, %v", worked, err)
	}
	waitForSignal(t, runner.streamed, "agent progress")

	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	comments := view.CommentsFor("workflow", created.ID)
	if len(comments) != 5 {
		t.Fatalf("live comments = %#v", comments)
	}
	for _, comment := range comments[1:] {
		if comment.ParentCommentID == nil || *comment.ParentCommentID != user.ID || comment.Final {
			t.Fatalf("live progress comment = %#v", comment)
		}
	}
	pending, found := view.PendingWorkflowComment()
	if !found || pending.ID != user.ID {
		t.Fatalf("pending authoring request = %#v, %v", pending, found)
	}
	if len(workflows.validations) != 0 || eventTypeCount(wire.Events(0), authoringCompleted) != 0 {
		t.Fatal("authoring completed before the agent process exited")
	}

	runner.release <- struct{}{}
	if err := finishAuthoring(t, loop); err != nil {
		t.Fatal(err)
	}
	view, err = state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	comments = view.CommentsFor("workflow", created.ID)
	if len(comments) != 6 || comments[5].Content != "Created the workflow." ||
		comments[5].Kind != "message" || !comments[5].Final {
		t.Fatalf("completed comments = %#v", comments)
	}
	if _, found := view.PendingWorkflowComment(); found {
		t.Fatal("final response did not answer the request")
	}
	events := wire.Events(0)
	completedIndex, finalIndex, discoveryIndex := -1, -1, -1
	for index, event := range events {
		switch event.Type {
		case state.WorkflowUpdated:
			if index > 2 {
				discoveryIndex = index
			}
		case authoringCompleted:
			completedIndex = index
		case state.CommentCreated:
			if event.ID == comments[5].ID {
				finalIndex = index
			}
		}
	}
	if len(workflows.validations) != 1 || discoveryIndex < 0 ||
		!(discoveryIndex < completedIndex && completedIndex < finalIndex) {
		t.Fatalf("terminal order: discovery=%d completed=%d final=%d events=%#v", discoveryIndex, completedIndex, finalIndex, events)
	}
}

func TestWorkflowAuthoringDoesNotBlockTriggeredRuns(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	draft, _ := wire.Publish(state.WorkflowCreated, state.WorkflowData{Name: "Draft"})
	wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: draft.ID, Author: "user", Content: "Build it",
	})
	sourcePath := "/workflows/review.js"
	review, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review", Path: &sourcePath})
	wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: review.ID, Enabled: true,
	})
	wire.Publish("release.ready", map[string]int{"version": 1})
	runner := &fakeAgent{
		output: "Created.", streamed: make(chan struct{}, 1), release: make(chan struct{}),
	}
	workflows := newFakeWorkflows()
	workflows.release = make(chan struct{})
	loop, _ := newTestLoop(wire, runner, workflows)
	if worked, _, err := loop.step(context.Background()); err != nil || !worked {
		t.Fatalf("authoring dispatch = %v, %v", worked, err)
	}
	waitForSignal(t, runner.streamed, "workflow authoring start")
	if worked, _, err := loop.step(context.Background()); err != nil || !worked {
		t.Fatalf("trigger dispatch during authoring = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "triggered workflow start")
	workflows.release <- struct{}{}
	runner.release <- struct{}{}
	waitForSignal(t, workflows.finished, "triggered workflow finish")
	if err := finishAuthoring(t, loop); err != nil {
		t.Fatal(err)
	}
	waitForActiveCount(t, loop, 0)
}

func TestAuthorPromptExcludesProgressComments(t *testing.T) {
	final := true
	intermediate := false
	parent := int64(2)
	comments := []state.Comment{
		{Record: state.Record{ID: 2}, Author: "user", Kind: "message", Content: "Build it"},
		{Record: state.Record{ID: 3}, Author: "agent", Kind: "reasoning", Final: false, Content: "Private progress"},
		{Record: state.Record{ID: 4}, Author: "agent", Kind: "tool-output", Final: intermediate, Content: "Noisy output"},
		{Record: state.Record{ID: 5}, Author: "agent", Kind: "message", Final: final, Content: "Built it", ParentCommentID: &parent},
		{Record: state.Record{ID: 6}, Author: "user", Kind: "message", Content: "Revise it"},
	}
	prompt := authorPrompt(state.Workflow{Name: "Draft"}, comments, "/workflows/review.js")
	for _, expected := range []string{"user: Build it", "agent: Built it", "user: Revise it"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("%q missing from prompt: %s", expected, prompt)
		}
	}
	for _, excluded := range []string{"Private progress", "Noisy output"} {
		if strings.Contains(prompt, excluded) {
			t.Fatalf("progress %q entered prompt: %s", excluded, prompt)
		}
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
	loop, err := newTestLoop(wire, &fakeAgent{output: "Updated and validated."}, workflows)
	if err != nil {
		t.Fatal(err)
	}
	worked, _, err := loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("authoring step = %v, %v", worked, err)
	}
	if err := finishAuthoring(t, loop); err != nil {
		t.Fatal(err)
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
		comments[1].Kind != "error" || !comments[1].Final ||
		!strings.Contains(comments[1].Content, "parse error near mktemp") {
		t.Fatalf("authoring failure comment = %#v", comments)
	}
}

func TestLoopRecordsRunnerAndDiscoveryFailuresAfterProgress(t *testing.T) {
	for _, test := range []struct {
		name      string
		runnerErr error
		listErr   error
	}{
		{name: "runner", runnerErr: errors.New("malformed agent stream")},
		{name: "discovery", listErr: errors.New("workflow list failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := openWire(t)
			defer wire.Close()
			created, _ := wire.Publish(state.WorkflowCreated, state.WorkflowData{Name: "Draft"})
			wire.Publish(state.CommentCreated, state.CommentData{
				RelationType: "workflow", RelationID: created.ID, Author: "user", Content: "Build it",
			})
			runner := &fakeAgent{
				steps:  []AgentStep{{Kind: "reasoning", Content: "Started work."}},
				output: "Partial response.", err: test.runnerErr,
			}
			workflows := newFakeWorkflows()
			workflows.listErr = test.listErr
			loop, _ := newTestLoop(wire, runner, workflows)
			worked, _, err := loop.step(context.Background())
			if err != nil || !worked {
				t.Fatalf("authoring step = %v, %v", worked, err)
			}
			if err := finishAuthoring(t, loop); err != nil {
				t.Fatal(err)
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
			if len(comments) != 3 || comments[1].Kind != "reasoning" || comments[1].Final ||
				comments[2].Kind != "error" || !comments[2].Final ||
				!strings.Contains(comments[2].Content, firstError(test.runnerErr, test.listErr).Error()) {
				t.Fatalf("failure comments = %#v", comments)
			}
		})
	}
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
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
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
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
	var args struct {
		Trigger state.Trigger `json:"trigger"`
		RunID   int64         `json:"runId"`
	}
	rawArgs, ok := workflows.runs[0].args.(json.RawMessage)
	if !ok || json.Unmarshal(rawArgs, &args) != nil ||
		!args.Trigger.Enabled || args.RunID < 1 {
		t.Fatalf("trigger args = %#v", workflows.runs[0].args)
	}
	runID := args.RunID
	workflows.mu.Unlock()
	view, _ := state.ProjectEvents(wire.Events(0))
	if !view.RunStarted(triggerEvent.ID, source.ID) {
		t.Fatal("run marker missing")
	}
	if len(view.Runs) != 1 || view.Runs[0].Status != "completed" ||
		view.Runs[0].WorkflowName != "review" || view.Runs[0].TaskID != 0 {
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

func TestReactionTriggerHasNoTaskContext(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	task, _ := wire.Publish(state.TaskCreated, state.TaskData{
		Title: "React to this", Status: state.Todo, ProjectID: 99,
	})
	sourcePath := "/workflows/reactions.js"
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{
		Name: "reactions", Path: &sourcePath,
	})
	wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: state.ReactionUpdated, WorkflowID: workflowEvent.ID, Enabled: true,
	})
	wire.Publish(state.ReactionUpdated, state.ReactionUpdatedData{
		TargetType: "task", TargetID: task.ID, Emoji: "👍", Active: true,
	})

	workflows := newFakeWorkflows()
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
	worked, _, err := loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("reaction dispatch = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "reaction workflow start")
	waitForSignal(t, workflows.finished, "reaction workflow finish")
	waitForActiveCount(t, loop, 0)
	workflows.mu.Lock()
	if len(workflows.runs) != 1 || workflows.runs[0].directory != "" ||
		workflows.runs[0].source != sourcePath {
		workflows.mu.Unlock()
		t.Fatalf("reaction workflow runs = %#v", workflows.runs)
	}
	workflows.mu.Unlock()
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Runs) != 1 || view.Runs[0].TaskID != 0 {
		t.Fatalf("reaction-triggered run = %#v", view.Runs)
	}
}

func TestLoopWaitsForHumanTaskCommentAndResumesTheSameRun(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	projectPath := t.TempDir()
	project, _ := wire.Publish(state.ProjectCreated, state.ProjectData{
		Name: "Factory", Path: projectPath,
	})
	sourcePath := "/workflows/review.js"
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{
		Name: "review", Path: &sourcePath, Phases: []string{"Review"},
	})
	wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: state.TaskCreated, WorkflowID: workflowEvent.ID, Enabled: true,
	})
	task, _ := wire.Publish(state.TaskCreated, state.TaskData{
		Title: "Ship it", Status: state.InReview, ProjectID: project.ID,
	})

	workflows := newFakeWorkflows()
	workflows.runEvents = [][]string{
		{
			`{"sequence":1,"at":"2026-07-19T12:00:00Z","type":"runtime.started","workflow":"review","backend":"codex"}`,
			`{"sequence":2,"at":"2026-07-19T12:00:01Z","type":"phase.started","workflow":"review","phase":"Review"}`,
			`{"sequence":3,"at":"2026-07-19T12:00:02Z","type":"step.started","workflow":"review","phase":"Review","stepId":1,"key":"human-key","agentId":"gate","backend":"human","kind":"gate","message":"Should this ship?"}`,
			`{"sequence":4,"at":"2026-07-19T12:00:03Z","type":"runtime.suspended","workflow":"review","phase":"Review","stepId":1,"key":"human-key","agentId":"gate","backend":"human","kind":"gate","message":"Should this ship?"}`,
		},
		{
			`{"sequence":6,"at":"2026-07-19T12:01:00Z","type":"runtime.resumed","workflow":"review","backend":"codex"}`,
			`{"sequence":7,"at":"2026-07-19T12:01:01Z","type":"step.cached","workflow":"review","phase":"Review","stepId":1,"key":"human-key","agentId":"gate","backend":"human","kind":"gate","result":"Yes, ship it."}`,
			`{"sequence":8,"at":"2026-07-19T12:01:02Z","type":"runtime.completed","workflow":"review","result":"complete"}`,
		},
	}
	workflows.runErrors = []error{workflow.ErrHumanReview, nil}
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)

	worked, _, err := loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("initial dispatch = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "waiting workflow start")
	waitForSignal(t, workflows.finished, "waiting workflow finish")
	waitForActiveCount(t, loop, 0)

	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Runs) != 1 || view.Runs[0].Status != "waiting" ||
		view.Runs[0].TaskID != task.ID || view.Runs[0].WaitingGate == nil {
		t.Fatalf("waiting run = %#v", view.Runs)
	}
	comments := view.CommentsFor("task", task.ID)
	if len(comments) != 1 || comments[0].Author != "agent" ||
		comments[0].Content != "Should this ship?" {
		t.Fatalf("gate comment = %#v", comments)
	}
	response, err := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: task.ID, Author: "user", Content: "Yes, ship it.",
	})
	if err != nil {
		t.Fatal(err)
	}

	worked, _, err = loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("resume dispatch = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "resumed workflow start")
	waitForSignal(t, workflows.finished, "resumed workflow finish")
	waitForActiveCount(t, loop, 0)

	view, err = state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	run := view.Runs[0]
	if run.Status != "completed" || run.ID < 1 || run.ResponseCommentID != response.ID {
		t.Fatalf("completed resumed run = %#v", run)
	}
	workflows.mu.Lock()
	defer workflows.mu.Unlock()
	if len(workflows.runs) != 2 || len(workflows.runs[1].resume) != 5 ||
		workflows.runs[1].directory != projectPath ||
		workflows.runs[1].source != sourcePath ||
		string(workflows.runs[1].resume[4]) == "" {
		t.Fatalf("resumed workflow request = %#v", workflows.runs)
	}
	var human workflow.Event
	if err := json.Unmarshal(workflows.runs[1].resume[4], &human); err != nil ||
		human.Type != "step.completed" || human.Sequence != 5 ||
		string(human.Result) != `"Yes, ship it."` {
		t.Fatalf("human journal result = %#v, %v", human, err)
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

			if _, _, _, found, err := wire.PendingTrigger(); err != nil || found {
				t.Fatal("workflow matched its own terminal event")
			}

			source, _ := wire.Publish(eventType, state.WorkflowRunData{
				TriggerID: 20, WorkflowID: other.ID, SourceEventID: 21,
			})
			selected, matched, _, found, err := wire.PendingTrigger()
			if err != nil || !found || selected.ID != trigger.ID || matched.ID != source.ID {
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

	loop, _ := newTestLoop(wire, &fakeAgent{}, newFakeWorkflows())
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

func TestLoopPreservesWaitingRunsAcrossRestart(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	started, _ := wire.Publish(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 2, WorkflowID: 1, WorkflowName: "review",
		SourceEventID: 3, TaskID: 3,
	})
	comment, _ := wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: 3, Author: "agent", Content: "Review it?",
	})
	wire.Publish(state.WorkflowRunWaiting, state.WorkflowRunStateData{
		RunID: started.ID, GateCommentID: comment.ID,
		Gate: &state.WorkflowGate{
			Workflow: "review", StepID: 1, Key: "human-key", Message: "Review it?",
		},
	})

	loop, _ := newTestLoop(wire, &fakeAgent{}, newFakeWorkflows())
	if err := loop.recoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	view, err := state.ProjectEvents(wire.Events(0))
	if err != nil {
		t.Fatal(err)
	}
	run, found := view.Run(started.ID)
	if !found || run.Status != "waiting" ||
		eventTypeCount(wire.Events(0), state.WorkflowRunFailed) != 0 {
		t.Fatalf("waiting run after recovery = %#v, %v", run, found)
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
			loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
			worked, _, err := loop.step(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			waitForSignal(t, workflows.started, "workflow start")
			waitForSignal(t, workflows.finished, "workflow finish")
			waitForActiveCount(t, loop, 0)
			workflows.mu.Lock()
			if !worked || len(workflows.runs) != 1 ||
				workflows.runs[0].directory != path || workflows.runs[0].source != sourcePath {
				workflows.mu.Unlock()
				t.Fatalf("runs = %#v, want project path %q and source %q", workflows.runs, path, sourcePath)
			}
			if eventType == state.TaskUpdated {
				var args struct {
					Event eventwire.Event `json:"event"`
				}
				rawArgs, ok := workflows.runs[0].args.(json.RawMessage)
				var data state.TaskData
				if !ok || json.Unmarshal(rawArgs, &args) != nil ||
					json.Unmarshal(args.Event.Data, &data) != nil || data.Status != state.InReview {
					workflows.mu.Unlock()
					t.Fatalf("task update args = %#v", workflows.runs[0].args)
				}
			}
			workflows.mu.Unlock()
			view, err := state.ProjectEvents(wire.Events(0))
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Runs) != 1 || view.Runs[0].TaskID != task.ID {
				t.Fatalf("task-triggered run = %#v, want task ID %d", view.Runs, task.ID)
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
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
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

func TestQuiescenceDrainsActiveTriggerAndBlocksNewAdmission(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	wire.Publish("release.ready", map[string]int{"version": 1})
	wire.Publish("release.ready", map[string]int{"version": 2})

	workflows := newFakeWorkflows()
	workflows.release = make(chan struct{})
	admission := quiescence.New()
	loop, _ := NewLoop(wire.Store, &fakeAgent{}, workflows, admission)
	worked, _, err := loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("initial dispatch = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "first workflow start")

	acquired := make(chan quiescence.Lease, 1)
	go func() {
		lease, acquireErr := admission.Acquire(context.Background(), time.Second)
		if acquireErr != nil {
			t.Errorf("acquire quiescence: %v", acquireErr)
			return
		}
		acquired <- lease
	}()
	waitForCondition(t, func() bool { return !admission.Accepting() }, "trigger admission to stop")
	if worked, _, err = loop.step(context.Background()); err != nil || worked {
		t.Fatalf("dispatch while draining = %v, %v", worked, err)
	}
	select {
	case <-workflows.started:
		t.Fatal("second workflow started while quiescing")
	case <-acquired:
		t.Fatal("quiescence completed before the active workflow drained")
	default:
	}

	workflows.release <- struct{}{}
	waitForSignal(t, workflows.finished, "first workflow finish")
	lease := receiveValue(t, acquired, "quiescence lease")
	if admission.Active() != 0 {
		t.Fatalf("active admissions = %d", admission.Active())
	}
	if worked, _, err = loop.step(context.Background()); err != nil || worked {
		t.Fatalf("dispatch while quiescent = %v, %v", worked, err)
	}
	if !admission.Release(lease.Token) {
		t.Fatal("quiescence lease did not release")
	}
	worked, _, err = loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("dispatch after release = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "second workflow start")
	workflows.release <- struct{}{}
	waitForSignal(t, workflows.finished, "second workflow finish")
	waitForActiveCount(t, loop, 0)
	if runs, _ := workflows.snapshot(); runs != 2 {
		t.Fatalf("runs = %d, want 2", runs)
	}
}

func TestQuiescenceDrainsWorkflowAuthoring(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	created, _ := wire.Publish(state.WorkflowCreated, state.WorkflowData{Name: "Draft"})
	wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: created.ID, Author: "user", Content: "Build it",
	})
	runner := &fakeAgent{
		output: "Created.", streamed: make(chan struct{}, 1), release: make(chan struct{}),
	}
	admission := quiescence.New()
	loop, _ := NewLoop(wire.Store, runner, newFakeWorkflows(), admission)
	if worked, _, stepErr := loop.step(context.Background()); stepErr != nil || !worked {
		t.Fatalf("authoring step = %v, %v", worked, stepErr)
	}
	waitForSignal(t, runner.streamed, "workflow authoring start")

	acquired := make(chan quiescence.Lease, 1)
	go func() {
		lease, acquireErr := admission.Acquire(context.Background(), time.Second)
		if acquireErr != nil {
			t.Errorf("acquire quiescence: %v", acquireErr)
			return
		}
		acquired <- lease
	}()
	waitForCondition(t, func() bool { return !admission.Accepting() }, "authoring admission to stop")
	select {
	case <-acquired:
		t.Fatal("quiescence completed before authoring drained")
	default:
	}

	runner.release <- struct{}{}
	if stepErr := finishAuthoring(t, loop); stepErr != nil {
		t.Fatal(stepErr)
	}
	lease := receiveValue(t, acquired, "quiescence lease")
	if eventTypeCount(wire.Events(0), authoringCompleted) != 1 {
		t.Fatal("quiescence completed before the authoring terminal event")
	}
	if !admission.Release(lease.Token) {
		t.Fatal("quiescence lease did not release")
	}
}

func TestQuiescenceReleaseWakesPendingDispatchWithoutAnotherWireEvent(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	sourcePath := "/workflows/review.js"
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{
		Name: "review", Path: &sourcePath,
	})
	wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	wire.Publish("release.ready", map[string]int{"version": 1})

	workflows := newFakeWorkflows()
	admission := quiescence.New()
	loop, _ := NewLoop(wire.Store, &fakeAgent{}, workflows, admission)
	lease, err := admission.Acquire(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	after := wire.LastID()
	admissionChanged := admission.Changes()
	worked, nextCron, err := loop.step(context.Background())
	if err != nil || worked || !nextCron.IsZero() {
		t.Fatalf("blocked step = %v, %v, next cron %v", worked, err, nextCron)
	}
	if !admission.Release(lease.Token) {
		t.Fatal("quiescence lease did not release")
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := loop.wait(waitContext, after, nextCron, admissionChanged); err != nil {
		t.Fatalf("wait after release = %v", err)
	}
	if wire.LastID() != after {
		t.Fatal("release wake required an unrelated wire event")
	}

	worked, _, err = loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("dispatch after release = %v, %v", worked, err)
	}
	waitForSignal(t, workflows.started, "released workflow start")
	waitForSignal(t, workflows.finished, "released workflow finish")
	waitForActiveCount(t, loop, 0)
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
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
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
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
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
	cronStates, err := wire.CronStates()
	if err != nil {
		t.Fatal(err)
	}
	if due, next := nextCron(cronStates, time.Now().UTC().Add(time.Hour)); due != nil || !next.IsZero() {
		t.Fatalf("disabled cron = %#v, %v", due, next)
	}
	wire.Publish(state.CronFired, state.CronData{TriggerID: triggerEvent.ID})
	if _, _, _, found, err := wire.PendingTrigger(); err != nil || found {
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
	reenabled, found, err := wire.Trigger(triggerEvent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("reenabled trigger not found")
	}
	cronStates, err := wire.CronStates()
	if err != nil {
		t.Fatal(err)
	}
	if due, next := nextCron(cronStates, reenabled.UpdatedAt); due != nil || next.IsZero() || !next.After(reenabled.UpdatedAt) {
		t.Fatalf("cron catch-up = %#v, next = %v, update = %v", due, next, reenabled.UpdatedAt)
	} else if dueAtTick, _ := nextCron(cronStates, next); dueAtTick == nil || dueAtTick.ID != triggerEvent.ID {
		t.Fatalf("first post-enable tick was not due: %#v", dueAtTick)
	}

	wire.Publish(state.CronFired, state.CronData{TriggerID: triggerEvent.ID})
	workflows := newFakeWorkflows()
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
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
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
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
	trigger, source, snapshotID, found, err := wire.PendingTrigger()
	if err != nil || !found {
		t.Fatal("trigger was not selected")
	}
	wire.Publish(state.TriggerUpdated, state.TriggerData{
		ID: triggerEvent.ID, EventType: "release.ready", WorkflowID: workflowEvent.ID, Enabled: false,
	})

	workflows := newFakeWorkflows()
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
	settings, _ := wire.Settings()
	published, err := loop.startTrigger(context.Background(), settings, trigger, source, snapshotID)
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
	loop, _ := newTestLoop(wire, &fakeAgent{}, workflows)
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

func waitForCondition(t *testing.T, check func() bool, label string) {
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

func receiveValue[T any](t *testing.T, channel <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		var zero T
		return zero
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

func finishAuthoring(t *testing.T, loop *Loop) error {
	t.Helper()
	select {
	case err := <-loop.authored:
		loop.authoring = false
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for workflow authoring")
		return nil
	}
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

func openWire(t *testing.T) *testStore {
	t.Helper()
	eventStore, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	return &testStore{Store: eventStore, t: t}
}
