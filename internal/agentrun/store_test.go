package agentrun

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCoalescesActiveIssueTriggers(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	first, created, err := store.Claim(Trigger{
		DeliveryID:      "delivery-1",
		IssueIdentifier: "ENG-123",
		Kind:            "linear-comment",
	}, now)
	if err != nil {
		t.Fatalf("claim first trigger: %v", err)
	}
	if !created {
		t.Fatal("first trigger did not create a run")
	}

	duplicate, created, err := store.Claim(Trigger{
		DeliveryID:      "delivery-2",
		IssueIdentifier: "ENG-123",
		Kind:            "linear-comment",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim duplicate trigger: %v", err)
	}
	if created {
		t.Fatal("active issue received a second run")
	}
	if duplicate.ID != first.ID || duplicate.DuplicateTriggers != 1 {
		t.Fatalf("duplicate = %#v, want run %s with one duplicate", duplicate, first.ID)
	}

	snapshot := store.Snapshot()
	if snapshot.Total != 1 || snapshot.Active != 1 || len(snapshot.Runs) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestStoreAllowsNewTriggerAfterTerminalRun(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	first, _, err := store.Claim(Trigger{
		DeliveryID:      "delivery-1",
		IssueIdentifier: "ENG-123",
		Kind:            "linear-comment",
	}, now)
	if err != nil {
		t.Fatalf("claim first trigger: %v", err)
	}
	if err := store.MarkStarting(first.ID, "factory-eng-123", t.TempDir(), now); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := store.MarkRunning(first.ID, 1, now.Add(time.Second)); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := store.Finish(first.ID, StateSucceeded, 1, "green", now.Add(2*time.Second)); err != nil {
		t.Fatalf("finish: %v", err)
	}

	second, created, err := store.Claim(Trigger{
		DeliveryID:      "delivery-2",
		IssueIdentifier: "ENG-123",
		Kind:            "linear-comment",
	}, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("claim second trigger: %v", err)
	}
	if !created || second.ID == first.ID {
		t.Fatalf("second run = %#v, first = %#v", second, first)
	}
}

func TestStoreDoesNotRestartTerminalRunForRetriedDelivery(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	trigger := Trigger{
		DeliveryID:      "delivery-1",
		IssueIdentifier: "ENG-123",
		Kind:            "linear-comment",
	}
	run, _, err := store.Claim(trigger, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), now); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := store.Finish(run.ID, StateFailed, 1, "failed", now.Add(time.Second)); err != nil {
		t.Fatalf("finish: %v", err)
	}

	retried, created, err := store.Claim(trigger, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("claim retry: %v", err)
	}
	if created || retried.ID != run.ID {
		t.Fatalf("retry created a new run: created=%v run=%#v", created, retried)
	}
}

func TestStoreClaimContinuationIgnoresIssueWithoutHistory(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	if _, _, err := store.Claim(Trigger{DeliveryID: "other-label", IssueIdentifier: "ENG-999", Kind: TriggerKindLabel}, now); err != nil {
		t.Fatalf("seed other issue: %v", err)
	}

	run, created, err := store.ClaimContinuation(Trigger{
		DeliveryID:      "comment-1",
		IssueIdentifier: "ENG-123",
		Kind:            TriggerKindComment,
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim continuation: %v", err)
	}
	if created || run.ID != "" {
		t.Fatalf("unmanaged issue continuation = %#v, created=%t", run, created)
	}
	if snapshot := store.Snapshot(); snapshot.Total != 1 || len(snapshot.Runs) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestStoreClaimContinuationStartsAfterTerminalHistory(t *testing.T) {
	t.Parallel()

	for _, terminal := range []State{StateSucceeded, StateBlocked, StateFailed} {
		terminal := terminal
		t.Run(string(terminal), func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 10)
			now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
			prior, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
			if err != nil {
				t.Fatalf("seed run: %v", err)
			}
			if err := store.Finish(prior.ID, terminal, 1, "done", now.Add(time.Second)); err != nil {
				t.Fatalf("finish prior run: %v", err)
			}

			continuation, created, err := store.ClaimContinuation(Trigger{
				DeliveryID:      "comment-1",
				IssueIdentifier: "ENG-123",
				Kind:            TriggerKindComment,
			}, now.Add(2*time.Second))
			if err != nil {
				t.Fatalf("claim continuation: %v", err)
			}
			if !created || continuation.ID == prior.ID || continuation.State != StatePending || continuation.TriggerKind != TriggerKindComment {
				t.Fatalf("continuation = %#v, created=%t", continuation, created)
			}
			if snapshot := store.Snapshot(); snapshot.Total != 2 || snapshot.Active != 1 || len(snapshot.Runs) != 2 {
				t.Fatalf("snapshot = %#v", snapshot)
			}
		})
	}
}

func TestStoreClaimContinuationCoalescesActiveAndDeduplicatesRetry(t *testing.T) {
	t.Parallel()

	for _, activeState := range []State{StatePending, StateStarting, StateRunning} {
		activeState := activeState
		t.Run(string(activeState), func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t, 10)
			now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
			active, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
			if err != nil {
				t.Fatalf("seed active run: %v", err)
			}
			if activeState != StatePending {
				if err := store.MarkStarting(active.ID, "factory-eng-123", t.TempDir(), now); err != nil {
					t.Fatalf("mark starting: %v", err)
				}
			}
			if activeState == StateRunning {
				if err := store.MarkRunning(active.ID, 1, now); err != nil {
					t.Fatalf("mark running: %v", err)
				}
			}

			trigger := Trigger{DeliveryID: "comment-1", IssueIdentifier: "ENG-123", Kind: TriggerKindComment}
			coalesced, created, err := store.ClaimContinuation(trigger, now.Add(time.Second))
			if err != nil {
				t.Fatalf("claim active continuation: %v", err)
			}
			if created || coalesced.ID != active.ID || coalesced.DuplicateTriggers != 1 {
				t.Fatalf("coalesced = %#v, created=%t", coalesced, created)
			}

			retried, created, err := store.ClaimContinuation(trigger, now.Add(2*time.Second))
			if err != nil {
				t.Fatalf("retry continuation: %v", err)
			}
			if created || retried.ID != active.ID || retried.DuplicateTriggers != 1 {
				t.Fatalf("retried = %#v, created=%t", retried, created)
			}
			if snapshot := store.Snapshot(); snapshot.Total != 1 || snapshot.Active != 1 {
				t.Fatalf("snapshot = %#v", snapshot)
			}
		})
	}
}

