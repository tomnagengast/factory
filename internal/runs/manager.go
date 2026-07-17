package runs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/repositories"
)

// Manager is the canonical Run lifecycle owner. It replaces the legacy
// triggerrouter routing loop and agentrun.Manager for the admission→execution→
// direct-terminal spine. It holds no store lock: it reads a Snapshot, decides,
// and emits Store.Transition. Every mutation therefore re-validates immutable
// admission identity and the appended-prefix invariant inside the store, so a
// concurrent writer that already advanced a Run fails this manager's transition
// closed rather than clobbering durable state.
//
// The manager owns the admission→execution→direct-terminal spine plus the merge
// lifecycle: ready-checkpoint parking, awaiting-merge/GitHub reconciliation,
// merged/closed resume, post-merge start-retry backoff, and rejected-terminal
// re-park. Admission entrypoints and feedback coalescing are owned by Admitter
// and the Store; the external match-by-repository GitHub remediation wake also
// uses its own Store operation.
type Manager struct {
	store         *Store
	dispatch      EventDispatchGate
	resolver      RepositoryResolver
	launcher      Launcher
	pullRequests  PullRequestReader
	terminal      TerminalValidator
	collector     RunCollector
	stateRoot     string
	maxConcurrent func() int
	pollInterval  time.Duration
	mergeInterval time.Duration
	now           func() time.Time
	logger        *slog.Logger
}

// EventDispatchGate protects event-derived Runs from advancing before every
// earlier wire record has been globally dispatched. Native, continuation, and
// migrated-direct admissions carry no source sequence and therefore do not wait
// on this cursor.
type EventDispatchGate interface {
	Status() eventwire.Status
}

// RepositoryResolver resolves the immutable repository route for an admitted
// Run. A returned error whose chain reports Permanent() == true is a fail-closed
// rejection that moves the Run to StateRejected; any other error is transient
// and the Run returns to StateAdmitted for a later attempt. This mirrors the
// legacy triggerrouter.finishClaim classification of permanentRoutingError.
type RepositoryResolver interface {
	ResolveRoute(ctx context.Context, run Run) (repositories.Route, error)
}

// Launcher owns the worker session lifecycle. The manager depends only
// on this narrow interface. The production tmux launcher enforces the
// environment allowlist, task capability, LINEAR_API_KEY withholding, and
// lifecycle artifact cleanup. ReadReadyCheckpoint reads the
// body-free ready-for-merge checkpoint a worker leaves in its run directory.
type Launcher interface {
	Prepare(ctx context.Context) error
	CleanupWorktrees(ctx context.Context) error
	Start(ctx context.Context, run Run, sessionName, runDirectory string) error
	SessionExists(ctx context.Context, sessionName string) (bool, error)
	ReadResult(runDirectory string) (ProcessResult, error)
	ReadReadyCheckpoint(runDirectory string) (ReadyCheckpoint, error)
}

// PullRequestReader is the read-only authority over a parked pull request's live
// state. The manager depends only on this narrow interface. The production
// GitHub CLI adapter preserves the established snapshot classification.
type PullRequestReader interface {
	Snapshot(ctx context.Context, checkpoint ReadyCheckpoint) (PullRequestSnapshot, error)
}

// PullRequestSnapshot is the body-free live view of a parked pull request. It
// mirrors the legacy agentrun.PullRequestSnapshot shape.
type PullRequestSnapshot struct {
	Number              int
	State               string
	IsDraft             bool
	BaseBranch          string
	HeadBranch          string
	HeadOID             string
	MergeCommitOID      string
	SafeguardRegression bool
	UpdatedAt           time.Time
}

// RunCollector observes the current Run set at the start and end of each
// reconcile pass, matching the legacy observer contract. It never mutates state.
type RunCollector interface {
	Collect(ctx context.Context, runs []Run) error
}

// ProcessResult is the body-free terminal signal a completed worker leaves in
// its run directory. It mirrors the legacy agentrun.ProcessResult shape so a
// faithful TerminalValidator classifies intents identically.
type ProcessResult struct {
	Status     string
	Blocker    string
	Attempts   int
	ExitCode   int
	Detail     string
	FinishedAt time.Time
}

