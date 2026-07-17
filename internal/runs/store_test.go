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

func TestStoreAppendReplayTransitionAndImmutableIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), 1, StatePending)
	if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
		t.Fatalf("append admission batch: %v", err)
	}
	illegal := nextLifecycleRun(run, StateRunning, modelTestNow.Add(500*time.Millisecond))
	if err := store.Transition(illegal); err == nil || !strings.Contains(err.Error(), "illegal lifecycle transition") {
		t.Fatalf("illegal pending-to-running transition error = %v", err)
	}

	starting := nextLifecycleRun(run, StateStarting, modelTestNow.Add(time.Second))
	starting.SessionName = "factory-run-1"
	starting.RunDirectory = filepath.Join(filepath.Dir(path), "run-1")
	starting.SegmentStartedAt = pointerTime(starting.UpdatedAt)
	if err := store.Transition(starting); err != nil {
		t.Fatalf("transition starting: %v", err)
	}
	running := nextLifecycleRun(starting, StateRunning, modelTestNow.Add(2*time.Second))
	running.Attempts = 1
	running.Transitions[len(running.Transitions)-1].Attempts = 1
	running.StartedAt = pointerTime(running.UpdatedAt)
	if err := store.Transition(running); err != nil {
		t.Fatalf("transition running: %v", err)
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	model := snapshot.Model()
	if model.JournalSequence != 3 || model.TotalBatches != 1 || model.TotalRuns != 1 || model.Runs[0].State != StateRunning || model.Runs[0].Attempts != 1 {
		t.Fatalf("live projection = %#v", model)
	}

	reopened, err := Open(filepath.Dir(path), path, 10)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	replayed, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed.Model(), model) {
		t.Fatalf("replayed projection = %#v, want %#v", replayed.Model(), model)
	}

	changed := nextLifecycleRun(running, StateFailed, modelTestNow.Add(3*time.Second))
	changed.Causation.AdmissionID = "admission-mutated"
	changed.FinishedAt = pointerTime(changed.UpdatedAt)
	if err := reopened.Transition(changed); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable identity transition error = %v", err)
	}
	after, err := reopened.Snapshot()
	if err != nil || !reflect.DeepEqual(after.Model(), model) {
		t.Fatalf("projection changed after rejected identity mutation: %#v, %v", after.Model(), err)
	}
}

func TestLifecycleTransitionLegalityIsComplete(t *testing.T) {
	states := []LifecycleState{
		StateAdmitted, StateRouting, StatePending, StatePostMergePending, StateStarting, StateRunning,
		StateAwaitingHumanMerge, StateSucceeded, StateBlocked, StateFailed, StateRejected,
	}
	allowed := map[LifecycleState][]LifecycleState{
		StateAdmitted:           {StateRouting, StateRejected},
		StateRouting:            {StateAdmitted, StatePending, StateRejected},
		StatePending:            {StateStarting, StateSucceeded, StateBlocked, StateFailed, StateRejected},
		StatePostMergePending:   {StateStarting, StateSucceeded, StateBlocked, StateFailed},
		StateStarting:           {StateRunning, StatePostMergePending, StateAwaitingHumanMerge, StateSucceeded, StateBlocked, StateFailed},
		StateRunning:            {StateAwaitingHumanMerge, StateSucceeded, StateBlocked, StateFailed},
		StateAwaitingHumanMerge: {StatePending, StatePostMergePending, StateSucceeded, StateBlocked, StateFailed},
	}
	for _, from := range states {
		for _, to := range states {
			want := slices.Contains(allowed[from], to)
			if got := legalTransition(from, to); got != want {
				t.Errorf("legalTransition(%s, %s) = %t, want %t", from, to, got, want)
			}
		}
	}
}

func TestAdmissionBatchAtomicityDuplicatesAndCollisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), 1, StatePending)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	badRate := rate
	badRate.Count = 2
	if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{badRate}); err == nil || !strings.Contains(err.Error(), "rate increments") {
		t.Fatalf("atomic validation error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("invalid batch changed disk: %v", err)
	}
	empty, err := store.Snapshot()
	if err != nil || len(empty.Model().AdmissionBatches) != 0 {
		t.Fatalf("invalid batch changed memory: %#v, %v", empty.Model(), err)
	}

	if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); !errors.Is(err, ErrDuplicateAdmissionBatch) {
		t.Fatalf("exact duplicate error = %v", err)
	}
	collisionBatch := batch
	collisionBatch.ID = "batch-collision"
	if err := store.ApplyAdmissionBatch([]AdmissionBatch{collisionBatch}, []Run{run}, []RateBucket{rate}); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("event collision error = %v", err)
	}

	secondBatch, secondRun, secondRate := testAdmissionProjection(t, filepath.Dir(path), 2, StatePending)
	secondRun.ID = run.ID
	secondBatch.Outcomes[1].RunID = run.ID
	if err := store.ApplyAdmissionBatch([]AdmissionBatch{secondBatch}, []Run{secondRun}, []RateBucket{secondRate}); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("Run collision error = %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil || snapshot.Model().TotalBatches != 1 || snapshot.Model().TotalRuns != 1 {
		t.Fatalf("collision changed projection: %#v, %v", snapshot.Model(), err)
	}
}

