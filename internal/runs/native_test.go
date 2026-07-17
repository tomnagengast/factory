package runs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

func TestNativeAdmissionMatchesLegacySyntheticLifecycle(t *testing.T) {
	root := t.TempDir()
	canonicalStore := createEmptyStore(t, filepath.Join(root, "canonical-runs.jsonl"), 10)
	canonicalAdmitter, _ := NewAdmitter(canonicalStore)
	legacyRouter, err := triggerrouter.Open(filepath.Join(root, "legacy-routing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	admission := nativeAdmissionFixture(t, modelTestNow)
	legacyAdmission := triggerrouter.NativeAdmission{
		Task: admission.Task, Workflow: admission.Workflow, WorkflowDigest: admission.WorkflowDigest,
		PolicyRevision: admission.PolicyRevision, RegistryRevision: admission.RegistryRevision, AdmittedAt: admission.AdmittedAt,
	}

	legacyStart, legacyCreated, err := legacyRouter.AdmitNative(legacyAdmission)
	if err != nil || !legacyCreated {
		t.Fatalf("legacy native start = %#v, %t, %v", legacyStart, legacyCreated, err)
	}
	canonicalStart, canonicalCreated, err := canonicalAdmitter.AdmitNative(admission)
	if err != nil || !canonicalCreated {
		t.Fatalf("canonical native start = %#v, %t, %v", canonicalStart, canonicalCreated, err)
	}
	assertNativeInvocationParity(t, legacyStart, canonicalStart)
	canonicalModel := snapshotModel(t, canonicalStore)
	legacyModel := legacyRouter.Snapshot()
	if len(canonicalModel.AdmissionBatches) != 1 || len(legacyModel.Decisions) != 1 || len(canonicalModel.RateBuckets) != 1 || len(legacyModel.RateBuckets) != 1 ||
		canonicalModel.AdmissionBatches[0].RegistryRevision != legacyModel.Decisions[0].RegistryRevision ||
		canonicalModel.AdmissionBatches[0].SettingsRevision != legacyModel.Decisions[0].SettingsRevision ||
		canonicalModel.RateBuckets[0].RuleID != legacyModel.RateBuckets[0].RuleID ||
		canonicalModel.RateBuckets[0].Minute != legacyModel.RateBuckets[0].Minute || canonicalModel.RateBuckets[0].Count != legacyModel.RateBuckets[0].Count {
		t.Fatalf("native admission evidence differs: canonical %#v legacy %#v", canonicalModel, legacyModel)
	}
	if canonicalModel.AdmissionBatches[0].EventSequence != 0 || legacyStart.EventSequence == 0 {
		t.Fatalf("canonical source sequence = %d, legacy synthetic sequence = %d", canonicalModel.AdmissionBatches[0].EventSequence, legacyStart.EventSequence)
	}

	canonicalOwner := admitExistingNativeToPending(t, canonicalStore, canonicalStart, root)
	legacyRuns, err := agentrun.Open(filepath.Join(root, "legacy-runs.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	legacyOwner, created, err := legacyRuns.EnsureInvocationRun(legacyClaim(legacyStart, legacyRepository(root), false), canonicalOwner.CreatedAt)
	if err != nil || !created {
		t.Fatalf("legacy native owner = %#v, %t, %v", legacyOwner, created, err)
	}

	continuationAt := canonicalOwner.UpdatedAt.Add(time.Second)
	admission.AdmittedAt = continuationAt
	legacyAdmission.AdmittedAt = continuationAt
	eventKey := "message:msg-0123456789abcdef"
	legacyFeedback, legacyCreated, err := legacyRouter.AdmitNativeContinuation(legacyAdmission, eventKey)
	if err != nil || !legacyCreated {
		t.Fatalf("legacy continuation = %#v, %t, %v", legacyFeedback, legacyCreated, err)
	}
	legacyUpdated, created, err := legacyRuns.EnsureInvocationRun(legacyClaim(legacyFeedback, legacyRepository(root), true), continuationAt.Add(time.Nanosecond))
	if err != nil || created {
		t.Fatalf("legacy coalescence = %#v, %t, %v", legacyUpdated, created, err)
	}
	reflectedAt := continuationAt.Add(time.Nanosecond)
	legacyRejected, err := legacyRouter.TransitionInvocation(
		legacyFeedback.ID, triggerrouter.StateRejected, legacyUpdated.ID, "feedback-coalesced", &reflectedAt, reflectedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBookkeeping, canonicalCreated, err := canonicalAdmitter.Continue(admission, eventKey)
	if err != nil || !canonicalCreated {
		t.Fatalf("canonical continuation = %#v, %t, %v", canonicalBookkeeping, canonicalCreated, err)
	}
	canonicalUpdated := modelRun(t, snapshotModel(t, canonicalStore), canonicalOwner.ID)
	assertNativeInvocationParity(t, legacyFeedback, canonicalBookkeeping)
	if legacyRejected.State != triggerrouter.StateRejected || legacyRejected.RunID != canonicalBookkeeping.Causation.ParentRunID ||
		legacyRejected.Reason != canonicalBookkeeping.Detail || legacyRejected.ReflectedAt == nil || *legacyRejected.ReflectedAt != canonicalBookkeeping.UpdatedAt ||
		canonicalBookkeeping.State != StateRejected || canonicalBookkeeping.Causation.ParentAdmissionID != legacyStart.ID ||
		!equalSortedStrings(legacyUpdated.DeliveryIDs, canonicalUpdated.DeliveryIDs) || legacyUpdated.DuplicateTriggers != canonicalUpdated.DuplicateDeliveries ||
		legacyUpdated.UpdatedAt != canonicalUpdated.UpdatedAt || string(legacyUpdated.State) != string(canonicalUpdated.State) {
		t.Fatalf("native continuation differs: legacy invocation %#v owner %#v; canonical bookkeeping %#v owner %#v", legacyRejected, legacyUpdated, canonicalBookkeeping, canonicalUpdated)
	}
}

func TestNativeAdmissionIdentityRetryAndRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	admitter, err := NewAdmitter(store)
	if err != nil {
		t.Fatal(err)
	}
	admission := nativeAdmissionFixture(t, modelTestNow)

	run, created, err := admitter.AdmitNative(admission)
	if err != nil || !created {
		t.Fatalf("native admission = %#v, %t, %v", run, created, err)
	}
	wantAdmissionID := admissionDigest("factory-native-invocation-v1", admission.Task.OwnershipKey(), admission.WorkflowDigest, "start")
	wantRunID := "run-" + admissionDigest("factory-trigger-run-v1", wantAdmissionID)[:16]
	if run.ID != wantRunID || run.Causation.AdmissionID != wantAdmissionID || run.Causation.EventID != "factory:native-start:"+admission.Task.ProviderID ||
		run.Causation.EventSequence != 0 || run.TriggerKind != triggerKindConfiguredRule ||
		run.State != StateAdmitted || !run.Causation.Task.Equal(admission.Task) {
		t.Fatalf("native Run identity = %#v", run)
	}
	initialModel := snapshotModel(t, store)
	if initialModel.AdmissionBatches[0].EventRecordDigest != "" || initialModel.AdmissionBatches[0].EventSequence != 0 {
		t.Fatalf("native admission invented source evidence = %#v", initialModel.AdmissionBatches[0])
	}

	retried, created, err := admitter.AdmitNative(admission)
	if err != nil || created || !reflect.DeepEqual(retried, run) {
		t.Fatalf("native retry = %#v, %t, %v", retried, created, err)
	}
	before, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(time.Time{}); err != nil {
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
	reopenedAdmitter, err := NewAdmitter(reopened)
	if err != nil {
		t.Fatal(err)
	}
	replayed, created, err := reopenedAdmitter.AdmitNative(admission)
	if err != nil || created || !reflect.DeepEqual(replayed, run) {
		t.Fatalf("reopened native retry = %#v, %t, %v", replayed, created, err)
	}
	after, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Model().JournalSequence != before.Model().JournalSequence || len(after.Model().Runs) != 1 || len(after.Model().AdmissionBatches) != 1 {
		t.Fatalf("reopened native model = %#v", after.Model())
	}
}

func TestNativeContinuationWithoutOwnerRemainsAdmitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	admitter, _ := NewAdmitter(store)
	admission := nativeAdmissionFixture(t, modelTestNow)
	eventKey := "message:msg-0123456789abcdef"

	run, created, err := admitter.Continue(admission, eventKey)
	if err != nil || !created {
		t.Fatalf("continuation admission = %#v, %t, %v", run, created, err)
	}
	wantEventID := "factory:native-continue:" + admission.Task.ProviderID + ":" + admissionDigest(eventKey)[:16]
	if run.State != StateAdmitted || run.Causation.EventID != wantEventID || run.TriggerKind != triggerKindComment ||
		run.Causation.ParentRunID != "" || run.Causation.ParentAdmissionID != "" {
		t.Fatalf("ownerless continuation = %#v", run)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	model := snapshot.Model()
	if len(model.AdmissionBatches) != 1 || model.AdmissionBatches[0].Origin != AdmissionOriginContinuation || len(model.Runs) != 1 {
		t.Fatalf("ownerless continuation model = %#v", model)
	}

	retried, created, err := admitter.Continue(admission, eventKey)
	if err != nil || created || !reflect.DeepEqual(retried, run) {
		t.Fatalf("ownerless continuation retry = %#v, %t, %v", retried, created, err)
	}
}

func TestNativeContinuationCoalescesAtomically(t *testing.T) {
	t.Run("oldest task owner", func(t *testing.T) {
		root := t.TempDir()
		store := createEmptyStore(t, filepath.Join(root, "runs.jsonl"), 10)
		admission := nativeAdmissionFixture(t, modelTestNow.Add(time.Minute))
		firstBatch, first, firstRate := admittedProjection(t, 1, admission.Task)
		secondBatch, second, secondRate := admittedProjection(t, 2, admission.Task)
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{firstBatch}, []Run{first}, []RateBucket{firstRate}); err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{secondBatch}, []Run{second}, []RateBucket{secondRate}); err != nil {
			t.Fatal(err)
		}
		admitter, _ := NewAdmitter(store)
		bookkeeping, created, err := admitter.Continue(admission, "message:msg-0123456789abcdef")
		if err != nil || !created {
			t.Fatalf("oldest-owner continuation = %#v, %t, %v", bookkeeping, created, err)
		}
		model := snapshotModel(t, store)
		updatedFirst := modelRun(t, model, first.ID)
		updatedSecond := modelRun(t, model, second.ID)
		if bookkeeping.Causation.ParentRunID != first.ID || !slices.Contains(updatedFirst.DeliveryIDs, bookkeeping.Causation.EventID) ||
			slices.Contains(updatedSecond.DeliveryIDs, bookkeeping.Causation.EventID) {
			t.Fatalf("continuation selected wrong owner: first %#v, second %#v, bookkeeping %#v", updatedFirst, updatedSecond, bookkeeping)
		}
	})

	t.Run("pending owner", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "runs.jsonl")
		store := createEmptyStore(t, path, 10)
		admitter, _ := NewAdmitter(store)
		admission := nativeAdmissionFixture(t, modelTestNow)
		owner := admitNativeToPending(t, store, admitter, admission, root)
		ownerTransitions := slices.Clone(owner.Transitions)
		admission.AdmittedAt = owner.UpdatedAt.Add(time.Second)
		eventKey := "message:msg-0123456789abcdef"

		bookkeeping, created, err := admitter.Continue(admission, eventKey)
		if err != nil || !created {
			t.Fatalf("coalesced continuation = %#v, %t, %v", bookkeeping, created, err)
		}
		if bookkeeping.State != StateRejected || bookkeeping.Detail != "feedback-coalesced" || bookkeeping.FinishedAt == nil ||
			bookkeeping.Causation.ParentRunID != owner.ID || bookkeeping.Causation.ParentAdmissionID != owner.Causation.AdmissionID ||
			!bookkeeping.UpdatedAt.After(admission.AdmittedAt) || len(bookkeeping.Transitions) != 2 {
			t.Fatalf("bookkeeping Run = %#v", bookkeeping)
		}
		model := snapshotModel(t, store)
		updated := modelRun(t, model, owner.ID)
		if updated.State != StatePending || !reflect.DeepEqual(updated.Transitions, ownerTransitions) || updated.UpdatedAt != bookkeeping.UpdatedAt ||
			!slices.Contains(updated.DeliveryIDs, bookkeeping.Causation.EventID) || updated.DuplicateDeliveries != 1 {
			t.Fatalf("coalesced owner = %#v", updated)
		}
		if len(model.AdmissionOperations) != 2 || len(model.Runs) != 2 || model.TotalBatches != 2 || model.TotalRuns != 2 {
			t.Fatalf("coalesced model = %#v", model)
		}

		if err := store.Compact(time.Time{}); err != nil {
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
		reopenedAdmitter, _ := NewAdmitter(reopened)
		beforeRetry := snapshotModel(t, reopened)
		retried, created, err := reopenedAdmitter.Continue(admission, eventKey)
		if err != nil || created || !reflect.DeepEqual(retried, bookkeeping) {
			t.Fatalf("coalesced retry = %#v, %t, %v", retried, created, err)
		}
		afterRetry := snapshotModel(t, reopened)
		if !reflect.DeepEqual(beforeRetry, afterRetry) {
			t.Fatal("coalesced durable retry appended or mutated state")
		}
	})

	t.Run("awaiting owner resumes", func(t *testing.T) {
		root := t.TempDir()
		store := createEmptyStore(t, filepath.Join(root, "runs.jsonl"), 10)
		admitter, _ := NewAdmitter(store)
		admission := nativeAdmissionFixture(t, modelTestNow)
		owner := admitNativeToAwaiting(t, store, admitter, admission, root)
		ready := *owner.Ready
		reconcileAt := owner.UpdatedAt.Add(time.Second)
		if err := store.ScheduleReconcile(ReconcileSchedule{
			RunID: owner.ID, ExpectedUpdatedAt: owner.UpdatedAt, At: reconcileAt, NextReconcileAt: reconcileAt.Add(time.Minute),
			FailureMode: ReconcileFailuresIncrement, AuthoritativeRefresh: true,
		}); err != nil {
			t.Fatal(err)
		}
		owner = modelRun(t, snapshotModel(t, store), owner.ID)
		wakeAt := owner.UpdatedAt.Add(time.Second)
		if err := store.ScheduleReconcile(ReconcileSchedule{
			RunID: owner.ID, ExpectedUpdatedAt: owner.UpdatedAt, At: wakeAt, NextReconcileAt: wakeAt,
			FailureMode: ReconcileFailuresUnchanged, Cursor: 1, RemediationRequested: true, DeliveryID: "delivery-native-test-wake",
		}); err != nil {
			t.Fatal(err)
		}
		owner = modelRun(t, snapshotModel(t, store), owner.ID)
		admission.AdmittedAt = owner.UpdatedAt.Add(time.Second)

		bookkeeping, created, err := admitter.Continue(admission, "gate:gate-0123456789abcdef:revision_requested")
		if err != nil || !created {
			t.Fatalf("awaiting continuation = %#v, %t, %v", bookkeeping, created, err)
		}
		updated := modelRun(t, snapshotModel(t, store), owner.ID)
		if updated.State != StatePending || updated.TriggerKind != triggerKindComment || updated.ResumeCount != owner.ResumeCount+1 ||
			updated.GitHub.NextReconcileAt != nil || updated.GitHub.ReconcileFailures != 0 || updated.GitHub.RemediationRequested ||
			updated.GitHub.LastAuthoritativeRefreshAt == nil || *updated.GitHub.LastAuthoritativeRefreshAt != bookkeeping.UpdatedAt ||
			updated.Detail != "task feedback received; resuming lifecycle" || !reflect.DeepEqual(updated.Ready, &ready) ||
			len(updated.Transitions) != len(owner.Transitions)+1 || updated.Transitions[len(updated.Transitions)-1].State != StatePending {
			t.Fatalf("resumed owner = %#v", updated)
		}
	})
}

func TestNativeContinuationJournalFailureBoundaries(t *testing.T) {
	t.Run("append rollback preserves both projections", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "runs.jsonl")
		store := createEmptyStore(t, path, 10)
		admitter, _ := NewAdmitter(store)
		admission := nativeAdmissionFixture(t, modelTestNow)
		owner := admitNativeToPending(t, store, admitter, admission, root)
		admission.AdmittedAt = owner.UpdatedAt.Add(time.Second)
		beforeDisk, _ := os.ReadFile(path)
		before := snapshotModel(t, store)
		store.write = func(file *os.File, data []byte) (int, error) {
			written, _ := file.Write(data[:len(data)/2])
			return written, errors.New("injected continuation append failure")
		}

		if _, _, err := admitter.Continue(admission, "message:msg-0123456789abcdef"); err == nil {
			t.Fatal("continuation append failure was ignored")
		}
		afterDisk, _ := os.ReadFile(path)
		after := snapshotModel(t, store)
		if !bytes.Equal(beforeDisk, afterDisk) || !reflect.DeepEqual(before, after) {
			t.Fatal("failed continuation append changed durable or projected state")
		}
	})

	t.Run("post-append apply failure poisons and replays atomically", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "runs.jsonl")
		store := createEmptyStore(t, path, 10)
		admitter, _ := NewAdmitter(store)
		admission := nativeAdmissionFixture(t, modelTestNow)
		owner := admitNativeToPending(t, store, admitter, admission, root)
		admission.AdmittedAt = owner.UpdatedAt.Add(time.Second)
		injected := errors.New("injected continuation apply failure")
		store.apply = func(Model, diskOperation) (Snapshot, error) { return Snapshot{}, injected }

		if _, _, err := admitter.Continue(admission, "message:msg-0123456789abcdef"); !errors.Is(err, injected) {
			t.Fatalf("continuation apply error = %v", err)
		}
		if _, err := store.Snapshot(); err == nil || !strings.Contains(err.Error(), "poisoned") {
			t.Fatalf("poisoned continuation store error = %v", err)
		}
		reopened, err := Open(root, path, 10)
		if err != nil {
			t.Fatalf("replay continuation operation: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		model := snapshotModel(t, reopened)
		if len(model.Runs) != 2 {
			t.Fatalf("replayed continuation model = %#v", model)
		}
		updated := modelRun(t, model, owner.ID)
		var bookkeeping Run
		for _, run := range model.Runs {
			if run.ID != owner.ID {
				bookkeeping = run
			}
		}
		if bookkeeping.State != StateRejected || updated.UpdatedAt != bookkeeping.UpdatedAt ||
			!slices.Contains(updated.DeliveryIDs, bookkeeping.Causation.EventID) {
			t.Fatalf("replayed continuation projections = owner %#v, bookkeeping %#v", updated, bookkeeping)
		}
	})
}

func TestNativeAdmissionRejectsInvalidAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	admitter, _ := NewAdmitter(store)
	valid := nativeAdmissionFixture(t, modelTestNow)

	if _, _, err := admitter.Continue(valid, "message:bad"); err == nil {
		t.Fatal("invalid continuation identity was accepted")
	}
	tests := []struct {
		name   string
		mutate func(*NativeAdmission)
	}{
		{name: "task", mutate: func(value *NativeAdmission) { value.Task.Source = taskmodel.SourceLinear }},
		{name: "workflow disabled", mutate: func(value *NativeAdmission) { value.Workflow.Enabled = false }},
		{name: "workflow digest", mutate: func(value *NativeAdmission) { value.WorkflowDigest = strings.Repeat("f", 64) }},
		{name: "policy generation", mutate: func(value *NativeAdmission) { value.PolicyGeneration = 0 }},
		{name: "decision time", mutate: func(value *NativeAdmission) { value.AdmittedAt = value.AdmittedAt.In(time.FixedZone("test", 3600)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Workflow = valid.Workflow.Clone()
			test.mutate(&candidate)
			if _, _, err := admitter.AdmitNative(candidate); err == nil {
				t.Fatal("invalid native admission was accepted")
			}
		})
	}
	if len(snapshotModel(t, store).Runs) != 0 {
		t.Fatal("invalid native admission changed the store")
	}
}