// ResultReadyForMerge is the worker status that signals a ready-for-merge
// checkpoint.
const ResultReadyForMerge = "ready_for_human_merge"

// TerminalValidator is the narrow, fail-closed authority for turning one
// non-PR terminal process result into a lifecycle ruling. It cannot be waived
// by workflow text. An unaccepted ruling always reduces to StateFailed; the
// manager stamps the durable ValidatedAt so the completion timestamp stays
// within the Run's lifecycle window.
type TerminalValidator interface {
	ValidateTerminal(ctx context.Context, run Run, result ProcessResult) TerminalDecision
}

// TerminalDecision is the validator's ruling for one terminal process result.
// State is the terminal lifecycle the Run must reach. Validation is the durable
// completion evidence; the manager owns its ValidatedAt. Repark asks the manager
// to return a still-parkable Run (one that still holds a ready checkpoint) to
// awaiting_human_merge instead of finishing it, matching legacy
// CompletionDecision.Repark.
type TerminalDecision struct {
	State      LifecycleState
	Detail     string
	Repark     bool
	Validation CompletionValidation
}

// NewManager constructs the Run manager. A package test confines production
// composition of this owner and its external authorities to internal/app.
func NewManager(
	store *Store,
	dispatch EventDispatchGate,
	resolver RepositoryResolver,
	launcher Launcher,
	pullRequests PullRequestReader,
	terminal TerminalValidator,
	collector RunCollector,
	stateRoot string,
	maxConcurrent func() int,
	pollInterval time.Duration,
	mergeInterval time.Duration,
	now func() time.Time,
	logger *slog.Logger,
) (*Manager, error) {
	if store == nil {
		return nil, errors.New("runs: manager store is required")
	}
	if dispatch == nil {
		return nil, errors.New("runs: manager event dispatch gate is required")
	}
	if resolver == nil {
		return nil, errors.New("runs: manager repository resolver is required")
	}
	if launcher == nil {
		return nil, errors.New("runs: manager launcher is required")
	}
	if pullRequests == nil {
		return nil, errors.New("runs: manager pull request reader is required")
	}
	if terminal == nil {
		return nil, errors.New("runs: manager terminal validator is required")
	}
	if collector == nil {
		return nil, errors.New("runs: manager run collector is required")
	}
	if !canonicalAbsolutePath(stateRoot) {
		return nil, errors.New("runs: manager state root must be a canonical absolute path")
	}
	if maxConcurrent == nil || maxConcurrent() < 1 {
		return nil, errors.New("runs: manager max concurrency must be positive")
	}
	if pollInterval <= 0 {
		return nil, errors.New("runs: manager poll interval must be positive")
	}
	if mergeInterval <= 0 {
		return nil, errors.New("runs: manager merge interval must be positive")
	}
	if now == nil {
		return nil, errors.New("runs: manager clock is required")
	}
	if logger == nil {
		return nil, errors.New("runs: manager logger is required")
	}
	return &Manager{
		store: store, dispatch: dispatch, resolver: resolver, launcher: launcher, pullRequests: pullRequests,
		terminal: terminal, collector: collector, stateRoot: stateRoot, maxConcurrent: maxConcurrent,
		pollInterval: pollInterval, mergeInterval: mergeInterval, now: now, logger: logger,
	}, nil
}

