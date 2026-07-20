package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestImportJSONLMatchesLegacyProjection(t *testing.T) {
	at := time.Date(2026, 7, 20, 12, 0, 0, 123456789, time.UTC)
	settings := state.DefaultSettings
	events := []eventwire.Event{
		testEvent(1, state.ProjectCreated, at, state.ProjectData{Name: "Factory", Path: "/factory"}),
		testEvent(2, state.TaskCreated, at.Add(time.Second), state.TaskData{Title: "Cut over", Status: state.Todo, ProjectID: 1}),
		testEvent(3, state.WorkflowDiscovered, at.Add(2*time.Second), state.WorkflowData{Name: "review", Path: stringPointer("/review.js")}),
		testEvent(4, state.TriggerCreated, at.Add(3*time.Second), state.TriggerData{EventType: state.TaskCreated, WorkflowID: 3, Enabled: true}),
		testEvent(5, state.WorkflowRunStarted, at.Add(4*time.Second), state.WorkflowRunData{
			TriggerID: 4, WorkflowID: 3, SourceEventID: 2, Directory: "/factory",
			Source: "/review.js", Settings: &settings, Arguments: json.RawMessage(`{"runId":5}`),
		}),
		testEvent(6, state.WorkflowRunEventRecorded, at.Add(5*time.Second), state.WorkflowRunEventData{
			RunID: 5, Event: json.RawMessage(`{"sequence":1,"at":"2026-07-20T12:00:05Z","type":"runtime.started","workflow":"review"}`),
		}),
		testEvent(7, state.WorkflowRunCompleted, at.Add(6*time.Second), state.WorkflowRunData{TriggerID: 4, WorkflowID: 3, SourceEventID: 2, Output: "done"}),
	}
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if err := json.NewEncoder(file).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	store := openTestStore(t)
	count, err := store.ImportJSONL(context.Background(), path)
	if err != nil || count != int64(len(events)) {
		t.Fatalf("import = %d, %v", count, err)
	}
	legacy, err := state.ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	view, checkpoint, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint != int64(len(events)) {
		t.Fatalf("checkpoint = %d", checkpoint)
	}
	if !reflect.DeepEqual(view.Projects, legacy.Projects) ||
		!reflect.DeepEqual(view.Tasks, legacy.Tasks) ||
		!reflect.DeepEqual(view.Triggers, legacy.Triggers) ||
		!reflect.DeepEqual(view.Workflows, legacy.Workflows) ||
		!reflect.DeepEqual(view.Runs, legacy.Runs) || view.Settings != legacy.Settings {
		t.Fatalf("projection mismatch:\nstore=%#v\nlegacy=%#v", view, legacy)
	}
	runEvents, err := store.RunEvents(5, 0, 200)
	if err != nil || len(runEvents) != 1 || runEvents[0].Sequence != 1 {
		t.Fatalf("run events = %#v, %v", runEvents, err)
	}
	if _, err := store.ImportJSONL(context.Background(), path); err == nil {
		t.Fatal("second import into non-empty database succeeded")
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