func TestStorePersistsPrivateAndPublicState(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runs.json")
	store, err := Open(path, 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	run, _, err := store.Claim(Trigger{
		DeliveryID:      "delivery-1",
		IssueIdentifier: "ENG-123",
		Kind:            "linear-comment",
	}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	reopened, err := Open(path, 10)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	private := reopened.Snapshot()
	if len(private.Runs) != 1 || private.Runs[0].IssueIdentifier != "ENG-123" {
		t.Fatalf("private snapshot = %#v", private)
	}
	public := reopened.PublicSnapshot()
	if len(public.Runs) != 1 || public.Runs[0].ID != run.ID {
		t.Fatalf("public snapshot = %#v", public)
	}
}

func TestStoreFindsNewestRunByIssueAndStartedMillisecond(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 123456789, time.UTC)
	first, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, now)
	if err != nil {
		t.Fatalf("claim first run: %v", err)
	}
	if err := store.MarkStarting(first.ID, "factory-eng-123", t.TempDir(), now); err != nil {
		t.Fatalf("mark first run starting: %v", err)
	}
	if err := store.MarkRunning(first.ID, 1, now); err != nil {
		t.Fatalf("mark first run running: %v", err)
	}
	if err := store.Finish(first.ID, StateSucceeded, 1, "done", now.Add(time.Second)); err != nil {
		t.Fatalf("finish first run: %v", err)
	}

	second, _, err := store.Claim(Trigger{DeliveryID: "delivery-2", IssueIdentifier: "ENG-123", Kind: "test"}, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("claim second run: %v", err)
	}
	if err := store.MarkStarting(second.ID, "factory-eng-123", t.TempDir(), now.Add(2*time.Second)); err != nil {
		t.Fatalf("mark second run starting: %v", err)
	}
	if err := store.MarkRunning(second.ID, 1, now); err != nil {
		t.Fatalf("mark second run running: %v", err)
	}

	found, ok := store.FindStarted("ENG-123", now.UnixMilli())
	if !ok || found.ID != second.ID {
		t.Fatalf("found = %#v, ok = %t, want newest run %s", found, ok, second.ID)
	}
	if _, ok := store.FindStarted("ENG-999", now.UnixMilli()); ok {
		t.Fatal("found run for wrong issue")
	}
	if !ValidIssueIdentifier("ENG-123") || ValidIssueIdentifier("eng-123") {
		t.Fatal("issue identifier validation mismatch")
	}

	activity := store.ActivitySnapshot()
	if activity.Total != 2 || activity.Active != 1 || len(activity.Runs) != 2 || activity.Runs[0].IssueIdentifier != "ENG-123" {
		t.Fatalf("activity snapshot = %#v", activity)
	}
	if activity.Runs[0].StartedAt == nil || activity.Runs[0].StartedAt.UnixMilli() != now.UnixMilli() {
		t.Fatalf("activity run = %#v", activity.Runs[0])
	}
}

func TestStorePersistsAndAcknowledgesLifecycleTransitions(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	run, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), now.Add(time.Second)); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := store.MarkRunning(run.ID, 1, now.Add(2*time.Second)); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := store.Finish(run.ID, StateSucceeded, 1, "done", now.Add(3*time.Second)); err != nil {
		t.Fatalf("finish: %v", err)
	}

	transitions := store.Snapshot().Runs[0].Transitions
	if len(transitions) != 4 {
		t.Fatalf("transitions = %#v, want four", transitions)
	}
	for i, want := range []State{StatePending, StateStarting, StateRunning, StateSucceeded} {
		if transitions[i].State != want || transitions[i].ID == "" {
			t.Fatalf("transition %d = %#v, want state %q", i, transitions[i], want)
		}
	}
	if err := store.AcknowledgeTransitions([]string{transitions[0].ID, transitions[1].ID}); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	remaining := store.Snapshot().Runs[0].Transitions
	if len(remaining) != 2 || remaining[0].State != StateRunning {
		t.Fatalf("remaining transitions = %#v", remaining)
	}
}

