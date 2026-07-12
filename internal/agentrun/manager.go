package agentrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type Launcher interface {
	Prepare(context.Context) error
	CleanupWorktrees(context.Context) error
	Start(context.Context, Run, string, string) error
	SessionExists(context.Context, string) (bool, error)
	ReadResult(string) (ProcessResult, error)
	ReadReadyCheckpoint(string) (ReadyCheckpoint, error)
}

type RunCollector interface {
	Collect(context.Context, []Run) error
}

type LifecycleConfig struct {
	Repository string
	BaseBranch string
}

type Manager struct {
	store         *Store
	launcher      Launcher
	collector     RunCollector
	pullRequests  PullRequestReader
	terminal      TerminalValidator
	lifecycle     LifecycleConfig
	stateRoot     string
	maxConcurrent int
	pollInterval  time.Duration
	mergeInterval time.Duration
	logger        *slog.Logger
	now           func() time.Time
	notify        chan struct{}
}

func NewManager(
	store *Store,
	launcher Launcher,
	collector RunCollector,
	pullRequests PullRequestReader,
	terminal TerminalValidator,
	lifecycle LifecycleConfig,
	stateRoot string,
	maxConcurrent int,
	pollInterval time.Duration,
	mergeInterval time.Duration,
	logger *slog.Logger,
	now func() time.Time,
) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("agent run manager: store is required")
	}
	if launcher == nil {
		return nil, fmt.Errorf("agent run manager: launcher is required")
	}
	if collector == nil {
		return nil, fmt.Errorf("agent run manager: collector is required")
	}
	if pullRequests == nil {
		return nil, fmt.Errorf("agent run manager: pull request reader is required")
	}
	if terminal == nil {
		return nil, fmt.Errorf("agent run manager: terminal validator is required")
	}
	if !repositoryPattern.MatchString(lifecycle.Repository) || !validBranch(lifecycle.BaseBranch) {
		return nil, fmt.Errorf("agent run manager: repository and base branch are required")
	}
	if stateRoot == "" {
		return nil, fmt.Errorf("agent run manager: state root is required")
	}
	if maxConcurrent < 1 {
		return nil, fmt.Errorf("agent run manager: max concurrency must be positive")
	}
	if pollInterval <= 0 {
		return nil, fmt.Errorf("agent run manager: poll interval must be positive")
	}
	if mergeInterval <= 0 {
		return nil, fmt.Errorf("agent run manager: merge interval must be positive")
	}
	if logger == nil {
		return nil, fmt.Errorf("agent run manager: logger is required")
	}
	if now == nil {
		return nil, fmt.Errorf("agent run manager: clock is required")
	}
	return &Manager{
		store:         store,
		launcher:      launcher,
		collector:     collector,
		pullRequests:  pullRequests,
		terminal:      terminal,
		lifecycle:     lifecycle,
		stateRoot:     stateRoot,
		maxConcurrent: maxConcurrent,
		pollInterval:  pollInterval,
		mergeInterval: mergeInterval,
		logger:        logger,
		now:           now,
		notify:        make(chan struct{}, 1),
	}, nil
}

func (m *Manager) Notify() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	m.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.notify:
			m.reconcile(ctx)
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	if err := m.collector.Collect(ctx, m.store.Snapshot().Runs); err != nil {
		m.logger.Warn("collect agent events", "error", err)
	}
	defer func() {
		if err := m.collector.Collect(ctx, m.store.Snapshot().Runs); err != nil {
			m.logger.Warn("collect agent events", "error", err)
		}
	}()

	snapshot := m.store.Snapshot()
	running := 0
	for _, run := range snapshot.Runs {
		switch run.State {
		case StateStarting, StateRunning:
			running++
			m.reconcileActive(ctx, run)
		case StateAwaitingMerge:
			m.reconcileAwaitingMerge(ctx, run)
		}
	}

	if running >= m.maxConcurrent {
		return
	}
	prepared := false
	for _, run := range snapshot.Runs {
		if (run.State != StatePending && run.State != StatePostMergePending) || running >= m.maxConcurrent {
			continue
		}
		if !prepared {
			if err := m.launcher.Prepare(ctx); err != nil {
				m.logger.Error("prepare agent workspace", "error", err)
				return
			}
			if running == 0 {
				if err := m.launcher.CleanupWorktrees(ctx); err != nil {
					m.logger.Warn("clean agent worktrees", "error", err)
				}
			}
			prepared = true
		}
		if m.start(ctx, run) {
			running++
		}
	}
}