func TestNativeAdmissionAcceptsInitialPublicPolicyRevisions(t *testing.T) {
	store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
	t.Cleanup(func() { _ = store.Close() })
	admission := nativeAdmissionFixture(t, modelTestNow)
	admission.PolicyRevision = 0
	admission.RegistryRevision = 0
	if _, created, err := mustAdmitter(t, store).AdmitNative(admission); err != nil || !created {
		t.Fatalf("revision-zero native admission: created=%t err=%v", created, err)
	}
}

func nativeAdmissionFixture(t *testing.T, at time.Time) NativeAdmission {
	t.Helper()
	updatedAt := at.UTC()
	pin := workflow.Pinned{
		ID: "full-sdlc-provider-neutral", Revision: 7, Name: "Full SDLC", Enabled: true,
		Markdown: "# Full SDLC\n", UpdatedAt: &updatedAt,
	}
	digest, err := pin.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return NativeAdmission{
		Task:     taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"},
		Workflow: pin, WorkflowDigest: digest, PolicyRevision: 3, RegistryRevision: 4, PolicyGeneration: 5, AdmittedAt: at.UTC(),
	}
}

func assertNativeInvocationParity(t *testing.T, legacy triggerrouter.Invocation, canonical Run) {
	t.Helper()
	wantRunID := "run-" + admissionDigest("factory-trigger-run-v1", legacy.ID)[:16]
	if canonical.ID != wantRunID || canonical.Causation.AdmissionID != legacy.ID || canonical.Causation.EventID != legacy.EventID ||
		canonical.Causation.EventSource != eventwire.SourceFactory || canonical.Causation.RuleID != legacy.Rule.ID ||
		canonical.Causation.RuleRevision != legacy.Rule.Revision || !reflect.DeepEqual(canonical.Causation.Workflow, &legacy.Workflow) ||
		canonical.Causation.WorkflowDigest != legacy.WorkflowDigest || canonical.Causation.PolicyRevision != legacy.PolicyRevision ||
		!canonical.Causation.Task.Equal(legacy.Task) || canonical.Causation.RootEventID != legacy.RootEventID ||
		canonical.Causation.Hop != legacy.Hop || !slices.Equal(canonical.Causation.AncestorRuleIDs, legacy.AncestorRuleIDs) ||
		canonical.Causation.AdmittedAt != legacy.AdmittedAt {
		t.Fatalf("native immutable identity differs: legacy %#v canonical %#v", legacy, canonical)
	}
}

