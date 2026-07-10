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

func openTestStore(t *testing.T, limit int) *Store {
	t.Helper()

	store, err := Open(filepath.Join(t.TempDir(), "runs.json"), limit)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store
}
