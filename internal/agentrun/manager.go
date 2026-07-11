package agentrun

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type Launcher interface {
	Prepare(context.Context) error
	CleanupWorktrees(context.Context) error
	Start(context.Context, Run, string, string) error
	SessionExists(context.Context, string) (bool, error)
	ReadResult(string) (ProcessResult, error)
}

type Manager struct {
	store         *Store
	launcher      Launcher
	stateRoot     string
	maxConcurrent int
	pollInterval  time.Duration
	logger        *slog.Logger
	now           func() time.Time
	notify        chan struct{}
}

func NewManager(
	store *Store,
	launcher Launcher,
	stateRoot string,
	maxConcurrent int,
	pollInterval time.Duration,
	logger *slog.Logger,
	now func() time.Time,
) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("agent run manager: store is required")
	}
	if launcher == nil {
		return nil, fmt.Errorf("agent run manager: launcher is required")
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
	if logger == nil {
		return nil, fmt.Errorf("agent run manager: logger is required")
	}
	if now == nil {
		return nil, fmt.Errorf("agent run manager: clock is required")
	}
	return &Manager{
		store:         store,
		launcher:      launcher,
		stateRoot:     stateRoot,
		maxConcurrent: maxConcurrent,
		pollInterval:  pollInterval,
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

	snapshot := m.store.Snapshot()
	running := 0
	for _, run := range snapshot.Runs {
		switch run.State {
		case StateStarting, StateRunning:
			running++
			m.reconcileActive(ctx, run)
		}
	}

	if running >= m.maxConcurrent {
		return
	}
	prepared := false
	for _, run := range snapshot.Runs {
		if run.State != StatePending || running >= m.maxConcurrent {
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
	state := State(result.Status)
	if finishErr := m.store.Finish(run.ID, state, result.Attempts, result.Detail, result.FinishedAt); finishErr != nil {
		m.logger.Error("finish agent run", "run_id", run.ID, "error", finishErr)
		return
	}
	m.logger.Info("agent run finished", "run_id", run.ID, "state", state, "attempts", result.Attempts)
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
