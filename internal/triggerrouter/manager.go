package triggerrouter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
)

type RunNotifier interface {
	Notify()
}

type Manager struct {
	routing  *Store
	runs     *agentrun.Store
	resolver agentrun.RepositoryResolver
	notifier RunNotifier
	logger   *slog.Logger
	now      func() time.Time
}

func NewManager(routing *Store, runs *agentrun.Store, resolver agentrun.RepositoryResolver, notifier RunNotifier, logger *slog.Logger, now func() time.Time) (*Manager, error) {
	if routing == nil || runs == nil || resolver == nil || notifier == nil || logger == nil || now == nil {
		return nil, errors.New("trigger router manager: dependencies are required")
	}
	return &Manager{routing: routing, runs: runs, resolver: resolver, notifier: notifier, logger: logger, now: now}, nil
}

func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := m.Reconcile(ctx); err != nil && ctx.Err() == nil {
			m.logger.Warn("reconcile trigger invocations", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) Reconcile(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.reconcileReceipts(); err != nil {
		return err
	}
	snapshot := m.routing.Snapshot()
	oldest := make(map[string]Invocation)
	for _, invocation := range snapshot.Invocations {
		if !invocation.Nonterminal() {
			continue
		}
		if _, found := oldest[invocation.IssueIdentifier]; !found {
			oldest[invocation.IssueIdentifier] = invocation
		}
	}
	for _, invocation := range snapshot.Invocations {
		candidate, found := oldest[invocation.IssueIdentifier]
		if !found || candidate.ID != invocation.ID {
			continue
		}
		if err := m.reconcileInvocation(ctx, invocation); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) reconcileReceipts() error {
	for _, run := range m.runs.Snapshot().Runs {
		if run.InvocationID == "" || run.State.Nonterminal() || run.InvocationReflectedAt != nil {
			continue
		}
		invocation, found := m.routing.Invocation(run.InvocationID)
		if found && !invocation.Nonterminal() && invocation.RunID == run.ID {
			if err := m.runs.MarkInvocationReflected(run.ID, m.now()); err != nil {
				return fmt.Errorf("trigger router manager: mark Run reflection: %w", err)
			}
		}
	}
	return nil
}

func (m *Manager) reconcileInvocation(ctx context.Context, invocation Invocation) error {
	switch invocation.State {
	case StateQueued:
		return m.beginClaim(ctx, invocation)
	case StateClaiming:
		return m.finishClaim(ctx, invocation)
	case StateClaimed:
		return m.reflectOrRecover(ctx, invocation)
	default:
		return nil
	}
}

func (m *Manager) beginClaim(ctx context.Context, invocation Invocation) error {
	repository, err := m.resolver.Resolve(ctx, invocation.IssueIdentifier)
	if err != nil {
		if isPermanentRouting(err) {
			_, transitionErr := m.routing.TransitionInvocation(invocation.ID, StateRejected, "", "repository-routing-rejected", nil, m.now())
			return transitionErr
		}
		return nil
	}
	runID := deterministicRunID(invocation.ID)
	if _, err := m.routing.TransitionInvocation(invocation.ID, StateClaiming, runID, "", nil, m.now()); err != nil {
		return err
	}
	invocation.RunID = runID
	return m.ensureAndClaim(invocation, repository)
}

func (m *Manager) finishClaim(ctx context.Context, invocation Invocation) error {
	repository, err := m.resolver.Resolve(ctx, invocation.IssueIdentifier)
	if err != nil {
		if isPermanentRouting(err) {
			_, transitionErr := m.routing.TransitionInvocation(invocation.ID, StateRejected, invocation.RunID, "repository-routing-rejected", nil, m.now())
			return transitionErr
		}
		return nil
	}
	return m.ensureAndClaim(invocation, repository)
}

func (m *Manager) ensureAndClaim(invocation Invocation, repository agentrun.RepositoryConfig) error {
	_, _, err := m.runs.EnsureInvocationRun(agentrun.InvocationClaim{
		RunID: invocation.RunID, InvocationID: invocation.ID, EventID: invocation.EventID,
		IssueIdentifier: invocation.IssueIdentifier, RootEventID: invocation.RootEventID,
		Hop: invocation.Hop, AncestorRuleIDs: invocation.AncestorRuleIDs,
		Workflow: invocation.Workflow, Repository: repository,
	}, m.now())
	if errors.Is(err, agentrun.ErrInvocationIssueOwned) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("trigger router manager: ensure invocation Run: %w", err)
	}
	if _, err := m.routing.TransitionInvocation(invocation.ID, StateClaimed, invocation.RunID, "", nil, m.now()); err != nil {
		return err
	}
	m.notifier.Notify()
	return nil
}

func (m *Manager) reflectOrRecover(ctx context.Context, invocation Invocation) error {
	run, found := m.runs.Find(invocation.RunID)
	if !found {
		repository, err := m.resolver.Resolve(ctx, invocation.IssueIdentifier)
		if err != nil {
			return nil
		}
		_, _, err = m.runs.EnsureInvocationRun(agentrun.InvocationClaim{
			RunID: invocation.RunID, InvocationID: invocation.ID, EventID: invocation.EventID,
			IssueIdentifier: invocation.IssueIdentifier, RootEventID: invocation.RootEventID,
			Hop: invocation.Hop, AncestorRuleIDs: invocation.AncestorRuleIDs,
			Workflow: invocation.Workflow, Repository: repository,
		}, m.now())
		if errors.Is(err, agentrun.ErrInvocationIssueOwned) {
			return nil
		}
		if err != nil {
			return err
		}
		m.notifier.Notify()
		return nil
	}
	if run.State.Nonterminal() {
		return nil
	}
	state := StateFailed
	switch run.State {
	case agentrun.StateSucceeded:
		state = StateSucceeded
	case agentrun.StateBlocked:
		state = StateBlocked
	case agentrun.StateFailed:
		state = StateFailed
	default:
		return fmt.Errorf("trigger router manager: Run %s has unsupported terminal state %q", run.ID, run.State)
	}
	reflected := m.now().UTC()
	if _, err := m.routing.TransitionInvocation(invocation.ID, state, run.ID, run.Detail, &reflected, reflected); err != nil {
		return err
	}
	if err := m.runs.MarkInvocationReflected(run.ID, reflected); err != nil {
		return fmt.Errorf("trigger router manager: mark Run reflection: %w", err)
	}
	return nil
}

func deterministicRunID(invocationID string) string {
	return "run-" + digestStrings("factory-trigger-run-v1", invocationID)[:16]
}

func isPermanentRouting(err error) bool {
	var classified interface{ Permanent() bool }
	return errors.As(err, &classified) && classified.Permanent()
}
