package runs

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/taskmodel"
)

// This file proves the dormant canonical runs.Manager preserves the behavior of
// the legacy agentrun manager and repository routing it replaces, by driving the
// real legacy owners (agentrun.Manager, agentrun.MechanicalCompletionValidator,
// agentrun.LinearRepositoryResolver) alongside the canonical manager through
// shared scenarios. Only the non-PR spine that this slice implements is
// exercised; merge parking and GitHub reconciliation are deferred.

// ---- shared legacy collaborators ---------------------------------------

// equivLauncher is safe for the concurrent access the legacy manager's Run loop
// and the driving test make. Its maps are guarded so the race detector stays
// clean while the goroutine reconciles against test-driven worker signals.
type equivLauncher struct {
	mu       sync.Mutex
	sessions map[string]bool
	results  map[string]agentrun.ProcessResult
	startErr error
}

func newEquivLauncher() *equivLauncher {
	return &equivLauncher{sessions: map[string]bool{}, results: map[string]agentrun.ProcessResult{}}
}

func (l *equivLauncher) Prepare(context.Context) error          { return nil }
func (l *equivLauncher) CleanupWorktrees(context.Context) error { return nil }

func (l *equivLauncher) Start(_ context.Context, _ agentrun.Run, session, _ string, _ agentrun.StartOptions) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.startErr != nil {
		return l.startErr
	}
	l.sessions[session] = true
	return nil
}

func (l *equivLauncher) SessionExists(_ context.Context, session string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessions[session], nil
}

func (l *equivLauncher) ReadResult(directory string) (agentrun.ProcessResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	result, ok := l.results[directory]
	if !ok {
		return agentrun.ProcessResult{}, errors.New("no result")
	}
	return result, nil
}

func (l *equivLauncher) endSession(session string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sessions[session] = false
}

func (l *equivLauncher) setResult(directory string, result agentrun.ProcessResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.results[directory] = result
}

func (l *equivLauncher) ReadReadyCheckpoint(string) (agentrun.ReadyCheckpoint, error) {
	return agentrun.ReadyCheckpoint{}, errors.New("no checkpoint")
}

type equivCollector struct{}

func (equivCollector) Collect(context.Context, []agentrun.Run) error { return nil }

// emptyPullRequests answers no matching pull requests, so a mechanical success
// ruling for a Run without a validated checkpoint fails closed on both managers.
type emptyPullRequests struct{}

func (emptyPullRequests) Snapshot(context.Context, agentrun.ReadyCheckpoint) (agentrun.PullRequestSnapshot, error) {
	return agentrun.PullRequestSnapshot{}, errors.New("no pull request")
}

func (emptyPullRequests) MatchingIssuePullRequests(context.Context, string, string) ([]agentrun.PullRequestSnapshot, error) {
	return nil, nil
}

type completeEvidence struct{}

func (completeEvidence) ReadCompletionEvidence(context.Context, agentrun.Run, agentrun.PullRequestSnapshot) (agentrun.CompletionEvidence, error) {
	return agentrun.CompletionEvidence{
		SourceValid: true, MergeContained: true, VerifiedHeadContained: true, HealthMatches: true,
		RemoteBranchAbsent: true, WorktreeAbsent: true, TaskComplete: true, ChildrenComplete: true,
	}, nil
}

func mustMechanicalValidator(t *testing.T, now func() time.Time) *agentrun.MechanicalCompletionValidator {
	t.Helper()
	validator, err := agentrun.NewMechanicalCompletionValidator(emptyPullRequests{}, completeEvidence{}, "tomnagengast/network", now)
	if err != nil {
		t.Fatalf("mechanical validator: %v", err)
	}
	return validator
}

// legacyValidatorAdapter feeds the canonical manager the exact legacy mechanical
// completion authority through a body-free projection. It is test-only: the
// slice ships no production adapter.
type legacyValidatorAdapter struct {
	validator *agentrun.MechanicalCompletionValidator
}

