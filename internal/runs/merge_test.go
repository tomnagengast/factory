package runs

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- merge-lifecycle fixtures ------------------------------------------

const testVerifiedHeadOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func taskBranchPrefix(t *testing.T, run Run) string {
	t.Helper()
	prefix, err := run.Causation.Task.BranchPrefix()
	if err != nil {
		t.Fatalf("branch prefix: %v", err)
	}
	return prefix
}

// readyCheckpointFor builds a valid worker ready checkpoint bound to a Run's
// immutable task and repository route.
func readyCheckpointFor(t *testing.T, run Run, headSuffix, verifiedHeadOID string) ReadyCheckpoint {
	t.Helper()
	return ReadyCheckpoint{
		ContractVersion: readyContractVersion, RunID: run.ID, Task: run.Causation.Task,
		Repository: run.Repository.Repository, PullRequest: 42, BaseBranch: run.Repository.DefaultBranch,
		HeadBranch: taskBranchPrefix(t, run) + headSuffix, VerifiedHeadOID: verifiedHeadOID,
		CreatedAt: run.UpdatedAt,
	}
}

// openSnapshot mirrors a clean, non-draft open pull request that matches a
// checkpoint exactly.
func openSnapshot(checkpoint ReadyCheckpoint) PullRequestSnapshot {
	return PullRequestSnapshot{
		Number: checkpoint.PullRequest, State: "OPEN", BaseBranch: checkpoint.BaseBranch,
		HeadBranch: checkpoint.HeadBranch, HeadOID: checkpoint.VerifiedHeadOID, UpdatedAt: checkpoint.CreatedAt,
	}
}

// parkWorker wires a running Run's worker to report a ready-for-merge checkpoint.
func (h *managerHarness) parkWorker(installed Run, checkpoint ReadyCheckpoint) {
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: ResultReadyForMerge, Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.launcher.checkpoint = func(string) (ReadyCheckpoint, error) { return checkpoint, nil }
}

// installAwaiting installs one Run already parked in awaiting_human_merge.
func (h *managerHarness) installAwaiting(customize func(*Run)) Run {
	h.t.Helper()
	batch, run, rate := runningProjection(h.t, h.stateRoot)
	awaiting := awaitingProjection(run)
	if customize != nil {
		customize(&awaiting)
	}
	h.install(batch, awaiting, rate)
	return h.run(awaiting.ID)
}

// runningReadyProjection is a running Run with deterministic worker identity and
// an already-held ready checkpoint, so it can resume, restart, and re-park.
func runningReadyProjection(t *testing.T, root string, number int) (AdmissionBatch, Run, RateBucket) {
	t.Helper()
	batch, run, rate := testAdmissionProjection(t, root, number, StatePending)
	startingAt := run.CreatedAt.Add(time.Second)
	runningAt := run.CreatedAt.Add(2 * time.Second)
	run.State = StateRunning
	run.SessionName = taskSessionName(run)
	run.RunDirectory = runPath(root, run.ID)
	run.Attempts = 1
	run.SegmentAttempt = 0
	run.UpdatedAt = runningAt
	run.StartedAt = pointerTime(runningAt)
	run.SegmentStartedAt = pointerTime(startingAt)
	run.Transitions = append(run.Transitions,
		LifecycleTransition{ID: run.ID + ":starting", State: StateStarting, At: startingAt},
		LifecycleTransition{ID: run.ID + ":running", State: StateRunning, Attempts: 1, At: runningAt},
	)
	run.DeliveredThrough = len(run.Transitions)
	prefix := "factory-task-" + strconv.Itoa(number) + "-"
	run.Ready = &ReadyCheckpoint{
		ContractVersion: readyContractVersion, RunID: run.ID, Task: run.Causation.Task,
		Repository: run.Repository.Repository, PullRequest: 18, BaseBranch: run.Repository.DefaultBranch,
		HeadBranch: prefix + "held", VerifiedHeadOID: testVerifiedHeadOID,
		CreatedAt: startingAt.Add(500 * time.Millisecond), ValidatedAt: runningAt,
	}
	return batch, run, rate
}

// ---- park matrix -------------------------------------------------------

