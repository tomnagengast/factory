package agentrun

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type fakeLauncher struct {
	prepareErr error
	starts     []Run
	sessions   map[string]bool
	results    map[string]ProcessResult
}

func (f *fakeLauncher) Prepare(context.Context) error {
	return f.prepareErr
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
		sessions: make(map[string]bool),
		results:  make(map[string]ProcessResult),
	}
	stateRoot := t.TempDir()
	manager := newTestManager(t, store, launcher, stateRoot, func() time.Time { return now })
	manager.reconcile(context.Background())

	if len(launcher.starts) != 1 || launcher.starts[0].ID != run.ID {
		t.Fatalf("starts = %#v", launcher.starts)
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
