package eventwire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var eventTestNow = time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)

func TestJournalPersistsGlobalAndChannelCursors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := Open(path, 10, map[string]uint64{"github": 3})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	github := testEvent("github:one", SourceGitHub, "check_run")
	github.Channels = []string{"github"}
	record, added, err := journal.Add(github)
	if err != nil || !added {
		t.Fatalf("add = %#v, %t, %v", record, added, err)
	}
	if got := record.ChannelSequences["github"]; got != 4 {
		t.Fatalf("channel sequence = %d, want 4", got)
	}
	factory := testEvent("factory:one", SourceFactory, "service")
	second, _, err := journal.Add(factory)
	if err != nil {
		t.Fatalf("add factory: %v", err)
	}
	if err := journal.Acknowledge(second.Sequence, map[string]uint64{"github": 4}); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	batch, err := Read(path, Filter{}, 0)
	if err != nil || batch.Cursor != 2 || len(batch.Events) != 2 {
		t.Fatalf("global batch = %#v, %v", batch, err)
	}
	channel, err := ReadChannel(path, "github", Filter{Source: SourceGitHub}, 3)
	if err != nil || channel.Cursor != 4 || len(channel.Events) != 1 || channel.Events[0].ID != github.ID {
		t.Fatalf("channel batch = %#v, %v", channel, err)
	}

	reopened, err := Open(path, 10, map[string]uint64{"github": 6})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	third := testEvent("github:two", SourceGitHub, "pull_request")
	third.Channels = []string{"github"}
	record, _, err = reopened.Add(third)
	if err != nil || record.ChannelSequences["github"] != 7 {
		t.Fatalf("fast-forwarded record = %#v, %v", record, err)
	}
}

func TestJournalRecoversIncompleteTailAndSameProcessShortWrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := Open(path, 10, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := journal.Add(testEvent("factory:one", SourceFactory, "service")); err != nil {
		t.Fatalf("add: %v", err)
	}
	originalWrite := journal.write
	failed := false
	journal.write = func(file *os.File, data []byte) (int, error) {
		if failed {
			return originalWrite(file, data)
		}
		failed = true
		return file.Write(data[:len(data)/2])
	}
	if _, _, err := journal.Add(testEvent("factory:short", SourceFactory, "service")); err == nil {
		t.Fatal("short write succeeded")
	}
	journal.write = originalWrite
	if _, _, err := journal.Add(testEvent("factory:two", SourceFactory, "service")); err != nil {
		t.Fatalf("add after rollback: %v", err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open tail: %v", err)
	}
	if _, err := file.WriteString(`{"kind":"event"`); err != nil {
		t.Fatalf("write tail: %v", err)
	}
	file.Close()
	batch, err := Read(path, Filter{}, 0)
	if err != nil || len(batch.Events) != 2 {
		t.Fatalf("read with partial tail = %#v, %v", batch, err)
	}
	reopened, err := Open(path, 10, nil)
	if err != nil {
		t.Fatalf("recover tail: %v", err)
	}
	if _, _, err := reopened.Add(testEvent("factory:three", SourceFactory, "service")); err != nil {
		t.Fatalf("add after recovery: %v", err)
	}
}

func TestWireCatchesUpWithoutProviderRedelivery(t *testing.T) {
	t.Parallel()
	journal, err := Open(filepath.Join(t.TempDir(), "events.jsonl"), 10, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wire, err := New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	calls := make(map[string]int)
	if err := wire.Handle(Filter{Source: SourceGitHub}, func(_ context.Context, record Record) error {
		calls[record.Event.ID]++
		if record.Event.ID == "github:one" && calls[record.Event.ID] == 1 {
			return errors.New("projection unavailable")
		}
		return nil
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	first := testEvent("github:one", SourceGitHub, "check_run")
	if _, _, err := wire.Publish(context.Background(), first); err == nil {
		t.Fatal("first publish succeeded")
	}
	second := testEvent("github:two", SourceGitHub, "pull_request")
	if _, _, err := wire.Publish(context.Background(), second); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if calls[first.ID] != 2 || calls[second.ID] != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	_, dispatched, _, _ := journal.Snapshot()
	if dispatched != 2 {
		t.Fatalf("dispatched = %d, want 2", dispatched)
	}
}

func TestJournalCompactionRetainsUnacknowledgedRecords(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := Open(path, 2, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 5; i++ {
		event := testEvent("factory:"+string(rune('a'+i)), SourceFactory, "agent")
		if _, _, err := journal.Add(event); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	_, _, _, records := journal.Snapshot()
	if len(records) != 5 {
		t.Fatalf("unacknowledged records = %d, want 5", len(records))
	}
	if err := journal.Acknowledge(5, nil); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	journal.mu.Lock()
	if err := journal.compactLocked(); err != nil {
		journal.mu.Unlock()
		t.Fatalf("compact: %v", err)
	}
	journal.mu.Unlock()
	_, _, _, records = journal.Snapshot()
	if len(records) != 2 || records[0].Sequence != 4 {
		t.Fatalf("compacted records = %#v", records)
	}
}

func testEvent(id string, source Source, eventType string) Event {
	return Event{
		ID:         id,
		Source:     source,
		Type:       eventType,
		Action:     "received",
		ReceivedAt: eventTestNow,
	}
}
