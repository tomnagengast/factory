package agentrun

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	ready        map[string]ReadyCheckpoint
}

type noopCollector struct{}

func (noopCollector) Collect(context.Context, []Run) error { return nil }

type permissiveTerminalValidator struct {
	now func() time.Time
}

func (v permissiveTerminalValidator) Validate(_ context.Context, _ Run, result ProcessResult) CompletionDecision {
	state := State(result.Status)
	return CompletionDecision{
		State:  state,
		Detail: result.Detail,
		Validation: CompletionValidation{
			Accepted:    true,
			Intent:      result.Status,
			State:       state,
			Reason:      "accepted by test validator",
			ValidatedAt: v.now(),
		},
	}
}

type completeTestEvidence struct{}

func (completeTestEvidence) ReadCompletionEvidence(context.Context, Run, PullRequestSnapshot) (CompletionEvidence, error) {
	return CompletionEvidence{
		Deployment:         DeploymentReceipt{Status: "success", DeploymentID: "deploy-test", SourceCommit: "378bfbbc26c0951a91bfc2db1e30c167b87bfa7b"},
		SourceValid:        true,
		MergeContained:     true,
		HealthMatches:      true,
		RemoteBranchAbsent: true,
		WorktreeAbsent:     true,
		LinearComplete:     true,
		ChildrenComplete:   true,
	}, nil
}

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

func (f *fakeLauncher) ReadReadyCheckpoint(runDirectory string) (ReadyCheckpoint, error) {
	checkpoint, ok := f.ready[runDirectory]
	if !ok {
		return ReadyCheckpoint{}, errors.New("not found")
	}
	return checkpoint, nil
}

type fakePullRequestReader struct {
	snapshot PullRequestSnapshot
	matches  []PullRequestSnapshot
	err      error
}

func (f fakePullRequestReader) Snapshot(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error) {
	snapshot := f.snapshot
	if snapshot.BaseBranch == "" {
		snapshot.BaseBranch = "main"
	}
	return snapshot, f.err
}

func (f fakePullRequestReader) MatchingIssuePullRequests(context.Context, string, string) ([]PullRequestSnapshot, error) {
	return f.matches, f.err
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
	manager, err := NewManager(store, launcher, collector, fakePullRequestReader{}, permissiveTerminalValidator{now: func() time.Time { return now }}, testLifecycleConfig(), stateRoot, 1, time.Second, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
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

func TestManagerParksValidatedReadyRun(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	store := openTestStore(t, 10)
	run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	launcher := &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult), ready: make(map[string]ReadyCheckpoint)}
	reader := &fakePullRequestReader{snapshot: PullRequestSnapshot{
		State:      "OPEN",
		HeadBranch: "eng-123-fix",
		HeadOID:    "08c1c678a0b23bbe8e2dc2da1e398583d7e4c416",
	}}
	manager := newTestManagerWithReader(t, store, launcher, t.TempDir(), reader, func() time.Time { return now })
	manager.reconcile(context.Background())
	running, _ := store.Find(run.ID)
	launcher.sessions[running.SessionName] = false
	launcher.results[running.RunDirectory] = ProcessResult{Status: ResultReadyForMerge, Attempts: 1, FinishedAt: now.Add(time.Minute)}
	launcher.ready[running.RunDirectory] = testReadyCheckpoint(run.ID, now)

	manager.reconcile(context.Background())
	parked, _ := store.Find(run.ID)
	if parked.State != StateAwaitingMerge || parked.Ready == nil || parked.NextReconcileAt == nil || parked.FinishedAt != nil {
		t.Fatalf("parked = %#v", parked)
	}
}

func TestManagerRejectsForgedReadyCheckpoint(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	store := openTestStore(t, 10)
	run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	launcher := &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult), ready: make(map[string]ReadyCheckpoint)}
	reader := &fakePullRequestReader{snapshot: PullRequestSnapshot{State: "OPEN", HeadBranch: "other-branch", HeadOID: "08c1c678a0b23bbe8e2dc2da1e398583d7e4c416"}}
	manager := newTestManagerWithReader(t, store, launcher, t.TempDir(), reader, func() time.Time { return now })
	manager.reconcile(context.Background())
	running, _ := store.Find(run.ID)
	launcher.sessions[running.SessionName] = false
	launcher.results[running.RunDirectory] = ProcessResult{Status: ResultReadyForMerge, Attempts: 1, FinishedAt: now.Add(time.Minute)}
	launcher.ready[running.RunDirectory] = testReadyCheckpoint(run.ID, now)

	manager.reconcile(context.Background())
	failed, _ := store.Find(run.ID)
	if failed.State != StateFailed || !strings.Contains(failed.Detail, "head branch") {
		t.Fatalf("failed = %#v", failed)
	}
}

