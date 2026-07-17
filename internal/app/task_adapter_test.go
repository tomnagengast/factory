package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

var (
	_ taskservice.Admitter   = (*NativeAdmitter)(nil)
	_ taskservice.Reconciler = (*TaskReconciler)(nil)
)

func TestNativeAdmitterSerializesCanonicalPolicyAndRunOwnership(t *testing.T) {
	policyAdapter := newPolicyAdapterFixture(t, func() bool { return false })
	store := newAppRunStore(t)
	canonical, err := runs.NewAdmitter(store)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewNativeAdmitter(policyAdapter.coordinator, canonical)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := policyAdapter.coordinator.Snapshot()
	configuration := policy.SettingsView(snapshot)
	definition, found := configuration.Workflow(workflow.ProviderNeutralID)
	if !found {
		t.Fatal("provider-neutral workflow is unavailable")
	}
	digest, err := workflow.Digest(definition)
	if err != nil {
		t.Fatal(err)
	}
	value := triggerrouter.NativeAdmission{
		Task:     taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"},
		Workflow: workflow.Pin(definition), WorkflowDigest: digest,
		PolicyRevision: configuration.Revision, RegistryRevision: policyAdapter.RegistrySnapshot().Revision,
		AdmittedAt: time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	}

	invocation, created, err := adapter.AdmitNative(value)
	if err != nil || !created {
		t.Fatalf("native admission = %#v, %t, %v", invocation, created, err)
	}
	model := appRunModel(t, store)
	if len(model.Runs) != 1 || model.Runs[0].Causation.PolicyGeneration != snapshot.Generation() ||
		invocation.ID != model.Runs[0].Causation.AdmissionID || invocation.RunID != model.Runs[0].ID || invocation.State != triggerrouter.StateQueued {
		t.Fatalf("canonical native projection = invocation %#v model %#v", invocation, model)
	}
	retried, created, err := adapter.AdmitNative(value)
	if err != nil || created || retried.ID != invocation.ID || len(appRunModel(t, store).Runs) != 1 {
		t.Fatalf("native retry = %#v, %t, %v", retried, created, err)
	}

	value.AdmittedAt = value.AdmittedAt.Add(time.Minute)
	continued, created, err := adapter.AdmitNativeContinuation(value, "message:msg-0123456789abcdef")
	if err != nil || !created || continued.State != triggerrouter.StateRejected || continued.ParentRunID != invocation.RunID {
		t.Fatalf("native continuation = %#v, %t, %v", continued, created, err)
	}
	if owner := appRunModel(t, store).Runs[0]; owner.DuplicateDeliveries != 1 {
		t.Fatalf("native owner did not receive feedback: %#v", owner)
	}
}

func TestNativeAdmitterRejectsMixedPolicyGeneration(t *testing.T) {
	policyAdapter := newPolicyAdapterFixture(t, func() bool { return false })
	store := newAppRunStore(t)
	canonical, _ := runs.NewAdmitter(store)
	adapter, _ := NewNativeAdmitter(policyAdapter.coordinator, canonical)
	snapshot := policyAdapter.coordinator.Snapshot()
	configuration := policy.SettingsView(snapshot)
	definition, _ := configuration.Workflow(workflow.ProviderNeutralID)
	digest, _ := workflow.Digest(definition)
	value := triggerrouter.NativeAdmission{
		Task:     taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"},
		Workflow: workflow.Pin(definition), WorkflowDigest: digest,
		PolicyRevision: configuration.Revision + 1, RegistryRevision: policyAdapter.RegistrySnapshot().Revision,
		AdmittedAt: time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	}
	if _, _, err := adapter.AdmitNative(value); !errors.Is(err, triggerrouter.ErrPolicyConflict) {
		t.Fatalf("mixed policy error = %v", err)
	}
	if len(appRunModel(t, store).Runs) != 0 {
		t.Fatal("mixed policy admission changed canonical Runs")
	}
}

func TestTaskReconcilerOrdersOutboxBeforeRunsAndPropagatesFailure(t *testing.T) {
	order := []string{}
	outbox := &testErrorReconciler{call: func(context.Context) error {
		order = append(order, "outbox")
		return nil
	}}
	manager := &testRunReconciler{call: func(context.Context) { order = append(order, "runs") }}
	reconciler, err := NewTaskReconciler(outbox, manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil || len(order) != 2 || order[0] != "outbox" || order[1] != "runs" {
		t.Fatalf("reconcile order = %#v, err=%v", order, err)
	}

	injected := errors.New("injected outbox failure")
	outbox.call = func(context.Context) error { return injected }
	if err := reconciler.Reconcile(context.Background()); !errors.Is(err, injected) || len(order) != 2 {
		t.Fatalf("outbox failure = %v, order=%#v", err, order)
	}
}

type testErrorReconciler struct{ call func(context.Context) error }

func (r *testErrorReconciler) Reconcile(ctx context.Context) error { return r.call(ctx) }

type testRunReconciler struct{ call func(context.Context) }

func (r *testRunReconciler) Reconcile(ctx context.Context) { r.call(ctx) }

func newAppRunStore(t *testing.T) *runs.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runs.NewSnapshot(runs.EmptyModel())
	if err != nil {
		t.Fatal(err)
	}
	store, err := runs.Create(root, filepath.Join(root, "runs.jsonl"), snapshot, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func appRunModel(t *testing.T, store *runs.Store) runs.Model {
	t.Helper()
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Model()
}