func (m *Manager) reconcileActive(ctx context.Context, run Run) {
	exists, err := m.launcher.SessionExists(ctx, run.SessionName)
	if err != nil {
		m.logger.Warn("inspect agent session", "run_id", run.ID, "error", err)
		return
	}
	if exists {
		if run.State == StateStarting {
			if err := m.store.MarkRunning(run.ID, max(run.Attempts, 1), m.now()); err != nil {
				m.logger.Error("mark agent running", "run_id", run.ID, "error", err)
			}
		}
		return
	}

	result, err := m.launcher.ReadResult(run.RunDirectory)
	if err != nil {
		detail := "tmux session ended without a process result"
		if finishErr := m.store.Finish(run.ID, StateFailed, run.Attempts, detail, m.now()); finishErr != nil {
			m.logger.Error("finish lost agent run", "run_id", run.ID, "error", finishErr)
		}
		return
	}
	if run.SegmentStartedAt == nil || result.Attempts <= run.SegmentAttempt || result.FinishedAt.Before(*run.SegmentStartedAt) {
		detail := "rejected stale or unbound process result"
		if finishErr := m.store.Finish(run.ID, StateFailed, run.Attempts, detail, m.now()); finishErr != nil {
			m.logger.Error("finish invalid agent result", "run_id", run.ID, "error", finishErr)
		}
		return
	}
	if result.Status == ResultReadyForMerge {
		m.parkReadyRun(ctx, run, result)
		return
	}
	state := State(result.Status)
	decision := m.terminal.Validate(ctx, run, result)
	if decision.Repark && run.Ready != nil {
		now := m.now()
		if err := m.store.ReparkRejected(run.ID, *run.Ready, now.Add(m.mergeInterval), result.Attempts, decision.Validation, now); err != nil {
			m.logger.Error("repark agent run", "run_id", run.ID, "error", err)
			return
		}
		m.logger.Warn("rejected terminal agent intent", "run_id", run.ID, "intent", result.Status, "reason", decision.Validation.Reason)
		return
	}
	state = decision.State
	if finishErr := m.store.FinishValidated(run.ID, state, result.Attempts, decision.Detail, decision.Validation, result.FinishedAt); finishErr != nil {
		m.logger.Error("finish agent run", "run_id", run.ID, "error", finishErr)
		return
	}
	m.logger.Info("agent run finished", "run_id", run.ID, "state", state, "attempts", result.Attempts)
}

func (m *Manager) parkReadyRun(ctx context.Context, run Run, result ProcessResult) {
	checkpoint, err := m.launcher.ReadReadyCheckpoint(run.RunDirectory)
	if err != nil {
		m.finishInvalidReady(run, result, err)
		return
	}
	if err := m.validateCheckpoint(run, checkpoint); err != nil {
		m.finishInvalidReady(run, result, err)
		return
	}
	snapshot, err := m.pullRequests.Snapshot(ctx, checkpoint)
	if err != nil {
		m.finishInvalidReady(run, result, err)
		return
	}
	if err := validateReadySnapshot(checkpoint, snapshot); err != nil {
		m.finishInvalidReady(run, result, err)
		return
	}
	checkpoint.Repository = m.lifecycle.Repository
	checkpoint.PullRequestUpdatedAt = snapshot.UpdatedAt
	now := m.now()
	if err := m.store.MarkAwaitingMerge(run.ID, checkpoint, now.Add(m.mergeInterval), result.Attempts, now); err != nil {
		m.logger.Error("park agent run", "run_id", run.ID, "error", err)
		return
	}
	m.logger.Info("agent run awaiting human merge", "run_id", run.ID, "repository", checkpoint.Repository, "pull_request", checkpoint.PullRequest)
}

