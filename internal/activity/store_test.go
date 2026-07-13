package activity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestStorePersistsEventsAndReadsPrivatePayload(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "activity.json")
	store, err := Open(path, 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for i, eventType := range []string{"Issue", "Comment", "Issue"} {
		payload := json.RawMessage(`{"type":"` + eventType + `","private":"ENG-23"}`)
		if _, err := store.AddWithPayload(
			"linear-"+eventType+strconv.Itoa(i),
			Event{Type: eventType, Action: "update", ReceivedAt: time.Unix(int64(i)*3600, 0).UTC()},
			payload,
		); err != nil {
			t.Fatalf("add %s: %v", eventType, err)
		}
	}
	if _, err := store.Add("github:delivery", Event{Type: "github/check_run", Action: "completed", ReceivedAt: time.Unix(10, 0).UTC()}); err != nil {
		t.Fatalf("add GitHub event: %v", err)
	}

	payloadID := "linear-Issue2"
	detail, err := store.StagedPayload(payloadID)
	if err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if string(detail) != `{"type":"Issue","private":"ENG-23"}` {
		t.Fatalf("detail = %s", detail)
	}
	info, err := os.Stat(store.payloadPath(eventID(payloadID)))
	if err != nil {
		t.Fatalf("stat payload: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("payload mode = %o, want %o", got, want)
	}

	reopened, err := Open(path, 10)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if payload, err := reopened.StagedPayload(payloadID); err != nil || string(payload) != string(detail) {
		t.Fatalf("reopened detail: payload=%q err=%v", payload, err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) == "" || strings.Contains(string(body), "ENG-23") {
		t.Fatalf("public index leaked payload: body=%q err=%v", body, err)
	}
}

func TestStoreHandlesHistoricalEventsAndPrunesPayloads(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "activity.json"), 2)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.Add("historical", Event{Type: "Issue", Action: "create", ReceivedAt: time.Unix(1, 0).UTC()}); err != nil {
		t.Fatalf("add historical event: %v", err)
	}
	if _, err := store.StagedPayload("historical"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("historical payload error = %v", err)
	}

	if _, err := store.AddWithPayload("payload-1", Event{Type: "Issue", Action: "update", ReceivedAt: time.Unix(2, 0).UTC()}, []byte(`{"n":1}`)); err != nil {
		t.Fatalf("add first payload: %v", err)
	}
	firstPath := store.payloadPath(eventID("payload-1"))
	if _, err := store.AddWithPayload("payload-2", Event{Type: "Issue", Action: "update", ReceivedAt: time.Unix(3, 0).UTC()}, []byte(`{"n":2}`)); err != nil {
		t.Fatalf("add second payload: %v", err)
	}
	if _, err := store.AddWithPayload("payload-3", Event{Type: "Issue", Action: "update", ReceivedAt: time.Unix(4, 0).UTC()}, []byte(`{"n":3}`)); err != nil {
		t.Fatalf("add third payload: %v", err)
	}
	if _, err := os.Stat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned payload still exists: %v", err)
	}
	if _, err := store.AddWithPayload("invalid", Event{}, []byte(`{"broken"`)); err == nil {
		t.Fatal("invalid payload was accepted")
	}
}