func TestAdmissionBatchPersistsMultipleEventsAsOneOperation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	firstBatch, firstRun, firstRate := testAdmissionProjection(t, filepath.Dir(path), 1, StatePending)
	secondBatch, secondRun, _ := testAdmissionProjection(t, filepath.Dir(path), 2, StatePending)
	secondBatch.DecidedAt = firstBatch.DecidedAt
	secondRun.Causation.AdmittedAt = firstRun.Causation.AdmittedAt
	secondRun.CreatedAt = firstRun.CreatedAt
	secondRun.UpdatedAt = firstRun.UpdatedAt
	secondRun.Transitions[0].At = firstRun.Transitions[0].At
	combinedRate := firstRate
	combinedRate.Count = 2
	if err := store.ApplyAdmissionBatch(
		[]AdmissionBatch{secondBatch, firstBatch},
		[]Run{secondRun, firstRun},
		[]RateBucket{combinedRate},
	); err != nil {
		t.Fatalf("apply multi-event admission batch: %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	model := snapshot.Model()
	if model.JournalSequence != 1 || model.TotalBatches != 2 || model.TotalRuns != 2 || len(model.AdmissionBatches) != 2 || len(model.Runs) != 2 ||
		len(model.RateBuckets) != 1 || model.RateBuckets[0].Count != 2 || model.AdmissionBatches[0].ID != firstBatch.ID {
		t.Fatalf("multi-event projection = %#v", model)
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Count(data, []byte{'\n'}) != 2 {
		t.Fatalf("multi-event operation count = %d, %v", bytes.Count(data, []byte{'\n'}), err)
	}
	reopened, err := Open(filepath.Dir(path), path, 10)
	if err != nil {
		t.Fatal(err)
	}
	replayed, _ := reopened.Snapshot()
	if !reflect.DeepEqual(replayed.Model(), model) {
		t.Fatalf("multi-event replay = %#v", replayed.Model())
	}
}

func TestAdmissionBatchExactMultiEventRetryAfterReopen(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	path := filepath.Join(root, "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	firstBatch, firstRun, firstRate := testAdmissionProjection(t, root, 1, StatePending)
	secondBatch, secondRun, _ := testAdmissionProjection(t, root, 2, StatePending)
	secondBatch.DecidedAt = firstBatch.DecidedAt
	secondRun.Causation.AdmittedAt = firstRun.Causation.AdmittedAt
	secondRun.CreatedAt = firstRun.CreatedAt
	secondRun.UpdatedAt = firstRun.UpdatedAt
	secondRun.Transitions[0].At = firstRun.Transitions[0].At
	combinedRate := firstRate
	combinedRate.Count = 2
	batches := []AdmissionBatch{firstBatch, secondBatch}
	runs := []Run{firstRun, secondRun}
	if err := store.ApplyAdmissionBatch(batches, runs, []RateBucket{combinedRate}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, path, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	beforeDisk, _ := os.ReadFile(path)
	before, _ := reopened.Snapshot()
	if err := reopened.ApplyAdmissionBatch(batches, runs, []RateBucket{combinedRate}); !errors.Is(err, ErrDuplicateAdmissionBatch) {
		t.Fatalf("exact multi-event retry error = %v", err)
	}
	afterDisk, _ := os.ReadFile(path)
	after, _ := reopened.Snapshot()
	if !bytes.Equal(beforeDisk, afterDisk) || !reflect.DeepEqual(before.Model(), after.Model()) || after.Model().RateBuckets[0].Count != 2 {
		t.Fatal("exact retry changed disk, memory, totals, or rates")
	}

	thirdBatch, thirdRun, _ := testAdmissionProjection(t, root, 3, StatePending)
	thirdBatch.DecidedAt = firstBatch.DecidedAt
	thirdRun.Causation.AdmittedAt = firstRun.Causation.AdmittedAt
	thirdRun.CreatedAt = firstRun.CreatedAt
	thirdRun.UpdatedAt = firstRun.UpdatedAt
	thirdRun.Transitions[0].At = firstRun.Transitions[0].At
	partialRate := firstRate
	partialRate.Count = 2
	if err := reopened.ApplyAdmissionBatch(
		[]AdmissionBatch{firstBatch, thirdBatch}, []Run{firstRun, thirdRun}, []RateBucket{partialRate},
	); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("partial overlap error = %v", err)
	}
	mismatchedRuns := []Run{firstRun, secondRun.Clone()}
	mismatchedRuns[1].Detail = "rewritten"
	if err := reopened.ApplyAdmissionBatch(batches, mismatchedRuns, []RateBucket{combinedRate}); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("associated Run mismatch error = %v", err)
	}
	if err := reopened.ApplyAdmissionBatch(batches, append(runs, thirdRun), []RateBucket{combinedRate}); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("mixed Run identity error = %v", err)
	}
	mismatchedRate := combinedRate
	mismatchedRate.Count++
	if err := reopened.ApplyAdmissionBatch(batches, runs, []RateBucket{mismatchedRate}); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("rate mismatch error = %v", err)
	}
	finalDisk, _ := os.ReadFile(path)
	final, _ := reopened.Snapshot()
	if !bytes.Equal(beforeDisk, finalDisk) || !reflect.DeepEqual(before.Model(), final.Model()) {
		t.Fatal("collision changed durable admission projection")
	}
}

