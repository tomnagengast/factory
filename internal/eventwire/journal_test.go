package eventwire

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestCreateRoundTripsCompleteJournalState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	event := testEvent("linear:one", SourceLinear, "Issue")
	event.Channels = []string{"linear"}
	event = canonicalEvent(event)
	rejection := Rejection{Sequence: 1, EventID: event.ID, Source: event.Source, Type: event.Type, Action: event.Action, Reason: "invalid routing", RejectedAt: eventTestNow}
	initial := State{
		Total: 1, ChannelTotals: map[string]uint64{"linear": 4}, Dispatched: 1,
		ChannelAcks: map[string]uint64{"linear": 4}, Records: []Record{{Sequence: 1, ChannelSequences: map[string]uint64{"linear": 4}, Event: event}},
		RejectedTotal: 1, Rejections: []Rejection{rejection},
	}
	journal, err := Create(path, 10, initial)
	if err != nil {
		t.Fatal(err)
	}
	if got := journal.State(); !reflect.DeepEqual(got, initial) {
		t.Fatalf("created state = %#v, want %#v", got, initial)
	}
	if _, err := Create(path, 10, initial); err == nil {
		t.Fatal("second create replaced journal")
	}
	reopened, err := OpenExisting(path, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.State(); !reflect.DeepEqual(got, initial) {
		t.Fatalf("reopened state = %#v, want %#v", got, initial)
	}
}