func TestManagerParksReadyRunToAwaitingMerge(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningProjection(t, h.stateRoot)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	checkpoint := readyCheckpointFor(t, installed, "park", testVerifiedHeadOID)
	h.parkWorker(installed, checkpoint)
	h.pullRequests.fn = func(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error) {
		snapshot := openSnapshot(checkpoint)
		snapshot.UpdatedAt = h.clock
		return snapshot, nil
	}

	h.reconcile()
	parked := h.run(run.ID)
	if parked.State != StateAwaitingHumanMerge {
		t.Fatalf("state = %q, want awaiting_human_merge", parked.State)
	}
	if parked.Ready == nil || parked.Ready.HeadBranch != checkpoint.HeadBranch || parked.Ready.Repository != installed.Repository.Repository {
		t.Fatalf("ready checkpoint = %+v", parked.Ready)
	}
	if !parked.Ready.ValidatedAt.Equal(parked.UpdatedAt) {
		t.Fatalf("ready ValidatedAt %s != park time %s", parked.Ready.ValidatedAt, parked.UpdatedAt)
	}
	if !parked.Ready.PullRequestUpdatedAt.Equal(h.clock.UTC()) {
		t.Fatalf("ready PullRequestUpdatedAt = %s, want %s", parked.Ready.PullRequestUpdatedAt, h.clock.UTC())
	}
	if parked.GitHub.NextReconcileAt == nil || !parked.GitHub.NextReconcileAt.Equal(parked.UpdatedAt.Add(h.mergeInterval)) {
		t.Fatalf("next reconcile = %v, want %s", parked.GitHub.NextReconcileAt, parked.UpdatedAt.Add(h.mergeInterval))
	}
	if parked.GitHub.ReconcileFailures != 0 || parked.GitHub.RemediationRequested || parked.Detail != "waiting for human merge" {
		t.Fatalf("park counters = %+v detail=%q", parked.GitHub, parked.Detail)
	}
}

func TestManagerParkRejectsUnmatchedPullRequest(t *testing.T) {
	cases := map[string]func(ReadyCheckpoint) PullRequestSnapshot{
		"draft": func(cp ReadyCheckpoint) PullRequestSnapshot {
			s := openSnapshot(cp)
			s.IsDraft = true
			return s
		},
		"base mismatch": func(cp ReadyCheckpoint) PullRequestSnapshot {
			s := openSnapshot(cp)
			s.BaseBranch = "release"
			return s
		},
		"head branch mismatch": func(cp ReadyCheckpoint) PullRequestSnapshot {
			s := openSnapshot(cp)
			s.HeadBranch = cp.HeadBranch + "-other"
			return s
		},
		"verified head mismatch": func(cp ReadyCheckpoint) PullRequestSnapshot {
			s := openSnapshot(cp)
			s.HeadOID = strings.Repeat("c", 40)
			return s
		},
		"unknown state": func(cp ReadyCheckpoint) PullRequestSnapshot {
			s := openSnapshot(cp)
			s.State = "LOCKED"
			return s
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			h := newManagerHarness(t, 2)
			batch, run, rate := runningProjection(t, h.stateRoot)
			h.install(batch, run, rate)
			installed := h.run(run.ID)
			checkpoint := readyCheckpointFor(t, installed, "park", testVerifiedHeadOID)
			h.parkWorker(installed, checkpoint)
			h.pullRequests.fn = func(_ context.Context, cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return build(cp), nil
			}

			h.reconcile()
			failed := h.run(run.ID)
			if failed.State != StateFailed || !strings.Contains(failed.Detail, "invalid ready checkpoint") {
				t.Fatalf("state = %q detail = %q", failed.State, failed.Detail)
			}
		})
	}
}

func TestManagerParkResumesMergeAndCloseRaces(t *testing.T) {
	mergeOID := strings.Repeat("b", 40)
	cases := map[string]struct {
		state    string
		mergeOID string
		wantOID  string
	}{
		"merged while parking":          {state: "MERGED", mergeOID: mergeOID, wantOID: mergeOID},
		"malformed merge while parking": {state: "MERGED", mergeOID: "not-an-oid", wantOID: ""},
		"closed while parking":          {state: "CLOSED", mergeOID: "", wantOID: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newManagerHarness(t, 2)
			batch, run, rate := runningProjection(t, h.stateRoot)
			h.install(batch, run, rate)
			installed := h.run(run.ID)
			checkpoint := readyCheckpointFor(t, installed, "race", testVerifiedHeadOID)
			h.parkWorker(installed, checkpoint)
			h.pullRequests.fn = func(_ context.Context, cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{
					Number: cp.PullRequest, State: tc.state, BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch,
					HeadOID: cp.VerifiedHeadOID, MergeCommitOID: tc.mergeOID, UpdatedAt: h.clock,
				}, nil
			}

			h.reconcile()
			resumed := h.run(run.ID)
			if resumed.State != StatePostMergePending {
				t.Fatalf("state = %q, want post_merge_pending", resumed.State)
			}
			if resumed.MergeCommitOID != tc.wantOID {
				t.Fatalf("merge OID = %q, want %q", resumed.MergeCommitOID, tc.wantOID)
			}
			if resumed.ResumeCount != 1 || resumed.TriggerKind != triggerKindPostMerge {
				t.Fatalf("resume count = %d trigger = %q", resumed.ResumeCount, resumed.TriggerKind)
			}
			if resumed.Ready == nil {
				t.Fatal("post-merge resume dropped the ready checkpoint")
			}
		})
	}
}