func TestTransitionDeltaAndOldestTaskOwnership(t *testing.T) {
	t.Run("durable evidence cannot be rewritten", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func(*Run)
		}{
			{name: "deliveries", mutate: func(run *Run) { run.DeliveryIDs = []string{"delivery-a"}; run.DuplicateDeliveries = 0 }},
			{name: "attempts", mutate: func(run *Run) { run.Attempts = 0; run.Transitions[len(run.Transitions)-1].Attempts = 0 }},
			{name: "started timestamp", mutate: func(run *Run) { run.StartedAt = pointerTime(run.StartedAt.Add(time.Second)) }},
			{name: "GitHub cursor", mutate: func(run *Run) { run.GitHub.LastCursor-- }},
			{name: "session", mutate: func(run *Run) { run.SessionName = "factory-rewritten" }},
			{name: "Run directory", mutate: func(run *Run) { run.RunDirectory += "-rewritten" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				root := trustedTestRoot(t, t.TempDir())
				path := filepath.Join(root, "runs.jsonl")
				batch, current, rate := runningProjection(t, root)
				initial, err := NewSnapshot(Model{Schema: SchemaVersion, TotalBatches: 1, TotalRuns: 1, AdmissionBatches: []AdmissionBatch{batch}, Runs: []Run{current}, RateBuckets: []RateBucket{rate}})
				if err != nil {
					t.Fatal(err)
				}
				store, err := Create(root, path, initial, 10)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				next := awaitingProjection(current)
				test.mutate(&next)
				before, _ := os.ReadFile(path)
				if err := store.Transition(next); err == nil {
					t.Fatal("durable evidence rewrite was accepted")
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("rejected transition changed disk")
				}
			})
		}
	})

	t.Run("ready merge and migrated baseline are immutable", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		batch, running, rate := runningProjection(t, root)
		awaiting := awaitingProjection(running)
		awaiting.MergeCommitOID = strings.Repeat("b", 40)
		initial, err := NewSnapshot(Model{Schema: SchemaVersion, TotalBatches: 1, TotalRuns: 1, AdmissionBatches: []AdmissionBatch{batch}, Runs: []Run{awaiting}, RateBuckets: []RateBucket{rate}})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "runs.jsonl")
		store, err := Create(root, path, initial, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		for _, mutate := range []func(*Run){
			func(run *Run) { run.Ready.VerifiedHeadOID = strings.Repeat("c", 40) },
			func(run *Run) { run.MergeCommitOID = strings.Repeat("c", 40) },
		} {
			next := nextLifecycleRun(awaiting, StatePostMergePending, awaiting.UpdatedAt.Add(time.Second))
			next.SessionName = ""
			mutate(&next)
			if err := store.Transition(next); err == nil {
				t.Fatal("ready or merge identity rewrite was accepted")
			}
		}

		migrated := running.Clone()
		migrated.Transitions = nil
		migrated.MigratedBaseline = &MigratedBaseline{State: StateRunning, ObservedAt: migrated.UpdatedAt, PriorTransitionsAcknowledged: true}
		migratedSnapshot, err := NewSnapshot(Model{Schema: SchemaVersion, TotalBatches: 1, TotalRuns: 1, AdmissionBatches: []AdmissionBatch{batch}, Runs: []Run{migrated}, RateBuckets: []RateBucket{rate}})
		if err != nil {
			t.Fatal(err)
		}
		migratedPath := filepath.Join(root, "migrated.jsonl")
		migratedStore, err := Create(root, migratedPath, migratedSnapshot, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = migratedStore.Close() })
		next := nextLifecycleRun(migrated, StateFailed, migrated.UpdatedAt.Add(time.Second))
		next.MigratedBaseline.ObservedAt = next.MigratedBaseline.ObservedAt.Add(time.Nanosecond)
		if err := migratedStore.Transition(next); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("migrated baseline rewrite error = %v", err)
		}
	})

	t.Run("resettable reconciliation fields remain valid", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		batch, running, rate := runningProjection(t, root)
		awaiting := awaitingProjection(running)
		awaiting.GitHub.NextReconcileAt = pointerTime(awaiting.UpdatedAt.Add(time.Hour))
		awaiting.GitHub.ReconcileFailures = 3
		awaiting.GitHub.RemediationRequested = true
		awaiting.TerminalIntent = string(StateSucceeded)
		awaiting.TerminalRejection = "retry"
		awaiting.Completion = &CompletionValidation{Accepted: false, Intent: string(StateSucceeded), State: StateFailed, Reason: "retry", ValidatedAt: awaiting.UpdatedAt}
		initial, err := NewSnapshot(Model{Schema: SchemaVersion, TotalBatches: 1, TotalRuns: 1, AdmissionBatches: []AdmissionBatch{batch}, Runs: []Run{awaiting}, RateBuckets: []RateBucket{rate}})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "runs.jsonl")
		store, err := Create(root, path, initial, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		next := nextLifecycleRun(awaiting, StatePostMergePending, awaiting.UpdatedAt.Add(time.Second))
		next.SessionName = ""
		next.MergeCommitOID = strings.Repeat("b", 40)
		next.GitHub.NextReconcileAt = nil
		next.GitHub.ReconcileFailures = 0
		next.GitHub.RemediationRequested = false
		next.TerminalIntent = ""
		next.TerminalRejection = ""
		if err := store.Transition(next); err != nil {
			t.Fatalf("legitimate reconciliation reset: %v", err)
		}
	})

	t.Run("younger same-task Run cannot enter ownership", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		firstBatch, firstRun, firstRate := testAdmissionProjection(t, root, 1, StatePending)
		secondBatch, secondRun, secondRate := testAdmissionProjection(t, root, 2, StatePending)
		secondRun.Causation.Task = firstRun.Causation.Task
		for _, run := range []*Run{&firstRun, &secondRun} {
			run.State = StateAdmitted
			run.Transitions[0].State = StateAdmitted
		}
		initial, err := NewSnapshot(Model{
			Schema: SchemaVersion, TotalBatches: 2, TotalRuns: 2,
			AdmissionBatches: []AdmissionBatch{firstBatch, secondBatch}, Runs: []Run{firstRun, secondRun}, RateBuckets: []RateBucket{firstRate, secondRate},
		})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "runs.jsonl")
		store, err := Create(root, path, initial, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		youngerRouting := nextLifecycleRun(secondRun, StateRouting, secondRun.UpdatedAt.Add(time.Second))
		if err := store.Transition(youngerRouting); err == nil || !strings.Contains(err.Error(), "oldest") {
			t.Fatalf("younger ownership error = %v", err)
		}
	})
}

