package eventwire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWirePersistsOrderedIntegerEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	wire, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := wire.Publish("task.created", map[string]string{"title": "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := wire.Publish("task.updated", map[string]int64{"id": first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != 1 || second.ID != 2 {
		t.Fatalf("unexpected IDs: %d, %d", first.ID, second.ID)
	}
	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events := reopened.Events(0)
	if len(events) != 2 || events[1].Type != "task.updated" {
		t.Fatalf("unexpected events: %#v", events)
	}
	if event, found := reopened.Event(1); !found || event.Type != "task.created" {
		t.Fatalf("event lookup failed: %#v, %v", event, found)
	}
}

func TestWaitWakesOnPublish(t *testing.T) {
	wire, err := Open(filepath.Join(t.TempDir(), "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()

	result := make(chan []Event, 1)
	go func() {
		events, _ := wire.Wait(context.Background(), 0)
		result <- events
	}()
	if _, err := wire.Publish("project.created", map[string]string{"name": "Factory"}); err != nil {
		t.Fatal(err)
	}
	select {
	case events := <-result:
		if len(events) != 1 || events[0].ID != 1 {
			t.Fatalf("unexpected wake: %#v", events)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not wake")
	}
}

func TestWaitHonorsCancellation(t *testing.T) {
	wire, err := Open(filepath.Join(t.TempDir(), "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := wire.Wait(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestTypesAreDistinctAndSorted(t *testing.T) {
	wire, err := Open(filepath.Join(t.TempDir(), "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	for _, eventType := range []string{"z", "a", "z"} {
		if _, err := wire.Publish(eventType, struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
	types := wire.Types()
	if len(types) != 2 || types[0] != "a" || types[1] != "z" {
		t.Fatalf("unexpected types: %#v", types)
	}
}

func TestPublishIfCurrentRejectsStaleSnapshotWithoutChangingWire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	wire, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()

	first, published, err := wire.PublishIfCurrent(0, "trigger.created", map[string]bool{"enabled": true})
	if err != nil || !published || first.ID != 1 {
		t.Fatalf("conditional publish = %#v, %v, %v", first, published, err)
	}
	if _, err := wire.Publish("trigger.updated", map[string]bool{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rejected, published, err := wire.PublishIfCurrent(1, "workflow.run.started", struct{}{})
	if err != nil || published || rejected.ID != 0 {
		t.Fatalf("stale publish = %#v, %v, %v", rejected, published, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if wire.LastID() != 2 || string(after) != string(before) || len(wire.Events(0)) != 2 {
		t.Fatalf("stale publish changed wire: last ID %d, before %q, after %q", wire.LastID(), before, after)
	}
}