func TestStrictJournalConstructionRejectsMissingAndInvalidState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := OpenExisting(filepath.Join(root, "missing.jsonl"), 10, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing open error = %v", err)
	}
	invalid := State{Total: 1, Dispatched: 2, ChannelTotals: map[string]uint64{}, ChannelAcks: map[string]uint64{}}
	if _, err := Create(filepath.Join(root, "invalid.jsonl"), 10, invalid); err == nil {
		t.Fatal("invalid state was created")
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

func TestWireRejectsPermanentFailureAndContinues(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := Open(path, 10, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wire, err := New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	wire.now = func() time.Time { return eventTestNow }
	calls := make(map[string]int)
	if err := wire.Handle(Filter{}, func(_ context.Context, record Record) error {
		calls[record.Event.ID]++
		if record.Event.ID == "linear:bad" {
			return Permanent(errors.New("repository is not allowlisted"))
		}
		return nil
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if _, _, err := wire.Publish(context.Background(), testEvent("linear:bad", SourceLinear, "Issue")); err != nil {
		t.Fatalf("publish rejected event: %v", err)
	}
	if _, _, err := wire.Publish(context.Background(), testEvent("linear:good", SourceLinear, "Issue")); err != nil {
		t.Fatalf("publish later event: %v", err)
	}
	status := wire.Status()
	if status.Total != 2 || status.Dispatched != 2 || status.Pending != 0 || status.RejectedTotal != 1 {
		t.Fatalf("status = %#v", status)
	}
	if status.LastRejection == nil || status.LastRejection.EventID != "linear:bad" || status.LastRejection.RejectedAt != eventTestNow {
		t.Fatalf("last rejection = %#v", status.LastRejection)
	}

	reopened, err := Open(path, 10, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	replayed, err := New(reopened)
	if err != nil {
		t.Fatalf("new reopened wire: %v", err)
	}
	if err := replayed.Handle(Filter{}, func(_ context.Context, record Record) error {
		calls[record.Event.ID]++
		return nil
	}); err != nil {
		t.Fatalf("handle reopened: %v", err)
	}
	if err := replayed.CatchUp(context.Background()); err != nil {
		t.Fatalf("catch up: %v", err)
	}
	if calls["linear:bad"] != 1 || calls["linear:good"] != 1 {
		t.Fatalf("calls after reopen = %#v", calls)
	}
	assertCurrentReaderCompatible(t, path)
}

func TestWireLeavesTransientFailurePending(t *testing.T) {
	t.Parallel()
	journal, err := Open(filepath.Join(t.TempDir(), "events.jsonl"), 10, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wire, err := New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	if err := wire.Handle(Filter{}, func(context.Context, Record) error {
		return errors.New("Linear rate limited")
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if _, _, err := wire.Publish(context.Background(), testEvent("linear:retry", SourceLinear, "Issue")); err == nil {
		t.Fatal("transient publish succeeded")
	}
	status := wire.Status()
	if status.Total != 1 || status.Dispatched != 0 || status.Pending != 1 || status.RejectedTotal != 0 {
		t.Fatalf("status = %#v", status)
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

func TestJournalReportsAddedWhenAutomaticCompactionFailsAfterAppend(t *testing.T) {
	journal, err := Open(filepath.Join(t.TempDir(), "events.jsonl"), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"factory:one", "factory:two"} {
		if _, _, err := journal.Add(testEvent(id, SourceFactory, "service")); err != nil {
			t.Fatal(err)
		}
	}
	journal.compact = func() error { return errors.New("injected compaction failure") }
	record, added, err := journal.Add(testEvent("factory:three", SourceFactory, "service"))
	if err == nil || !added || record.Sequence != 3 || record.Event.ID != "factory:three" {
		t.Fatalf("add = %#v, %t, %v", record, added, err)
	}
	status := journal.Status()
	if status.Total != 3 || status.Pending != 3 {
		t.Fatalf("status = %#v", status)
	}
}

func assertCurrentReaderCompatible(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open current-reader fixture: %v", err)
	}
	defer file.Close()
	type currentDiskLine struct {
		Kind        string            `json:"kind"`
		Version     int               `json:"version,omitempty"`
		Dispatched  uint64            `json:"dispatched,omitempty"`
		ChannelAcks map[string]uint64 `json:"channelAcks,omitempty"`
		Record      *Record           `json:"record,omitempty"`
	}
	foundCheckpoint := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line currentDiskLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("current reader decode: %v", err)
		}
		switch line.Kind {
		case "checkpoint":
			if foundCheckpoint || line.Version != 1 {
				t.Fatalf("current reader rejected checkpoint: %#v", line)
			}
			foundCheckpoint = true
		case "event", "ack":
			if !foundCheckpoint {
				t.Fatalf("current reader saw %s before checkpoint", line.Kind)
			}
		default:
			t.Fatalf("current reader rejected line kind %q", line.Kind)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("current reader scan: %v", err)
	}
}

func TestJournalQueryPagesNewestMatchingRecordsAndReturnsRetainedCounts(t *testing.T) {
	journal, err := Open(filepath.Join(t.TempDir(), "events.jsonl"), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 13, 18, 0, 0, 0, time.UTC)
	for _, event := range []Event{
		{ID: "linear-1", Source: SourceLinear, Type: "Issue", Action: "create", ReceivedAt: base},
		{ID: "github-1", Source: SourceGitHub, Type: "pull_request", Action: "opened", ReceivedAt: base.Add(time.Hour)},
		{ID: "future-1", Source: SourceFactory, Type: "future-kind", Action: "observed", ReceivedAt: base.Add(2 * time.Hour)},
		{ID: "linear-2", Source: SourceLinear, Type: "Issue", Action: "update", ReceivedAt: base.Add(3 * time.Hour)},
	} {
		if _, _, err := journal.Add(event); err != nil {
			t.Fatal(err)
		}
	}

	page, err := journal.Query(Query{Source: SourceLinear, Page: 1, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Retained != 4 || page.Matching != 2 || page.PageCount != 2 {
		t.Fatalf("unexpected page metadata: %#v", page)
	}
	if len(page.Records) != 1 || page.Records[0].Event.ID != "linear-2" {
		t.Fatalf("unexpected records: %#v", page.Records)
	}
	if got := page.SourceCounts; len(got) != 3 || got[0] != (Count{Label: "linear", Count: 2}) {
		t.Fatalf("unexpected source counts: %#v", got)
	}
	if got := page.TypeCounts; len(got) != 3 || got[0] != (Count{Label: "Issue", Count: 2}) {
		t.Fatalf("unexpected type counts: %#v", got)
	}
	if len(page.HourCounts) != 4 {
		t.Fatalf("unexpected hour counts: %#v", page.HourCounts)
	}

	record, found := journal.Record(3)
	if !found || record.Event.Type != "future-kind" {
		t.Fatalf("unexpected record lookup: found=%v record=%#v", found, record)
	}
	if _, found := journal.Record(99); found {
		t.Fatal("unexpected missing record match")
	}
}

func TestJournalQueryValidatesPagination(t *testing.T) {
	journal, err := Open(filepath.Join(t.TempDir(), "events.jsonl"), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Query(Query{Page: 0, PageSize: 25}); err == nil {
		t.Fatal("expected invalid page error")
	}
	if _, err := journal.Query(Query{Page: 1, PageSize: 0}); err == nil {
		t.Fatal("expected invalid page size error")
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