func TestReplayRecoversTornTailButRejectsCompleteCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), 1, StatePending)
	if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendBytes(path, []byte(`{"kind":"lifecycle-transition"`)); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(path), path, 10)
	if err != nil {
		t.Fatalf("recover torn tail: %v", err)
	}
	snapshot, err := reopened.Snapshot()
	if err != nil || len(snapshot.Model().Runs) != 1 {
		t.Fatalf("recovered projection = %#v, %v", snapshot.Model(), err)
	}
	recovered, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(recovered, valid) {
		t.Fatalf("torn tail was not removed: %v", err)
	}

	corruptions := []struct {
		name string
		line string
	}{
		{name: "unknown operation", line: `{"kind":"unknown","version":1,"sequence":2}`},
		{name: "unknown field", line: `{"kind":"lifecycle-transition","version":1,"sequence":2,"unknown":true}`},
		{name: "malformed complete JSON", line: `{"kind":`},
	}
	for _, test := range corruptions {
		t.Run(test.name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "runs.jsonl")
			if err := os.WriteFile(candidate, append(append([]byte(nil), valid...), append([]byte(test.line), '\n')...), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(trustedTestRoot(t, filepath.Dir(candidate)), candidate, 10); err == nil {
				t.Fatal("complete corrupt operation was accepted")
			}
		})
	}
}

