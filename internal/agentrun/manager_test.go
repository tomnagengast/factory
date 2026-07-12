package agentrun

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/eventwire"
)

type fakeLauncher struct {
	prepareErr   error
	cleanupErr   error
	cleanupCalls int
	starts       []Run
	sessions     map[string]bool
	results      map[string]ProcessResult
}

type noopCollector struct{}

func (noopCollector) Collect(context.Context, []Run) error { return nil }

func (f *fakeLauncher) Prepare(context.Context) error {
	return f.prepareErr
}

func (f *fakeLauncher) CleanupWorktrees(context.Context) error {
	f.cleanupCalls++
	return f.cleanupErr
}

func (f *fakeLauncher) Start(_ context.Context, run Run, sessionName, runDirectory string) error {
	f.starts = append(f.starts, run)
	if f.sessions == nil {
		f.sessions = make(map[string]bool)
	}
	f.sessions[sessionName] = true
	return nil
}

func (f *fakeLauncher) SessionExists(_ context.Context, sessionName string) (bool, error) {
	return f.sessions[sessionName], nil
}

func (f *fakeLauncher) ReadResult(runDirectory string) (ProcessResult, error) {
	result, ok := f.results[runDirectory]
	if !ok {
		return ProcessResult{}, errors.New("not found")
	}
	return result, nil
}

func TestManagerStartsPendingRunAndRecordsCompletion(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	run, _, err := store.Claim(Trigger{
		DeliveryID:      "delivery-1",
		IssueIdentifier: "ENG-123",
		Kind:            "linear-comment",
	}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	launcher := &fakeLauncher{
		cleanupErr: errors.New("cleanup unavailable"),
		sessions:   make(map[string]bool),
		results:    make(map[string]ProcessResult),
	}
	stateRoot := t.TempDir()
	manager := newTestManager(t, store, launcher, stateRoot, func() time.Time { return now })
	manager.reconcile(context.Background())

	if len(launcher.starts) != 1 || launcher.starts[0].ID != run.ID {
		t.Fatalf("starts = %#v", launcher.starts)
	}
	if launcher.cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", launcher.cleanupCalls)
	}
	running := store.Snapshot().Runs[0]
	if running.State != StateRunning || running.SessionName != "factory-eng-123" {
		t.Fatalf("running = %#v", running)
	}

	launcher.sessions[running.SessionName] = false
	finishedAt := now.Add(time.Minute)
	launcher.results[running.RunDirectory] = ProcessResult{
		Status:     string(StateSucceeded),
		Attempts:   1,
		FinishedAt: finishedAt,
	}
	manager.reconcile(context.Background())
	finished := store.Snapshot().Runs[0]
	if finished.State != StateSucceeded || finished.FinishedAt == nil || !finished.FinishedAt.Equal(finishedAt) {
		t.Fatalf("finished = %#v", finished)
	}
}

func TestManagerLeavesPendingRunWhenWorkspacePreparationFails(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	_, _, err := store.Claim(Trigger{
		DeliveryID:      "delivery-1",
		IssueIdentifier: "ENG-123",
		Kind:            "linear-comment",
	}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	launcher := &fakeLauncher{prepareErr: errors.New("offline")}
	manager := newTestManager(t, store, launcher, t.TempDir(), func() time.Time { return now })
	manager.reconcile(context.Background())

	if got := store.Snapshot().Runs[0].State; got != StatePending {
		t.Fatalf("state = %q, want %q", got, StatePending)
	}
	if len(launcher.starts) != 0 {
		t.Fatalf("starts = %#v", launcher.starts)
	}
}

func TestManagerHonorsConcurrencyLimit(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	for i, issue := range []string{"ENG-123", "ENG-124"} {
		_, _, err := store.Claim(Trigger{
			DeliveryID:      "delivery-" + strconv.Itoa(i+1),
			IssueIdentifier: issue,
			Kind:            "linear-comment",
		}, now)
		if err != nil {
			t.Fatalf("claim %s: %v", issue, err)
		}
	}
	launcher := &fakeLauncher{sessions: make(map[string]bool)}
	manager := newTestManager(t, store, launcher, t.TempDir(), func() time.Time { return now })
	manager.maxConcurrent = 1
	manager.reconcile(context.Background())

	if len(launcher.starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(launcher.starts))
	}
	if got := store.Snapshot().Active; got != 2 {
		t.Fatalf("active runs = %d, want 2 including one pending", got)
	}
}

