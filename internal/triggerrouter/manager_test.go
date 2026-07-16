package triggerrouter

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

type resolverStub struct {
	config agentrun.RepositoryConfig
	err    error
	calls  int
}

func (s *resolverStub) Resolve(context.Context, string) (agentrun.RepositoryConfig, error) {
	s.calls++
	return s.config, s.err
}

func (s *resolverStub) ResolveTask(_ context.Context, _ taskmodel.TaskRef) (agentrun.RepositoryConfig, error) {
	s.calls++
	return s.config, s.err
}

type notifierStub struct{ calls int }

func (s *notifierStub) Notify() { s.calls++ }

type dispatchStub struct{ dispatched uint64 }

func (s dispatchStub) Status() eventwire.Status { return eventwire.Status{Dispatched: s.dispatched} }

func TestManagerSerializesSameIssueAndReflectsTerminalRun(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	clock := now
	configuration, registry := testPolicy()
	registry.Rules = registry.Rules[:1]
	routing, err := Open(filepath.Join(directory, "routing.jsonl"))
	if err != nil {
		t.Fatalf("open routing: %v", err)
	}
	records := []eventwire.Record{
		testRecord("factory:one", 1, eventwire.SourceFactory, now),
		testRecord("factory:two", 2, eventwire.SourceFactory, now),
	}
	if _, err := routing.ApplyDecisionBatch(records, registry, configuration, now); err != nil {
		t.Fatalf("admit: %v", err)
	}
	runs, err := agentrun.Open(filepath.Join(directory, "runs.json"), 100)
	if err != nil {
		t.Fatalf("open runs: %v", err)
	}
	resolver := &resolverStub{config: testRepository(directory)}
	notifier := &notifierStub{}
	manager, err := NewManager(routing, runs, dispatchStub{dispatched: 2}, resolver, notifier, slog.Default(), func() time.Time { return clock })
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	snapshot := routing.Snapshot()
	if snapshot.Invocations[0].State != StateClaimed || snapshot.Invocations[1].State != StateQueued {
		t.Fatalf("serialized invocations = %#v", snapshot.Invocations)
	}
	run := runs.Snapshot().Runs[0]
	if run.InvocationID != snapshot.Invocations[0].ID || run.PinnedWorkflow == nil || !routing.ClaimedInvocation(run.InvocationID, run.ID) {
		t.Fatalf("claimed Run = %#v", run)
	}
	clock = clock.Add(time.Minute)
	if err := runs.Finish(run.ID, agentrun.StateSucceeded, 1, "complete", clock); err != nil {
		t.Fatalf("finish Run: %v", err)
	}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatalf("reflect: %v", err)
	}
	first, found := routing.Invocation(run.InvocationID)
	if !found || first.State != StateSucceeded || first.Workflow.ID == "" || len(first.Workflow.Steps) != 0 {
		t.Fatalf("reflected invocation = %#v, found=%t", first, found)
	}
	reflectedRun, _ := runs.Find(run.ID)
	if reflectedRun.InvocationReflectedAt == nil {
		t.Fatalf("Run reflection receipt = %#v", reflectedRun)
	}
	if reflectedRun.PinnedWorkflow == nil || reflectedRun.PinnedWorkflow.ID == "" || len(reflectedRun.PinnedWorkflow.Steps) != 0 {
		t.Fatalf("reflected Run retained execution payload = %#v", reflectedRun)
	}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatalf("promote second: %v", err)
	}
	if second := routing.Snapshot().Invocations[1]; second.State != StateClaimed {
		t.Fatalf("second invocation = %#v", second)
	}
}

func TestManagerRejectsPermanentRoutingButRetriesTransientFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		err       error
		wantState string
	}{
		{name: "permanent", err: permanentTestError{errors.New("not allowlisted")}, wantState: StateRejected},
		{name: "transient", err: errors.New("Linear unavailable"), wantState: StateQueued},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
			configuration, registry := testPolicy()
			registry.Rules = registry.Rules[:1]
			routing, _ := Open(filepath.Join(directory, "routing.jsonl"))
			_, _ = routing.ApplyDecisionBatch([]eventwire.Record{testRecord("factory:one", 1, eventwire.SourceFactory, now)}, registry, configuration, now)
			runs, _ := agentrun.Open(filepath.Join(directory, "runs.json"), 100)
			manager, _ := NewManager(routing, runs, dispatchStub{dispatched: 1}, &resolverStub{err: test.err}, &notifierStub{}, slog.Default(), func() time.Time { return now })
			if err := manager.Reconcile(context.Background()); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if got := routing.Snapshot().Invocations[0].State; got != test.wantState {
				t.Fatalf("state = %q, want %q", got, test.wantState)
			}
		})
	}
}