func TestReplayRejectsSchemaVersionSequenceAndNoncanonicalCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), 1, StatePending)
	if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(time.Time{}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var operation diskOperation
	if err := json.Unmarshal(bytes.TrimSpace(checkpoint), &operation); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*diskOperation)
	}{
		{name: "version", mutate: func(value *diskOperation) { value.Version++ }},
		{name: "schema", mutate: func(value *diskOperation) { value.Schema++; value.Checkpoint.Schema++ }},
		{name: "sequence mismatch", mutate: func(value *diskOperation) { value.Sequence++; value.Checkpoint.JournalSequence = value.Sequence - 1 }},
		{name: "noncanonical ordering", mutate: func(value *diskOperation) {
			slices.Reverse(value.Checkpoint.Runs[0].DeliveryIDs)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := operation
			model := cloneModel(*operation.Checkpoint)
			candidate.Checkpoint = &model
			test.mutate(&candidate)
			data, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			candidatePath := filepath.Join(t.TempDir(), "runs.jsonl")
			if err := os.WriteFile(candidatePath, append(data, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(trustedTestRoot(t, filepath.Dir(candidatePath)), candidatePath, 10); err == nil {
				t.Fatal("invalid checkpoint was accepted")
			}
		})
	}
}

func TestAppendFailureRollbackPoisonAndApplyFailureBoundaries(t *testing.T) {
	t.Run("append rollback preserves projection", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runs.jsonl")
		store := createEmptyStore(t, path, 10)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		store.write = func(file *os.File, data []byte) (int, error) {
			written, err := file.Write(data[:len(data)/2])
			return written, errors.Join(err, errors.New("injected write failure"))
		}
		batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), 1, StatePending)
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err == nil {
			t.Fatal("append failure was ignored")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("append rollback changed disk: %v", err)
		}
		snapshot, err := store.Snapshot()
		if err != nil || len(snapshot.Model().Runs) != 0 {
			t.Fatalf("append rollback changed projection: %#v, %v", snapshot.Model(), err)
		}
	})

	t.Run("rollback failure poisons", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runs.jsonl")
		store := createEmptyStore(t, path, 10)
		store.write = func(file *os.File, data []byte) (int, error) {
			written, _ := file.Write(data[:len(data)/2])
			return written, errors.New("injected append failure")
		}
		store.rollback = func(*os.File, int64) error { return errors.New("injected rollback failure") }
		batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), 1, StatePending)
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err == nil || !strings.Contains(err.Error(), "rollback failed") {
			t.Fatalf("poisoning append error = %v", err)
		}
		if _, err := store.Snapshot(); err == nil || !strings.Contains(err.Error(), "poisoned") {
			t.Fatalf("poisoned snapshot error = %v", err)
		}
	})

	t.Run("post-append apply failure poisons but replays", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runs.jsonl")
		store := createEmptyStore(t, path, 10)
		injected := errors.New("injected apply failure")
		store.apply = func(Model, diskOperation) (Snapshot, error) { return Snapshot{}, injected }
		batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), 1, StatePending)
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); !errors.Is(err, injected) {
			t.Fatalf("apply failure = %v", err)
		}
		if _, err := store.Snapshot(); err == nil || !strings.Contains(err.Error(), "poisoned") {
			t.Fatalf("poisoned snapshot error = %v", err)
		}
		reopened, err := Open(filepath.Dir(path), path, 10)
		if err != nil {
			t.Fatalf("replay durable operation: %v", err)
		}
		snapshot, err := reopened.Snapshot()
		if err != nil || len(snapshot.Model().Runs) != 1 {
			t.Fatalf("replayed applied projection = %#v, %v", snapshot.Model(), err)
		}
	})
}