// Reconcile performs one bounded pass over the canonical projection: it resolves
// routing Runs, promotes the oldest admitted owner per task, recovers and
// completes active workers, and starts runnable Runs up to the concurrency
// limit. It is idempotent and holds no lock across store calls, so a persisted
// transition failing closed (identity or appended-prefix conflict) is a benign
// no-op that the next pass re-derives from fresh state.
func (m *Manager) Reconcile(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	snapshot, err := m.store.Snapshot()
	if err != nil {
		m.logger.Error("read run projection", "error", err)
		return
	}
	runs := snapshot.Model().Runs
	if err := m.collector.Collect(ctx, cloneRuns(runs)); err != nil {
		m.logger.Warn("collect run observations", "error", err)
	}
	defer func() {
		final, err := m.store.Snapshot()
		if err != nil {
			m.logger.Warn("read run projection for final collection", "error", err)
			return
		}
		if err := m.collector.Collect(ctx, final.Model().Runs); err != nil {
			m.logger.Warn("collect run observations", "error", err)
		}
	}()

	maxConcurrent := m.maxConcurrent()
	if maxConcurrent < 1 {
		m.logger.Error("read run concurrency", "value", maxConcurrent)
		return
	}
	owners := oldestTaskOwners(runs)
	dispatched := m.dispatch.Status().Dispatched

	// Worker-bound work first, counting active segments toward the limit.
	// Awaiting-merge Runs are polled here but do not consume a worker slot.
	running := 0
	for _, run := range runs {
		switch run.State {
		case StateStarting, StateRunning:
			running++
			m.reconcileActive(ctx, run)
		case StateAwaitingHumanMerge:
			m.reconcileAwaitingMerge(ctx, run)
		}
	}

	// Repository resolution is not worker-bound.
	for _, run := range runs {
		switch {
		case run.State == StateRouting && sourceDispatched(run, dispatched):
			m.resolveRouting(ctx, run)
		case run.State == StateAdmitted && owners[run.ID] && sourceDispatched(run, dispatched):
			m.promoteToRouting(run)
		}
	}

	if running >= maxConcurrent {
		return
	}
	prepared := false
	for _, run := range runs {
		if run.State != StatePending && run.State != StatePostMergePending {
			continue
		}
		if running >= maxConcurrent {
			return
		}
		// A post-merge start-retry (or resume) records a durable backoff timer;
		// skip the Run until it elapses so a failing start does not spin.
		if run.GitHub.NextReconcileAt != nil && m.now().Before(*run.GitHub.NextReconcileAt) {
			continue
		}
		if !prepared {
			if err := m.launcher.Prepare(ctx); err != nil {
				m.logger.Error("prepare run workspace", "error", err)
				return
			}
			if running == 0 {
				if err := m.launcher.CleanupWorktrees(ctx); err != nil {
					m.logger.Warn("clean run worktrees", "error", err)
				}
			}
			prepared = true
		}
		if m.start(ctx, run) {
			running++
		}
	}
}

// promoteToRouting moves the oldest admitted owner of a task into the routing
// state so its repository can be resolved asynchronously. Younger admitted Runs
// for the same task remain admitted; the store's task-ownership invariant is the
// authority, so a mis-selection fails closed rather than creating a second
// owner.
func (m *Manager) promoteToRouting(run Run) {
	if err := m.transition(run, StateRouting, func(next *Run, _ time.Time) {
		next.Detail = "resolving repository route"
	}); err != nil {
		m.logger.Warn("promote run to routing", "run_id", run.ID, "error", err)
	}
}

// resolveRouting resolves a routing Run's repository. Success binds the
// immutable route and advances to pending; a permanent error rejects the Run
// with a durable reason; a transient error returns the Run to admitted for a
// later attempt, matching legacy triggerrouter.finishClaim.
func (m *Manager) resolveRouting(ctx context.Context, run Run) {
	route, err := m.resolver.ResolveRoute(ctx, run)
	if err != nil {
		if isPermanentRouting(err) {
			reason := truncateText(err.Error(), maximumTextBytes)
			if transitionErr := m.transition(run, StateRejected, func(next *Run, at time.Time) {
				next.RepositoryRejection = reason
				next.Detail = "repository routing rejected"
				finished := at
				next.FinishedAt = &finished
			}); transitionErr != nil {
				m.logger.Warn("reject unroutable run", "run_id", run.ID, "error", transitionErr)
			}
			return
		}
		// Transient failure: the research contract returns the Run to admitted so
		// a later pass re-promotes and retries. Durable poll backoff is a
		// deferred store-op concern and is intentionally not applied here.
		if transitionErr := m.transition(run, StateAdmitted, func(next *Run, _ time.Time) {
			next.Detail = "repository resolution deferred: " + truncateText(err.Error(), 512)
		}); transitionErr != nil {
			m.logger.Warn("defer repository resolution", "run_id", run.ID, "error", transitionErr)
		}
		return
	}
	if err := validateRoute(route); err != nil {
		if transitionErr := m.transition(run, StateRejected, func(next *Run, at time.Time) {
			next.RepositoryRejection = "resolved route is invalid"
			next.Detail = truncateText(err.Error(), maximumTextBytes)
			finished := at
			next.FinishedAt = &finished
		}); transitionErr != nil {
			m.logger.Warn("reject invalid resolved route", "run_id", run.ID, "error", transitionErr)
		}
		return
	}
	resolved := route
	if err := m.transition(run, StatePending, func(next *Run, _ time.Time) {
		next.Repository = &resolved
		next.Detail = ""
	}); err != nil {
		m.logger.Warn("bind repository route", "run_id", run.ID, "error", err)
	}
}

