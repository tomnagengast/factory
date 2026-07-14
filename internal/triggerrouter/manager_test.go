package triggerrouter

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
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

type notifierStub struct{ calls int }

func (s *notifierStub) Notify() { s.calls++ }

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
	manager, err := NewManager(routing, runs, resolver, notifier, slog.Default(), func() time.Time { return clock })
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
			manager, _ := NewManager(routing, runs, &resolverStub{err: test.err}, &notifierStub{}, slog.Default(), func() time.Time { return now })
			if err := manager.Reconcile(context.Background()); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if got := routing.Snapshot().Invocations[0].State; got != test.wantState {
				t.Fatalf("state = %q, want %q", got, test.wantState)
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
