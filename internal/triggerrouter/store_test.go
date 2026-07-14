package triggerrouter

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

func TestWorstCaseRetainedDecisionProjectionCompactsAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	for sequence := 1; sequence <= 10_000; sequence++ {
		eventID := fmt.Sprintf("factory:retained:%05d", sequence)
		outcomes := make([]Outcome, 32)
		for rule := range outcomes {
			outcomes[rule] = Outcome{
				Kind: OutcomeSuppressed, RuleID: fmt.Sprintf("rule-%02d", rule),
				RuleRevision: 1, Reason: "global-outstanding-limit",
			}
		}
		store.decisions[eventID] = Decision{
			EventID: eventID, EventSequence: uint64(sequence), Source: eventwire.SourceFactory,
			RegistryRevision: 1, SettingsRevision: 1, DecidedAt: now, Outcomes: outcomes,
		}
	}
	started := time.Now()
	if err := store.writeCheckpointLocked(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	t.Logf("compacted 10,000 retained events with 32 outcomes each in %s", time.Since(started))
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if snapshot := reopened.Snapshot(); len(snapshot.Decisions) != 10_000 || len(snapshot.Decisions[0].Outcomes) != 32 {
		t.Fatalf("reopened projection = %d decisions", len(snapshot.Decisions))
	}
}

func TestStoreRecoversIncompleteTailButRejectsCompleteOrphan(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "routing.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	configuration, registry := testPolicy()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	if _, err := store.ApplyDecisionBatch([]eventwire.Record{testRecord("factory:one", 1, eventwire.SourceFactory, now)}, registry, configuration, now); err != nil {
		t.Fatalf("apply: %v", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open tail: %v", err)
	}
	if _, err := file.WriteString(`{"kind":"transition"`); err != nil {
		t.Fatalf("write tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close tail: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("recover tail: %v", err)
	}
	if got := reopened.Snapshot(); len(got.Decisions) != 1 || len(got.Invocations) != 2 {
		t.Fatalf("recovered snapshot = %#v", got)
	}

	orphan := Invocation{ID: "orphan", EventID: "factory:orphan", EventSequence: 2, State: StateQueued}
	operation := diskOperation{
		Kind:        operationDecisionBatch,
		Decisions:   []Decision{{EventID: "factory:two", EventSequence: 2, Source: eventwire.SourceFactory, DecidedAt: now, Outcomes: []Outcome{}}},
		Invocations: []Invocation{orphan},
	}
	data, err := json.Marshal(operation)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open corrupt append: %v", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		t.Fatalf("write corrupt append: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close corrupt append: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("complete orphan operation was accepted")
	}
}

func TestTerminalTransitionCompactsPinnedExecutionPayload(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "routing.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	configuration, registry := testPolicy()
	registry.Rules = registry.Rules[:1]
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	decisions, err := store.ApplyDecisionBatch([]eventwire.Record{testRecord("factory:one", 1, eventwire.SourceFactory, now)}, registry, configuration, now)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	id := decisions[0].Outcomes[0].InvocationID
	if _, err := store.TransitionInvocation(id, StateClaiming, "run-1", "", nil, now.Add(time.Second)); err != nil {
		t.Fatalf("claiming: %v", err)
	}
	if _, err := store.TransitionInvocation(id, StateClaimed, "run-1", "", nil, now.Add(2*time.Second)); err != nil {
		t.Fatalf("claimed: %v", err)
	}
	reflected := now.Add(4 * time.Second)
	terminal, err := store.TransitionInvocation(id, StateSucceeded, "run-1", "complete", &reflected, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("succeeded: %v", err)
	}
	if terminal.Rule.ID == "" || terminal.Rule.WorkflowID != "" || terminal.Workflow.ID == "" || len(terminal.Workflow.Steps) != 0 {
		t.Fatalf("terminal payload was not compacted: %#v", terminal)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	terminal, found := reopened.Invocation(id)
	if !found || terminal.Rule.WorkflowID != "" || len(terminal.Workflow.Steps) != 0 {
		t.Fatalf("reopened terminal = %#v, found=%t", terminal, found)
	}
}

func TestAppendFailureRollsBackWithoutProjectionMutation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "routing.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	store.write = func(file *os.File, data []byte) (int, error) {
		written, err := file.Write(data[:len(data)/2])
		return written, errors.Join(err, errors.New("injected write failure"))
	}
	configuration, registry := testPolicy()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	if _, err := store.ApplyDecisionBatch([]eventwire.Record{testRecord("factory:one", 1, eventwire.SourceFactory, now)}, registry, configuration, now); err == nil {
		t.Fatal("write failure was ignored")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) || len(store.Snapshot().Decisions) != 0 {
		t.Fatalf("append rollback failed: before=%q after=%q snapshot=%#v", before, after, store.Snapshot())
	}
}

func TestPruneRequiresWireEvictionAndTerminalInvocation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "routing.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	configuration, registry := testPolicy()
	registry.Rules = registry.Rules[:1]
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	records := []eventwire.Record{
		testRecord("factory:one", 1, eventwire.SourceFactory, now),
		testRecord("factory:two", 2, eventwire.SourceFactory, now),
	}
	decisions, err := store.ApplyDecisionBatch(records, registry, configuration, now)
	if err != nil {
		t.Fatal(err)
	}
	firstID := decisions[0].Outcomes[0].InvocationID
	if err := store.Prune(map[string]bool{"factory:two": true}); err != nil {
		t.Fatalf("prune nonterminal: %v", err)
	}
	if got := store.Snapshot(); len(got.Decisions) != 2 || len(got.Invocations) != 2 {
		t.Fatalf("nonterminal projection pruned: %#v", got)
	}
	if _, err := store.TransitionInvocation(firstID, StateRejected, "", "operator-rejected", nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(map[string]bool{"factory:two": true}); err != nil {
		t.Fatalf("prune terminal: %v", err)
	}
	got := store.Snapshot()
	if len(got.Decisions) != 1 || got.Decisions[0].EventID != "factory:two" || len(got.Invocations) != 1 {
		t.Fatalf("pruned projection = %#v", got)
	}
	if reopened, err := Open(path); err != nil || len(reopened.Snapshot().Decisions) != 1 {
		t.Fatalf("reopen: store=%#v err=%v", reopened, err)
	}
}