// start binds session, run-directory, and segment identity (pending or
// post_merge_pending → starting), launches the worker, and marks it running.
// A first-segment start failure of a pending Run is terminal; a post-merge
// start failure returns to post_merge_pending with a durable retry backoff.
// start returns true when a worker slot was consumed.
func (m *Manager) start(ctx context.Context, run Run) bool {
	origin := run.State
	sessionName := taskSessionName(run)
	runDirectory := runPath(m.stateRoot, run.ID)
	current, ok := m.reload(run.ID)
	if !ok || current.State != origin {
		return false
	}
	if err := m.transition(current, StateStarting, func(next *Run, at time.Time) {
		next.SessionName = sessionName
		next.RunDirectory = runDirectory
		segment := at
		next.SegmentStartedAt = &segment
		next.SegmentAttempt = next.Attempts
		next.Detail = ""
	}); err != nil {
		m.logger.Error("mark run starting", "run_id", run.ID, "error", err)
		return false
	}
	if err := m.launcher.Start(ctx, run, sessionName, runDirectory); err != nil {
		m.finishStartFailure(run.ID, origin, fmt.Sprintf("start tmux session: %v", err))
		return false
	}
	if starting, ok := m.reload(run.ID); ok && starting.State == StateStarting {
		if err := m.transition(starting, StateRunning, func(next *Run, at time.Time) {
			if next.Attempts < 1 {
				next.Attempts = 1
			}
			// StartedAt is set once, on the first segment. A post-merge restart of
			// a Run that already ran keeps its original start time, matching legacy
			// MarkRunning and the store's immutable-start-timestamp rule.
			if next.StartedAt == nil {
				started := at
				next.StartedAt = &started
			}
			next.GitHub.ReconcileFailures = 0
			next.Detail = ""
		}); err != nil {
			m.logger.Error("mark launched run running", "run_id", run.ID, "error", err)
		}
	}
	return true
}

// finishStartFailure resolves a start failure from the starting state. A
// pending origin is terminal (legacy: first-segment start failure). A
// post-merge origin returns to post_merge_pending with a durable exponential
// reconcile backoff (legacy RetryPostMergeStart) so a repeatedly failing start
// does not spin. It keeps the deterministic SessionName the store treats as
// immutable and clears the segment for a fresh attempt.
func (m *Manager) finishStartFailure(runID string, origin LifecycleState, detail string) {
	current, ok := m.reload(runID)
	if !ok || current.State != StateStarting {
		return
	}
	if origin == StatePostMergePending {
		if err := m.transition(current, StatePostMergePending, func(next *Run, at time.Time) {
			next.SegmentStartedAt = nil
			next.GitHub.ReconcileFailures = current.GitHub.ReconcileFailures + 1
			reconcile := at.Add(reconcileDelay(m.pollInterval, current.GitHub.ReconcileFailures))
			next.GitHub.NextReconcileAt = &reconcile
			next.Detail = truncateText(detail, maximumTextBytes)
		}); err != nil {
			m.logger.Error("defer post-merge start", "run_id", runID, "error", err)
		}
		return
	}
	if err := m.transition(current, StateFailed, func(next *Run, at time.Time) {
		next.Detail = detail
		finished := at
		next.FinishedAt = &finished
	}); err != nil {
		m.logger.Error("record run start failure", "run_id", runID, "error", err)
	}
}