// ---- ready-refresh -----------------------------------------------------

func TestManagerDefersReadyRefreshOnTransientError(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningProjection(t, h.stateRoot)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	checkpoint := readyCheckpointFor(t, installed, "defer", testVerifiedHeadOID)
	h.parkWorker(installed, checkpoint)
	h.pullRequests.fn = func(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error) {
		return PullRequestSnapshot{}, errors.New("GitHub CLI timeout")
	}

	h.reconcile()
	deferred := h.run(run.ID)
	if deferred.State != StateRunning {
		t.Fatalf("state = %q, want running (defer stays active)", deferred.State)
	}
	if deferred.GitHub.ReconcileFailures != 1 {
		t.Fatalf("reconcile failures = %d, want 1", deferred.GitHub.ReconcileFailures)
	}
	wantNext := deferred.UpdatedAt.Add(reconcileDelay(h.pollInterval, 0))
	if deferred.GitHub.NextReconcileAt == nil || !deferred.GitHub.NextReconcileAt.Equal(wantNext) {
		t.Fatalf("next reconcile = %v, want %s", deferred.GitHub.NextReconcileAt, wantNext)
	}
	if !strings.Contains(deferred.Detail, "ready checkpoint refresh failed") {
		t.Fatalf("detail = %q", deferred.Detail)
	}
	// A same-state reconcile schedule mints no lifecycle transition or outbox
	// delivery: the history and its delivery obligations are untouched.
	if !slices.Equal(deferred.Transitions, installed.Transitions) || !slices.Equal(deferred.TransitionDeliveries, installed.TransitionDeliveries) || deferred.DeliveredThrough != installed.DeliveredThrough {
		t.Fatalf("defer changed the outbox: transitions=%d deliveries=%d watermark=%d", len(deferred.Transitions), len(deferred.TransitionDeliveries), deferred.DeliveredThrough)
	}
}

