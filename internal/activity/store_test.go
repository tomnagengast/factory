package activity

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsAndDeduplicates(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "activity.json")
	store, err := Open(path, 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	event := Event{Type: "Issue", Action: "update", ReceivedAt: time.Unix(10, 0).UTC()}
	added, err := store.Add("delivery-1", event)
	if err != nil {
		t.Fatalf("add event: %v", err)
	}
	if !added {
		t.Fatal("first delivery was not added")
	}
	added, err = store.Add("delivery-1", event)
	if err != nil {
		t.Fatalf("add duplicate: %v", err)
	}
	if added {
		t.Fatal("duplicate delivery was added")
	}

	reopened, err := Open(path, 10)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	snapshot := reopened.Snapshot()
	if snapshot.Total != 1 {
		t.Fatalf("total = %d, want 1", snapshot.Total)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0] != event {
		t.Fatalf("events = %#v, want %#v", snapshot.Events, []Event{event})
	}
}

func TestStoreKeepsNewestEventsWithinLimit(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "activity.json"), 2)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for i, eventType := range []string{"Issue", "Comment", "Project"} {
		_, err := store.Add(eventType, Event{
			Type:       eventType,
			Action:     "create",
			ReceivedAt: time.Unix(int64(i), 0).UTC(),
		})
		if err != nil {
			t.Fatalf("add %s: %v", eventType, err)
		}
	}

	snapshot := store.Snapshot()
	if snapshot.Total != 3 {
		t.Fatalf("total = %d, want 3", snapshot.Total)
	}
	if got, want := len(snapshot.Events), 2; got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
	if snapshot.Events[0].Type != "Project" || snapshot.Events[1].Type != "Comment" {
		t.Fatalf("events are not newest first: %#v", snapshot.Events)
	}
}
