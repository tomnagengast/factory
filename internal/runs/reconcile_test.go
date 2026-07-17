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

func TestScheduleReconcilePersistsWorkerBackoffWithoutLifecycleDelivery(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	batch, running, rate := runningProjection(t, root)
	initial, err := NewSnapshot(testSingleAdmissionModel(batch, running, rate))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runs.jsonl")
	store, err := Create(root, path, initial, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	transitions := slices.Clone(running.Transitions)
	deliveries := slices.Clone(running.TransitionDeliveries)
	detail := "ready checkpoint refresh failed"
	at := running.UpdatedAt.Add(time.Second)
	next := at.Add(time.Minute)
	if err := store.ScheduleReconcile(ReconcileSchedule{
		RunID: running.ID, ExpectedUpdatedAt: running.UpdatedAt, At: at, NextReconcileAt: next,
		FailureMode: ReconcileFailuresIncrement, AuthoritativeRefresh: true, Detail: &detail,
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.Model().Runs[0]
	if snapshot.Model().JournalSequence != 1 || got.State != StateRunning || got.UpdatedAt != at || got.Detail != detail ||
		got.GitHub.NextReconcileAt == nil || *got.GitHub.NextReconcileAt != next ||
		got.GitHub.LastAuthoritativeRefreshAt == nil || *got.GitHub.LastAuthoritativeRefreshAt != at ||
		got.GitHub.ReconcileFailures != 1 {
		t.Fatalf("worker reconcile schedule = %#v", got)
	}
	if !slices.Equal(got.Transitions, transitions) || !slices.Equal(got.TransitionDeliveries, deliveries) ||
		got.DeliveredThrough != running.DeliveredThrough || got.Attempts != running.Attempts {
		t.Fatalf("worker schedule changed lifecycle/outbox evidence: %#v", got)
	}

	emptyDetail := ""
	resetAt := got.UpdatedAt.Add(time.Second)
	resetNext := resetAt.Add(2 * time.Minute)
	if err := store.ScheduleReconcile(ReconcileSchedule{
		RunID: got.ID, ExpectedUpdatedAt: got.UpdatedAt, At: resetAt, NextReconcileAt: resetNext,
		FailureMode: ReconcileFailuresReset, Detail: &emptyDetail,
	}); err != nil {
		t.Fatal(err)
	}
	resetSnapshot, _ := store.Snapshot()
	reset := resetSnapshot.Model().Runs[0]
	if reset.GitHub.ReconcileFailures != 0 || reset.Detail != "" || reset.GitHub.NextReconcileAt == nil || *reset.GitHub.NextReconcileAt != resetNext ||
		reset.GitHub.LastAuthoritativeRefreshAt == nil || *reset.GitHub.LastAuthoritativeRefreshAt != at ||
		!slices.Equal(reset.Transitions, transitions) || len(reset.TransitionDeliveries) != len(deliveries) {
		t.Fatalf("reset worker schedule = %#v", reset)
	}

	reopened, err := Open(root, path, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, err := reopened.Snapshot()
	if err != nil || !reflect.DeepEqual(replayed.Model(), resetSnapshot.Model()) {
		t.Fatalf("replayed reconcile schedule = %#v, %v", replayed.Model(), err)
	}
}

func TestScheduleReconcileCoalescesAwaitingGitHubWake(t *testing.T) {
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
	store, err := Create(root, path, initial, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	transitions := slices.Clone(awaiting.Transitions)
	deliveries := slices.Clone(awaiting.TransitionDeliveries)
	refresh := *awaiting.GitHub.LastAuthoritativeRefreshAt
	at := awaiting.UpdatedAt.Add(time.Second)
	if err := store.ScheduleReconcile(ReconcileSchedule{
		RunID: awaiting.ID, ExpectedUpdatedAt: awaiting.UpdatedAt, At: at, NextReconcileAt: at,
		FailureMode: ReconcileFailuresUnchanged, Cursor: awaiting.GitHub.LastCursor + 4,
		RemediationRequested: true, DeliveryID: "delivery-github-wake",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot()
	got := snapshot.Model().Runs[0]
	if got.GitHub.LastCursor != awaiting.GitHub.LastCursor+4 || got.GitHub.ReconcileFailures != 2 || !got.GitHub.RemediationRequested ||
		got.GitHub.NextReconcileAt == nil || *got.GitHub.NextReconcileAt != at || got.Detail != awaiting.Detail ||
		got.GitHub.LastAuthoritativeRefreshAt == nil || *got.GitHub.LastAuthoritativeRefreshAt != refresh ||
		!slices.Contains(got.DeliveryIDs, "delivery-github-wake") || got.DuplicateDeliveries != uint64(len(got.DeliveryIDs)-1) ||
		!slices.IsSorted(got.DeliveryIDs) || !slices.Equal(got.Transitions, transitions) || !slices.Equal(got.TransitionDeliveries, deliveries) {
		t.Fatalf("GitHub wake schedule = %#v", got)
	}

	secondAt := got.UpdatedAt.Add(time.Second)
	if err := store.ScheduleReconcile(ReconcileSchedule{
		RunID: got.ID, ExpectedUpdatedAt: got.UpdatedAt, At: secondAt, NextReconcileAt: secondAt,
		FailureMode: ReconcileFailuresUnchanged, Cursor: 1, DeliveryID: "delivery-github-wake",
	}); err != nil {
		t.Fatal(err)
	}
	secondSnapshot, _ := store.Snapshot()
	second := secondSnapshot.Model().Runs[0]
	if second.GitHub.LastCursor != got.GitHub.LastCursor || !second.GitHub.RemediationRequested ||
		len(second.DeliveryIDs) != len(got.DeliveryIDs) || second.DuplicateDeliveries != got.DuplicateDeliveries {
		t.Fatalf("duplicate GitHub wake = %#v", second)
	}
}

func TestScheduleReconcileRejectsStaleIllegalAndMalformedIntent(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	batch, running, rate := runningProjection(t, root)
	initial, err := NewSnapshot(testSingleAdmissionModel(batch, running, rate))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runs.jsonl")
	store, err := Create(root, path, initial, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	before, _ := store.Snapshot()

	valid := ReconcileSchedule{
		RunID: running.ID, ExpectedUpdatedAt: running.UpdatedAt, At: running.UpdatedAt.Add(time.Second),
		NextReconcileAt: running.UpdatedAt.Add(2 * time.Second), FailureMode: ReconcileFailuresIncrement,
	}
	tests := []struct {
		name   string
		mutate func(*ReconcileSchedule)
	}{
		{name: "stale snapshot", mutate: func(value *ReconcileSchedule) { value.ExpectedUpdatedAt = value.ExpectedUpdatedAt.Add(-time.Second) }},
		{name: "next before operation", mutate: func(value *ReconcileSchedule) { value.NextReconcileAt = value.At.Add(-time.Nanosecond) }},
		{name: "unknown failure mode", mutate: func(value *ReconcileSchedule) { value.FailureMode = "replace" }},
		{name: "worker GitHub wake", mutate: func(value *ReconcileSchedule) { value.Cursor = 11; value.DeliveryID = "delivery-wake" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := store.ScheduleReconcile(candidate); err == nil {
				t.Fatal("invalid reconcile schedule was accepted")
			}
			after, _ := store.Snapshot()
			if !reflect.DeepEqual(after.Model(), before.Model()) {
				t.Fatal("rejected reconcile schedule changed projection")
			}
		})
	}

	pendingBatch, pending, pendingRate := testAdmissionProjection(t, root, 2, StatePending)
	pendingSnapshot, err := NewSnapshot(testSingleAdmissionModel(pendingBatch, pending, pendingRate))
	if err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(root, "pending.jsonl")
	pendingStore, err := Create(root, pendingPath, pendingSnapshot, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pendingStore.Close() })
	if err := pendingStore.ScheduleReconcile(ReconcileSchedule{
		RunID: pending.ID, ExpectedUpdatedAt: pending.UpdatedAt, At: pending.UpdatedAt.Add(time.Second),
		NextReconcileAt: pending.UpdatedAt.Add(2 * time.Second), FailureMode: ReconcileFailuresUnchanged,
	}); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("pending reconcile error = %v", err)
	}

	awaiting := awaitingProjection(running)
	awaitingSnapshot, err := NewSnapshot(testSingleAdmissionModel(batch, awaiting, rate))
	if err != nil {
		t.Fatal(err)
	}
	awaitingPath := filepath.Join(root, "awaiting.jsonl")
	awaitingStore, err := Create(root, awaitingPath, awaitingSnapshot, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = awaitingStore.Close() })
	if err := awaitingStore.ScheduleReconcile(ReconcileSchedule{
		RunID: awaiting.ID, ExpectedUpdatedAt: awaiting.UpdatedAt, At: awaiting.UpdatedAt.Add(time.Second),
		NextReconcileAt: awaiting.UpdatedAt.Add(time.Second), FailureMode: ReconcileFailuresUnchanged,
		DeliveryID: "delivery-without-cursor",
	}); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Fatalf("cursorless delivery wake error = %v", err)
	}
}

func TestScheduleReconcileJournalVersionAndFailureBoundaries(t *testing.T) {
	t.Run("old operation version is rejected", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		batch, running, rate := runningProjection(t, root)
		initial, err := NewSnapshot(testSingleAdmissionModel(batch, running, rate))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "runs.jsonl")
		store, err := Create(root, path, initial, 10)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		schedule := ReconcileSchedule{
			RunID: running.ID, ExpectedUpdatedAt: running.UpdatedAt, At: running.UpdatedAt.Add(time.Second),
			NextReconcileAt: running.UpdatedAt.Add(2 * time.Second), FailureMode: ReconcileFailuresIncrement,
		}
		operation := diskOperation{Kind: operationReconcile, Version: JournalVersion - 1, Sequence: 1, Reconcile: &schedule}
		data, err := json.Marshal(operation)
		if err != nil {
			t.Fatal(err)
		}
		if err := appendBytes(path, append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(root, path, 10); err == nil || !strings.Contains(err.Error(), "journal version") {
			t.Fatalf("old reconcile journal version error = %v", err)
		}
	})

	t.Run("append rollback preserves projection", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		batch, running, rate := runningProjection(t, root)
		initial, err := NewSnapshot(testSingleAdmissionModel(batch, running, rate))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "runs.jsonl")
		store, err := Create(root, path, initial, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		beforeDisk, _ := os.ReadFile(path)
		before, _ := store.Snapshot()
		store.write = func(file *os.File, data []byte) (int, error) {
			written, _ := file.Write(data[:len(data)/2])
			return written, errors.New("injected append failure")
		}
		err = store.ScheduleReconcile(ReconcileSchedule{
			RunID: running.ID, ExpectedUpdatedAt: running.UpdatedAt, At: running.UpdatedAt.Add(time.Second),
			NextReconcileAt: running.UpdatedAt.Add(2 * time.Second), FailureMode: ReconcileFailuresIncrement,
		})
		if err == nil {
			t.Fatal("append failure was ignored")
		}
		afterDisk, _ := os.ReadFile(path)
		after, _ := store.Snapshot()
		if !bytes.Equal(afterDisk, beforeDisk) || !reflect.DeepEqual(after.Model(), before.Model()) {
			t.Fatal("append rollback changed disk or projection")
		}
	})

	t.Run("post-append apply failure poisons and replays", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		batch, running, rate := runningProjection(t, root)
		initial, err := NewSnapshot(testSingleAdmissionModel(batch, running, rate))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "runs.jsonl")
		store, err := Create(root, path, initial, 10)
		if err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected apply failure")
		store.apply = func(Model, diskOperation) (Snapshot, error) { return Snapshot{}, injected }
		err = store.ScheduleReconcile(ReconcileSchedule{
			RunID: running.ID, ExpectedUpdatedAt: running.UpdatedAt, At: running.UpdatedAt.Add(time.Second),
			NextReconcileAt: running.UpdatedAt.Add(2 * time.Second), FailureMode: ReconcileFailuresIncrement,
		})
		if !errors.Is(err, injected) {
			t.Fatalf("apply failure = %v", err)
		}
		if _, err := store.Snapshot(); err == nil || !strings.Contains(err.Error(), "poisoned") {
			t.Fatalf("poisoned store error = %v", err)
		}
		reopened, err := Open(root, path, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		replayed, err := reopened.Snapshot()
		if err != nil || replayed.Model().Runs[0].GitHub.ReconcileFailures != 1 {
			t.Fatalf("replayed schedule = %#v, %v", replayed.Model(), err)
		}
	})
}
