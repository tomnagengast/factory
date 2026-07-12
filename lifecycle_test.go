package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/eventwire"
)

type recordingPublisher struct {
	mu     sync.Mutex
	events []eventwire.Event
	notify chan struct{}
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
