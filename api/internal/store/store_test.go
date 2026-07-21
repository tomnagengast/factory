package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
)

func TestAppendProjectsAtomicallyAndPreservesConditionalOrder(t *testing.T) {
	store := openTestStore(t)
	project, err := store.Append(state.ProjectCreated, state.ProjectData{Name: "Factory", Path: "/factory"})
	if err != nil {
		t.Fatal(err)
	}
	if project.ID != 1 {
		t.Fatalf("project event ID = %d", project.ID)
	}
	projectView, found, err := store.Project(project.ID)
	if err != nil || !found || projectView.Name != "Factory" || projectView.ID != project.ID {
		t.Fatalf("project projection = %#v, %v, %v", projectView, found, err)
	}

	if _, err := store.Append("release.ready", map[string]string{"version": "1"}); err != nil {
		t.Fatal(err)
	}
	rejected, published, err := store.AppendIfCurrent(1, state.ProjectDeleted, state.IDData{ID: project.ID})
	if err != nil || published || rejected.ID != 0 {
		t.Fatalf("stale append = %#v, %v, %v", rejected, published, err)
	}
	lastID, err := store.LastID()
	if err != nil || lastID != 2 {
		t.Fatalf("last ID = %d, %v", lastID, err)
	}

	if _, err := store.Append(state.TaskCreated, json.RawMessage(`{"title":`)); err == nil {
		t.Fatal("malformed known event was committed")
	}
	lastID, _ = store.LastID()
	if lastID != 2 {
		t.Fatalf("failed projection advanced wire to %d", lastID)
	}
}

func TestWaitWakesAndCloseStopsWaiters(t *testing.T) {
	store := openTestStore(t)
	result := make(chan []eventwire.Event, 1)
	errResult := make(chan error, 1)
	go func() {
		events, err := store.Wait(context.Background(), 0, 200)
		result <- events
		errResult <- err
	}()
	created, err := store.Append("release.ready", map[string]bool{"ready": true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case events := <-result:
		if len(events) != 1 || events[0].ID != created.ID {
			t.Fatalf("wait returned %#v", events)
		}
		if err := <-errResult; err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not wake")
	}

	closed := make(chan error, 1)
	go func() {
		_, err := store.Wait(context.Background(), created.ID, 200)
		closed <- err
	}()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("closed wait error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not wake waiter")
	}
}

func TestPendingTriggerUsesEventBoundaryAndClaim(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.Append(state.WorkflowDiscovered, state.WorkflowData{Name: "review"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("release.ready", map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	triggerEvent, err := store.Append(state.TriggerCreated, state.TriggerData{EventType: "release.ready", WorkflowID: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Append("release.ready", map[string]int{"version": 2})
	if err != nil {
		t.Fatal(err)
	}
	trigger, selected, snapshot, found, err := store.PendingTrigger()
	if err != nil || !found || trigger.ID != triggerEvent.ID || selected.ID != source.ID || snapshot != source.ID {
		t.Fatalf("pending trigger = %#v, %#v, %d, %v, %v", trigger, selected, snapshot, found, err)
	}
	if _, _, err := store.AppendIfCurrent(snapshot, state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: trigger.ID, WorkflowID: trigger.WorkflowID, SourceEventID: source.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, found, err := store.PendingTrigger(); err != nil || found {
		t.Fatalf("claimed trigger pending = %v, %v", found, err)
	}
}

func TestHistoryFiltersAndPagesWorkflowRunProjection(t *testing.T) {
	store := openTestStore(t)
	workflow, err := store.Append(state.WorkflowDiscovered, state.WorkflowData{Name: "history-test"})
	if err != nil {
		t.Fatal(err)
	}
	statuses := []string{"running", "waiting", "failed", "completed"}
	expected := make(map[string][]int64, len(statuses))
	for index := 0; index < 26; index++ {
		for statusIndex, status := range statuses {
			source, err := store.Append("history.source", map[string]int{"index": index, "status": statusIndex})
			if err != nil {
				t.Fatal(err)
			}
			claim := state.WorkflowRunData{
				TriggerID: int64(statusIndex + 1), WorkflowID: workflow.ID,
				WorkflowName: "history-test", SourceEventID: source.ID,
			}
			started, err := store.Append(state.WorkflowRunStarted, claim)
			if err != nil {
				t.Fatal(err)
			}
			expected[status] = append(expected[status], started.ID)
			switch status {
			case "waiting":
				_, err = store.Append(state.WorkflowRunWaiting, state.WorkflowRunStateData{RunID: started.ID})
			case "failed":
				_, err = store.Append(state.WorkflowRunFailed, claim)
			case "completed":
				if _, err = store.Append(state.WorkflowRunWaiting, state.WorkflowRunStateData{RunID: started.ID}); err == nil {
					_, err = store.Append(state.WorkflowRunResumed, state.WorkflowRunStateData{RunID: started.ID})
				}
				if err == nil {
					_, err = store.Append(state.WorkflowRunCompleted, claim)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	checkpoint, err := store.LastID()
	if err != nil {
		t.Fatal(err)
	}

	for _, status := range statuses {
		want := slices.Clone(expected[status])
		slices.Reverse(want)
		page, pageCheckpoint, err := store.History(status, 0, 5)
		if err != nil {
			t.Fatal(err)
		}
		if pageCheckpoint != checkpoint {
			t.Fatalf("%s checkpoint = %d, want %d", status, pageCheckpoint, checkpoint)
		}
		if got := runIDs(page); !slices.Equal(got, want[:5]) {
			t.Fatalf("%s newest IDs = %v, want %v", status, got, want[:5])
		}
		for _, run := range page {
			if run.Status != status {
				t.Fatalf("%s query returned run %d with status %q", status, run.ID, run.Status)
			}
		}

		first, _, err := store.History(status, 0, 25)
		if err != nil {
			t.Fatal(err)
		}
		second, _, err := store.History(status, first[len(first)-1].ID, 25)
		if err != nil {
			t.Fatal(err)
		}
		combined := append(runIDs(first), runIDs(second)...)
		if !slices.Equal(combined, want) {
			t.Fatalf("%s paged IDs = %v, want %v", status, combined, want)
		}
		seen := make(map[int64]bool, len(combined))
		for _, id := range combined {
			if seen[id] {
				t.Fatalf("%s pagination duplicated run %d", status, id)
			}
			seen[id] = true
		}
	}
}

func runIDs(runs []state.WorkflowRun) []int64 {
	ids := make([]int64, len(runs))
	for index, run := range runs {
		ids[index] = run.ID
	}
	return ids
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	value, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	return value
}

func testEvent(id int64, eventType string, at time.Time, payload any) eventwire.Event {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return eventwire.Event{ID: id, Type: eventType, At: at, Data: data}
}

func stringPointer(value string) *string { return &value }
