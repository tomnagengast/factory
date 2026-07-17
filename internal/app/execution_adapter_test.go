package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

var _ runs.Launcher = (*RunLauncher)(nil)

func TestRunLauncherProjectsCanonicalLifecycleThroughHardenedLauncher(t *testing.T) {
	now := time.Date(2026, time.July, 17, 15, 0, 0, 0, time.UTC)
	legacy := &recordingLegacyLauncher{
		result: agentrun.ProcessResult{Status: "failed", Blocker: "decision_required", Attempts: 2, ExitCode: 1, Detail: "blocked", FinishedAt: now},
		ready: agentrun.ReadyCheckpoint{
			ContractVersion: 1, RunID: "run-1", Task: taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-47", Identifier: "ENG-47"},
			Repository: "tomnagengast/factory", PullRequest: 18, BaseBranch: "main", HeadBranch: "eng-47-review",
			VerifiedHeadOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: now,
		},
	}
	adapter, err := NewRunLauncher(legacy)
	if err != nil {
		t.Fatal(err)
	}
	pin := workflow.Pin(workflow.ProviderNeutralDefault(now))
	digest, _ := pin.Digest()
	run := runs.Run{
		ID: "run-1", Causation: runs.Causation{
			AdmissionID: "admission-1", EventID: "linear:event-1", EventSource: "linear", RuleID: "linear-label", RuleRevision: 1,
			Workflow: &pin, WorkflowDigest: digest, PolicyRevision: 2,
			Task: taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-47", Identifier: "ENG-47"},
		},
		Repository: &repositories.Route{
			ProjectID: "project-factory", Repository: "tomnagengast/factory", Origin: "git@github.com:tomnagengast/factory.git",
			ManagedPath: "/managed/factory", ManagedRoot: "/managed", DefaultBranch: "main", CloudURL: "https://factory.nags.cloud",
		},
		TriggerKind: "configured-rule", State: runs.StatePending,
	}
	if err := adapter.Start(context.Background(), run, "factory-linear-1", "/state/runs/run-1"); err != nil {
		t.Fatal(err)
	}
	if legacy.started.Repository != run.Repository.Repository || legacy.started.RepositoryPath != run.Repository.ManagedPath ||
		legacy.started.PinnedWorkflowDigest != digest || !legacy.options.CleanupWorktrees {
		t.Fatalf("legacy launch projection = %#v options=%#v", legacy.started, legacy.options)
	}
	result, err := adapter.ReadResult("/state/runs/run-1")
	if err != nil || result.Status != legacy.result.Status || result.Blocker != legacy.result.Blocker || result.FinishedAt != now {
		t.Fatalf("result projection = %#v, %v", result, err)
	}
	ready, err := adapter.ReadReadyCheckpoint("/state/runs/run-1")
	if err != nil || ready.RunID != legacy.ready.RunID || !reflect.DeepEqual(ready.Task, legacy.ready.Task) || ready.VerifiedHeadOID != legacy.ready.VerifiedHeadOID {
		t.Fatalf("ready projection = %#v, %v", ready, err)
	}
}

type recordingLegacyLauncher struct {
	started agentrun.Run
	options agentrun.StartOptions
	result  agentrun.ProcessResult
	ready   agentrun.ReadyCheckpoint
}

func (*recordingLegacyLauncher) Prepare(context.Context) error          { return nil }
func (*recordingLegacyLauncher) CleanupWorktrees(context.Context) error { return nil }
func (l *recordingLegacyLauncher) Start(_ context.Context, run agentrun.Run, _ string, _ string, options agentrun.StartOptions) error {
	l.started, l.options = run, options
	return nil
}
func (*recordingLegacyLauncher) SessionExists(context.Context, string) (bool, error) {
	return false, nil
}
func (l *recordingLegacyLauncher) ReadResult(string) (agentrun.ProcessResult, error) {
	if l.result.Status == "" {
		return agentrun.ProcessResult{}, errors.New("missing")
	}
	return l.result, nil
}
func (l *recordingLegacyLauncher) ReadReadyCheckpoint(string) (agentrun.ReadyCheckpoint, error) {
	return l.ready, nil
}