func TestManagerFailsUnreadableAndUnboundCheckpoint(t *testing.T) {
	t.Run("checkpoint read error", func(t *testing.T) {
		h := newManagerHarness(t, 2)
		batch, run, rate := runningProjection(t, h.stateRoot)
		h.install(batch, run, rate)
		installed := h.run(run.ID)
		h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
		h.launcher.result = func(string) (ProcessResult, error) {
			return ProcessResult{Status: ResultReadyForMerge, Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
		}
		h.launcher.checkpoint = func(string) (ReadyCheckpoint, error) {
			return ReadyCheckpoint{}, errors.New("checkpoint file missing")
		}

		h.reconcile()
		failed := h.run(run.ID)
		if failed.State != StateFailed || !strings.Contains(failed.Detail, "invalid ready checkpoint") {
			t.Fatalf("state = %q detail = %q", failed.State, failed.Detail)
		}
	})

	t.Run("checkpoint predates segment", func(t *testing.T) {
		h := newManagerHarness(t, 2)
		batch, run, rate := runningProjection(t, h.stateRoot)
		h.install(batch, run, rate)
		installed := h.run(run.ID)
		checkpoint := readyCheckpointFor(t, installed, "old", testVerifiedHeadOID)
		checkpoint.CreatedAt = installed.SegmentStartedAt.Add(-time.Second)
		h.parkWorker(installed, checkpoint)

		h.reconcile()
		failed := h.run(run.ID)
		if failed.State != StateFailed || !strings.Contains(failed.Detail, "invalid ready checkpoint") {
			t.Fatalf("state = %q detail = %q", failed.State, failed.Detail)
		}
	})
}

// ---- awaiting-merge poll matrix ----------------------------------------

func TestManagerReconcilesAwaitingMerge(t *testing.T) {
	mergeOID := strings.Repeat("d", 40)
	type expect struct {
		state    LifecycleState
		mergeOID string
		trigger  string
		failures int
	}
	cases := []struct {
		name      string
		customize func(*Run)
		snapshot  func(ReadyCheckpoint) (PullRequestSnapshot, error)
		want      expect
	}{
		{
			name: "open unchanged defers reset",
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "OPEN", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID}, nil
			},
			want: expect{state: StateAwaitingHumanMerge, failures: 0},
		},
		{
			name:      "open updated resumes",
			customize: func(r *Run) { r.Ready.PullRequestUpdatedAt = r.UpdatedAt },
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "OPEN", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID, UpdatedAt: cp.PullRequestUpdatedAt.Add(time.Hour)}, nil
			},
			want: expect{state: StatePending, trigger: triggerKindGitHub},
		},
		{
			name: "open draft resumes",
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "OPEN", IsDraft: true, BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID}, nil
			},
			want: expect{state: StatePending, trigger: triggerKindGitHub},
		},
		{
			name: "open head change resumes",
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "OPEN", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: strings.Repeat("e", 40)}, nil
			},
			want: expect{state: StatePending, trigger: triggerKindGitHub},
		},
		{
			name: "open safeguard regression resumes",
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "OPEN", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID, SafeguardRegression: true}, nil
			},
			want: expect{state: StatePending, trigger: triggerKindGitHub},
		},
		{
			name:      "remediation requested resumes",
			customize: func(r *Run) { r.GitHub.RemediationRequested = true },
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "OPEN", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID}, nil
			},
			want: expect{state: StatePending, trigger: triggerKindGitHub},
		},
		{
			name: "merged with commit resumes post-merge",
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "MERGED", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID, MergeCommitOID: mergeOID}, nil
			},
			want: expect{state: StatePostMergePending, mergeOID: mergeOID, trigger: triggerKindPostMerge},
		},
		{
			name: "merged missing commit resumes post-merge",
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "MERGED", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID}, nil
			},
			want: expect{state: StatePostMergePending, mergeOID: "", trigger: triggerKindPostMerge},
		},
		{
			name: "merged malformed commit resumes for blocker review",
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "MERGED", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID, MergeCommitOID: "not-an-oid"}, nil
			},
			want: expect{state: StatePostMergePending, mergeOID: "", trigger: triggerKindPostMerge},
		},
		{
			name: "closed resumes post-merge",
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "CLOSED", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID}, nil
			},
			want: expect{state: StatePostMergePending, trigger: triggerKindPostMerge},
		},
		{
			name: "unknown state defers increment",
			snapshot: func(cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{Number: cp.PullRequest, State: "QUEUED", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID}, nil
			},
			want: expect{state: StateAwaitingHumanMerge, failures: 1},
		},
		{
			name: "transient error defers increment",
			snapshot: func(ReadyCheckpoint) (PullRequestSnapshot, error) {
				return PullRequestSnapshot{}, errors.New("timeout")
			},
			want: expect{state: StateAwaitingHumanMerge, failures: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newManagerHarness(t, 2)
			run := h.installAwaiting(tc.customize)
			h.pullRequests.fn = func(_ context.Context, cp ReadyCheckpoint) (PullRequestSnapshot, error) {
				return tc.snapshot(cp)
			}

			h.reconcile()
			got := h.run(run.ID)
			if got.State != tc.want.state {
				t.Fatalf("state = %q, want %q", got.State, tc.want.state)
			}
			if got.MergeCommitOID != tc.want.mergeOID {
				t.Fatalf("merge OID = %q, want %q", got.MergeCommitOID, tc.want.mergeOID)
			}
			if tc.want.trigger != "" && got.TriggerKind != tc.want.trigger {
				t.Fatalf("trigger = %q, want %q", got.TriggerKind, tc.want.trigger)
			}
			if got.State == StateAwaitingHumanMerge && got.GitHub.ReconcileFailures != tc.want.failures {
				t.Fatalf("reconcile failures = %d, want %d", got.GitHub.ReconcileFailures, tc.want.failures)
			}
			if got.State != StateAwaitingHumanMerge && got.ResumeCount != 1 {
				t.Fatalf("resume count = %d, want 1", got.ResumeCount)
			}
			if got.State != StateAwaitingHumanMerge && got.GitHub.NextReconcileAt != nil {
				t.Fatalf("resumed Run kept a reconcile timer: %v", got.GitHub.NextReconcileAt)
			}
		})
	}
}