func TestManagerRejectsCheckpointOutsideConfiguredLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ReadyCheckpoint, time.Time)
		want   string
	}{
		{name: "repository", mutate: func(checkpoint *ReadyCheckpoint, _ time.Time) { checkpoint.Repository = "other/repository" }, want: "configured repository"},
		{name: "base", mutate: func(checkpoint *ReadyCheckpoint, _ time.Time) { checkpoint.BaseBranch = "release" }, want: "configured base"},
		{name: "stale", mutate: func(checkpoint *ReadyCheckpoint, now time.Time) { checkpoint.CreatedAt = now.Add(-time.Second) }, want: "predates"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
			store := openTestStore(t, 10)
			run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			launcher := &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult), ready: make(map[string]ReadyCheckpoint)}
			manager := newTestManagerWithReader(t, store, launcher, t.TempDir(), &fakePullRequestReader{snapshot: PullRequestSnapshot{
				State: "OPEN", HeadBranch: "eng-123-fix", HeadOID: "08c1c678a0b23bbe8e2dc2da1e398583d7e4c416",
			}}, func() time.Time { return now })
			manager.reconcile(context.Background())
			running, _ := store.Find(run.ID)
			checkpoint := testReadyCheckpoint(run.ID, now)
			test.mutate(&checkpoint, now)
			launcher.sessions[running.SessionName] = false
			launcher.results[running.RunDirectory] = ProcessResult{Status: ResultReadyForMerge, Attempts: 1, FinishedAt: now.Add(time.Minute)}
			launcher.ready[running.RunDirectory] = checkpoint

			manager.reconcile(context.Background())
			failed, _ := store.Find(run.ID)
			if failed.State != StateFailed || !strings.Contains(failed.Detail, test.want) {
				t.Fatalf("failed = %#v, want detail containing %q", failed, test.want)
			}
		})
	}
}

func TestManagerResumesMergedParkedRun(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	store := openTestStore(t, 10)
	run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), now); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	checkpoint := testReadyCheckpoint(run.ID, now)
	if err := store.MarkAwaitingMerge(run.ID, checkpoint, now, 1, now); err != nil {
		t.Fatalf("mark awaiting: %v", err)
	}
	reopened, err := Open(store.path, 10)
	if err != nil {
		t.Fatalf("reopen parked store: %v", err)
	}
	reader := &fakePullRequestReader{snapshot: PullRequestSnapshot{
		State:          "MERGED",
		HeadBranch:     checkpoint.HeadBranch,
		HeadOID:        checkpoint.VerifiedHeadOID,
		MergeCommitOID: "378bfbbc26c0951a91bfc2db1e30c167b87bfa7b",
	}}
	launcher := &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult)}
	manager := newTestManagerWithReader(t, reopened, launcher, t.TempDir(), reader, func() time.Time { return now })

	manager.reconcile(context.Background())
	resumed, _ := reopened.Find(run.ID)
	if resumed.State != StatePostMergePending || resumed.TriggerKind != TriggerKindPostMerge || resumed.MergeCommitOID == "" {
		t.Fatalf("resumed = %#v", resumed)
	}
}

