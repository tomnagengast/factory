package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Completed Status = "completed"
	Failed    Status = "failed"
)

type Output struct {
	Sequence uint64    `json:"sequence"`
	Stream   string    `json:"stream"`
	Text     string    `json:"text"`
	At       time.Time `json:"at"`
}

type Task struct {
	ID          string     `json:"id"`
	Prompt      string     `json:"prompt"`
	Status      Status     `json:"status"`
	RunID       string     `json:"runId,omitempty"`
	SubmittedAt time.Time  `json:"submittedAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	Error       string     `json:"error,omitempty"`
	Output      []Output   `json:"output"`
}

type submittedData struct {
	Prompt string `json:"prompt"`
}

type outputData struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type failedData struct {
	Error string `json:"error"`
}

// Project rebuilds the complete task view from the wire. No mutable task or
// Run state exists outside this projection.
func Project(events []eventwire.Event) ([]Task, error) {
	tasks := make([]Task, 0)
	positions := make(map[string]int)
	for _, event := range events {
		switch event.Type {
		case eventwire.TaskSubmitted:
			if event.TaskID == "" || positions[event.TaskID] != 0 {
				return nil, fmt.Errorf("task submission %q is invalid", event.TaskID)
			}
			var data submittedData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return nil, fmt.Errorf("decode task %q: %w", event.TaskID, err)
			}
			if data.Prompt == "" {
				return nil, fmt.Errorf("task %q has no prompt", event.TaskID)
			}
			positions[event.TaskID] = len(tasks) + 1
			tasks = append(tasks, Task{
				ID: event.TaskID, Prompt: data.Prompt, Status: Queued,
				SubmittedAt: event.At, Output: []Output{},
			})
		case eventwire.RunStarted:
			task, err := referencedTask(tasks, positions, event)
			if err != nil {
				return nil, err
			}
			if task.Status != Queued || event.RunID == "" {
				return nil, fmt.Errorf("run start for task %q is invalid", event.TaskID)
			}
			task.Status = Running
			task.RunID = event.RunID
			task.StartedAt = timePointer(event.At)
		case eventwire.AgentOutput:
			task, err := referencedRun(tasks, positions, event)
			if err != nil {
				return nil, err
			}
			if task.Status != Running {
				return nil, fmt.Errorf("agent output for task %q is invalid", event.TaskID)
			}
			var data outputData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return nil, fmt.Errorf("decode output for task %q: %w", event.TaskID, err)
			}
			if data.Stream == "" || data.Text == "" {
				return nil, fmt.Errorf("output for task %q is incomplete", event.TaskID)
			}
			task.Output = append(task.Output, Output{
				Sequence: event.Sequence, Stream: data.Stream, Text: data.Text, At: event.At,
			})
		case eventwire.RunCompleted:
			task, err := referencedRun(tasks, positions, event)
			if err != nil {
				return nil, err
			}
			if task.Status != Running {
				return nil, fmt.Errorf("run completion for task %q is invalid", event.TaskID)
			}
			task.Status = Completed
			task.FinishedAt = timePointer(event.At)
		case eventwire.RunFailed:
			task, err := referencedRun(tasks, positions, event)
			if err != nil {
				return nil, err
			}
			if task.Status != Running {
				return nil, fmt.Errorf("run failure for task %q is invalid", event.TaskID)
			}
			var data failedData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return nil, fmt.Errorf("decode failure for task %q: %w", event.TaskID, err)
			}
			if data.Error == "" {
				return nil, fmt.Errorf("failure for task %q has no error", event.TaskID)
			}
			task.Status = Failed
			task.Error = data.Error
			task.FinishedAt = timePointer(event.At)
		default:
			return nil, fmt.Errorf("unknown event type %q", event.Type)
		}
	}
	return tasks, nil
}

func QueuedTask(tasks []Task) (Task, bool) {
	for _, task := range tasks {
		if task.Status == Queued {
			return cloneTask(task), true
		}
	}
	return Task{}, false
}

func RunningTasks(tasks []Task) []Task {
	var running []Task
	for _, task := range tasks {
		if task.Status == Running {
			running = append(running, cloneTask(task))
		}
	}
	return running
}

func referencedTask(tasks []Task, positions map[string]int, event eventwire.Event) (*Task, error) {
	position := positions[event.TaskID]
	if position == 0 {
		return nil, fmt.Errorf("event %d references unknown task %q", event.Sequence, event.TaskID)
	}
	return &tasks[position-1], nil
}

func referencedRun(tasks []Task, positions map[string]int, event eventwire.Event) (*Task, error) {
	task, err := referencedTask(tasks, positions, event)
	if err != nil {
		return nil, err
	}
	if event.RunID == "" || task.RunID != event.RunID {
		return nil, errors.New("event references the wrong run")
	}
	return task, nil
}

func cloneTask(task Task) Task {
	task.Output = append([]Output(nil), task.Output...)
	if task.StartedAt != nil {
		task.StartedAt = timePointer(*task.StartedAt)
	}
	if task.FinishedAt != nil {
		task.FinishedAt = timePointer(*task.FinishedAt)
	}
	return task
}

func timePointer(value time.Time) *time.Time {
	return &value
}