func TestManagerGatesAwaitingPollUntilTimerElapses(t *testing.T) {
	h := newManagerHarness(t, 2)
	future := h.clock.Add(time.Hour)
	run := h.installAwaiting(func(r *Run) { r.GitHub.NextReconcileAt = pointerTime(future) })
	polled := false
	h.pullRequests.fn = func(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error) {
		polled = true
		return PullRequestSnapshot{}, errors.New("should not poll")
	}

	h.reconcile()
	if polled {
		t.Fatal("awaiting poll ran before its reconcile timer elapsed")
	}
	if got := h.run(run.ID); got.State != StateAwaitingHumanMerge || got.UpdatedAt != run.UpdatedAt {
		t.Fatalf("gated awaiting Run changed: %+v", got)
	}
}

// ---- post-merge start retry backoff ------------------------------------

func TestManagerPostMergeStartRetryBacksOff(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := postMergePendingProjection(t, h.stateRoot, 1)
	h.install(batch, run, rate)
	h.launcher.start = func(context.Context, Run, string, string) error { return errors.New("tmux unavailable") }

	h.reconcile()
	deferred := h.run(run.ID)
	if deferred.State != StatePostMergePending {
		t.Fatalf("state = %q, want post_merge_pending", deferred.State)
	}
	if deferred.GitHub.ReconcileFailures != 1 {
		t.Fatalf("reconcile failures = %d, want 1", deferred.GitHub.ReconcileFailures)
	}
	wantNext := deferred.UpdatedAt.Add(reconcileDelay(h.pollInterval, 0))
	if deferred.GitHub.NextReconcileAt == nil || !deferred.GitHub.NextReconcileAt.Equal(wantNext) {
		t.Fatalf("next reconcile = %v, want %s", deferred.GitHub.NextReconcileAt, wantNext)
	}
	if deferred.SegmentStartedAt != nil || deferred.SessionName != taskSessionName(deferred) {
		t.Fatalf("post-merge retry lost deterministic identity: segment=%v session=%q", deferred.SegmentStartedAt, deferred.SessionName)
	}

	// The backoff timer gates the next start attempt until it elapses.
	started := false
	h.launcher.start = func(context.Context, Run, string, string) error { started = true; return nil }
	h.reconcile()
	if started {
		t.Fatal("start attempted before the post-merge backoff elapsed")
	}
	if got := h.run(run.ID).State; got != StatePostMergePending {
		t.Fatalf("gated post-merge state = %q, want post_merge_pending", got)
	}

	// Once the timer elapses the Run starts.
	h.clock = *deferred.GitHub.NextReconcileAt
	h.reconcile()
	if got := h.run(run.ID).State; got != StateRunning {
		t.Fatalf("post-backoff state = %q, want running", got)
	}
}

// ---- rejected-terminal re-park -----------------------------------------

func TestManagerReparksRejectedTerminal(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningReadyProjection(t, h.stateRoot, 1)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateSucceeded), Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.terminal.fn = func(context.Context, Run, ProcessResult) TerminalDecision {
		return TerminalDecision{
			State: StateFailed, Repark: true,
			Validation: CompletionValidation{
				Accepted: false, Intent: "succeeded", State: StateFailed, Reason: "pull request is still open",
				PullRequestState: "OPEN", PullRequestHead: strings.Repeat("f", 40), MergeCommitOID: strings.Repeat("9", 40),
			},
		}
	}

	h.reconcile()
	reparked := h.run(run.ID)
	if reparked.State != StateAwaitingHumanMerge {
		t.Fatalf("state = %q, want awaiting_human_merge", reparked.State)
	}
	if reparked.Ready == nil || reparked.Ready.HeadBranch != installed.Ready.HeadBranch {
		t.Fatalf("re-park changed the ready checkpoint: %+v", reparked.Ready)
	}
	if reparked.Completion == nil || reparked.Completion.Accepted {
		t.Fatalf("re-park completion = %+v, want unaccepted", reparked.Completion)
	}
	if reparked.Completion.MergeCommitOID != "" || reparked.Completion.PullRequestHead != "" {
		t.Fatalf("re-park completion leaked conflicting merge identity: %+v", reparked.Completion)
	}
	if reparked.TerminalRejection != "pull request is still open" {
		t.Fatalf("terminal rejection = %q", reparked.TerminalRejection)
	}
	wantNext := reparked.UpdatedAt.Add(reconcileDelay(h.mergeInterval, installed.ResumeCount))
	if reparked.GitHub.NextReconcileAt == nil || !reparked.GitHub.NextReconcileAt.Equal(wantNext) {
		t.Fatalf("next reconcile = %v, want %s", reparked.GitHub.NextReconcileAt, wantNext)
	}
}

