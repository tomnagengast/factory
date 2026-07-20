package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/quiescence"
	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/workflow"
)

const (
	authoringStarted   = "workflow.authoring.started"
	authoringCompleted = "workflow.authoring.completed"
	authoringFailed    = "workflow.authoring.failed"
	interruptedRun     = "workflow interrupted by Factory restart before a terminal event was recorded"
)

type Loop struct {
	wire      *eventwire.Wire
	agent     Runner
	workflows workflow.Runner
	admission *quiescence.Controller
	active    int
	completed chan error
}

func NewLoop(
	wire *eventwire.Wire,
	runner Runner,
	workflows workflow.Runner,
	admission *quiescence.Controller,
) (*Loop, error) {
	if wire == nil || runner == nil || workflows == nil || admission == nil {
		return nil, errors.New("workflow coordinator requires a wire, agent, workflow CLI, and admission controller")
	}
	return &Loop{
		wire: wire, agent: runner, workflows: workflows, admission: admission,
		completed: make(chan error, state.MaxWorkflowCapacity),
	}, nil
}

// Run owns Factory's coordinator. Workflow conversations remain sequential,
// while event and cron trigger runs are dispatched up to the selected capacity.
func (l *Loop) Run(ctx context.Context) error {
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := l.syncWorkflows(runContext); err != nil {
		return err
	}
	if err := l.recoverInterruptedRuns(); err != nil {
		return err
	}
	for {
		after := l.wire.LastID()
		admissionChanged := l.admission.Changes()
		worked, nextCron, err := l.step(runContext)
		if err != nil {
			cancel()
			return l.stop(err)
		}
		if worked {
			continue
		}
		if err := l.wait(runContext, after, nextCron, admissionChanged); err != nil {
			cancel()
			return l.stop(err)
		}
	}
}