func TestCheckpointCompactionRetentionAndFailureBoundaries(t *testing.T) {
	t.Run("retention keeps nonterminal and newest terminal batches", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runs.jsonl")
		store := createEmptyStore(t, path, 1)
		for number, state := range []LifecycleState{StateSucceeded, StateSucceeded, StatePending} {
			batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), number+1, state)
			if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
				t.Fatalf("append %d: %v", number+1, err)
			}
		}
		if err := store.Compact(modelTestNow.Add(time.Minute)); err != nil {
			t.Fatalf("compact: %v", err)
		}
		snapshot, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		model := snapshot.Model()
		if model.TotalBatches != 3 || model.TotalRuns != 3 || len(model.AdmissionBatches) != 2 || len(model.Runs) != 2 || len(model.RateBuckets) != 2 ||
			model.AdmissionBatches[0].ID != "batch-2" || model.AdmissionBatches[1].ID != "batch-3" {
			t.Fatalf("retained projection = %#v", model)
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.Count(data, []byte{'\n'}) != 1 {
			t.Fatalf("compacted journal lines = %d, %v", bytes.Count(data, []byte{'\n'}), err)
		}
		reopened, err := Open(filepath.Dir(path), path, 1)
		if err != nil {
			t.Fatal(err)
		}
		replayed, _ := reopened.Snapshot()
		if !reflect.DeepEqual(replayed.Model(), model) {
			t.Fatalf("replayed compacted model = %#v", replayed.Model())
		}
	})

	t.Run("pre-replace checkpoint failure preserves disk and memory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runs.jsonl")
		store := createEmptyStore(t, path, 1)
		batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), 1, StateSucceeded)
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		beforeSnapshot, _ := store.Snapshot()
		injected := errors.New("injected checkpoint failure")
		store.checkpoint = func(*storeLocation, diskOperation, bool, func(*os.File) error) (bool, error) { return false, injected }
		if err := store.Compact(time.Time{}); !errors.Is(err, injected) {
			t.Fatalf("checkpoint error = %v", err)
		}
		after, _ := os.ReadFile(path)
		afterSnapshot, _ := store.Snapshot()
		if !bytes.Equal(before, after) || !reflect.DeepEqual(beforeSnapshot.Model(), afterSnapshot.Model()) {
			t.Fatal("pre-replace checkpoint failure changed state")
		}
	})

	t.Run("automatic checkpoint failure follows durable apply", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runs.jsonl")
		store := createEmptyStore(t, path, 10)
		store.operationsSinceCheckpoint = 100
		injected := errors.New("injected automatic checkpoint failure")
		store.checkpoint = func(*storeLocation, diskOperation, bool, func(*os.File) error) (bool, error) { return false, injected }
		batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), 1, StatePending)
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); !errors.Is(err, injected) {
			t.Fatalf("automatic checkpoint error = %v", err)
		}
		live, err := store.Snapshot()
		if err != nil || len(live.Model().Runs) != 1 {
			t.Fatalf("durable apply missing after checkpoint failure: %#v, %v", live.Model(), err)
		}
		reopened, err := Open(filepath.Dir(path), path, 10)
		if err != nil {
			t.Fatal(err)
		}
		replayed, _ := reopened.Snapshot()
		if !reflect.DeepEqual(replayed.Model(), live.Model()) {
			t.Fatalf("durable apply did not replay: %#v", replayed.Model())
		}
	})

	t.Run("post-replace directory sync failure converges", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runs.jsonl")
		store := createEmptyStore(t, path, 1)
		for number := 1; number <= 2; number++ {
			batch, run, rate := testAdmissionProjection(t, filepath.Dir(path), number, StateSucceeded)
			if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
				t.Fatal(err)
			}
		}
		injected := errors.New("injected directory sync failure")
		store.checkpoint = func(location *storeLocation, operation diskOperation, create bool, _ func(*os.File) error) (bool, error) {
			return writeCheckpoint(location, operation, create, func(*os.File) error { return injected })
		}
		if err := store.Compact(time.Time{}); !errors.Is(err, injected) {
			t.Fatalf("post-replace checkpoint error = %v", err)
		}
		if len(store.state.Model().Runs) != 1 || store.state.Model().Runs[0].ID != "run-2" {
			t.Fatalf("post-replace memory did not converge: %#v", store.state.Model())
		}
		if _, err := store.Snapshot(); err == nil || !strings.Contains(err.Error(), "poisoned") {
			t.Fatalf("post-replace store was not poisoned: %v", err)
		}
		poisonedDisk, _ := os.ReadFile(path)
		thirdBatch, thirdRun, thirdRate := testAdmissionProjection(t, filepath.Dir(path), 3, StatePending)
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{thirdBatch}, []Run{thirdRun}, []RateBucket{thirdRate}); err == nil || !strings.Contains(err.Error(), "poisoned") {
			t.Fatalf("post-replace mutation error = %v", err)
		}
		afterPoisonedMutation, _ := os.ReadFile(path)
		if !bytes.Equal(poisonedDisk, afterPoisonedMutation) {
			t.Fatal("poisoned store changed disk")
		}
		reopened, err := Open(filepath.Dir(path), path, 1)
		if err != nil {
			t.Fatal(err)
		}
		replayed, _ := reopened.Snapshot()
		if len(replayed.Model().Runs) != 1 || replayed.Model().Runs[0].ID != "run-2" {
			t.Fatalf("post-replace disk did not converge: %#v", replayed.Model())
		}
	})
}