func TestStoreParksAndCoalescesAwaitingMergeRun(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), now); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := store.MarkRunning(run.ID, 1, now); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	checkpoint := testReadyCheckpoint(run.ID, now)
	if err := store.MarkAwaitingMerge(run.ID, checkpoint, now.Add(time.Minute), 1, now); err != nil {
		t.Fatalf("mark awaiting merge: %v", err)
	}

	coalesced, created, err := store.ClaimContinuation(Trigger{
		DeliveryID:      "comment-1",
		IssueIdentifier: "ENG-123",
		Kind:            TriggerKindComment,
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("coalesce comment: %v", err)
	}
	if created || coalesced.ID != run.ID || coalesced.State != StatePending || coalesced.TriggerKind != TriggerKindComment || coalesced.NextReconcileAt != nil || coalesced.ResumeCount != 1 {
		t.Fatalf("coalesced run = %#v, created=%t", coalesced, created)
	}
	if snapshot := store.Snapshot(); snapshot.Total != 1 || snapshot.Active != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestStoreSchedulesMatchingPullRequestAndResumesOnce(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 10)
	now := time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC)
	run, _, err := store.Claim(Trigger{DeliveryID: "label-1", IssueIdentifier: "ENG-123", Kind: TriggerKindLabel}, now)
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), now); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	checkpoint := testReadyCheckpoint(run.ID, now)
	if err := store.MarkAwaitingMerge(run.ID, checkpoint, now.Add(time.Hour), 1, now); err != nil {
		t.Fatalf("mark awaiting merge: %v", err)
	}

	scheduled, err := store.SchedulePullRequestReconcile(checkpoint.Repository, checkpoint.PullRequest, checkpoint.HeadBranch, "github-1", now.Add(time.Second))
	if err != nil || !scheduled {
		t.Fatalf("schedule = %t, err = %v", scheduled, err)
	}
	if scheduled, err := store.SchedulePullRequestReconcile(checkpoint.Repository, 99, checkpoint.HeadBranch, "github-2", now.Add(2*time.Second)); err != nil || scheduled {
		t.Fatalf("nonmatching schedule = %t, err = %v", scheduled, err)
	}
	if err := store.ResumeAwaiting(run.ID, TriggerKindPostMerge, "378bfbbc26c0951a91bfc2db1e30c167b87bfa7b", "merged", now.Add(3*time.Second)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	resumed, _ := store.Find(run.ID)
	if resumed.State != StatePending || resumed.TriggerKind != TriggerKindPostMerge || resumed.ResumeCount != 1 || resumed.MergeCommitOID == "" {
		t.Fatalf("resumed = %#v", resumed)
	}
}

func testReadyCheckpoint(runID string, now time.Time) ReadyCheckpoint {
	return ReadyCheckpoint{
		ContractVersion: LifecycleContractVersion,
		RunID:           runID,
		Repository:      "tomnagengast/network",
		PullRequest:     8,
		BaseBranch:      "main",
		HeadBranch:      "eng-123-fix",
		VerifiedHeadOID: "08c1c678a0b23bbe8e2dc2da1e398583d7e4c416",
		CreatedAt:       now,
	}
}

func TestStorePruningRetainsRunsWithPendingTransitions(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, 1)
	now := time.Date(2026, time.July, 10, 9, 0, 0, 0, time.UTC)
	first, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, now)
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	if err := store.Finish(first.ID, StateSucceeded, 0, "done", now.Add(time.Second)); err != nil {
		t.Fatalf("finish first: %v", err)
	}
	second, _, err := store.Claim(Trigger{DeliveryID: "delivery-2", IssueIdentifier: "ENG-124", Kind: "test"}, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("claim second: %v", err)
	}
	if len(store.Snapshot().Runs) != 2 {
		t.Fatal("run with pending transitions was pruned")
	}
	firstRun, _ := store.Find(first.ID)
	ids := make([]string, len(firstRun.Transitions))
	for i, transition := range firstRun.Transitions {
		ids[i] = transition.ID
	}
	if err := store.AcknowledgeTransitions(ids); err != nil {
		t.Fatalf("acknowledge first transitions: %v", err)
	}
	runs := store.Snapshot().Runs
	if len(runs) != 1 || runs[0].ID != second.ID {
		t.Fatalf("runs after acknowledgment = %#v", runs)
	}
}

func openTestStore(t *testing.T, limit int) *Store {
	t.Helper()

	store, err := Open(filepath.Join(t.TempDir(), "runs.json"), limit)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store
}