func TestManagerAcceptedTerminalOverwritesReparkedCompletion(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningReadyProjection(t, h.stateRoot, 1)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	mergeOID := strings.Repeat("b", 40)

	// 1. Worker reports success; the validator rejects and re-parks.
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateSucceeded), Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.terminal.fn = func(context.Context, Run, ProcessResult) TerminalDecision {
		return TerminalDecision{State: StateFailed, Repark: true, Validation: CompletionValidation{
			Accepted: false, Intent: "succeeded", State: StateFailed, Reason: "pull request is still open",
		}}
	}
	h.reconcile()
	if got := h.run(run.ID); got.State != StateAwaitingHumanMerge || got.Completion == nil || got.Completion.Accepted {
		t.Fatalf("re-park did not store an unaccepted completion: %+v", got.Completion)
	}

	// 2. The pull request merges; the parked Run resumes post-merge.
	h.clock = h.clock.Add(time.Hour)
	h.pullRequests.fn = func(_ context.Context, cp ReadyCheckpoint) (PullRequestSnapshot, error) {
		return PullRequestSnapshot{Number: cp.PullRequest, State: "MERGED", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID, MergeCommitOID: mergeOID}, nil
	}
	h.reconcile()
	if got := h.run(run.ID); got.State != StatePostMergePending || got.MergeCommitOID != mergeOID {
		t.Fatalf("post-merge resume = %+v", got)
	}

	// 3. The Run restarts and completes with an accepted successful terminal.
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) { return ProcessResult{}, errors.New("no result yet") }
	h.reconcile() // post_merge_pending -> starting -> running
	running := h.run(run.ID)
	if running.State != StateRunning {
		t.Fatalf("restart state = %q, want running", running.State)
	}
	h.clock = h.clock.Add(time.Minute)
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateSucceeded), Attempts: running.SegmentAttempt + 1, FinishedAt: running.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.terminal.fn = func(_ context.Context, r Run, _ ProcessResult) TerminalDecision {
		return TerminalDecision{State: StateSucceeded, Validation: CompletionValidation{
			Accepted: true, Intent: "succeeded", State: StateSucceeded, Reason: "all mechanical post-merge conditions verified",
			PullRequestState: "MERGED", PullRequestHead: r.Ready.VerifiedHeadOID, MergeCommitOID: r.MergeCommitOID,
		}}
	}
	h.reconcile()
	finished := h.run(run.ID)
	if finished.State != StateSucceeded || finished.Completion == nil || !finished.Completion.Accepted {
		t.Fatalf("final completion = %+v state = %q", finished.Completion, finished.State)
	}
	if finished.Completion.MergeCommitOID != mergeOID {
		t.Fatalf("accepted completion merge OID = %q, want %q", finished.Completion.MergeCommitOID, mergeOID)
	}
}

func TestManagerReparksResumedPostMergeRunWithHistoricalReady(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, runningBeforePark, rate := runningReadyProjection(t, h.stateRoot, 1)
	awaiting := awaitingProjection(runningBeforePark)
	h.install(batch, awaiting, rate)
	installed := h.run(awaiting.ID)
	originalReady := *installed.Ready
	mergeOID := strings.Repeat("b", 40)

	// Resume the parked Run after merge, then start its post-merge segment. The
	// retained checkpoint necessarily predates this new segment.
	h.clock = h.clock.Add(time.Hour)
	h.pullRequests.fn = func(_ context.Context, cp ReadyCheckpoint) (PullRequestSnapshot, error) {
		return PullRequestSnapshot{Number: cp.PullRequest, State: "MERGED", BaseBranch: cp.BaseBranch, HeadBranch: cp.HeadBranch, HeadOID: cp.VerifiedHeadOID, MergeCommitOID: mergeOID}, nil
	}
	h.reconcile()
	if got := h.run(installed.ID).State; got != StatePostMergePending {
		t.Fatalf("merge resume state = %q, want post_merge_pending", got)
	}
	h.reconcile()
	running := h.run(installed.ID)
	if running.State != StateRunning || running.SegmentStartedAt == nil || !running.SegmentStartedAt.After(originalReady.CreatedAt) {
		t.Fatalf("post-merge segment did not advance beyond Ready: state=%q segment=%v ready=%s", running.State, running.SegmentStartedAt, originalReady.CreatedAt)
	}

	// A transient authority failure rejects the terminal intent with Repark=true.
	// The unchanged historical checkpoint must remain valid evidence.
	h.clock = h.clock.Add(time.Minute)
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateSucceeded), Attempts: running.SegmentAttempt + 1, FinishedAt: running.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.terminal.fn = func(context.Context, Run, ProcessResult) TerminalDecision {
		return TerminalDecision{State: StateFailed, Repark: true, Validation: CompletionValidation{
			Accepted: false, Intent: string(StateSucceeded), State: StateFailed,
			Reason: "authoritative pull request refresh failed: timeout",
		}}
	}
	h.reconcile()
	reparked := h.run(installed.ID)
	if reparked.State != StateAwaitingHumanMerge || reparked.Ready == nil || *reparked.Ready != originalReady {
		t.Fatalf("historical Ready was not preserved on repark: state=%q ready=%+v", reparked.State, reparked.Ready)
	}
	if reparked.Completion == nil || reparked.Completion.Accepted {
		t.Fatalf("repark completion = %+v, want unaccepted", reparked.Completion)
	}
}