func TestStorePermissionSymlinkPathAndCreateSafety(t *testing.T) {
	empty, err := NewSnapshot(EmptyModel())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(rootForInvalidTest(t), "relative/runs.jsonl", empty, 1); err == nil {
		t.Fatal("relative path was accepted")
	}
	root := t.TempDir()
	trustedTestRoot(t, root)
	if _, err := Create(root, root+"/nested/../runs.jsonl", empty, 1); err == nil {
		t.Fatal("noncanonical path was accepted")
	}
	path := filepath.Join(root, "runs.jsonl")
	if _, err := Create(root, path, empty, 1); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %v, %v", info, err)
	}
	before, _ := os.ReadFile(path)
	if _, err := Create(root, path, empty, 1); err == nil {
		t.Fatal("Create replaced an existing artifact")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("Create conflict changed existing artifact")
	}

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, path, 1); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("unsafe mode error = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "runs-link.jsonl")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, link, 1); err == nil || !strings.Contains(err.Error(), "nonsymlink") {
		t.Fatalf("symlink artifact error = %v", err)
	}
	oversized := filepath.Join(root, "oversized.jsonl")
	file, err := os.OpenFile(oversized, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxJournalBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, oversized, 1); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized journal error = %v", err)
	}
	targetDirectory := filepath.Join(root, "target")
	if err := os.Mkdir(targetDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDirectory := filepath.Join(root, "linked")
	if err := os.Symlink(targetDirectory, linkedDirectory); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(targetDirectory, "real-runs.jsonl")
	if _, err := Create(root, realPath, empty, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, filepath.Join(linkedDirectory, "real-runs.jsonl"), 1); err == nil || !strings.Contains(err.Error(), "nonsymlink") {
		t.Fatalf("symlink parent open error = %v", err)
	}
	if _, err := Create(root, filepath.Join(linkedDirectory, "new-runs.jsonl"), empty, 1); err == nil || !strings.Contains(err.Error(), "nonsymlink") {
		t.Fatalf("symlink directory error = %v", err)
	}
}

func TestStoreTrustedRootRejectsNestedSymlinksAndSurvivesParentReplacement(t *testing.T) {
	empty, err := NewSnapshot(EmptyModel())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("root identity permits only ancestor symlinks", func(t *testing.T) {
		outer := trustedTestRoot(t, t.TempDir())
		realParent := filepath.Join(outer, "real")
		realRoot := filepath.Join(realParent, "generation")
		if err := os.MkdirAll(realRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		ancestorLink := filepath.Join(outer, "ancestor-link")
		if err := os.Symlink(realParent, ancestorLink); err != nil {
			t.Fatal(err)
		}
		rootThroughAncestor := filepath.Join(ancestorLink, "generation")
		path := filepath.Join(rootThroughAncestor, "runs.jsonl")
		store, err := Create(rootThroughAncestor, path, empty, 1)
		if err != nil {
			t.Fatalf("symlink above trusted root: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })

		rootLink := filepath.Join(outer, "root-link")
		if err := os.Symlink(realRoot, rootLink); err != nil {
			t.Fatal(err)
		}
		if _, err := Create(rootLink, filepath.Join(rootLink, "other.jsonl"), empty, 1); err == nil || !strings.Contains(err.Error(), "nonsymlink") {
			t.Fatalf("symlink trusted root error = %v", err)
		}
	})

	t.Run("nested intermediate symlink", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		target := filepath.Join(root, "target", "state")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(root, "nested")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(nested, "link")
		if err := os.Symlink(filepath.Join(root, "target"), link); err != nil {
			t.Fatal(err)
		}
		realPath := filepath.Join(target, "runs.jsonl")
		realStore, err := Create(root, realPath, empty, 1)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = realStore.Close() })
		linkedPath := filepath.Join(link, "state", "runs.jsonl")
		if _, err := Open(root, linkedPath, 1); err == nil || !strings.Contains(err.Error(), "nonsymlink") {
			t.Fatalf("nested symlink open error = %v", err)
		}
		if _, err := Create(root, filepath.Join(link, "state", "new.jsonl"), empty, 1); err == nil || !strings.Contains(err.Error(), "nonsymlink") {
			t.Fatalf("nested symlink create error = %v", err)
		}
	})

	t.Run("captured parent cannot be redirected", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		path := filepath.Join(root, "generation", "state", "runs.jsonl")
		store, err := Create(root, path, empty, 1)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		original := filepath.Join(root, "original")
		if err := os.Rename(filepath.Join(root, "generation"), original); err != nil {
			t.Fatal(err)
		}
		attacker := filepath.Join(root, "attacker")
		if err := os.MkdirAll(filepath.Join(attacker, "state"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(attacker, filepath.Join(root, "generation")); err != nil {
			t.Fatal(err)
		}
		batch, run, rate := testAdmissionProjection(t, filepath.Join(original, "state"), 1, StateSucceeded)
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
			t.Fatalf("append through captured parent: %v", err)
		}
		if err := store.Compact(time.Time{}); err != nil {
			t.Fatalf("checkpoint through captured parent: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(attacker, "state", "runs.jsonl")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement parent received artifact: %v", err)
		}
		movedPath := filepath.Join(original, "state", "runs.jsonl")
		reopened, err := Open(root, movedPath, 1)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		snapshot, err := reopened.Snapshot()
		if err != nil || len(snapshot.Model().Runs) != 1 {
			t.Fatalf("captured parent projection = %#v, %v", snapshot.Model(), err)
		}
	})
}