func (a legacyValidatorAdapter) ValidateTerminal(ctx context.Context, run Run, result ProcessResult) TerminalDecision {
	legacyRun := agentrun.Run{Task: run.Causation.Task}
	if run.Repository != nil {
		legacyRun.Repository = run.Repository.Repository
	}
	decision := a.validator.Validate(ctx, legacyRun, agentrun.ProcessResult{
		Status: result.Status, Blocker: result.Blocker, Attempts: result.Attempts,
		Detail: result.Detail, FinishedAt: result.FinishedAt,
	})
	return TerminalDecision{
		State:  LifecycleState(decision.State),
		Detail: decision.Detail,
		Validation: CompletionValidation{
			Accepted: decision.Validation.Accepted, Intent: decision.Validation.Intent,
			Blocker: decision.Validation.Blocker, State: LifecycleState(decision.Validation.State),
			Reason: decision.Validation.Reason,
		},
	}
}

// ---- non-PR terminal ruling parity -------------------------------------

func TestManagerAppliesLegacyMechanicalTerminalRulings(t *testing.T) {
	cases := map[string]struct {
		status  string
		blocker string
	}{
		"process failure":      {status: string(StateFailed)},
		"pre-PR typed blocker": {status: string(StateBlocked), blocker: agentrun.BlockerMissingRoutingMetadata},
		"unvalidated success":  {status: string(StateSucceeded)},
		"unsupported blocker":  {status: string(StateBlocked), blocker: agentrun.BlockerSafeguardRegression},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newManagerHarness(t, 2)
			validator := mustMechanicalValidator(t, func() time.Time { return h.clock })
			h.manager.terminal = legacyValidatorAdapter{validator: validator}

			batch, run, rate := runningProjection(t, h.stateRoot)
			h.install(batch, run, rate)
			installed := h.run(run.ID)
			h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
			h.launcher.result = func(string) (ProcessResult, error) {
				return ProcessResult{Status: tc.status, Blocker: tc.blocker, Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
			}

			// Independently ask the legacy validator for its ruling on an
			// equivalent Run so the assertion is not self-referential.
			ruling := validator.Validate(context.Background(), agentrun.Run{Task: installed.Causation.Task, Repository: installed.Repository.Repository}, agentrun.ProcessResult{
				Status: tc.status, Blocker: tc.blocker, Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second),
			})

			h.reconcile()
			finished := h.run(run.ID)
			if !finished.State.Terminal() {
				t.Fatalf("canonical manager left Run in %q", finished.State)
			}
			if string(finished.State) != string(ruling.State) {
				t.Fatalf("canonical terminal state %q != legacy ruling %q", finished.State, ruling.State)
			}
			if finished.Completion == nil || finished.Completion.Accepted != ruling.Validation.Accepted {
				t.Fatalf("canonical accepted %v != legacy accepted %v", finished.Completion, ruling.Validation.Accepted)
			}
		})
	}
}

// ---- full-manager start + terminal parity ------------------------------

func TestManagerMatchesLegacyStartAndInvalidSuccessLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 16, 21, 0, 0, 0, time.UTC)

	// Legacy owner: real agentrun.Store + Manager driven through its Run loop.
	legacyState := legacyRunToTerminal(t, now)

	// Canonical owner: runs.Manager with the same mechanical validator.
	h := newManagerHarness(t, 3)
	h.clock = now.Add(time.Hour)
	h.manager.terminal = legacyValidatorAdapter{validator: mustMechanicalValidator(t, func() time.Time { return h.clock })}
	batch, run, rate := testAdmissionProjection(t, h.stateRoot, 1, StatePending)
	h.install(batch, run, rate)
	h.reconcile() // pending -> starting -> running
	running := h.run(run.ID)
	if running.State != StateRunning {
		t.Fatalf("canonical run state = %q, want running", running.State)
	}
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateSucceeded), Attempts: running.SegmentAttempt + 1, FinishedAt: running.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.reconcile() // running -> failed (unvalidated success rejected)
	canonical := h.run(run.ID)

	if string(canonical.State) != string(legacyState.State) {
		t.Fatalf("canonical terminal %q != legacy terminal %q", canonical.State, legacyState.State)
	}
	if legacyState.State != agentrun.StateFailed {
		t.Fatalf("expected both managers to reject unvalidated success, legacy = %q", legacyState.State)
	}
	if canonical.Completion == nil || canonical.Completion.Accepted {
		t.Fatalf("canonical accepted an unvalidated success: %+v", canonical.Completion)
	}
	if legacyState.Completion == nil || legacyState.Completion.Accepted {
		t.Fatalf("legacy accepted an unvalidated success: %+v", legacyState.Completion)
	}
}

