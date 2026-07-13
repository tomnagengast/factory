package agentrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

func TestCollectorPublishesCompleteAgentRecordsAndLifecycle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := Open(filepath.Join(root, "data", "runs.json"), 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	run, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	runDirectory := runPath(root, run.ID)
	if err := os.MkdirAll(filepath.Join(runDirectory, "children", "review"), 0o700); err != nil {
		t.Fatalf("create run files: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", runDirectory, now.Add(time.Second)); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := store.MarkRunning(run.ID, 1, now.Add(2*time.Second)); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	principal := `{"type":"item.completed","item":{"text":"private principal body"}}` + "\n" + `{"type":"turn.started"`
	if err := os.WriteFile(filepath.Join(runDirectory, "attempt-1-events.jsonl"), []byte(principal), 0o600); err != nil {
		t.Fatalf("write principal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "children", "review", "events.jsonl"), []byte("not json\n"), 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	journalPath := filepath.Join(root, "data", "system-events.jsonl")
	journal, err := eventwire.Open(journalPath, 100, nil)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	collector, err := NewCollector(store, wire, root, filepath.Join(root, "data", "offsets.json"))
	if err != nil {
		t.Fatalf("new collector: %v", err)
	}
	if err := collector.Collect(context.Background(), store.Snapshot().Runs); err != nil {
		t.Fatalf("collect: %v", err)
	}

	_, _, _, records := journal.Snapshot()
	if len(records) != 5 {
		t.Fatalf("records = %#v, want three lifecycle and two audit events", records)
	}
	actions := map[string]bool{}
	for _, record := range records {
		actions[record.Event.Action] = true
	}
	if !actions["item.completed"] || !actions["malformed"] || !actions[string(StateRunning)] {
		t.Fatalf("actions = %#v", actions)
	}
	if transitions := store.Snapshot().Runs[0].Transitions; len(transitions) != 0 {
		t.Fatalf("acknowledged transitions remain: %#v", transitions)
	}
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if strings.Contains(string(data), "private principal body") {
		t.Fatal("normalized journal contains private agent body")
	}

	file, err := os.OpenFile(filepath.Join(runDirectory, "attempt-1-events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open principal append: %v", err)
	}
	if _, err := file.WriteString("}\n"); err != nil {
		t.Fatalf("finish partial line: %v", err)
	}
	file.Close()
	if err := collector.Collect(context.Background(), store.Snapshot().Runs); err != nil {
		t.Fatalf("collect completed tail: %v", err)
	}
	_, _, _, records = journal.Snapshot()
	if len(records) != 6 || records[5].Event.Action != "turn.started" {
		t.Fatalf("records after tail completion = %#v", records)
	}

	reopened, err := NewCollector(store, wire, root, filepath.Join(root, "data", "offsets.json"))
	if err != nil {
		t.Fatalf("reopen collector: %v", err)
	}
	if err := reopened.Collect(context.Background(), store.Snapshot().Runs); err != nil {
		t.Fatalf("recollect: %v", err)
	}
	_, _, _, records = journal.Snapshot()
	if len(records) != 6 {
		t.Fatalf("restart duplicated records: %#v", records)
	}

	if err := os.WriteFile(filepath.Join(runDirectory, "attempt-1-events.jsonl"), []byte("{\"type\":\"assistant\"}\n"), 0o600); err != nil {
		t.Fatalf("replace principal file: %v", err)
	}
	if err := reopened.Collect(context.Background(), store.Snapshot().Runs); err != nil {
		t.Fatalf("collect replaced file: %v", err)
	}
	_, _, _, records = journal.Snapshot()
	if len(records) != 7 || records[6].Event.Action != "assistant" {
		t.Fatalf("records after replacement = %#v", records)
	}
}

func TestCollectorRetainsOutboxWhenPublicationFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := Open(filepath.Join(root, "data", "runs.json"), 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_, _, err = store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, time.Now())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	collector, err := NewCollector(store, failingPublisher{}, root, filepath.Join(root, "data", "offsets.json"))
	if err != nil {
		t.Fatalf("new collector: %v", err)
	}
	if err := collector.Collect(context.Background(), store.Snapshot().Runs); err == nil {
		t.Fatal("collect succeeded with failing publisher")
	}
	if len(store.Snapshot().Runs[0].Transitions) != 1 {
		t.Fatal("failed publication acknowledged lifecycle outbox")
	}
	if _, err := os.Stat(filepath.Join(root, "data", "offsets.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint exists after failed publication: %v", err)
	}
}

type failingPublisher struct{}

func (failingPublisher) PublishBatch(context.Context, []eventwire.Event) ([]eventwire.Record, error) {
	return nil, errors.New("offline")
}
