package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/state"
)

type Loop struct {
	wire   *eventwire.Wire
	runner Runner
}

func NewLoop(wire *eventwire.Wire, runner Runner) (*Loop, error) {
	if wire == nil || runner == nil {
		return nil, errors.New("agent loop requires a wire and runner")
	}
	return &Loop{wire: wire, runner: runner}, nil
}

// Run owns the only worker in Factory. It always selects the oldest queued
// task, runs it to a terminal event, then looks at the wire again.
func (l *Loop) Run(ctx context.Context) error {
	if err := l.failInterrupted(); err != nil {
		return err
	}
	for {
		events := l.wire.Events(0)
		tasks, err := state.Project(events)
		if err != nil {
			return err
		}
		if task, found := state.QueuedTask(tasks); found {
			if err := l.runTask(ctx, task); err != nil {
				return err
			}
			continue
		}

		after := uint64(0)
		if len(events) > 0 {
			after = events[len(events)-1].Sequence
		}
		if _, err := l.wire.Wait(ctx, after); err != nil {
			return err
		}
	}
}

func (l *Loop) failInterrupted() error {
	tasks, err := state.Project(l.wire.Events(0))
	if err != nil {
		return err
	}
	for _, task := range state.RunningTasks(tasks) {
		if _, err := l.wire.Publish(
			eventwire.RunFailed,
			task.ID,
			task.RunID,
			map[string]string{"error": "Factory stopped before this run completed."},
		); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) runTask(ctx context.Context, task state.Task) error {
	runID, err := eventwire.NewID("run")
	if err != nil {
		return err
	}
	if _, err := l.wire.Publish(eventwire.RunStarted, task.ID, runID, struct{}{}); err != nil {
		return err
	}

	runErr := l.runner.Run(ctx, task.Prompt, func(output Output) error {
		_, err := l.wire.Publish(eventwire.AgentOutput, task.ID, runID, map[string]string{
			"stream": output.Stream,
			"text":   output.Text,
		})
		return err
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr != nil {
		_, publishErr := l.wire.Publish(eventwire.RunFailed, task.ID, runID, map[string]string{
			"error": runErr.Error(),
		})
		if publishErr != nil {
			return publishErr
		}
		return nil
	}
	if _, err := l.wire.Publish(eventwire.RunCompleted, task.ID, runID, struct{}{}); err != nil {
		return fmt.Errorf("complete run: %w", err)
	}
	return nil
}