func TestManagerWaitsForProtectedDispatchBeforePromotion(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	configuration, registry := testPolicy()
	registry.Rules = registry.Rules[:1]
	routing, _ := Open(filepath.Join(directory, "routing.jsonl"))
	_, _ = routing.ApplyDecisionBatch([]eventwire.Record{testRecord("factory:one", 1, eventwire.SourceFactory, now)}, registry, configuration, now)
	runs, _ := agentrun.Open(filepath.Join(directory, "runs.json"), 100)
	gate := &dispatchStub{}
	manager, _ := NewManager(routing, runs, gate, &resolverStub{config: testRepository(directory)}, &notifierStub{}, slog.Default(), func() time.Time { return now })
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := routing.Snapshot().Invocations[0].State; got != StateQueued || len(runs.Snapshot().Runs) != 0 {
		t.Fatalf("promoted before protected dispatch: state=%q runs=%#v", got, runs.Snapshot().Runs)
	}
	gate.dispatched = 1
	if err := manager.ReconcileExisting(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := routing.Snapshot().Invocations[0].State; got != StateQueued {
		t.Fatalf("startup repair promoted new queued work: state=%q", got)
	}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := routing.Snapshot().Invocations[0].State; got != StateClaimed || len(runs.Snapshot().Runs) != 1 {
		t.Fatalf("did not promote after protected dispatch: state=%q runs=%#v", got, runs.Snapshot().Runs)
	}
}

func TestManagerCoalescesNativeFeedbackIntoAwaitingRun(t *testing.T) {
	for _, eventKey := range []string{"message:msg-0123456789abcdef", "gate:gate-0123456789abcdef:approved"} {
		t.Run(eventKey, func(t *testing.T) {
			directory := t.TempDir()
			clock := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
			routing, err := Open(filepath.Join(directory, "routing.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			runs, err := agentrun.Open(filepath.Join(directory, "runs.json"), 100)
			if err != nil {
				t.Fatal(err)
			}
			configuration, _ := testPolicy()
			pinned := workflow.Pin(configuration.Workflows[0])
			digest, err := pinned.Digest()
			if err != nil {
				t.Fatal(err)
			}
			task := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"}
			admission := NativeAdmission{Task: task, Workflow: pinned, WorkflowDigest: digest, PolicyRevision: configuration.Revision, RegistryRevision: 1, AdmittedAt: clock}
			started, _, err := routing.AdmitNative(admission)
			if err != nil {
				t.Fatal(err)
			}
			notifier := &notifierStub{}
			manager, _ := NewManager(routing, runs, dispatchStub{dispatched: 100}, &resolverStub{config: testRepository(directory)}, notifier, slog.Default(), func() time.Time { return clock })
			if err := manager.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			owner := runs.Snapshot().Runs[0]
			if err := runs.MarkStarting(owner.ID, "native", filepath.Join(directory, "run"), clock); err != nil {
				t.Fatal(err)
			}
			if err := runs.MarkRunning(owner.ID, 1, clock); err != nil {
				t.Fatal(err)
			}
			checkpoint := agentrun.ReadyCheckpoint{
				ContractVersion: agentrun.LifecycleContractVersion, RunID: owner.ID, Task: task,
				Repository: "tomnagengast/factory", PullRequest: 15, BaseBranch: "main",
				HeadBranch: "factory-task-0123456789abcdef-work", VerifiedHeadOID: strings.Repeat("a", 40), CreatedAt: clock,
			}
			if err := runs.MarkAwaitingMerge(owner.ID, checkpoint, clock.Add(time.Minute), 1, clock); err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Minute)
			continuation, _, err := routing.AdmitNativeContinuation(admission, eventKey)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			resumed, _ := runs.Find(owner.ID)
			if resumed.State != agentrun.StatePending || resumed.ResumeCount != 1 || len(resumed.DeliveryIDs) != 2 || len(runs.Snapshot().Runs) != 1 {
				t.Fatalf("resumed Run = %#v", resumed)
			}
			terminal, _ := routing.Invocation(continuation.ID)
			if terminal.State != StateRejected || terminal.RunID != owner.ID || terminal.ReflectedAt == nil || terminal.Reason != "native-feedback-coalesced" {
				t.Fatalf("continuation = %#v", terminal)
			}
			if started.State != StateQueued || notifier.calls < 2 {
				t.Fatalf("notifications=%d started=%#v", notifier.calls, started)
			}
			if err := manager.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			repeated, _ := runs.Find(owner.ID)
			if repeated.ResumeCount != 1 || len(repeated.DeliveryIDs) != 2 {
				t.Fatalf("repeat changed Run = %#v", repeated)
			}
		})
	}
}

type permanentTestError struct{ error }

func (permanentTestError) Permanent() bool { return true }

func testRepository(directory string) agentrun.RepositoryConfig {
	root := filepath.Join(directory, "repos")
	return agentrun.RepositoryConfig{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "https://github.com/tomnagengast/factory",
		RepoPath: filepath.Join(root, "factory"), ManagedRoot: root, BaseBranch: "main",
	}
}