func TestManagerResumesSameHeadForGitHubRemediationWake(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	store := openTestStore(t, 10)
	run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), now); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	checkpoint := testReadyCheckpoint(run.ID, now)
	if err := store.MarkAwaitingMerge(run.ID, checkpoint, now, 1, now); err != nil {
		t.Fatalf("mark awaiting: %v", err)
	}
	if _, err := store.SchedulePullRequestReconcile(checkpoint.Repository, checkpoint.PullRequest, checkpoint.HeadBranch, "review-1", 42, true, now); err != nil {
		t.Fatalf("schedule remediation: %v", err)
	}
	reader := &fakePullRequestReader{snapshot: PullRequestSnapshot{
		State:      "OPEN",
		HeadBranch: checkpoint.HeadBranch,
		HeadOID:    checkpoint.VerifiedHeadOID,
	}}
	manager := newTestManagerWithReader(t, store, &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult)}, t.TempDir(), reader, func() time.Time { return now })

	manager.reconcile(context.Background())
	resumed, _ := store.Find(run.ID)
	if resumed.State != StatePending || resumed.TriggerKind != TriggerKindGitHub || resumed.ResumeCount != 1 || resumed.RemediationRequested {
		t.Fatalf("resumed = %#v", resumed)
	}
}

func TestManagerPeriodicSweepResumesDroppedSameHeadFeedback(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	store := openTestStore(t, 10)
	run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), now); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	checkpoint := testReadyCheckpoint(run.ID, now)
	checkpoint.PullRequestUpdatedAt = now
	if err := store.MarkAwaitingMerge(run.ID, checkpoint, now, 1, now); err != nil {
		t.Fatalf("mark awaiting: %v", err)
	}
	reader := &fakePullRequestReader{snapshot: PullRequestSnapshot{
		State:      "OPEN",
		HeadBranch: checkpoint.HeadBranch,
		HeadOID:    checkpoint.VerifiedHeadOID,
		UpdatedAt:  now.Add(time.Minute),
	}}
	manager := newTestManagerWithReader(t, store, &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult)}, t.TempDir(), reader, func() time.Time { return now })

	manager.reconcile(context.Background())
	resumed, _ := store.Find(run.ID)
	if resumed.State != StatePending || resumed.TriggerKind != TriggerKindGitHub || resumed.ResumeCount != 1 {
		t.Fatalf("resumed = %#v", resumed)
	}
}

func TestManagerRejectsTerminalIntentWhilePullRequestOpen(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	store := openTestStore(t, 10)
	run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), now); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	checkpoint := testReadyCheckpoint(run.ID, now)
	if err := store.MarkAwaitingMerge(run.ID, checkpoint, now, 1, now); err != nil {
		t.Fatalf("mark awaiting: %v", err)
	}
	if err := store.ResumeAwaiting(run.ID, TriggerKindPostMerge, "", "resume", now); err != nil {
		t.Fatalf("resume: %v", err)
	}
	launcher := &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult)}
	reader := &fakePullRequestReader{snapshot: PullRequestSnapshot{State: "OPEN", HeadBranch: checkpoint.HeadBranch, HeadOID: checkpoint.VerifiedHeadOID}}
	manager := newTestManagerWithReader(t, store, launcher, t.TempDir(), reader, func() time.Time { return now })
	manager.reconcile(context.Background())
	running, _ := store.Find(run.ID)
	launcher.sessions[running.SessionName] = false
	launcher.results[running.RunDirectory] = ProcessResult{Status: string(StateBlocked), Attempts: 2, FinishedAt: now.Add(time.Minute)}

	manager.reconcile(context.Background())
	parked, _ := store.Find(run.ID)
	if parked.State != StateAwaitingMerge || parked.FinishedAt != nil || !strings.Contains(parked.Detail, "terminal intent rejected") {
		t.Fatalf("parked = %#v", parked)
	}
}