func TestManagerSkipsWorktreeCleanupWhileRunIsActive(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	active, _, err := store.Claim(Trigger{
		DeliveryID:      "delivery-1",
		IssueIdentifier: "ENG-123",
		Kind:            "linear-comment",
	}, now)
	if err != nil {
		t.Fatalf("claim active: %v", err)
	}
	runDirectory := filepath.Join(t.TempDir(), active.ID)
	if err := store.MarkStarting(active.ID, "factory-eng-123", runDirectory, now); err != nil {
		t.Fatalf("mark active starting: %v", err)
	}
	if err := store.MarkRunning(active.ID, 1, now); err != nil {
		t.Fatalf("mark active running: %v", err)
	}
	_, _, err = store.Claim(Trigger{
		DeliveryID:      "delivery-2",
		IssueIdentifier: "ENG-124",
		Kind:            "linear-comment",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim pending: %v", err)
	}
	launcher := &fakeLauncher{sessions: map[string]bool{"factory-eng-123": true}}
	manager := newTestManager(t, store, launcher, t.TempDir(), func() time.Time { return now })
	manager.reconcile(context.Background())

	if launcher.cleanupCalls != 0 {
		t.Fatalf("cleanup calls = %d, want 0", launcher.cleanupCalls)
	}
	if len(launcher.starts) != 1 || launcher.starts[0].IssueIdentifier != "ENG-124" {
		t.Fatalf("starts = %#v", launcher.starts)
	}
}

func TestManagerCollectsFinalAgentOutputAndTerminalTransition(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	store, err := Open(filepath.Join(stateRoot, "data", "runs.json"), 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	run, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	journal, err := eventwire.Open(filepath.Join(stateRoot, "data", "events.jsonl"), 100, nil)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	collector, err := NewCollector(store, wire, stateRoot, filepath.Join(stateRoot, "data", "offsets.json"))
	if err != nil {
		t.Fatalf("new collector: %v", err)
	}
	launcher := &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult)}
	manager, err := NewManager(store, launcher, collector, stateRoot, 1, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	manager.reconcile(context.Background())
	running, _ := store.Find(run.ID)
	if err := os.MkdirAll(running.RunDirectory, 0o700); err != nil {
		t.Fatalf("create run directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(running.RunDirectory, "attempt-1-events.jsonl"), []byte("{\"type\":\"result\"}\n"), 0o600); err != nil {
		t.Fatalf("write final output: %v", err)
	}
	launcher.sessions[running.SessionName] = false
	launcher.results[running.RunDirectory] = ProcessResult{Status: string(StateSucceeded), Attempts: 1, FinishedAt: now.Add(time.Minute)}
	manager.reconcile(context.Background())

	_, _, _, records := journal.Snapshot()
	foundAudit, foundTerminal := false, false
	for _, record := range records {
		foundAudit = foundAudit || record.Event.Type == "agent-record" && record.Event.Action == "result"
		foundTerminal = foundTerminal || record.Event.Type == "agent-run" && record.Event.Action == string(StateSucceeded)
	}
	if !foundAudit || !foundTerminal {
		t.Fatalf("final records = %#v", records)
	}
}

func newTestManager(
	t *testing.T,
	store *Store,
	launcher Launcher,
	stateRoot string,
	now func() time.Time,
) *Manager {
	t.Helper()
	manager, err := NewManager(
		store,
		launcher,
		noopCollector{},
		filepath.Clean(stateRoot),
		3,
		time.Second,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		now,
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}
