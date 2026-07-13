package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

type recordingPublisher struct {
	mu     sync.Mutex
	events []eventwire.Event
	notify chan struct{}
}

func TestRecoverEventWireGatesRuntimeUntilCatchUp(t *testing.T) {
	t.Parallel()
	journal, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"), 10, nil)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	var attempts atomic.Int32
	if err := wire.Handle(eventwire.Filter{Source: eventwire.SourceFactory}, func(context.Context, eventwire.Record) error {
		if attempts.Add(1) < 3 {
			return errors.New("temporary projection failure")
		}
		return nil
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	event := eventwire.Event{
		ID: "factory:test:recovery", Source: eventwire.SourceFactory, Type: "service",
		Action: "started", Subject: "factory", ReceivedAt: time.Now().UTC(),
	}
	if _, _, err := wire.Publish(context.Background(), event); err == nil {
		t.Fatal("initial publication succeeded")
	}
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recoverEventWire(ctx, wire, time.Millisecond, func() error {
		if pending := wire.Status().Pending; pending != 0 {
			t.Errorf("runtime started with %d pending events", pending)
		}
		close(ready)
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("runtime did not start after catch-up")
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("dispatch attempts = %d, want 3", got)
	}
}

func (p *recordingPublisher) PublishBatch(_ context.Context, events []eventwire.Event) ([]eventwire.Record, error) {
	p.mu.Lock()
	p.events = append(p.events, events...)
	p.mu.Unlock()
	select {
	case p.notify <- struct{}{}:
	default:
	}
	return nil, nil
}

func TestServiceLifecycleEvents(t *testing.T) {
	t.Parallel()

	publisher := &recordingPublisher{notify: make(chan struct{}, 4)}
	startedAt := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	if err := publishServiceEvent(context.Background(), publisher, "started", startedAt, startedAt); err != nil {
		t.Fatalf("publish started: %v", err)
	}
	<-publisher.notify

	ctx, cancel := context.WithCancel(context.Background())
	go publishServiceHeartbeats(ctx, publisher, startedAt, time.Millisecond, func() time.Time {
		return startedAt.Add(time.Second)
	})
	select {
	case <-publisher.notify:
	case <-time.After(time.Second):
		t.Fatal("heartbeat was not published")
	}
	cancel()
	if err := publishServiceEvent(context.Background(), publisher, "stopping", startedAt, startedAt.Add(2*time.Second)); err != nil {
		t.Fatalf("publish stopping: %v", err)
	}

	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	actions := map[string]bool{}
	for _, event := range publisher.events {
		actions[event.Action] = true
		if event.Type != "service" || event.Source != eventwire.SourceFactory || event.Attributes["pid"][0] == "" {
			t.Fatalf("service event = %#v", event)
		}
	}
	for _, action := range []string{"started", "heartbeat", "stopping"} {
		if !actions[action] {
			t.Fatalf("missing %s event: %#v", action, publisher.events)
		}
	}
}