// reconcileActive recovers a worker and drives completion. A live session
// advances starting → running (crash recovery). A gone session with a readable
// ready-for-merge checkpoint parks to awaiting_human_merge; with no readable
// result and no checkpoint it fails closed; a stale/unbound result fails closed.
// A ready-for-merge result parks; a plain process failure that left a checkpoint
// also parks (the validator decides its terminal fate later). Every other
// terminal intent is ruled on by the TerminalValidator, whose Repark ruling on a
// still-parked Run re-parks it instead of finishing it.
func (m *Manager) reconcileActive(ctx context.Context, run Run) {
	if run.GitHub.NextReconcileAt != nil && m.now().Before(*run.GitHub.NextReconcileAt) {
		return
	}
	exists, err := m.launcher.SessionExists(ctx, run.SessionName)
	if err != nil {
		m.logger.Warn("inspect run session", "run_id", run.ID, "error", err)
		return
	}
	if exists {
		if run.State == StateStarting {
			if err := m.transition(run, StateRunning, func(next *Run, at time.Time) {
				if next.Attempts < 1 {
					next.Attempts = 1
				}
				if next.StartedAt == nil {
					started := at
					next.StartedAt = &started
				}
				next.GitHub.ReconcileFailures = 0
				next.Detail = ""
			}); err != nil {
				m.logger.Error("mark run running", "run_id", run.ID, "error", err)
			}
		}
		return
	}

	result, err := m.launcher.ReadResult(run.RunDirectory)
	if err != nil {
		if _, checkpointErr := m.launcher.ReadReadyCheckpoint(run.RunDirectory); checkpointErr == nil {
			m.parkReadyRun(ctx, run, ProcessResult{
				Status:     ResultReadyForMerge,
				Attempts:   max(run.Attempts, run.SegmentAttempt+1),
				FinishedAt: m.now(),
			})
			return
		}
		m.finishActive(run.ID, StateFailed, "tmux session ended without a process result", 0, nil)
		return
	}
	if run.SegmentStartedAt == nil || result.Attempts <= run.SegmentAttempt || result.FinishedAt.Before(*run.SegmentStartedAt) {
		m.finishActive(run.ID, StateFailed, "rejected stale or unbound process result", run.Attempts, nil)
		return
	}
	if result.Status == ResultReadyForMerge {
		m.parkReadyRun(ctx, run, result)
		return
	}
	if result.Status == string(StateFailed) {
		if _, checkpointErr := m.launcher.ReadReadyCheckpoint(run.RunDirectory); checkpointErr == nil {
			m.parkReadyRun(ctx, run, result)
			return
		}
	}

	decision := m.terminal.ValidateTerminal(ctx, run, result)
	if decision.Repark && run.Ready != nil {
		m.repark(run, result, decision)
		return
	}
	if !decision.State.Terminal() || !decision.Validation.Accepted && decision.State != StateFailed {
		const reason = "terminal validator returned an unsafe ruling"
		intent := strings.TrimSpace(result.Status)
		if !validText(intent, 256) {
			intent = "invalid-terminal-result"
		}
		blocker := strings.TrimSpace(result.Blocker)
		if !validOptionalText(blocker, 256) {
			blocker = ""
		}
		decision = TerminalDecision{
			State:  StateFailed,
			Detail: "terminal intent rejected: " + reason,
			Validation: CompletionValidation{
				Accepted: false, Intent: intent, Blocker: blocker,
				State: StateFailed, Reason: reason,
			},
		}
	}
	validation := decision.Validation
	m.finishActive(run.ID, decision.State, decision.Detail, result.Attempts, &validation)
}

// finishActive applies a terminal transition to a currently active Run,
// re-reading it so the transition is derived from fresh durable state. It stamps
// the terminal timestamp and completion so the store's completion window and
// finish-time invariants hold.
func (m *Manager) finishActive(runID string, state LifecycleState, detail string, attempts int, validation *CompletionValidation) {
	if !state.Terminal() {
		m.logger.Error("terminal completion produced a nonterminal state", "run_id", runID, "state", state)
		return
	}
	current, ok := m.reload(runID)
	if !ok || (current.State != StateStarting && current.State != StateRunning) {
		return
	}
	if err := m.transition(current, state, func(next *Run, at time.Time) {
		if attempts > next.Attempts {
			next.Attempts = attempts
		}
		next.Detail = truncateText(detail, maximumTextBytes)
		finished := at
		next.FinishedAt = &finished
		if validation != nil {
			completion := *validation
			completion.State = state
			completion.ValidatedAt = at
			next.Completion = &completion
			next.TerminalIntent = truncateText(completion.Intent, 256)
			if completion.Accepted {
				next.TerminalRejection = ""
			} else {
				next.TerminalRejection = truncateText(completion.Reason, maximumTextBytes)
			}
		}
	}); err != nil {
		m.logger.Error("finish run", "run_id", runID, "state", state, "error", err)
	}
}

