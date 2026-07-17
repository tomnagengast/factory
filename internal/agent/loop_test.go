package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/state"
)

type fakeRunner struct {
	outputs []Output
	err     error
}

func (r fakeRunner) Run(_ context.Context, _ string, emit func(Output) error) error {
	for _, output := range r.outputs {
		if err := emit(output); err != nil {
			return err
		}
	}
	return r.err
}

func TestLoopRunsTasksToCompletion(t *testing.T) {
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	if _, err := wire.Publish(eventwire.TaskSubmitted, "task-1", "", map[string]string{"prompt": "Do it"}); err != nil {
		t.Fatal(err)
	}

	loop, err := NewLoop(wire, fakeRunner{outputs: []Output{{Stream: "stdout", Text: "done"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- loop.Run(ctx) }()

	task := waitForStatus(t, wire, state.Completed)
	if len(task.Output) != 1 || task.Output[0].Text != "done" {
		t.Fatalf("output = %#v", task.Output)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("loop error = %v", err)
	}
}

func TestLoopRecordsFailureAndContinues(t *testing.T) {
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	if _, err := wire.Publish(eventwire.TaskSubmitted, "task-1", "", map[string]string{"prompt": "Fail"}); err != nil {
		t.Fatal(err)
	}

	loop, err := NewLoop(wire, fakeRunner{err: errors.New("boom")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- loop.Run(ctx) }()

	task := waitForStatus(t, wire, state.Failed)
	if task.Error != "boom" {
		t.Fatalf("error = %q", task.Error)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("loop error = %v", err)
	}
}

func TestLoopRunsQueuedTasksInSubmissionOrder(t *testing.T) {
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	for _, task := range []struct {
		id     string
		prompt string
	}{
		{id: "task-1", prompt: "First"},
		{id: "task-2", prompt: "Second"},
	} {
		if _, err := wire.Publish(eventwire.TaskSubmitted, task.id, "", map[string]string{"prompt": task.prompt}); err != nil {
			t.Fatal(err)
		}
	}

	loop, err := NewLoop(wire, fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- loop.Run(ctx) }()

	waitContext, stopWaiting := context.WithTimeout(context.Background(), time.Second)
	if _, err := wire.Wait(waitContext, 5); err != nil {
		stopWaiting()
		t.Fatal(err)
	}
	stopWaiting()
	events := wire.Events(0)
	if len(events) != 6 {
		t.Fatalf("events = %d, want 6", len(events))
	}
	if events[2].Type != eventwire.RunStarted || events[2].TaskID != "task-1" {
		t.Fatalf("first run start = %#v", events[2])
	}
	if events[4].Type != eventwire.RunStarted || events[4].TaskID != "task-2" {
		t.Fatalf("second run start = %#v", events[4])
	}

	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("loop error = %v", err)
	}
}

func TestLoopFailsRunInterruptedBeforeRestart(t *testing.T) {
	wire, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	if _, err := wire.Publish(eventwire.TaskSubmitted, "task-1", "", map[string]string{"prompt": "Resume"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Publish(eventwire.RunStarted, "task-1", "run-1", struct{}{}); err != nil {
		t.Fatal(err)
	}

	loop, err := NewLoop(wire, fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- loop.Run(ctx) }()

	task := waitForStatus(t, wire, state.Failed)
	if task.Error != "Factory stopped before this run completed." {
		t.Fatalf("error = %q", task.Error)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("loop error = %v", err)
	}
}

func waitForStatus(t *testing.T, wire *eventwire.Wire, status state.Status) state.Task {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	after := uint64(0)
	for time.Now().Before(deadline) {
		tasks, err := state.Project(wire.Events(0))
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			if task.Status == status {
				return task
			}
		}
		waitContext, cancel := context.WithDeadline(context.Background(), deadline)
		events, err := wire.Wait(waitContext, after)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				break
			}
			t.Fatal(err)
		}
		after = events[len(events)-1].Sequence
	}
	t.Fatalf("task never reached %s", status)
	return state.Task{}
}