func createEmptyStore(t *testing.T, path string, retention int) *Store {
	t.Helper()
	root := trustedTestRoot(t, filepath.Dir(path))
	empty, err := NewSnapshot(EmptyModel())
	if err != nil {
		t.Fatal(err)
	}
	store, err := Create(root, path, empty, retention)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func rootForInvalidTest(t *testing.T) string {
	t.Helper()
	return trustedTestRoot(t, t.TempDir())
}

func trustedTestRoot(t *testing.T, root string) string {
	t.Helper()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func nextLifecycleRun(current Run, state LifecycleState, at time.Time) Run {
	next := current.Clone()
	next.State = state
	next.UpdatedAt = at.UTC()
	next.Transitions = append(next.Transitions, LifecycleTransition{
		ID:    next.ID + ":" + string(state) + ":" + at.UTC().Format("150405.000000000"),
		State: state, Attempts: next.Attempts, At: at.UTC(),
	})
	if state.Terminal() {
		next.FinishedAt = pointerTime(at.UTC())
	}
	return next
}

func runningProjection(t *testing.T, root string) (AdmissionBatch, Run, RateBucket) {
	t.Helper()
	batch, run, rate := testAdmissionProjection(t, root, 1, StatePending)
	startingAt := run.CreatedAt.Add(time.Second)
	runningAt := run.CreatedAt.Add(2 * time.Second)
	run.State = StateRunning
	run.SessionName = "factory-sanitized"
	run.RunDirectory = filepath.Join(root, "run-sanitized")
	run.Attempts = 1
	run.UpdatedAt = runningAt
	run.StartedAt = pointerTime(runningAt)
	run.SegmentStartedAt = pointerTime(startingAt)
	run.Transitions = append(run.Transitions,
		LifecycleTransition{ID: run.ID + ":starting", State: StateStarting, At: startingAt},
		LifecycleTransition{ID: run.ID + ":running", State: StateRunning, Attempts: 1, At: runningAt},
	)
	run.GitHub.LastCursor = 10
	run.GitHub.LastAuthoritativeRefreshAt = pointerTime(runningAt)
	return batch, run, rate
}

func awaitingProjection(current Run) Run {
	next := nextLifecycleRun(current, StateAwaitingHumanMerge, current.UpdatedAt.Add(time.Second))
	next.Ready = &ReadyCheckpoint{
		ContractVersion: readyContractVersion, RunID: next.ID, Task: next.Causation.Task,
		Repository: next.Repository.Repository, PullRequest: 18, BaseBranch: next.Repository.DefaultBranch,
		HeadBranch: "factory-task-1-sanitized", VerifiedHeadOID: strings.Repeat("a", 40),
		CreatedAt: current.UpdatedAt.Add(500 * time.Millisecond), ValidatedAt: next.UpdatedAt,
	}
	return next
}

func appendBytes(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}
