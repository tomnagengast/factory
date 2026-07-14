package eventwire

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestWireDispatchesOneBatchBeforePerRecordRoutes(t *testing.T) {
	t.Parallel()
	journal, err := Open(filepath.Join(t.TempDir(), "events.jsonl"), 10, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wire, err := New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	var order []string
	if err := wire.HandleBatch(func(_ context.Context, records []Record) error {
		order = append(order, "batch")
		if len(records) != 3 || records[0].Sequence != 1 || records[2].Sequence != 3 {
			t.Fatalf("batch = %#v", records)
		}
		records[0].Event.Attributes = map[string][]string{"mutated": {"true"}}
		return nil
	}); err != nil {
		t.Fatalf("handle batch: %v", err)
	}
	if err := wire.Handle(Filter{}, func(_ context.Context, record Record) error {
		if record.Event.Has("mutated", "true") {
			t.Fatal("batch handler mutated per-record input")
		}
		order = append(order, record.Event.ID)
		return nil
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	events := []Event{
		testEvent("factory:one", SourceFactory, "service"),
		testEvent("factory:two", SourceFactory, "service"),
		testEvent("factory:three", SourceFactory, "service"),
	}
	if _, err := wire.PublishBatch(context.Background(), events); err != nil {
		t.Fatalf("publish batch: %v", err)
	}
	want := []string{"batch", "factory:one", "factory:two", "factory:three"}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
}

func TestWireBatchFailureLeavesWholePrefixPending(t *testing.T) {
	t.Parallel()
	journal, err := Open(filepath.Join(t.TempDir(), "events.jsonl"), 10, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wire, err := New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	fail := true
	batchCalls := 0
	routeCalls := 0
	if err := wire.HandleBatch(func(context.Context, []Record) error {
		batchCalls++
		if fail {
			fail = false
			return errors.New("routing store unavailable")
		}
		return nil
	}); err != nil {
		t.Fatalf("handle batch: %v", err)
	}
	if err := wire.Handle(Filter{}, func(context.Context, Record) error {
		routeCalls++
		return nil
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	events := []Event{
		testEvent("factory:one", SourceFactory, "service"),
		testEvent("factory:two", SourceFactory, "service"),
	}
	if _, err := wire.PublishBatch(context.Background(), events); err == nil {
		t.Fatal("batch failure was ignored")
	}
	if got := wire.Status(); got.Pending != 2 || got.Dispatched != 0 || routeCalls != 0 {
		t.Fatalf("after failure status=%#v routeCalls=%d", got, routeCalls)
	}
	if err := wire.CatchUp(context.Background()); err != nil {
		t.Fatalf("catch up: %v", err)
	}
	if got := wire.Status(); got.Pending != 0 || got.Dispatched != 2 || batchCalls != 2 || routeCalls != 2 {
		t.Fatalf("after replay status=%#v batchCalls=%d routeCalls=%d", got, batchCalls, routeCalls)
	}
}

func TestWireRejectsNilBatchHandler(t *testing.T) {
	t.Parallel()
	journal, err := Open(filepath.Join(t.TempDir(), "events.jsonl"), 10, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wire, err := New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	if err := wire.HandleBatch(nil); err == nil {
		t.Fatal("nil batch handler was accepted")
	}
}