func legacyRunToTerminal(t *testing.T, now time.Time) agentrun.Run {
	t.Helper()
	store, err := agentrun.Open(filepath.Join(t.TempDir(), "agentrun.json"), 16)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	claimed, _, err := store.Claim(agentrun.Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "linear-comment"}, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	launcher := newEquivLauncher()
	validator := mustMechanicalValidator(t, func() time.Time { return now })
	manager, err := agentrun.NewManager(
		store, launcher, equivCollector{}, emptyPullRequests{}, validator,
		agentrun.LifecycleConfig{Repository: "tomnagengast/network", BaseBranch: "main"},
		t.TempDir(), func() int { return 3 }, 5*time.Millisecond, time.Minute, discardLogger(), func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("legacy manager: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { manager.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	running := settleLegacy(t, manager, store, claimed.ID, func(run agentrun.Run) bool { return run.State == agentrun.StateRunning })
	// The worker exits reporting an unvalidated success.
	launcher.endSession(running.SessionName)
	launcher.setResult(running.RunDirectory, agentrun.ProcessResult{
		Status: string(agentrun.StateSucceeded), Attempts: 1, FinishedAt: now.Add(time.Minute),
	})
	return settleLegacy(t, manager, store, claimed.ID, func(run agentrun.Run) bool { return !run.State.Nonterminal() })
}

func settleLegacy(t *testing.T, manager *agentrun.Manager, store *agentrun.Store, runID string, want func(agentrun.Run) bool) agentrun.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		manager.Notify()
		for _, run := range store.Snapshot().Runs {
			if run.ID == runID && want(run) {
				return run
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("legacy manager did not settle for run %q", runID)
	return agentrun.Run{}
}

// ---- routing permanence classification parity --------------------------

func TestManagerRoutingHonorsLegacyPermanenceContract(t *testing.T) {
	permanent := legacyPermanentRoutingError(t)
	if !isPermanentRouting(permanent) {
		t.Fatal("canonical classifier disagrees with legacy permanentRoutingError")
	}
	if isPermanentRouting(errors.New("Linear HTTP 503")) {
		t.Fatal("canonical classifier treated a transient error as permanent")
	}

	t.Run("permanent rejects", func(t *testing.T) {
		h := newManagerHarness(t, 2)
		batch, run, rate := admittedProjection(t, 1, linearTask("ENG-1"))
		h.install(batch, run, rate)
		h.resolver.fn = func(context.Context, Run) (repositories.Route, error) {
			return repositories.Route{}, permanent
		}
		h.reconcile() // admitted -> routing
		h.reconcile() // routing -> rejected
		if got := h.run(run.ID).State; got != StateRejected {
			t.Fatalf("permanent routing error state = %q, want rejected", got)
		}
	})

	t.Run("transient retries", func(t *testing.T) {
		h := newManagerHarness(t, 2)
		batch, run, rate := admittedProjection(t, 1, linearTask("ENG-1"))
		h.install(batch, run, rate)
		h.resolver.fn = func(context.Context, Run) (repositories.Route, error) {
			return repositories.Route{}, errors.New("Linear HTTP 503")
		}
		h.reconcile() // admitted -> routing
		h.reconcile() // routing -> admitted (transient)
		if got := h.run(run.ID).State; got != StateAdmitted {
			t.Fatalf("transient routing error state = %q, want admitted", got)
		}
	})
}

// legacyPermanentRoutingError returns a genuine permanent routing error minted
// by the legacy agentrun resolver (source-provider mismatch, no network) so the
// canonical classifier is proven against the real contract it must honor.
func legacyPermanentRoutingError(t *testing.T) error {
	t.Helper()
	catalog, err := agentrun.NewRepositoryCatalog([]agentrun.RepositoryConfig{{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git",
		RepoPath: "/tmp/factory", ManagedRoot: "/tmp", BaseBranch: "main",
	}})
	if err != nil {
		t.Fatalf("legacy catalog: %v", err)
	}
	resolver, err := agentrun.NewLinearRepositoryResolver("https://example.com/graphql", "test-key", http.DefaultClient, catalog)
	if err != nil {
		t.Fatalf("legacy resolver: %v", err)
	}
	_, err = resolver.ResolveTask(context.Background(), taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-x", Identifier: "FAC-1"})
	if err == nil {
		t.Fatal("legacy resolver accepted a non-Linear task")
	}
	return err
}
