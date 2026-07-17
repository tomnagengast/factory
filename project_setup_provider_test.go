package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
)

type staticProviderCoordinator struct {
	issue projectsetup.ProviderIssue
	calls int
}

func (c *staticProviderCoordinator) Ensure(context.Context, projectsetup.Spec) (projectsetup.ProviderIssue, error) {
	c.calls++
	return c.issue, nil
}

type countingRunNotifier struct{ calls int }

func (n *countingRunNotifier) Notify() { n.calls++ }

type mutableProviderWorkflowSettings struct {
	snapshot settings.Snapshot
	marks    int
}

func (s *mutableProviderWorkflowSettings) Snapshot() settings.Snapshot { return s.snapshot.Clone() }

func (s *mutableProviderWorkflowSettings) MarkWorkflowRollbackIncompatible(now time.Time) (settings.Snapshot, error) {
	s.marks++
	if !s.snapshot.WorkflowRollbackIncompatible {
		s.snapshot.WorkflowRollbackIncompatible = true
		s.snapshot.Revision++
		s.snapshot.UpdatedAt = now.UTC()
	}
	return s.snapshot.Clone(), nil
}

func TestProviderAgentStarterClaimsOneNetworkRun(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := agentrun.Open(filepath.Join(root, "runs.json"), 10)
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	coordinator := &staticProviderCoordinator{issue: projectsetup.ProviderIssue{ID: "issue-provider", Identifier: "ENG-88"}}
	notifier := &countingRunNotifier{}
	workflowSettings := &mutableProviderWorkflowSettings{snapshot: settings.Defaults(3)}
	now := time.Date(2026, time.July, 13, 18, 0, 0, 0, time.UTC)
	starter, err := newProviderAgentStarter(coordinator, store, notifier, workflowSettings, agentrun.RepositoryConfig{
		App: "network", Repository: "tomnagengast/network", RepoURL: "git@github.com:tomnagengast/network.git",
		RepoPath: filepath.Join(root, "network"), ManagedRoot: root, ProjectPath: filepath.Join(root, "network"), BaseBranch: "main",
	}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new provider starter: %v", err)
	}
	spec := projectsetup.Spec{
		ProjectID: "project-cellar", ProjectName: "Cellar", Repository: "tomnagengast/cellar",
		RepoURL: "git@github.com:tomnagengast/cellar.git", LocalPath: filepath.Join(root, "cellar"),
		ManagedRoot: root, CloudURL: "https://cellar.nags.cloud", BaseBranch: "main", Bootstrap: true, Managed: true,
	}
	if err := starter.Start(t.Context(), spec); err != nil {
		t.Fatalf("first start: %v", err)
	}
	firstSnapshot := workflowSettings.Snapshot()
	for index := range workflowSettings.snapshot.Workflows {
		workflowSettings.snapshot.Workflows[index].Enabled = false
	}
	if err := starter.Start(t.Context(), spec); err != nil {
		t.Fatalf("deduplicated start with unavailable binding: %v", err)
	}

	snapshot := store.Snapshot()
	if snapshot.Total != 1 || snapshot.Active != 1 || len(snapshot.Runs) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	run := snapshot.Runs[0]
	if run.IssueIdentifier != "ENG-88" || run.Repository != "tomnagengast/network" || run.RepositoryPath != filepath.Join(root, "network") {
		t.Fatalf("run = %#v", run)
	}
	if run.PinnedWorkflow == nil || run.PinnedWorkflowDigest == "" || run.PinnedPolicyRevision != firstSnapshot.Revision {
		t.Fatalf("run workflow pin = %#v, digest = %q, policy = %d", run.PinnedWorkflow, run.PinnedWorkflowDigest, run.PinnedPolicyRevision)
	}
	if notifier.calls != 1 || coordinator.calls != 2 || run.DuplicateTriggers != 0 {
		t.Fatalf("notifier calls = %d, coordinator calls = %d, duplicate triggers = %d", notifier.calls, coordinator.calls, run.DuplicateTriggers)
	}
	if err := store.Finish(run.ID, agentrun.StateBlocked, 1, "waiting for tenant manifest", now.Add(time.Minute)); err != nil {
		t.Fatalf("finish first run: %v", err)
	}
	workflowSettings.snapshot = firstSnapshot
	spec.CloudURL = "https://new-cellar.nags.cloud"
	if err := starter.Start(t.Context(), spec); err != nil {
		t.Fatalf("changed Cloud URL start: %v", err)
	}
	snapshot = store.Snapshot()
	if snapshot.Total != 2 || snapshot.Active != 1 || notifier.calls != 2 || coordinator.calls != 3 {
		t.Fatalf("changed Cloud URL snapshot = %#v, notifier calls = %d, coordinator calls = %d", snapshot, notifier.calls, coordinator.calls)
	}
}

var _ projectsetup.ProviderCoordinator = (*staticProviderCoordinator)(nil)
var _ agentRunNotifier = (*countingRunNotifier)(nil)
