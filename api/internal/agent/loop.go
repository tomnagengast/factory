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
	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/workflow"
)

const (
	authoringStarted   = "workflow.authoring.started"
	authoringCompleted = "workflow.authoring.completed"
	authoringFailed    = "workflow.authoring.failed"
)

type Loop struct {
	wire      *eventwire.Wire
	agent     Runner
	workflows workflow.Runner
	active    int
	completed chan error
}

func NewLoop(wire *eventwire.Wire, runner Runner, workflows workflow.Runner) (*Loop, error) {
	if wire == nil || runner == nil || workflows == nil {
		return nil, errors.New("workflow coordinator requires a wire, agent, and workflow CLI")
	}
	return &Loop{
		wire: wire, agent: runner, workflows: workflows,
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
	for {
		after := l.wire.LastID()
		worked, nextCron, err := l.step(runContext)
		if err != nil {
			cancel()
			return l.stop(err)
		}
		if worked {
			continue
		}
		if err := l.wait(runContext, after, nextCron); err != nil {
			cancel()
			return l.stop(err)
		}
	}
}

func (l *Loop) step(ctx context.Context) (bool, time.Time, error) {
	if err := l.collectCompleted(); err != nil {
		return false, time.Time{}, err
	}
	events := l.wire.Events(0)
	view, err := state.ProjectEvents(events)
	if err != nil {
		return false, time.Time{}, err
	}
	if comment, found := view.PendingWorkflowComment(); found {
		return true, time.Time{}, l.authorWorkflow(ctx, view, comment)
	}
	if l.active < view.Settings.WorkflowCapacity {
		if trigger, source, found := pendingTrigger(view, events); found {
			return true, time.Time{}, l.startTrigger(ctx, view, trigger, source)
		}
		due, next := nextCron(view, time.Now().UTC())
		if due != nil {
			_, err := l.wire.Publish(state.CronFired, state.CronData{TriggerID: due.ID})
			return true, time.Time{}, err
		}
		return false, next, nil
	}
	return false, time.Time{}, nil
}

func (l *Loop) wait(ctx context.Context, after int64, nextCron time.Time) error {
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
	)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr == nil {
		runErr = l.syncWorkflows(ctx)
	}
	response := strings.TrimSpace(output)
	if response == "" {
		response = "Workflow updated."
	}
	eventType := authoringCompleted
	if runErr != nil {
		eventType = authoringFailed
		response = strings.TrimSpace(response + "\n\nError: " + runErr.Error())
	}
	if _, err := l.wire.Publish(eventType, map[string]any{
		"workflowId": selected.ID, "commentId": comment.ID, "response": response,
	}); err != nil {
		return err
	}
	_, err := l.wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: selected.ID, ParentCommentID: &comment.ID,
		Author: "agent", Content: response,
	})
	return err
}

func (l *Loop) startTrigger(
	ctx context.Context,
	view state.Snapshot,
	trigger state.Trigger,
	source eventwire.Event,
) error {
	selected, found := view.Workflow(trigger.WorkflowID)
	run := state.WorkflowRunData{
		TriggerID: trigger.ID, WorkflowID: trigger.WorkflowID, SourceEventID: source.ID,
	}
	if found {
		run.WorkflowName, run.WorkflowPhases = selected.Name, slices.Clone(selected.Phases)
	}
	started, err := l.wire.Publish(state.WorkflowRunStarted, run)
	if err != nil {
		return err
	}
	if !found || selected.DeletedAt != nil {
		run.Error = "workflow not found"
		_, err := l.wire.Publish(state.WorkflowRunFailed, run)
		return err
	}
	directory, directoryErr := taskDirectory(view, source)
	if directoryErr != nil {
		run.Error = directoryErr.Error()
		_, err := l.wire.Publish(state.WorkflowRunFailed, run)
		return err
	}
	l.active++
	go func() {
		l.completed <- l.executeTrigger(
			ctx, selected, trigger, source, started.ID, directory, view.Settings, run,
		)
	}()
	return nil
}

func (l *Loop) executeTrigger(
	ctx context.Context,
	selected state.Workflow,
	trigger state.Trigger,
	source eventwire.Event,
	runID int64,
	directory string,
	settings state.Settings,
	run state.WorkflowRunData,
) error {
	output, runErr := l.workflows.Run(
		ctx,
		directory,
		selected.Name,
		stringValue(selected.Path),
		settings,
		map[string]any{"event": source, "trigger": trigger},
		func(event workflow.Event) error {
			_, err := l.wire.Publish(state.WorkflowRunEventRecorded, state.WorkflowRunEventData{
				RunID: runID, Event: event.Raw,
			})
			return err
		},
	)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	run.Output = output
	if runErr != nil {
		run.Error = runErr.Error()
		_, err := l.wire.Publish(state.WorkflowRunFailed, run)
		return err
	}
	_, err := l.wire.Publish(state.WorkflowRunCompleted, run)
	return err
}

func taskDirectory(view state.Snapshot, event eventwire.Event) (string, error) {
	var projectID int64
	switch event.Type {
	case state.TaskCreated, state.TaskUpdated:
		var data state.TaskData
		if json.Unmarshal(event.Data, &data) != nil {
			return "", errors.New("decode task event")
		}
		projectID = data.ProjectID
	case state.TaskDeleted:
		var data state.IDData
		if json.Unmarshal(event.Data, &data) != nil {
			return "", errors.New("decode task event")
		}
		task, found := view.Task(data.ID)
		if !found {
			return "", errors.New("task project not found")
		}
		projectID = task.ProjectID
	default:
		return "", nil
	}
	project, found := view.Project(projectID)
	if !found || strings.TrimSpace(project.Path) == "" {
		return "", errors.New("task project path is required")
	}
	return strings.TrimSpace(project.Path), nil
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
			if trigger.DeletedAt != nil || trigger.EventType != event.Type || !event.At.After(trigger.UpdatedAt) {
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

func nextCron(view state.Snapshot, now time.Time) (*state.Trigger, time.Time) {
	var next time.Time
	for index := range view.Triggers {
		trigger := &view.Triggers[index]
		if trigger.DeletedAt != nil || trigger.EventType != state.CronFired || trigger.Schedule == nil {
			continue
		}
		schedule, err := cron.ParseStandard(*trigger.Schedule)
		if err != nil {
			continue
		}
		anchor := trigger.CreatedAt
		if last, found := view.LastCron(trigger.ID); found {
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
If an existing workflow is being edited, its resolved source is %s. Preserve its name unless the user asks to change it.
Edit no other file. Return a concise, useful response to the user after writing the workflow.

Conversation:
%s`, target, source, conversation.String())
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
