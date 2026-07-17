package eventwire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWirePublishesWaitsAndReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	wire, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	first, err := wire.Publish(TaskSubmitted, "task-1", "", map[string]string{"prompt": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1", first.Sequence)
	}

	result := make(chan []Event, 1)
	go func() {
		events, _ := wire.Wait(context.Background(), 1)
		result <- events
	}()
	second, err := wire.Publish(RunStarted, "task-1", "run-1", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case events := <-result:
		if len(events) != 1 || events[0].ID != second.ID {
			t.Fatalf("wait events = %#v", events)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not observe the published event")
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
	if len(events) != 2 || events[0].Type != TaskSubmitted || events[1].Type != RunStarted {
		t.Fatalf("replayed events = %#v", events)
	}
}

func TestWireWaitStopsWithContext(t *testing.T) {
	wire, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = wire.Wait(ctx, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestWireRejectsMalformedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"sequence":2}`+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("expected malformed history to fail")
	}
}