func legacyRepository(root string) agentrun.RepositoryConfig {
	return agentrun.RepositoryConfig{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git",
		RepoPath: filepath.Join(root, "factory"), ManagedRoot: root, BaseBranch: "main", CloudURL: "https://factory.nags.cloud",
	}
}

func legacyClaim(invocation triggerrouter.Invocation, repository agentrun.RepositoryConfig, feedback bool) agentrun.InvocationClaim {
	return agentrun.InvocationClaim{
		RunID: "run-" + admissionDigest("factory-trigger-run-v1", invocation.ID)[:16], InvocationID: invocation.ID, EventID: invocation.EventID,
		Task: invocation.Task, IssueIdentifier: invocation.IssueIdentifier, RootEventID: invocation.RootEventID,
		Hop: invocation.Hop, AncestorRuleIDs: slices.Clone(invocation.AncestorRuleIDs), Workflow: invocation.Workflow,
		WorkflowDigest: invocation.WorkflowDigest, PolicyRevision: invocation.PolicyRevision, Repository: repository, Feedback: feedback,
	}
}

func admitExistingNativeToPending(t *testing.T, store *Store, admitted Run, root string) Run {
	t.Helper()
	routing := nextLifecycleRun(admitted, StateRouting, admitted.UpdatedAt.Add(time.Second))
	if err := store.Transition(routing); err != nil {
		t.Fatal(err)
	}
	pending := nextLifecycleRun(routing, StatePending, routing.UpdatedAt.Add(time.Second))
	route := managerRoute(root)
	pending.Repository = &route
	if err := store.Transition(pending); err != nil {
		t.Fatal(err)
	}
	return modelRun(t, snapshotModel(t, store), admitted.ID)
}

