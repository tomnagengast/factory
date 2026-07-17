package state

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

func TestProjectTaskLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	events := []eventwire.Event{
		event(1, eventwire.TaskSubmitted, "task-1", "", now, map[string]string{"prompt": "Build it"}),
		event(2, eventwire.RunStarted, "task-1", "run-1", now.Add(time.Second), struct{}{}),
		event(3, eventwire.AgentOutput, "task-1", "run-1", now.Add(2*time.Second), map[string]string{"stream": "stdout", "text": "working"}),
		event(4, eventwire.RunCompleted, "task-1", "run-1", now.Add(3*time.Second), struct{}{}),
	}
	tasks, err := Project(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks))
	}
	task := tasks[0]
	if task.Status != Completed || task.Prompt != "Build it" || task.RunID != "run-1" {
		t.Fatalf("task = %#v", task)
	}
	if len(task.Output) != 1 || task.Output[0].Text != "working" {
		t.Fatalf("output = %#v", task.Output)
	}
}

func TestProjectRejectsBrokenCausality(t *testing.T) {
	now := time.Now().UTC()
	_, err := Project([]eventwire.Event{
		event(1, eventwire.RunStarted, "missing", "run-1", now, struct{}{}),
	})
	if err == nil {
		t.Fatal("expected unknown task to fail")
	}
}

func event(sequence uint64, kind, taskID, runID string, at time.Time, value any) eventwire.Event {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return eventwire.Event{
		Sequence: sequence, ID: "event", Type: kind, At: at,
		TaskID: taskID, RunID: runID, Data: data,
	}
}