func (m *Manager) finishInvalidReady(run Run, result ProcessResult, cause error) {
	detail := "invalid ready checkpoint: " + cause.Error()
	if err := m.store.Finish(run.ID, StateFailed, result.Attempts, detail, m.now()); err != nil {
		m.logger.Error("finish invalid ready checkpoint", "run_id", run.ID, "error", err)
		return
	}
	m.logger.Error("reject ready checkpoint", "run_id", run.ID, "error", cause)
}

func validateReadySnapshot(checkpoint ReadyCheckpoint, snapshot PullRequestSnapshot) error {
	if snapshot.State != "OPEN" {
		return fmt.Errorf("pull request state is %q, want OPEN", snapshot.State)
	}
	if snapshot.IsDraft {
		return fmt.Errorf("pull request is still a draft")
	}
	if snapshot.BaseBranch != checkpoint.BaseBranch {
		return fmt.Errorf("pull request base branch is %q, want %q", snapshot.BaseBranch, checkpoint.BaseBranch)
	}
	if snapshot.HeadBranch != checkpoint.HeadBranch {
		return fmt.Errorf("pull request head branch is %q, want %q", snapshot.HeadBranch, checkpoint.HeadBranch)
	}
	if snapshot.HeadOID != checkpoint.VerifiedHeadOID {
		return fmt.Errorf("pull request head is %q, want verified head %q", snapshot.HeadOID, checkpoint.VerifiedHeadOID)
	}
	return nil
}

func (m *Manager) validateCheckpoint(run Run, checkpoint ReadyCheckpoint) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	if !strings.EqualFold(checkpoint.Repository, m.lifecycle.Repository) {
		return fmt.Errorf("repository is %q, want configured repository %q", checkpoint.Repository, m.lifecycle.Repository)
	}
	if checkpoint.BaseBranch != m.lifecycle.BaseBranch {
		return fmt.Errorf("base branch is %q, want configured base %q", checkpoint.BaseBranch, m.lifecycle.BaseBranch)
	}
	if run.SegmentStartedAt == nil || checkpoint.CreatedAt.Before(*run.SegmentStartedAt) {
		return errors.New("checkpoint predates the current lifecycle segment")
	}
	return nil
}

func validateMergedSnapshot(checkpoint ReadyCheckpoint, snapshot PullRequestSnapshot) error {
	if snapshot.State != "MERGED" {
		return fmt.Errorf("pull request state is %q, want MERGED", snapshot.State)
	}
	if snapshot.BaseBranch != checkpoint.BaseBranch {
		return fmt.Errorf("merged pull request base branch is %q, want %q", snapshot.BaseBranch, checkpoint.BaseBranch)
	}
	if snapshot.HeadBranch != checkpoint.HeadBranch || snapshot.HeadOID != checkpoint.VerifiedHeadOID {
		return errors.New("merged pull request head does not match the verified checkpoint")
	}
	if !gitOIDPattern.MatchString(snapshot.MergeCommitOID) {
		return errors.New("merged pull request has no valid merge commit OID")
	}
	return nil
}

