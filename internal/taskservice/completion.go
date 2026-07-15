package taskservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskstore"
)

type CompletionTasks interface {
	Find(string) (taskstore.Task, bool)
	Messages(string, uint64, int) (taskstore.MessagePage, error)
	Gates(string) ([]taskstore.Gate, error)
}

type CompletionMutator interface {
	Execute(context.Context, taskstore.CommandEnvelope, time.Time) (taskstore.Result, error)
}

type Completer struct {
	tasks   CompletionTasks
	mutator CompletionMutator
	now     func() time.Time
}

func NewCompleter(tasks CompletionTasks, mutator CompletionMutator, now func() time.Time) (*Completer, error) {
	if tasks == nil || mutator == nil || now == nil {
		return nil, errors.New("task completion: tasks, mutator, and clock are required")
	}
	return &Completer{tasks: tasks, mutator: mutator, now: now}, nil
}

func (c *Completer) Complete(ctx context.Context, ref taskmodel.TaskRef, runID, repository, evidenceRef string) (bool, error) {
	ref, err := ref.Normalize()
	if err != nil || ref.Source != taskmodel.SourceFactory || runID == "" || repository == "" || evidenceRef == "" {
		return false, errors.New("task completion: exact task and evidence are required")
	}
	task, found := c.tasks.Find(ref.ProviderID)
	if !found || !task.Ref.Equal(ref) {
		return false, taskstore.ErrNotFound
	}
	if task.Completion != nil {
		return task.State == taskstore.StateCompleted && task.Completion.RunID == runID && task.Completion.EvidenceRef == evidenceRef, nil
	}
	if task.State != taskstore.StateInProgress || task.Routing == nil || task.Routing.Repository != repository {
		return false, errors.New("task completion: task is not in the exact routed lifecycle")
	}
	gates, err := c.tasks.Gates(task.Ref.ProviderID)
	if err != nil {
		return false, fmt.Errorf("task completion: read gates: %w", err)
	}
	for _, gate := range gates {
		if gate.Status != taskstore.GateApproved {
			return false, fmt.Errorf("task completion: gate %s is not approved", gate.ID)
		}
	}
	if err := c.requireHumanFeedbackAnswered(task); err != nil {
		return false, err
	}
	result, err := c.mutator.Execute(ctx, taskstore.CompletionEnvelope(taskstore.CompletionCommand{
		Actor:  taskstore.Actor{ID: "system:completion", Kind: taskstore.AuthorSystem},
		TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision,
		Completion:     taskstore.Completion{RunID: runID, EvidenceRef: evidenceRef},
		IdempotencyKey: "task-complete:" + runID,
	}), c.now())
	if err != nil {
		return false, fmt.Errorf("task completion: record evidence: %w", err)
	}
	return result.Task.State == taskstore.StateCompleted && result.Task.Completion != nil &&
		result.Task.Completion.RunID == runID && result.Task.Completion.EvidenceRef == evidenceRef, nil
}

func (c *Completer) requireHumanFeedbackAnswered(task taskstore.Task) error {
	if task.LatestHumanAt == nil {
		return nil
	}
	after := uint64(0)
	answered := false
	for {
		page, err := c.tasks.Messages(task.Ref.ProviderID, after, 500)
		if err != nil {
			return fmt.Errorf("task completion: read messages: %w", err)
		}
		for _, message := range page.Messages {
			if message.Author.Kind != taskstore.AuthorHuman && message.CreatedAt.After(*task.LatestHumanAt) {
				answered = true
			}
		}
		if page.NextAfter == 0 {
			break
		}
		after = page.NextAfter
	}
	if !answered {
		return errors.New("task completion: later human feedback is unanswered")
	}
	return nil
}
