package runs

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func createPullRequestWakeStore(t *testing.T) (*Store, string, Run) {
	t.Helper()
	root := trustedTestRoot(t, t.TempDir())
	batch, running, rate := runningProjection(t, root)
	awaiting := awaitingProjection(running)
	awaiting.Detail = "waiting for human merge"
	awaiting.GitHub.NextReconcileAt = pointerTime(awaiting.UpdatedAt.Add(time.Hour))
	awaiting.GitHub.ReconcileFailures = 2
	initial, err := NewSnapshot(testSingleAdmissionModel(batch, awaiting, rate))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runs.jsonl")
	store, err := Create(root, path, initial, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path, awaiting
}

func TestPullRequestWakeMatchesAndCoalescesWithoutLifecycleDelivery(t *testing.T) {
	store, path, awaiting := createPullRequestWakeStore(t)
	transitions := slices.Clone(awaiting.Transitions)
	deliveries := slices.Clone(awaiting.TransitionDeliveries)
	refresh := *awaiting.GitHub.LastAuthoritativeRefreshAt

	// An old provider timestamp is nudged past UpdatedAt so the same-state wake
	// stays strictly monotonic while preserving the event cursor as authority.
	matched, err := store.SchedulePullRequestReconcile(
		awaiting.Ready.Repository, awaiting.Ready.PullRequest, awaiting.Ready.HeadBranch,
		"github-wake-1", 42, true, awaiting.CreatedAt,
	)
	if err != nil || !matched {
		t.Fatalf("matching wake = %t, %v", matched, err)
	}
	firstSnapshot, _ := store.Snapshot()
	first := firstSnapshot.Model().Runs[0]
	if firstSnapshot.Model().JournalSequence != 1 || first.State != StateAwaitingHumanMerge ||
		!first.UpdatedAt.After(awaiting.UpdatedAt) || first.GitHub.NextReconcileAt == nil || *first.GitHub.NextReconcileAt != first.UpdatedAt ||
		first.GitHub.LastCursor != 42 || !first.GitHub.RemediationRequested || first.GitHub.ReconcileFailures != 2 ||
		first.GitHub.LastAuthoritativeRefreshAt == nil || *first.GitHub.LastAuthoritativeRefreshAt != refresh ||
		!slices.Contains(first.DeliveryIDs, "github-wake-1") || first.DuplicateDeliveries != uint64(len(first.DeliveryIDs)-1) ||
		!slices.IsSorted(first.DeliveryIDs) || first.Detail != awaiting.Detail {
		t.Fatalf("first pull request wake = %#v", first)
	}
	if !slices.Equal(first.Transitions, transitions) || !slices.Equal(first.TransitionDeliveries, deliveries) ||
		first.DeliveredThrough != awaiting.DeliveredThrough {
		t.Fatalf("wake changed lifecycle/outbox evidence: %#v", first)
	}

	// A branch-only duplicate still wakes and journals like the legacy owner, but
	// cannot regress cursor, clear remediation, or duplicate delivery evidence.
	matched, err = store.SchedulePullRequestReconcile(
		first.Ready.Repository, 0, first.Ready.HeadBranch, "github-wake-1", 41, false, first.UpdatedAt.Add(time.Second),
	)
	if err != nil || !matched {
		t.Fatalf("branch duplicate wake = %t, %v", matched, err)
	}
	secondSnapshot, _ := store.Snapshot()
	second := secondSnapshot.Model().Runs[0]
	if secondSnapshot.Model().JournalSequence != 2 || second.GitHub.LastCursor != 42 || !second.GitHub.RemediationRequested ||
		len(second.DeliveryIDs) != len(first.DeliveryIDs) || second.DuplicateDeliveries != first.DuplicateDeliveries ||
		second.GitHub.NextReconcileAt == nil || *second.GitHub.NextReconcileAt != second.UpdatedAt {
		t.Fatalf("duplicate pull request wake = %#v", second)
	}

	reopened, err := Open(filepath.Dir(path), path, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, err := reopened.Snapshot()
	if err != nil || !reflect.DeepEqual(replayed.Model(), secondSnapshot.Model()) {
		t.Fatalf("replayed pull request wakes = %#v, %v", replayed.Model(), err)
	}
}

func TestPullRequestWakeNonmatchAndInvalidIntentAreReadOnly(t *testing.T) {
	store, path, awaiting := createPullRequestWakeStore(t)
	before, _ := store.Snapshot()
	beforeDisk, _ := os.ReadFile(path)

	matched, err := store.SchedulePullRequestReconcile(
		awaiting.Ready.Repository, awaiting.Ready.PullRequest+1, awaiting.Ready.HeadBranch,
		"github-nonmatch", 43, false, awaiting.UpdatedAt.Add(time.Second),
	)
	if err != nil || matched {
		t.Fatalf("nonmatching wake = %t, %v", matched, err)
	}
	after, _ := store.Snapshot()
	afterDisk, _ := os.ReadFile(path)
	if !reflect.DeepEqual(after.Model(), before.Model()) || !bytes.Equal(afterDisk, beforeDisk) {
		t.Fatal("nonmatching wake changed projection or journal")
	}

	valid := PullRequestWake{
		Repository: awaiting.Ready.Repository, PullRequest: awaiting.Ready.PullRequest,
		DeliveryID: "github-valid", Cursor: 44, At: awaiting.UpdatedAt.Add(time.Second),
	}
	tests := []struct {
		name   string
		mutate func(*PullRequestWake)
	}{
		{name: "repository", mutate: func(w *PullRequestWake) { w.Repository = "invalid" }},
		{name: "negative pull request", mutate: func(w *PullRequestWake) { w.PullRequest = -1 }},
		{name: "missing pull request and branch", mutate: func(w *PullRequestWake) { w.PullRequest = 0 }},
		{name: "invalid branch", mutate: func(w *PullRequestWake) { w.PullRequest = 0; w.HeadBranch = "bad..branch" }},
		{name: "delivery", mutate: func(w *PullRequestWake) { w.DeliveryID = "" }},
		{name: "cursor", mutate: func(w *PullRequestWake) { w.Cursor = 0 }},
		{name: "time", mutate: func(w *PullRequestWake) { w.At = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wake := valid
			test.mutate(&wake)
			matched, err := store.SchedulePullRequestReconcile(
				wake.Repository, wake.PullRequest, wake.HeadBranch, wake.DeliveryID, wake.Cursor, wake.RemediationRequested, wake.At,
			)
			if err == nil || matched {
				t.Fatalf("invalid wake = %t, %v", matched, err)
			}
			got, _ := store.Snapshot()
			if !reflect.DeepEqual(got.Model(), before.Model()) {
				t.Fatal("invalid wake changed projection")
			}
		})
	}
}

func TestPullRequestWakeJournalVersionRollbackAndPoison(t *testing.T) {
	t.Run("old operation version is rejected", func(t *testing.T) {
		store, path, awaiting := createPullRequestWakeStore(t)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		wake := PullRequestWake{
			Repository: awaiting.Ready.Repository, PullRequest: awaiting.Ready.PullRequest,
			DeliveryID: "github-old-version", Cursor: 45, At: awaiting.UpdatedAt.Add(time.Second),
		}
		operation := diskOperation{Kind: operationPullRequestWake, Version: JournalVersion - 1, Sequence: 1, PullRequestWake: &wake}
		data, err := json.Marshal(operation)
		if err != nil {
			t.Fatal(err)
		}
		if err := appendBytes(path, append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(filepath.Dir(path), path, 64); err == nil || !strings.Contains(err.Error(), "journal version") {
			t.Fatalf("old wake journal version error = %v", err)
		}
	})

	t.Run("append rollback preserves projection", func(t *testing.T) {
		store, path, awaiting := createPullRequestWakeStore(t)
		before, _ := store.Snapshot()
		beforeDisk, _ := os.ReadFile(path)
		store.write = func(file *os.File, data []byte) (int, error) {
			written, _ := file.Write(data[:len(data)/2])
			return written, errors.New("injected append failure")
		}
		matched, err := store.SchedulePullRequestReconcile(
			awaiting.Ready.Repository, awaiting.Ready.PullRequest, "", "github-rollback", 46, false, awaiting.UpdatedAt.Add(time.Second),
		)
		if err == nil || matched {
			t.Fatalf("append failure = %t, %v", matched, err)
		}
		after, _ := store.Snapshot()
		afterDisk, _ := os.ReadFile(path)
		if !reflect.DeepEqual(after.Model(), before.Model()) || !bytes.Equal(afterDisk, beforeDisk) {
			t.Fatal("append rollback changed projection or journal")
		}
	})

	t.Run("post-append apply failure poisons and replays", func(t *testing.T) {
		store, path, awaiting := createPullRequestWakeStore(t)
		injected := errors.New("injected apply failure")
		store.apply = func(Model, diskOperation) (Snapshot, error) { return Snapshot{}, injected }
		matched, err := store.SchedulePullRequestReconcile(
			awaiting.Ready.Repository, awaiting.Ready.PullRequest, "", "github-poison", 47, true, awaiting.UpdatedAt.Add(time.Second),
		)
		if !errors.Is(err, injected) || matched {
			t.Fatalf("apply failure = %t, %v", matched, err)
		}
		if _, err := store.Snapshot(); err == nil || !strings.Contains(err.Error(), "poisoned") {
			t.Fatalf("poisoned store error = %v", err)
		}
		reopened, err := Open(filepath.Dir(path), path, 64)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		replayed, err := reopened.Snapshot()
		if err != nil || replayed.Model().Runs[0].GitHub.LastCursor != 47 || !replayed.Model().Runs[0].GitHub.RemediationRequested {
			t.Fatalf("replayed wake = %#v, %v", replayed.Model(), err)
		}
	})
}