// ---- validation delta negatives ----------------------------------------

func TestStoreTransitionRejectsForbiddenReadyAndIdentityDeltas(t *testing.T) {
	newAwaiting := func(t *testing.T) (*Store, Run) {
		t.Helper()
		root := trustedTestRoot(t, t.TempDir())
		batch, run, rate := runningProjection(t, root)
		awaiting := awaitingProjection(run)
		snapshot, err := NewSnapshot(testSingleAdmissionModel(batch, awaiting, rate))
		if err != nil {
			t.Fatal(err)
		}
		store, err := Create(root, filepath.Join(root, "runs.jsonl"), snapshot, 16)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store, awaiting
	}
	manual := func(run Run, state LifecycleState, mutate func(*Run)) Run {
		next := run.Clone()
		next.State = state
		next.UpdatedAt = run.UpdatedAt.Add(time.Second)
		if mutate != nil {
			mutate(&next)
		}
		next.Transitions = append(slices.Clone(run.Transitions), LifecycleTransition{
			ID: run.ID + ":neg:" + string(state), State: state, Attempts: next.Attempts, At: next.UpdatedAt,
		})
		return next
	}

	t.Run("clearing ready is rejected", func(t *testing.T) {
		store, awaiting := newAwaiting(t)
		next := manual(awaiting, StatePending, func(r *Run) { r.Ready = nil })
		if err := store.Transition(next); err == nil || !strings.Contains(err.Error(), "ready checkpoint") {
			t.Fatalf("clearing ready error = %v", err)
		}
	})

	t.Run("replacing ready outside awaiting is rejected", func(t *testing.T) {
		store, awaiting := newAwaiting(t)
		next := manual(awaiting, StatePending, func(r *Run) { r.Ready.PullRequest = 999 })
		if err := store.Transition(next); err == nil || !strings.Contains(err.Error(), "ready checkpoint") {
			t.Fatalf("replace-outside-awaiting error = %v", err)
		}
	})

	t.Run("new ready predating segment is rejected", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		batch, running, rate := runningProjection(t, root)
		snapshot, err := NewSnapshot(testSingleAdmissionModel(batch, running, rate))
		if err != nil {
			t.Fatal(err)
		}
		store, err := Create(root, filepath.Join(root, "runs.jsonl"), snapshot, 16)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		checkpoint := readyCheckpointFor(t, running, "stale", testVerifiedHeadOID)
		checkpoint.CreatedAt = running.SegmentStartedAt.Add(-time.Second)
		next := manual(running, StateAwaitingHumanMerge, func(r *Run) { r.Ready = &checkpoint })
		if err := store.Transition(next); err == nil || !strings.Contains(err.Error(), "predates") {
			t.Fatalf("stale new Ready error = %v", err)
		}
	})

	t.Run("route rewrite is rejected", func(t *testing.T) {
		store, awaiting := newAwaiting(t)
		next := manual(awaiting, StateFailed, func(r *Run) {
			route := *r.Repository
			route.DefaultBranch = "release"
			r.Repository = &route
			finished := r.UpdatedAt
			r.FinishedAt = &finished
		})
		if err := store.Transition(next); err == nil || !strings.Contains(err.Error(), "repository route") {
			t.Fatalf("route rewrite error = %v", err)
		}
	})
}

// ---- migrated empty-transitions first schedule -------------------------

