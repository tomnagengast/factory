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
	"github.com/tomnagengast/factory/api/internal/store"
	"github.com/tomnagengast/factory/api/internal/workflow"
)

const (
	authoringStarted   = "workflow.authoring.started"
	authoringCompleted = "workflow.authoring.completed"
	authoringFailed    = "workflow.authoring.failed"
	interruptedRun     = "workflow interrupted by Factory restart before a terminal event was recorded"
)

type Loop struct {
	store     *store.Store
	agent     Runner
	workflows workflow.Runner
	admission *quiescence.Controller
	active    int
	completed chan error
	authoring bool
	authored  chan error
}

func NewLoop(
	eventStore *store.Store,
	runner Runner,
	workflows workflow.Runner,
	admission *quiescence.Controller,
) (*Loop, error) {
	if eventStore == nil || runner == nil || workflows == nil || admission == nil {
		return nil, errors.New("workflow coordinator requires an event store, agent, workflow CLI, and admission controller")
	}
	return &Loop{
		store: eventStore, agent: runner, workflows: workflows, admission: admission,
		completed: make(chan error, state.MaxWorkflowCapacity),
		authored:  make(chan error, 1),
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
		after, err := l.store.LastID()
		if err != nil {
			return err
		}
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
	runs, err := l.store.RunningRuns()
	if err != nil {
		return err
	}
	for _, run := range runs {
		if _, err := l.store.Append(state.WorkflowRunFailed, state.WorkflowRunData{
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
	settings, err := l.store.Settings()
	if err != nil {
		return false, time.Time{}, err
	}
	if comment, found, err := l.store.PendingWorkflowComment(); err != nil {
		return false, time.Time{}, err
	} else if found && !l.authoring {
		if !l.admission.TryStart() {
			return false, time.Time{}, nil
		}
		l.authoring = true
		go func() {
			authorErr := l.authorWorkflow(ctx, settings, comment)
			l.authored <- authorErr
			l.admission.Done(authorErr)
		}()
		return true, time.Time{}, nil
	}
	if l.active < settings.WorkflowCapacity {
		if run, response, found, err := l.store.PendingHumanResponse(); err != nil {
			return false, time.Time{}, err
		} else if found {
			checkpoint, err := l.store.LastID()
			if err != nil {
				return false, time.Time{}, err
			}
			started, err := l.startResume(ctx, run, response, checkpoint)
			return started, time.Time{}, err
		}
		trigger, source, checkpoint, found, err := l.store.PendingTrigger()
		if err != nil {
			return false, time.Time{}, err
		}
		if found {
			started, err := l.startTrigger(ctx, settings, trigger, source, checkpoint)
			return started, time.Time{}, err
		}
		if !l.admission.Accepting() {
			return false, time.Time{}, nil
		}
		cronStates, err := l.store.CronStates()
		if err != nil {
			return false, time.Time{}, err
		}
		due, next := nextCron(cronStates, time.Now().UTC())
		if due != nil {
			_, _, err := l.store.AppendIfCurrent(
				checkpoint, state.CronFired, state.CronData{TriggerID: due.ID},
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
		_, err := l.store.Wait(waitContext, after, 200)
		wireResult <- err
	}()
	select {
	case err := <-l.completed:
		l.active--
		cancel()
		<-wireResult
		return err
	case err := <-l.authored:
		l.authoring = false
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
		case err := <-l.authored:
			l.authoring = false
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
	for l.active > 0 || l.authoring {
		var err error
		if l.authoring && l.active == 0 {
			err = <-l.authored
			l.authoring = false
		} else if !l.authoring {
			err = <-l.completed
			l.active--
		} else {
			select {
			case err = <-l.completed:
				l.active--
			case err = <-l.authored:
				l.authoring = false
			}
		}
		if err != nil && !errors.Is(err, context.Canceled) && executionErr == nil {
			executionErr = err
		}
	}
	if executionErr != nil && errors.Is(cause, context.Canceled) {
		return executionErr
	}
	return cause
}

func (l *Loop) authorWorkflow(ctx context.Context, settings state.Settings, comment state.Comment) error {
	selected, found, err := l.store.Workflow(comment.RelationID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	comments, err := l.store.CommentsFor("workflow", selected.ID)
	if err != nil {
		return err
	}
	target := l.workflows.LocalPath(selected.ID)
	if filepath.Clean(stringValue(selected.Path)) != filepath.Clean(target) {
		if _, err := l.store.Append(state.WorkflowUpdated, state.WorkflowData{
			ID: selected.ID, Name: selected.Name, Description: selected.Description,
			Path: &target, Scope: selected.Scope, Phases: slices.Clone(selected.Phases),
			Mutating: selected.Mutating,
		}); err != nil {
			return err
		}
	}
	if _, err := l.store.Append(authoringStarted, map[string]int64{
		"workflowId": selected.ID, "commentId": comment.ID,
	}); err != nil {
		return err
	}
	output, runErr := l.agent.Run(
		ctx,
		settings,
		authorPrompt(selected, comments, target),
		func(step AgentStep) error {
			final := false
			_, err := l.store.Append(state.CommentCreated, state.CommentData{
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
	if _, err := l.store.Append(eventType, map[string]any{
		"workflowId": selected.ID, "commentId": comment.ID, "response": response,
	}); err != nil {
		return err
	}
	final := true
	_, err = l.store.Append(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: selected.ID, ParentCommentID: &comment.ID,
		Author: "agent", Kind: kind, Final: &final, Content: response,
	})
	return err
}

func (l *Loop) startTrigger(
	ctx context.Context,
	settings state.Settings,
	trigger state.Trigger,
	source eventwire.Event,
	expectedLastID int64,
) (startedRun bool, err error) {
	selected, found, err := l.store.Workflow(trigger.WorkflowID)
	if err != nil {
		return false, err
	}
	directory, taskID, directoryErr := l.taskContext(source)
	predictedRunID := expectedLastID + 1
	arguments, err := json.Marshal(map[string]any{
		"event": source, "trigger": trigger, "runId": predictedRunID,
	})
	if err != nil {
		return false, fmt.Errorf("encode workflow arguments: %w", err)
	}
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
	started, published, err := l.store.AppendIfCurrent(expectedLastID, state.WorkflowRunStarted, run)
	if err != nil {
		return false, err
	}
	if !published {
		return false, nil
	}
	if !found || selected.DeletedAt != nil {
		run.Error = "workflow not found"
		_, err := l.store.Append(state.WorkflowRunFailed, run)
		return true, err
	}
	if directoryErr != nil {
		run.Error = directoryErr.Error()
		_, err := l.store.Append(state.WorkflowRunFailed, run)
		return true, err
	}
	l.active++
	releaseAdmission = false
	go func() {
		executionErr := l.executeRun(ctx, started.ID, workflow.RunRequest{
			Directory: directory,
			Source:    stringValue(selected.Path),
			Settings:  settings,
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
			_, err := l.store.Append(state.WorkflowRunEventRecorded, state.WorkflowRunEventData{
				RunID: runID, Event: event.Raw,
			})
			return err
		},
	)
	run.Output = output
	if ctx.Err() != nil {
		run.Error = "workflow canceled before completion: " + ctx.Err().Error()
		if _, err := l.store.Append(state.WorkflowRunFailed, run); err != nil {
			return err
		}
		return ctx.Err()
	}
	if errors.Is(runErr, workflow.ErrHumanReview) {
		if suspended == nil {
			run.Error = "workflow exited for human review without a runtime.suspended event"
			_, err := l.store.Append(state.WorkflowRunFailed, run)
			return err
		}
		return l.suspendRun(runID, run, *suspended)
	}
	if runErr != nil {
		run.Error = runErr.Error()
		_, err := l.store.Append(state.WorkflowRunFailed, run)
		return err
	}
	_, err := l.store.Append(state.WorkflowRunCompleted, run)
	return err
}

func (l *Loop) suspendRun(runID int64, run state.WorkflowRunData, event workflow.Event) error {
	if run.TaskID < 1 {
		run.Error = "human review gates require a task-triggered workflow"
		_, err := l.store.Append(state.WorkflowRunFailed, run)
		return err
	}
	content := strings.TrimSpace(event.Message)
	if len(event.Schema) > 0 {
		content += "\n\nReply with JSON matching this schema:\n" + string(event.Schema)
	}
	comment, err := l.store.Append(state.CommentCreated, state.CommentData{
		RelationType: "task", RelationID: run.TaskID, Author: "agent", Content: content,
	})
	if err != nil {
		return err
	}
	_, err = l.store.Append(state.WorkflowRunWaiting, state.WorkflowRunStateData{
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
	run state.WorkflowRun,
	response state.Comment,
	expectedLastID int64,
) (startedRun bool, err error) {
	result, err := humanResult(response.Content, run.WaitingGate.Schema)
	if err != nil {
		_, publishErr := l.store.Append(state.CommentCreated, state.CommentData{
			RelationType: "task", RelationID: run.TaskID, ParentCommentID: &response.ID,
			Author: "agent", Content: "I could not use this review response: " + err.Error(),
		})
		return true, publishErr
	}
	if run.Settings == nil || strings.TrimSpace(run.Source) == "" || len(run.Arguments) == 0 {
		terminal := workflowRunData(run)
		terminal.Error = "waiting workflow is missing durable continuation context"
		_, err := l.store.Append(state.WorkflowRunFailed, terminal)
		return true, err
	}
	events, err := l.store.RunJournal(run.ID)
	if err != nil {
		return false, err
	}
	sequence := int64(0)
	resume := make([]json.RawMessage, 0, len(events)+1)
	for _, rawEvent := range events {
		var event workflow.Event
		if err := json.Unmarshal(rawEvent, &event); err != nil {
			return false, fmt.Errorf("decode run %d journal after sequence %d: %w", run.ID, sequence, err)
		}
		if event.Sequence != sequence+1 || len(rawEvent) == 0 {
			return false, fmt.Errorf("run %d has invalid journal after sequence %d", run.ID, sequence)
		}
		sequence = event.Sequence
		resume = append(resume, append(json.RawMessage(nil), rawEvent...))
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
	_, published, err := l.store.AppendIfCurrent(
		expectedLastID,
		state.WorkflowRunResumed,
		state.WorkflowRunStateData{RunID: run.ID, ResponseCommentID: response.ID},
	)
	if err != nil || !published {
		return published, err
	}
	if _, err := l.store.Append(state.WorkflowRunEventRecorded, state.WorkflowRunEventData{
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

func (l *Loop) taskContext(event eventwire.Event) (string, int64, error) {
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
		task, found, err := l.store.Task(data.ID)
		if err != nil {
			return "", 0, err
		}
		if !found {
			return "", 0, errors.New("task project not found")
		}
		projectID = task.ProjectID
	default:
		return "", 0, nil
	}
	project, found, err := l.store.Project(projectID)
	if err != nil {
		return "", taskID, err
	}
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
	workflows, err := l.store.Workflows()
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		existing, found := matchingWorkflow(workflows, definition, l.workflows)
		data := state.WorkflowData{
			Name: definition.Name, Description: stringPointer(definition.Description),
			Path: stringPointer(definition.Path), Scope: stringPointer(definition.Scope),
			Phases: slices.Clone(definition.Phases), Mutating: definition.Mutating,
		}
		if !found {
			if _, err := l.store.Append(state.WorkflowDiscovered, data); err != nil {
				return err
			}
			continue
		}
		data.ID = existing.ID
		if workflowChanged(existing, data) {
			if _, err := l.store.Append(state.WorkflowUpdated, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func nextCron(states []store.CronState, now time.Time) (*state.Trigger, time.Time) {
	var next time.Time
	for index := range states {
		value := &states[index]
		trigger := &value.Trigger
		if trigger.Schedule == nil {
			continue
		}
		schedule, err := cron.ParseStandard(*trigger.Schedule)
		if err != nil {
			continue
		}
		anchor := trigger.UpdatedAt
		if value.Last != nil && value.Last.After(anchor) {
			anchor = *value.Last
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

func matchingWorkflow(workflows []state.Workflow, definition workflow.Definition, runner workflow.Runner) (state.Workflow, bool) {
	for _, selected := range workflows {
		if stringValue(selected.Path) == definition.Path {
			return selected, true
		}
		if filepath.Clean(runner.LocalPath(selected.ID)) == filepath.Clean(definition.Path) {
			return selected, true
		}
	}
	for _, selected := range workflows {
		if selected.Name == definition.Name {
			return selected, true
		}
	}
	return state.Workflow{}, false
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