func admitNativeToPending(t *testing.T, store *Store, admitter *Admitter, admission NativeAdmission, root string) Run {
	t.Helper()
	admitted, created, err := admitter.AdmitNative(admission)
	if err != nil || !created {
		t.Fatalf("admit native owner = %#v, %t, %v", admitted, created, err)
	}
	return admitExistingNativeToPending(t, store, admitted, root)
}

func admitNativeToAwaiting(t *testing.T, store *Store, admitter *Admitter, admission NativeAdmission, root string) Run {
	t.Helper()
	pending := admitNativeToPending(t, store, admitter, admission, root)
	starting := nextLifecycleRun(pending, StateStarting, pending.UpdatedAt.Add(time.Second))
	starting.SessionName = taskSessionName(starting)
	starting.RunDirectory = runPath(root, starting.ID)
	starting.SegmentStartedAt = pointerTime(starting.UpdatedAt)
	if err := store.Transition(starting); err != nil {
		t.Fatal(err)
	}
	running := nextLifecycleRun(starting, StateRunning, starting.UpdatedAt.Add(time.Second))
	running.Attempts = 1
	running.Transitions[len(running.Transitions)-1].Attempts = 1
	running.StartedAt = pointerTime(running.UpdatedAt)
	if err := store.Transition(running); err != nil {
		t.Fatal(err)
	}
	awaiting := nextLifecycleRun(running, StateAwaitingHumanMerge, running.UpdatedAt.Add(time.Second))
	awaiting.Ready = &ReadyCheckpoint{
		ContractVersion: readyContractVersion, RunID: awaiting.ID, Task: awaiting.Causation.Task,
		Repository: awaiting.Repository.Repository, PullRequest: 18, BaseBranch: awaiting.Repository.DefaultBranch,
		HeadBranch: "factory-" + awaiting.Causation.Task.ProviderID + "-review", VerifiedHeadOID: strings.Repeat("a", 40),
		CreatedAt: running.UpdatedAt.Add(time.Nanosecond), ValidatedAt: awaiting.UpdatedAt,
	}
	if err := store.Transition(awaiting); err != nil {
		t.Fatal(err)
	}
	return modelRun(t, snapshotModel(t, store), awaiting.ID)
}

func snapshotModel(t *testing.T, store *Store) Model {
	t.Helper()
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Model()
}

func modelRun(t *testing.T, model Model, runID string) Run {
	t.Helper()
	for _, run := range model.Runs {
		if run.ID == runID {
			return run
		}
	}
	t.Fatalf("Run %q not found", runID)
	return Run{}
}

func equalSortedStrings(left, right []string) bool {
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}