// transition builds the next Run projection from current, appends exactly one
// lifecycle transition whose timestamp strictly advances UpdatedAt, and submits
// it. mutate receives the strictly-advancing transition time so terminal and
// segment timestamps stay coherent with the store's delta rules.
func (m *Manager) transition(current Run, state LifecycleState, mutate func(next *Run, at time.Time)) error {
	at := m.advance(current.UpdatedAt)
	next := current.Clone()
	next.State = state
	next.UpdatedAt = at
	if mutate != nil {
		mutate(&next, at)
	}
	next.Transitions = append(slices.Clone(current.Transitions), LifecycleTransition{
		ID: transitionID(current, state), State: state, Attempts: next.Attempts, At: at,
	})
	return m.store.Transition(next)
}

// advance returns a strictly-monotonic UTC time. Under a fixed clock two
// transitions on the same Run would otherwise share a timestamp; nudging past
// the previous UpdatedAt keeps each transition strictly after the last and
// preserves the store's advancing-time invariant.
func (m *Manager) advance(previous time.Time) time.Time {
	at := m.now().UTC()
	if !at.After(previous) {
		at = previous.Add(time.Nanosecond)
	}
	return at
}

func (m *Manager) reload(runID string) (Run, bool) {
	snapshot, err := m.store.Snapshot()
	if err != nil {
		m.logger.Warn("reload run", "run_id", runID, "error", err)
		return Run{}, false
	}
	for _, run := range snapshot.Model().Runs {
		if run.ID == runID {
			return run, true
		}
	}
	return Run{}, false
}

// transitionID is unique per Run even under a fixed clock: the transition index
// strictly increases with each appended history record, and it embeds the Run
// ID so the derived wire event ID (RunTransitionEventID) is globally unique.
func transitionID(current Run, state LifecycleState) string {
	return fmt.Sprintf("%s:t%d:%s", current.ID, len(current.Transitions), state)
}

// oldestTaskOwners returns the set of Run IDs that hold ownership of their task
// among nonterminal Runs: the single oldest by admission then creation then ID.
// Only an owner may leave StateAdmitted, mirroring validateTaskOwnership so the
// manager never asks the store to advance a non-owner.
func oldestTaskOwners(runs []Run) map[string]bool {
	byTask := make(map[string][]Run)
	for _, run := range runs {
		if run.State.Nonterminal() {
			key := run.Causation.Task.OwnershipKey()
			byTask[key] = append(byTask[key], run)
		}
	}
	owners := make(map[string]bool, len(byTask))
	for _, taskRuns := range byTask {
		slices.SortFunc(taskRuns, compareOwnershipOrder)
		owners[taskRuns[0].ID] = true
	}
	return owners
}

func sourceDispatched(run Run, dispatched uint64) bool {
	return run.Causation.EventSequence == 0 || run.Causation.EventSequence <= dispatched
}

// isPermanentRouting classifies a resolver error as a fail-closed rejection,
// matching legacy triggerrouter.isPermanentRouting and the permanentError
// contract used by internal/repositories.
func isPermanentRouting(err error) bool {
	var classified interface{ Permanent() bool }
	return errors.As(err, &classified) && classified.Permanent()
}

// truncateText bounds a diagnostic detail to a valid-text budget, trimming any
// trailing partial rune so the result still satisfies the store's text rules.
func truncateText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	trimmed := value[:maximum]
	for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return strings.TrimSpace(trimmed)
}

// taskSessionName and runPath reproduce the legacy deterministic worker
// identity. Determinism is load-bearing: the canonical store treats SessionName
// and RunDirectory as immutable once set, so a re-started segment must recompute
// the identical values.
func taskSessionName(run Run) string {
	source := string(run.Causation.Task.Source)
	if source == "" {
		source = "linear"
	}
	return "factory-" + source + "-" + strings.TrimPrefix(run.ID, "run-")
}

func runPath(stateRoot, runID string) string {
	return filepath.Join(stateRoot, "runs", runID)
}
