package triggerscheduler

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

type registryStub struct{ snapshot triggerregistry.Snapshot }

func (s *registryStub) Snapshot() triggerregistry.Snapshot { return s.snapshot.Clone() }

type publisherStub struct {
	events []eventwire.Event
	err    error
}

func (s *publisherStub) PublishBatch(_ context.Context, events []eventwire.Event) ([]eventwire.Record, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.events = append(s.events, events...)
	return nil, nil
}

func TestSchedulerPublishesOldestMissedAndAdvancesPastSkipped(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	cursors, err := Open(filepath.Join(directory, "cursors.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	registry := &registryStub{snapshot: triggerregistry.Snapshot{Schema: triggerregistry.SchemaVersion, Schedules: []triggerregistry.Schedule{{
		ID: "hourly", Revision: 1, Name: "Hourly", Enabled: true, Cron: "0 * * * *", Timezone: "UTC",
		Attributes: map[string][]string{"kind": {"maintenance"}},
	}}}}
	publisher := &publisherStub{}
	now := time.Date(2026, 7, 14, 10, 30, 0, 0, time.UTC)
	scheduler, err := New(registry, cursors, publisher, slog.Default(), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := scheduler.Tick(context.Background(), now); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("initialization published %#v", publisher.events)
	}
	now = now.Add(3 * time.Hour)
	if err := scheduler.Tick(context.Background(), now); err != nil {
		t.Fatalf("catch up: %v", err)
	}
	if len(publisher.events) != 1 || publisher.events[0].Attributes[triggerregistry.AttributeScheduledAt][0] != "2026-07-14T11:00:00Z" {
		t.Fatalf("events = %#v", publisher.events)
	}
	cursor, found := cursors.Cursor("hourly")
	if !found || !cursor.LastScheduledAt.Equal(time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)) || cursor.Skipped != 2 {
		t.Fatalf("cursor = %#v, found=%t", cursor, found)
	}
	if err := scheduler.Tick(context.Background(), now); err != nil || len(publisher.events) != 1 {
		t.Fatalf("repeat events=%#v err=%v", publisher.events, err)
	}
}

func TestSchedulerRetriesDeterministicEventWithoutAdvancingOnPublishFailure(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	cursors, _ := Open(filepath.Join(directory, "cursors.json"))
	registry := &registryStub{snapshot: triggerregistry.Snapshot{Schema: triggerregistry.SchemaVersion, Schedules: []triggerregistry.Schedule{{
		ID: "daily", Revision: 1, Name: "Daily", Enabled: true, Cron: "0 8 * * *", Timezone: "America/Los_Angeles",
	}}}}
	publisher := &publisherStub{}
	initial := time.Date(2026, 3, 7, 17, 0, 0, 0, time.UTC)
	scheduler, _ := New(registry, cursors, publisher, slog.Default(), func() time.Time { return initial })
	if err := scheduler.Tick(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 3, 8, 16, 1, 0, 0, time.UTC)
	publisher.err = errors.New("wire unavailable")
	if err := scheduler.Tick(context.Background(), due); err == nil {
		t.Fatal("publish failure was ignored")
	}
	before, _ := cursors.Cursor("daily")
	if !before.LastScheduledAt.Equal(initial) {
		t.Fatalf("cursor advanced on failure: %#v", before)
	}
	publisher.err = nil
	if err := scheduler.Tick(context.Background(), due); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("events = %#v", publisher.events)
	}
	want := scheduledEvent(registry.snapshot.Schedules[0], time.Date(2026, 3, 8, 15, 0, 0, 0, time.UTC), due)
	if publisher.events[0].ID != want.ID {
		t.Fatalf("event ID = %q, want %q", publisher.events[0].ID, want.ID)
	}
}

func TestScheduleRevisionBeginsStrictlyAfterEdit(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	cursors, _ := Open(filepath.Join(directory, "cursors.json"))
	registry := &registryStub{snapshot: triggerregistry.Snapshot{Schema: triggerregistry.SchemaVersion, Schedules: []triggerregistry.Schedule{{
		ID: "hourly", Revision: 1, Name: "Hourly", Enabled: true, Cron: "0 * * * *", Timezone: "UTC",
	}}}}
	publisher := &publisherStub{}
	now := time.Date(2026, 7, 14, 10, 30, 0, 0, time.UTC)
	scheduler, _ := New(registry, cursors, publisher, slog.Default(), func() time.Time { return now })
	_ = scheduler.Tick(context.Background(), now)
	registry.snapshot.Schedules[0].Revision = 2
	now = now.Add(2 * time.Hour)
	if err := scheduler.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("edited schedule backfilled %#v", publisher.events)
	}
	cursor, _ := cursors.Cursor("hourly")
	if cursor.ScheduleRevision != 2 || !cursor.LastScheduledAt.Equal(now) {
		t.Fatalf("cursor = %#v", cursor)
	}
}