func migratedRunningModel(t *testing.T, root string) Model {
	t.Helper()
	batch, run, rate := testAdmissionProjection(t, root, 1, StatePending)
	makeMigratedDirect(&batch, &run)
	run.State = StateRunning
	run.SessionName = taskSessionName(run)
	run.RunDirectory = runPath(root, run.ID)
	run.Attempts = 1
	run.SegmentAttempt = 0
	run.StartedAt = pointerTime(run.CreatedAt)
	run.SegmentStartedAt = pointerTime(run.CreatedAt)
	run.UpdatedAt = run.CreatedAt
	run.Transitions = nil
	run.DeliveredThrough = 0
	run.MigratedBaseline = &MigratedBaseline{State: StateRunning, ObservedAt: run.CreatedAt, PriorTransitionsAcknowledged: true}
	model := Model{
		Schema: SchemaVersion, TotalBatches: 1, TotalRuns: 1,
		AdmissionOperations: []AdmissionOperationReceipt{}, AdmissionBatches: []AdmissionBatch{batch},
		Runs: []Run{run}, RateBuckets: []RateBucket{rate},
	}
	model.Migration = testMigrationReceipt(t, model)
	return model
}

func TestManagerMigratedRunTakesFirstSameStateSchedule(t *testing.T) {
	stateRoot := trustedTestRoot(t, t.TempDir())
	model := migratedRunningModel(t, stateRoot)
	snapshot, err := NewSnapshot(model)
	if err != nil {
		t.Fatalf("migrated snapshot: %v", err)
	}
	storeRoot := trustedTestRoot(t, t.TempDir())
	store, err := Create(storeRoot, filepath.Join(storeRoot, "runs.jsonl"), snapshot, 64)
	if err != nil {
		t.Fatalf("create migrated store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := newManagerHarnessWithStore(t, store, stateRoot, 2)

	installed := h.run(model.Runs[0].ID)
	if len(installed.Transitions) != 0 || installed.MigratedBaseline == nil {
		t.Fatalf("migrated run is not an empty-transitions baseline: %+v", installed)
	}
	checkpoint := readyCheckpointFor(t, installed, "migrated", testVerifiedHeadOID)
	checkpoint.CreatedAt = installed.SegmentStartedAt.Add(time.Millisecond)
	h.parkWorker(installed, checkpoint)
	h.pullRequests.fn = func(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error) {
		return PullRequestSnapshot{}, errors.New("GitHub CLI timeout")
	}

	// The very first same-state ScheduleReconcile on a migrated empty-transitions
	// Run must be accepted (the P2 validation relaxation) without minting a
	// transition or an outbox delivery.
	h.reconcile()
	scheduled := h.run(installed.ID)
	if scheduled.State != StateRunning {
		t.Fatalf("state = %q, want running", scheduled.State)
	}
	if scheduled.GitHub.ReconcileFailures != 1 || scheduled.GitHub.NextReconcileAt == nil {
		t.Fatalf("first same-state schedule not applied: %+v", scheduled.GitHub)
	}
	if len(scheduled.Transitions) != 0 || len(scheduled.TransitionDeliveries) != 0 {
		t.Fatalf("migrated same-state schedule minted history/outbox: transitions=%d deliveries=%d", len(scheduled.Transitions), len(scheduled.TransitionDeliveries))
	}
	if !scheduled.UpdatedAt.After(installed.UpdatedAt) {
		t.Fatalf("schedule did not advance UpdatedAt: %s !> %s", scheduled.UpdatedAt, installed.UpdatedAt)
	}
}

// ---- fixed-clock monotonicity ------------------------------------------

func TestManagerMergeTransitionsAdvanceUnderFixedClock(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningProjection(t, h.stateRoot)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	// Pin the clock at the Run's own timestamp so only the monotonic nudge keeps
	// the park transition strictly after the prior running transition.
	h.clock = installed.UpdatedAt
	checkpoint := readyCheckpointFor(t, installed, "fixed", testVerifiedHeadOID)
	checkpoint.CreatedAt = installed.SegmentStartedAt.Add(time.Millisecond)
	h.parkWorker(installed, checkpoint)
	h.pullRequests.fn = func(_ context.Context, cp ReadyCheckpoint) (PullRequestSnapshot, error) {
		return openSnapshot(cp), nil
	}

	h.reconcile()
	parked := h.run(run.ID)
	if parked.State != StateAwaitingHumanMerge {
		t.Fatalf("state = %q, want awaiting_human_merge", parked.State)
	}
	last := parked.Transitions[len(parked.Transitions)-1]
	if !last.At.After(installed.UpdatedAt) || !last.At.Equal(parked.UpdatedAt) {
		t.Fatalf("park transition time %s did not strictly advance past %s", last.At, installed.UpdatedAt)
	}
}