func (l *Loop) recoverInterruptedRuns() error {
	view, err := state.ProjectEvents(l.wire.Events(0))
	if err != nil {
		return err
	}
	for _, run := range view.Runs {
		if run.Status != "running" {
			continue
		}
		if _, err := l.wire.Publish(state.WorkflowRunFailed, state.WorkflowRunData{
			TriggerID: run.TriggerID, WorkflowID: run.WorkflowID,
			WorkflowName: run.WorkflowName, WorkflowPhases: slices.Clone(run.WorkflowPhases),
			SourceEventID: run.SourceEventID, Output: run.Output, Error: interruptedRun,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) step(ctx context.Context) (bool, time.Time, error) {
	if err := l.collectCompleted(); err != nil {
		return false, time.Time{}, err
	}
	events := l.wire.Events(0)
	snapshotID := int64(0)
	if len(events) > 0 {
		snapshotID = events[len(events)-1].ID
	}
	view, err := state.ProjectEvents(events)
	if err != nil {
		return false, time.Time{}, err
	}
	if comment, found := view.PendingWorkflowComment(); found {
		if !l.admission.TryStart() {
			return false, time.Time{}, nil
		}
		authorErr := l.authorWorkflow(ctx, view, comment)
		l.admission.Done(authorErr)
		return true, time.Time{}, authorErr
	}
	if l.active < view.Settings.WorkflowCapacity {
		if run, response, found := view.PendingHumanResponse(); found {
			started, err := l.startResume(ctx, view, run, response, snapshotID)
			return started, time.Time{}, err
		}
		if trigger, source, found := pendingTrigger(view, events); found {
			started, err := l.startTrigger(ctx, view, trigger, source, snapshotID)
			return started, time.Time{}, err
		}
		if !l.admission.Accepting() {
			return false, time.Time{}, nil
		}
		due, next := nextCron(view, time.Now().UTC())
		if due != nil {
			_, _, err := l.wire.PublishIfCurrent(
				snapshotID, state.CronFired, state.CronData{TriggerID: due.ID},
			)
			return true, time.Time{}, err
		}
		return false, next, nil
	}
	return false, time.Time{}, nil
}

func (l *Loop) wait(
	ctx context.Context,
	after int64,
	nextCron time.Time,
	admissionChanged <-chan struct{},
) error {
	var waitContext context.Context
	var cancel context.CancelFunc
	if !nextCron.IsZero() {
		waitContext, cancel = context.WithDeadline(ctx, nextCron)
	} else {
		waitContext, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	wireResult := make(chan error, 1)
	go func() {
		_, err := l.wire.Wait(waitContext, after)
		wireResult <- err
	}()
	select {
	case err := <-l.completed:
		l.active--
		cancel()
		<-wireResult
		return err
	case err := <-wireResult:
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	case <-admissionChanged:
		cancel()
		<-wireResult
		return nil
	}
}

func (l *Loop) collectCompleted() error {
	for {
		select {
		case err := <-l.completed:
			l.active--
			if err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (l *Loop) stop(cause error) error {
	var executionErr error
	for l.active > 0 {
		err := <-l.completed
		l.active--
		if err != nil && !errors.Is(err, context.Canceled) && executionErr == nil {
			executionErr = err
		}
	}
	if executionErr != nil && errors.Is(cause, context.Canceled) {
		return executionErr
	}
	return cause
}

func (l *Loop) authorWorkflow(ctx context.Context, view state.Snapshot, comment state.Comment) error {
	selected, found := view.Workflow(comment.RelationID)
	if !found {
		return nil
	}
	target := l.workflows.LocalPath(selected.ID)
	if filepath.Clean(stringValue(selected.Path)) != filepath.Clean(target) {
		if _, err := l.wire.Publish(state.WorkflowUpdated, state.WorkflowData{
			ID: selected.ID, Name: selected.Name, Description: selected.Description,
			Path: &target, Scope: selected.Scope, Phases: slices.Clone(selected.Phases),
			Mutating: selected.Mutating,
		}); err != nil {
			return err
		}
	}
	if _, err := l.wire.Publish(authoringStarted, map[string]int64{
		"workflowId": selected.ID, "commentId": comment.ID,
	}); err != nil {
		return err
	}
	output, runErr := l.agent.Run(
		ctx,
		view.Settings,
		authorPrompt(selected, view.CommentsFor("workflow", selected.ID), target),
		func(step AgentStep) error {
			final := false
			_, err := l.wire.Publish(state.CommentCreated, state.CommentData{
				RelationType: "workflow", RelationID: selected.ID, ParentCommentID: &comment.ID,
				Author: "agent", Kind: step.Kind, Label: step.Label, Final: &final,
				Content: step.Content,
			})
			return err
		},
	)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr == nil {
		runErr = l.workflows.Validate(ctx, target)
	}
	if runErr == nil {
		runErr = l.syncWorkflows(ctx)
	}
	response := strings.TrimSpace(output)
	if response == "" {
		response = "Workflow updated."
	}
	eventType := authoringCompleted
	kind := "message"
	if runErr != nil {
		eventType = authoringFailed
		kind = "error"
		response = strings.TrimSpace(response + "\n\nError: " + runErr.Error())
	}
	if _, err := l.wire.Publish(eventType, map[string]any{
		"workflowId": selected.ID, "commentId": comment.ID, "response": response,
	}); err != nil {
		return err
	}
	final := true
	_, err := l.wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: selected.ID, ParentCommentID: &comment.ID,
		Author: "agent", Kind: kind, Final: &final, Content: response,
	})
	return err
}

func (l *Loop) startTrigger(
	ctx context.Context,
	view state.Snapshot,
	trigger state.Trigger,
	source eventwire.Event,
	expectedLastID int64,
) (startedRun bool, err error) {
	selected, found := view.Workflow(trigger.WorkflowID)
	directory, taskID, directoryErr := taskContext(view, source)
	predictedRunID := expectedLastID + 1
	arguments, err := json.Marshal(map[string]any{
		"event": source, "trigger": trigger, "runId": predictedRunID,
	})
	if err != nil {
		return false, fmt.Errorf("encode workflow arguments: %w", err)
	}
	settings := view.Settings
	run := state.WorkflowRunData{
		TriggerID: trigger.ID, WorkflowID: trigger.WorkflowID, SourceEventID: source.ID,
		TaskID: taskID, Directory: directory, Settings: &settings, Arguments: arguments,
	}
	if found {
		run.WorkflowName, run.WorkflowPhases = selected.Name, slices.Clone(selected.Phases)
		run.Source = stringValue(selected.Path)
	}
	if !l.admission.TryStart() {
		return false, nil
	}
	releaseAdmission := true
	defer func() {
		if releaseAdmission {
			l.admission.Done(err)
		}
	}()
	started, published, err := l.wire.PublishIfCurrent(expectedLastID, state.WorkflowRunStarted, run)
	if err != nil {
		return false, err
	}
	if !published {
		return false, nil
	}
	if !found || selected.DeletedAt != nil {
		run.Error = "workflow not found"
		_, err := l.wire.Publish(state.WorkflowRunFailed, run)
		return true, err
	}
	if directoryErr != nil {
		run.Error = directoryErr.Error()
		_, err := l.wire.Publish(state.WorkflowRunFailed, run)
		return true, err
	}
	l.active++
	releaseAdmission = false
	go func() {
		executionErr := l.executeRun(ctx, started.ID, workflow.RunRequest{
			Directory: directory,
			Source:    stringValue(selected.Path),
			Settings:  view.Settings,
			Arguments: json.RawMessage(arguments),
		}, run)
		l.completed <- executionErr
		l.admission.Done(executionErr)
	}()
	return true, nil
}

func (l *Loop) executeRun(
	ctx context.Context,
	runID int64,
	request workflow.RunRequest,
	run state.WorkflowRunData,
) error {
	var suspended *workflow.Event
	output, runErr := l.workflows.Run(
		ctx,
		request,
		func(event workflow.Event) error {
			if event.Type == "runtime.suspended" && event.Backend == "human" {
				copied := event
				suspended = &copied
			}
			_, err := l.wire.Publish(state.WorkflowRunEventRecorded, state.WorkflowRunEventData{
				RunID: runID, Event: event.Raw,
			})
			return err
		},
	)
	run.Output = output
	if ctx.Err() != nil {
		run.Error = "workflow canceled before completion: " + ctx.Err().Error()
		if _, err := l.wire.Publish(state.WorkflowRunFailed, run); err != nil {
			return err
		}
		return ctx.Err()
	}
	if errors.Is(runErr, workflow.ErrHumanReview) {
		if suspended == nil {
			run.Error = "workflow exited for human review without a runtime.suspended event"
			_, err := l.wire.Publish(state.WorkflowRunFailed, run)
			return err
		}
		return l.suspendRun(runID, run, *suspended)
	}
	if runErr != nil {
		run.Error = runErr.Error()
		_, err := l.wire.Publish(state.WorkflowRunFailed, run)
		return err
	}
	_, err := l.wire.Publish(state.WorkflowRunCompleted, run)
	return err
}

func (l *Loop) suspendRun(runID int64, run state.WorkflowRunData, event workflow.Event) error {
	if run.TaskID < 1 {
		run.Error = "human review gates require a task-triggered workflow"
		_, err := l.wire.Publish(state.WorkflowRunFailed, run)
		return err
	}
	content := strings.TrimSpace(event.Message)
	if len(event.Schema) > 0 {
		content += "\n\nReply with JSON matching this schema:\n" + string(event.Schema)
	}
	comment, err := l.wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: run.TaskID, Author: "agent", Content: content,
	})
	if err != nil {
		return err
	}
	_, err = l.wire.Publish(state.WorkflowRunWaiting, state.WorkflowRunStateData{
		RunID: runID,
		Gate: &state.WorkflowGate{
			Workflow: event.Workflow, Phase: event.Phase, StepID: event.StepID,
			Key: event.Key, AgentID: event.AgentID, Message: event.Message,
			Schema: append(json.RawMessage(nil), event.Schema...),
		},
		GateCommentID: comment.ID,
	})
	return err
}

func (l *Loop) startResume(
	ctx context.Context,
	view state.Snapshot,
	run state.WorkflowRun,
	response state.Comment,
	expectedLastID int64,
) (startedRun bool, err error) {
	result, err := humanResult(response.Content, run.WaitingGate.Schema)
	if err != nil {
		_, publishErr := l.wire.Publish(state.CommentCreated, state.CommentData{
			RelationType: "task", RelationID: run.TaskID, ParentCommentID: &response.ID,
			Author: "agent", Content: "I could not use this review response: " + err.Error(),
		})
		return true, publishErr
	}
	if run.Settings == nil || strings.TrimSpace(run.Source) == "" || len(run.Arguments) == 0 {
		terminal := workflowRunData(run)
		terminal.Error = "waiting workflow is missing durable continuation context"
		_, err := l.wire.Publish(state.WorkflowRunFailed, terminal)
		return true, err
	}
	events := view.EventsFor(run.ID)
	sequence := int64(0)
	resume := make([]json.RawMessage, 0, len(events)+1)
	for _, event := range events {
		if event.Sequence != sequence+1 || len(event.Raw) == 0 {
			return false, fmt.Errorf("run %d has invalid journal after sequence %d", run.ID, sequence)
		}
		sequence = event.Sequence
		resume = append(resume, append(json.RawMessage(nil), event.Raw...))
	}
	gate := run.WaitingGate
	completed := workflow.Event{
		Sequence: sequence + 1, At: time.Now().UTC(), Type: "step.completed",
		Workflow: gate.Workflow, Phase: gate.Phase, StepID: gate.StepID,
		Key: gate.Key, AgentID: gate.AgentID, Backend: "human", Kind: "gate", Result: result,
	}
	raw, err := json.Marshal(completed)
	if err != nil {
		return false, fmt.Errorf("encode human review result: %w", err)
	}
	if !l.admission.TryStart() {
		return false, nil
	}
	releaseAdmission := true
	defer func() {
		if releaseAdmission {
			l.admission.Done(err)
		}
	}()
	_, published, err := l.wire.PublishIfCurrent(
		expectedLastID,
		state.WorkflowRunResumed,
		state.WorkflowRunStateData{RunID: run.ID, ResponseCommentID: response.ID},
	)
	if err != nil || !published {
		return published, err
	}
	if _, err := l.wire.Publish(state.WorkflowRunEventRecorded, state.WorkflowRunEventData{
		RunID: run.ID, Event: raw,
	}); err != nil {
		return true, err
	}
	resume = append(resume, raw)
	l.active++
	request := workflow.RunRequest{
		Directory: run.Directory,
		Source:    run.Source,
		Settings:  *run.Settings,
		Arguments: append(json.RawMessage(nil), run.Arguments...),
		Resume:    resume,
	}
	terminal := workflowRunData(run)
	releaseAdmission = false
	go func() {
		executionErr := l.executeRun(ctx, run.ID, request, terminal)
		l.completed <- executionErr
		l.admission.Done(executionErr)
	}()
	return true, nil
}

func workflowRunData(run state.WorkflowRun) state.WorkflowRunData {
	return state.WorkflowRunData{
		TriggerID: run.TriggerID, WorkflowID: run.WorkflowID,
		WorkflowName: run.WorkflowName, WorkflowPhases: slices.Clone(run.WorkflowPhases),
		SourceEventID: run.SourceEventID, TaskID: run.TaskID,
		Directory: run.Directory, Source: run.Source, Settings: run.Settings,
		Arguments: append(json.RawMessage(nil), run.Arguments...),
		Output:    run.Output, Error: run.Error,
	}
}

func taskContext(view state.Snapshot, event eventwire.Event) (string, int64, error) {
	var projectID int64
	var taskID int64
	switch event.Type {
	case state.TaskCreated, state.TaskUpdated:
		var data state.TaskData
		if json.Unmarshal(event.Data, &data) != nil {
			return "", 0, errors.New("decode task event")
		}
		taskID = data.ID
		if event.Type == state.TaskCreated {
			taskID = event.ID
		}
		projectID = data.ProjectID
	case state.TaskDeleted:
		var data state.IDData
		if json.Unmarshal(event.Data, &data) != nil {
			return "", 0, errors.New("decode task event")
		}
		taskID = data.ID
		task, found := view.Task(data.ID)
		if !found {
			return "", 0, errors.New("task project not found")
		}
		projectID = task.ProjectID
	default:
		return "", 0, nil
	}
	project, found := view.Project(projectID)
	if !found || strings.TrimSpace(project.Path) == "" {
		return "", taskID, errors.New("task project path is required")
	}
	return strings.TrimSpace(project.Path), taskID, nil
}

func (l *Loop) syncWorkflows(ctx context.Context) error {
	definitions, err := l.workflows.List(ctx)
	if err != nil {
		return err
	}
	view, err := state.ProjectEvents(l.wire.Events(0))
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		existing, found := matchingWorkflow(view, definition, l.workflows)
		data := state.WorkflowData{
			Name: definition.Name, Description: stringPointer(definition.Description),
			Path: stringPointer(definition.Path), Scope: stringPointer(definition.Scope),
			Phases: slices.Clone(definition.Phases), Mutating: definition.Mutating,
		}
		if !found {
			if _, err := l.wire.Publish(state.WorkflowDiscovered, data); err != nil {
				return err
			}
			continue
		}
		data.ID = existing.ID
		if workflowChanged(existing, data) {
			if _, err := l.wire.Publish(state.WorkflowUpdated, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func pendingTrigger(view state.Snapshot, events []eventwire.Event) (state.Trigger, eventwire.Event, bool) {
	for _, event := range events {
		for _, trigger := range view.Triggers {
			if !trigger.Enabled || trigger.DeletedAt != nil ||
				trigger.EventType != event.Type || !event.At.After(trigger.UpdatedAt) {
				continue
			}
			if workflowID, found := terminalWorkflow(event); found && workflowID == trigger.WorkflowID {
				continue
			}
			if event.Type == state.CronFired {
				var cronEvent state.CronData
				if json.Unmarshal(event.Data, &cronEvent) != nil || cronEvent.TriggerID != trigger.ID {
					continue
				}
			}
			if !view.RunStarted(trigger.ID, event.ID) {
				return trigger, event, true
			}
		}
	}
	return state.Trigger{}, eventwire.Event{}, false
}

func terminalWorkflow(event eventwire.Event) (int64, bool) {
	if event.Type != state.WorkflowRunCompleted && event.Type != state.WorkflowRunFailed {
		return 0, false
	}
	var run state.WorkflowRunData
	if json.Unmarshal(event.Data, &run) != nil {
		return 0, false
	}
	return run.WorkflowID, true
}

func nextCron(view state.Snapshot, now time.Time) (*state.Trigger, time.Time) {
	var next time.Time
	for index := range view.Triggers {
		trigger := &view.Triggers[index]
		if !trigger.Enabled || trigger.DeletedAt != nil ||
			trigger.EventType != state.CronFired || trigger.Schedule == nil {
			continue
		}
		schedule, err := cron.ParseStandard(*trigger.Schedule)
		if err != nil {
			continue
		}
		anchor := trigger.UpdatedAt
		if last, found := view.LastCron(trigger.ID); found && last.After(anchor) {
			anchor = last
		}
		due := schedule.Next(anchor)
		if !due.After(now) {
			return trigger, due
		}
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return nil, next
}

func matchingWorkflow(view state.Snapshot, definition workflow.Definition, runner workflow.Runner) (state.Workflow, bool) {
	if selected, found := view.WorkflowByPath(definition.Path); found {
		return selected, true
	}
	for _, selected := range view.Workflows {
		if filepath.Clean(runner.LocalPath(selected.ID)) == filepath.Clean(definition.Path) {
			return selected, true
		}
	}
	return view.WorkflowByName(definition.Name)
}

func workflowChanged(existing state.Workflow, data state.WorkflowData) bool {
	return existing.Name != data.Name ||
		stringValue(existing.Description) != stringValue(data.Description) ||
		stringValue(existing.Path) != stringValue(data.Path) ||
		stringValue(existing.Scope) != stringValue(data.Scope) ||
		!slices.Equal(existing.Phases, data.Phases) ||
		existing.Mutating != data.Mutating
}

func authorPrompt(selected state.Workflow, comments []state.Comment, target string) string {
	var conversation strings.Builder
	for _, comment := range comments {
		if comment.Author != "user" && !(comment.Author == "agent" && comment.Final) {
			continue
		}
		fmt.Fprintf(&conversation, "%s: %s\n\n", comment.Author, comment.Content)
	}
	source := ""
	if selected.Path != nil {
		source = *selected.Path
	}
	return fmt.Sprintf(`You are collaborating with a user to author one dynamic workflow.

Read workflow CLI help and https://github.com/tomnagengast/workflow when useful.
Write the complete workflow to %s. This Factory-owned path is outside git.
You may use $FACTORY_CLI to inspect Factory resources and create or update a trigger; $FACTORY_URL targets this server.
The first statement must export const meta with name, description, and phases.
Use the workflow runtime globals such as phase, agent, parallel, workflow, gate, and log.
gate(prompt, { reviewer: "agent" | "codex" | "claude" | "human" }) defaults to agent review.
A human gate is only valid for a task event trigger. Factory posts its prompt as a task comment and resumes from the user's next root comment or direct reply.
If an existing workflow is being edited, its resolved source is %s. Preserve its name unless the user asks to change it.
Before replying, run workflow validate %q and fix every error until it exits zero. workflow list and workflow show do not validate source.
Edit no other file. Return a concise, useful response to the user after writing the workflow.

Conversation:
%s`, target, source, target, conversation.String())
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
