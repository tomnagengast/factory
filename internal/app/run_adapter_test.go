package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/server"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

var (
	_ server.RunStore    = (*RunAdapter)(nil)
	_ server.RunNotifier = (*RunAdapter)(nil)
)

func TestRunAdapterProjectsCanonicalLifecycleWithoutLegacyAdmission(t *testing.T) {
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
	defer store.Close()
	admitter, err := runs.NewAdmitter(store)
	if err != nil {
		t.Fatal(err)
	}
	definition := workflow.ProviderNeutralDefault(time.Time{})
	pin := workflow.Pin(definition)
	digest, err := pin.Digest()
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC)
	created, admitted, err := admitter.AdmitNative(runs.NativeAdmission{
		Task:     taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-1", Identifier: "FAC-1"},
		Workflow: pin, WorkflowDigest: digest, PolicyRevision: 1, RegistryRevision: 1, PolicyGeneration: 1, AdmittedAt: createdAt,
	})
	if err != nil || !admitted {
		t.Fatalf("admit canonical run: admitted=%v err=%v", admitted, err)
	}
	notifications := 0
	adapter, err := NewRunAdapter(store, func() triggerregistry.Snapshot {
		return triggerregistry.Snapshot{Schema: triggerregistry.SchemaVersion, Rules: []triggerregistry.Rule{}, Schedules: []triggerregistry.Schedule{}}
	}, func() { notifications++ })
	if err != nil {
		t.Fatal(err)
	}
	public := adapter.PublicSnapshot()
	if public.Total != 1 || public.Active != 1 || len(public.Runs) != 1 || public.Runs[0].ID != created.ID || public.Runs[0].State != agentrun.StatePending {
		t.Fatalf("public projection = %#v", public)
	}
	activity := adapter.ActivitySnapshot()
	if activity.Total != 1 || len(activity.Runs) != 1 || activity.Runs[0].Task.ProviderID != "task-1" {
		t.Fatalf("activity projection = %#v", activity)
	}
	found, ok := adapter.Find(created.ID)
	if !ok || found.InvocationID != created.Causation.AdmissionID || found.PinnedWorkflow == nil || found.PinnedWorkflowDigest != digest {
		t.Fatalf("legacy run projection = %#v, found=%v", found, ok)
	}
	byReference, ok := adapter.FindObserverRun(taskmodel.SourceFactory, "FAC-1", 0)
	if ok || byReference.ID != "" {
		t.Fatal("matched an unstarted observer reference")
	}
	routing := adapter.RoutingSnapshot()
	if len(routing.Decisions) != 1 || len(routing.Invocations) != 1 || routing.Invocations[0].ID != created.Causation.AdmissionID || routing.Invocations[0].State != "queued" {
		t.Fatalf("routing projection = %#v", routing)
	}
	adapter.Notify()
	if notifications != 1 {
		t.Fatalf("notifications = %d", notifications)
	}
	if _, _, err := adapter.Claim(agentrun.Trigger{}, createdAt); err == nil {
		t.Fatal("legacy direct admission remained writable")
	}
	if _, _, err := adapter.ClaimContinuation(agentrun.ContinuationClaim{}, createdAt); err == nil {
		t.Fatal("legacy continuation remained writable")
	}
}