func (m *Manager) reconcileAwaitingMerge(ctx context.Context, run Run) {
	if run.Ready == nil {
		if err := m.store.Finish(run.ID, StateFailed, run.Attempts, "awaiting merge without a ready checkpoint", m.now()); err != nil {
			m.logger.Error("finish invalid awaiting run", "run_id", run.ID, "error", err)
		}
		return
	}
	now := m.now()
	if run.NextReconcileAt != nil && now.Before(*run.NextReconcileAt) {
		return
	}
	snapshot, err := m.pullRequests.Snapshot(ctx, *run.Ready)
	if err != nil {
		next := now.Add(reconcileDelay(m.mergeInterval, run.ReconcileFailures))
		if deferErr := m.store.DeferMergeReconcile(run.ID, "GitHub refresh failed: "+err.Error(), next, true, now); deferErr != nil {
			m.logger.Error("defer merge reconciliation", "run_id", run.ID, "error", deferErr)
		}
		return
	}
	switch snapshot.State {
	case "OPEN":
		updated := !run.Ready.PullRequestUpdatedAt.IsZero() && snapshot.UpdatedAt.After(run.Ready.PullRequestUpdatedAt)
		if run.RemediationRequested || snapshot.SafeguardRegression || updated || snapshot.IsDraft || snapshot.HeadBranch != run.Ready.HeadBranch || snapshot.HeadOID != run.Ready.VerifiedHeadOID {
			if err := m.store.ResumeAwaiting(run.ID, TriggerKindGitHub, "", "pull request changed; resuming remediation", now); err != nil {
				m.logger.Error("resume changed pull request", "run_id", run.ID, "error", err)
			}
			return
		}
		if err := m.store.DeferMergeReconcile(run.ID, "waiting for human merge", now.Add(m.mergeInterval), false, now); err != nil {
			m.logger.Error("defer open pull request", "run_id", run.ID, "error", err)
		}
	case "MERGED":
		detail := "pull request merged; resuming post-merge lifecycle"
		if snapshot.MergeCommitOID == "" || snapshot.HeadOID != run.Ready.VerifiedHeadOID {
			detail = "merged pull request requires authoritative blocker review"
		}
		if err := m.store.ResumeAwaiting(run.ID, TriggerKindPostMerge, snapshot.MergeCommitOID, detail, now); err != nil {
			m.logger.Error("resume merged pull request", "run_id", run.ID, "error", err)
		}
	case "CLOSED":
		if err := m.store.ResumeAwaiting(run.ID, TriggerKindPostMerge, "", "pull request closed without merge; resuming blocker report", now); err != nil {
			m.logger.Error("resume closed pull request", "run_id", run.ID, "error", err)
		}
	default:
		next := now.Add(reconcileDelay(m.mergeInterval, run.ReconcileFailures))
		if err := m.store.DeferMergeReconcile(run.ID, "unknown pull request state: "+snapshot.State, next, true, now); err != nil {
			m.logger.Error("defer unknown pull request state", "run_id", run.ID, "error", err)
		}
	}
}

func reconcileDelay(base time.Duration, failures int) time.Duration {
	if failures < 0 {
		failures = 0
	}
	if failures > 4 {
		failures = 4
	}
	return base * time.Duration(1<<failures)
}

func (m *Manager) start(ctx context.Context, run Run) bool {
	sessionName := sessionName(run.IssueIdentifier)
	runDirectory := runPath(m.stateRoot, run.ID)
	if err := m.store.MarkStarting(run.ID, sessionName, runDirectory, m.now()); err != nil {
		m.logger.Error("mark agent starting", "run_id", run.ID, "error", err)
		return false
	}
	if err := m.launcher.Start(ctx, run, sessionName, runDirectory); err != nil {
		detail := fmt.Sprintf("start tmux session: %v", err)
		if finishErr := m.store.Finish(run.ID, StateFailed, 0, detail, m.now()); finishErr != nil {
			m.logger.Error("record agent start failure", "run_id", run.ID, "error", finishErr)
		}
		m.logger.Error("start agent run", "run_id", run.ID, "error", err)
		return false
	}
	if err := m.store.MarkRunning(run.ID, 1, m.now()); err != nil {
		m.logger.Error("mark launched agent running", "run_id", run.ID, "error", err)
	}
	m.logger.Info("agent run started", "run_id", run.ID, "session", sessionName)
	return true
}