func TestManagerRequiresVerifiedMergeBeforeTerminalSuccess(t *testing.T) {
	t.Parallel()

	validMergeOID := "378bfbbc26c0951a91bfc2db1e30c167b87bfa7b"
	tests := []struct {
		name     string
		snapshot func(ReadyCheckpoint) PullRequestSnapshot
		want     State
	}{
		{name: "closed unmerged", snapshot: func(checkpoint ReadyCheckpoint) PullRequestSnapshot {
			return PullRequestSnapshot{State: "CLOSED", BaseBranch: checkpoint.BaseBranch, HeadBranch: checkpoint.HeadBranch, HeadOID: checkpoint.VerifiedHeadOID}
		}, want: StateFailed},
		{name: "missing merge commit", snapshot: func(checkpoint ReadyCheckpoint) PullRequestSnapshot {
			return PullRequestSnapshot{State: "MERGED", BaseBranch: checkpoint.BaseBranch, HeadBranch: checkpoint.HeadBranch, HeadOID: checkpoint.VerifiedHeadOID}
		}, want: StateFailed},
		{name: "head mismatch", snapshot: func(checkpoint ReadyCheckpoint) PullRequestSnapshot {
			return PullRequestSnapshot{State: "MERGED", BaseBranch: checkpoint.BaseBranch, HeadBranch: checkpoint.HeadBranch, HeadOID: "18c1c678a0b23bbe8e2dc2da1e398583d7e4c416", MergeCommitOID: validMergeOID}
		}, want: StateFailed},
		{name: "verified merge", snapshot: func(checkpoint ReadyCheckpoint) PullRequestSnapshot {
			return PullRequestSnapshot{State: "MERGED", BaseBranch: checkpoint.BaseBranch, HeadBranch: checkpoint.HeadBranch, HeadOID: checkpoint.VerifiedHeadOID, MergeCommitOID: validMergeOID}
		}, want: StateSucceeded},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
			store := openTestStore(t, 10)
			run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), now); err != nil {
				t.Fatalf("mark starting: %v", err)
			}
			checkpoint := testReadyCheckpoint(run.ID, now)
			if err := store.MarkAwaitingMerge(run.ID, checkpoint, now, 1, now); err != nil {
				t.Fatalf("mark awaiting: %v", err)
			}
			if err := store.ResumeAwaiting(run.ID, TriggerKindPostMerge, "", "resume", now); err != nil {
				t.Fatalf("resume: %v", err)
			}
			launcher := &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult)}
			manager := newTestManagerWithReader(t, store, launcher, t.TempDir(), &fakePullRequestReader{snapshot: test.snapshot(checkpoint)}, func() time.Time { return now })
			manager.reconcile(context.Background())
			running, _ := store.Find(run.ID)
			launcher.sessions[running.SessionName] = false
			launcher.results[running.RunDirectory] = ProcessResult{Status: string(StateSucceeded), Attempts: 2, FinishedAt: now.Add(time.Minute)}

			manager.reconcile(context.Background())
			finished, _ := store.Find(run.ID)
			if finished.State != test.want {
				t.Fatalf("finished = %#v, want state %q", finished, test.want)
			}
		})
	}
}

func TestManagerRejectsStaleSegmentResult(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	store := openTestStore(t, 10)
	run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	launcher := &fakeLauncher{sessions: make(map[string]bool), results: make(map[string]ProcessResult)}
	manager := newTestManager(t, store, launcher, t.TempDir(), func() time.Time { return now })
	manager.reconcile(context.Background())
	running, _ := store.Find(run.ID)
	launcher.sessions[running.SessionName] = false
	launcher.results[running.RunDirectory] = ProcessResult{Status: string(StateSucceeded), Attempts: running.SegmentAttempt, FinishedAt: now.Add(time.Minute)}

	manager.reconcile(context.Background())
	failed, _ := store.Find(run.ID)
	if failed.State != StateFailed || !strings.Contains(failed.Detail, "stale") {
		t.Fatalf("failed = %#v", failed)
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
		&fakePullRequestReader{},
		permissiveTerminalValidator{now: now},
		testLifecycleConfig(),
		filepath.Clean(stateRoot),
		3,
		time.Second,
		time.Minute,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		now,
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func newTestManagerWithReader(
	t *testing.T,
	store *Store,
	launcher Launcher,
	stateRoot string,
	pullRequests PullRequestReader,
	now func() time.Time,
) *Manager {
	t.Helper()
	terminal, err := NewMechanicalCompletionValidator(pullRequests, completeTestEvidence{}, "tomnagengast/network", now)
	if err != nil {
		t.Fatalf("new terminal validator: %v", err)
	}
	manager, err := NewManager(
		store,
		launcher,
		noopCollector{},
		pullRequests,
		terminal,
		testLifecycleConfig(),
		filepath.Clean(stateRoot),
		3,
		time.Second,
		time.Minute,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		now,
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func testLifecycleConfig() LifecycleConfig {
	return LifecycleConfig{Repository: "tomnagengast/network", BaseBranch: "main"}
}
