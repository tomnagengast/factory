package app

import (
	"context"
	"errors"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/runs"
)

type legacyLauncher interface {
	Prepare(context.Context) error
	CleanupWorktrees(context.Context) error
	Start(context.Context, agentrun.Run, string, string, agentrun.StartOptions) error
	SessionExists(context.Context, string) (bool, error)
	ReadResult(string) (agentrun.ProcessResult, error)
	ReadReadyCheckpoint(string) (agentrun.ReadyCheckpoint, error)
}

// RunLauncher retains the hardened tmux, worktree, environment, capability,
// and lifecycle-artifact implementation while projecting its inputs and
// outputs from the canonical Run model.
type RunLauncher struct{ launcher legacyLauncher }

func NewRunLauncher(launcher legacyLauncher) (*RunLauncher, error) {
	if launcher == nil {
		return nil, errors.New("app Run launcher: tmux launcher is required")
	}
	return &RunLauncher{launcher: launcher}, nil
}

func (l *RunLauncher) Prepare(ctx context.Context) error { return l.launcher.Prepare(ctx) }

func (l *RunLauncher) CleanupWorktrees(ctx context.Context) error {
	return l.launcher.CleanupWorktrees(ctx)
}

func (l *RunLauncher) Start(ctx context.Context, run runs.Run, sessionName, runDirectory string) error {
	return l.launcher.Start(ctx, legacyRun(run), sessionName, runDirectory, agentrun.StartOptions{CleanupWorktrees: true})
}

func (l *RunLauncher) SessionExists(ctx context.Context, sessionName string) (bool, error) {
	return l.launcher.SessionExists(ctx, sessionName)
}

func (l *RunLauncher) ReadResult(runDirectory string) (runs.ProcessResult, error) {
	result, err := l.launcher.ReadResult(runDirectory)
	return runs.ProcessResult{
		Status: result.Status, Blocker: result.Blocker, Attempts: result.Attempts,
		ExitCode: result.ExitCode, Detail: result.Detail, FinishedAt: result.FinishedAt,
	}, err
}

func (l *RunLauncher) ReadReadyCheckpoint(runDirectory string) (runs.ReadyCheckpoint, error) {
	checkpoint, err := l.launcher.ReadReadyCheckpoint(runDirectory)
	return runs.ReadyCheckpoint{
		ContractVersion: checkpoint.ContractVersion, RunID: checkpoint.RunID, Task: checkpoint.Task,
		Repository: checkpoint.Repository, PullRequest: checkpoint.PullRequest, BaseBranch: checkpoint.BaseBranch,
		HeadBranch: checkpoint.HeadBranch, VerifiedHeadOID: checkpoint.VerifiedHeadOID,
		PullRequestUpdatedAt: checkpoint.PullRequestUpdatedAt, CreatedAt: checkpoint.CreatedAt, ValidatedAt: checkpoint.ValidatedAt,
	}, err
}

// RunCollector keeps agent JSONL observation and canonical transition delivery
// as distinct mechanisms while presenting one collector to the Run manager.
type RunCollector struct {
	outbox  *runs.OutboxCollector
	records *agentrun.Collector
}

func NewRunCollector(outbox *runs.OutboxCollector, records *agentrun.Collector) (*RunCollector, error) {
	if outbox == nil || records == nil {
		return nil, errors.New("app Run collector: transition outbox and agent record collector are required")
	}
	return &RunCollector{outbox: outbox, records: records}, nil
}

func (c *RunCollector) Collect(ctx context.Context, values []runs.Run) error {
	if err := c.outbox.Deliver(ctx); err != nil {
		return err
	}
	legacy := make([]agentrun.Run, len(values))
	for index, run := range values {
		legacy[index] = legacyRun(run)
	}
	return c.records.Collect(ctx, legacy)
}
