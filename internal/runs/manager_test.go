package runs

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

// ---- test collaborators -------------------------------------------------

type fakeResolver struct {
	fn func(context.Context, Run) (repositories.Route, error)
}

type fakeDispatchGate struct{ dispatched uint64 }

func (g *fakeDispatchGate) Status() eventwire.Status {
	return eventwire.Status{Dispatched: g.dispatched}
}

func (f fakeResolver) ResolveRoute(ctx context.Context, run Run) (repositories.Route, error) {
	return f.fn(ctx, run)
}

type fakeLauncher struct {
	prepare    func(context.Context) error
	cleanup    func(context.Context) error
	start      func(context.Context, Run, string, string) error
	exists     func(context.Context, string) (bool, error)
	result     func(string) (ProcessResult, error)
	checkpoint func(string) (ReadyCheckpoint, error)
}

func (f fakeLauncher) Prepare(ctx context.Context) error {
	if f.prepare != nil {
		return f.prepare(ctx)
	}
	return nil
}

func (f fakeLauncher) CleanupWorktrees(ctx context.Context) error {
	if f.cleanup != nil {
		return f.cleanup(ctx)
	}
	return nil
}

func (f fakeLauncher) Start(ctx context.Context, run Run, session, directory string) error {
	if f.start != nil {
		return f.start(ctx, run, session, directory)
	}
	return nil
}

func (f fakeLauncher) SessionExists(ctx context.Context, session string) (bool, error) {
	if f.exists != nil {
		return f.exists(ctx, session)
	}
	return false, nil
}

func (f fakeLauncher) ReadResult(directory string) (ProcessResult, error) {
	if f.result != nil {
		return f.result(directory)
	}
	return ProcessResult{}, errors.New("no result")
}

func (f fakeLauncher) ReadReadyCheckpoint(directory string) (ReadyCheckpoint, error) {
	if f.checkpoint != nil {
		return f.checkpoint(directory)
	}
	return ReadyCheckpoint{}, errors.New("no checkpoint")
}

type fakeTerminal struct {
	fn func(context.Context, Run, ProcessResult) TerminalDecision
}

func (f fakeTerminal) ValidateTerminal(ctx context.Context, run Run, result ProcessResult) TerminalDecision {
	return f.fn(ctx, run, result)
}

// fakePullRequests answers the parked pull request read with a caller-supplied
// snapshot or error, so merge-lifecycle tests drive every GitHub branch.
type fakePullRequests struct {
	fn func(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error)
}

func (f fakePullRequests) Snapshot(ctx context.Context, checkpoint ReadyCheckpoint) (PullRequestSnapshot, error) {
	if f.fn != nil {
		return f.fn(ctx, checkpoint)
	}
	return PullRequestSnapshot{}, errors.New("no pull request")
}

type recordingCollector struct {
	calls int
}

func (c *recordingCollector) Collect(_ context.Context, _ []Run) error {
	c.calls++
	return nil
}

type permanentTestError struct{ message string }

func (e permanentTestError) Error() string { return e.message }
func (permanentTestError) Permanent() bool { return true }

// ---- harness ------------------------------------------------------------

type managerHarness struct {
	t             *testing.T
	store         *Store
	manager       *Manager
	dispatch      *fakeDispatchGate
	resolver      *fakeResolver
	launcher      *fakeLauncher
	pullRequests  *fakePullRequests
	terminal      *fakeTerminal
	collector     *recordingCollector
	clock         time.Time
	pollInterval  time.Duration
	mergeInterval time.Duration
	stateRoot     string
}

func newManagerHarness(t *testing.T, maxConcurrent int) *managerHarness {
	t.Helper()
	store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 64)
	return newManagerHarnessWithStore(t, store, t.TempDir(), maxConcurrent)
}

// newManagerHarnessWithStore builds a manager harness over a caller-supplied
// store and state root, so migrated-projection fixtures can be installed before
// the manager runs.
func newManagerHarnessWithStore(t *testing.T, store *Store, stateRoot string, maxConcurrent int) *managerHarness {
	t.Helper()
	harness := &managerHarness{
		t:        t,
		store:    store,
		dispatch: &fakeDispatchGate{dispatched: ^uint64(0)},
		resolver: &fakeResolver{fn: func(context.Context, Run) (repositories.Route, error) {
			return repositories.Route{}, errors.New("unset resolver")
		}},
		launcher:     &fakeLauncher{},
		pullRequests: &fakePullRequests{},
		terminal: &fakeTerminal{fn: func(context.Context, Run, ProcessResult) TerminalDecision {
			return TerminalDecision{State: StateFailed}
		}},
		collector:     &recordingCollector{},
		clock:         modelTestNow.Add(time.Hour),
		pollInterval:  time.Minute,
		mergeInterval: 5 * time.Minute,
		stateRoot:     stateRoot,
	}
	manager, err := NewManager(
		store, harness.dispatch, harness.resolver, harness.launcher, harness.pullRequests, harness.terminal,
		harness.collector, harness.stateRoot, func() int { return maxConcurrent },
		harness.pollInterval, harness.mergeInterval, harness.nowFunc(), discardLogger(),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	harness.manager = manager
	return harness
}

func (h *managerHarness) nowFunc() func() time.Time {
	return func() time.Time { return h.clock }
}

func (h *managerHarness) reconcile() {
	h.t.Helper()
	h.manager.Reconcile(context.Background())
}

func (h *managerHarness) run(runID string) Run {
	h.t.Helper()
	snapshot, err := h.store.Snapshot()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, run := range snapshot.Model().Runs {
		if run.ID == runID {
			return run
		}
	}
	h.t.Fatalf("Run %q not found", runID)
	return Run{}
}

func (h *managerHarness) install(batch AdmissionBatch, run Run, rate RateBucket) {
	h.t.Helper()
	if err := h.store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
		h.t.Fatalf("install projection: %v", err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---- fixtures -----------------------------------------------------------

func managerRoute(root string) repositories.Route {
	return repositories.Route{
		ProjectID: "project-factory", Repository: "tomnagengast/factory",
		Origin:      "git@github.com:tomnagengast/factory.git",
		ManagedPath: filepath.Join(root, "factory"), ManagedRoot: root,
		DefaultBranch: "main", Bootstrap: false, CloudURL: "https://factory.nags.cloud",
	}
}

// admittedProjection builds a runnable admission for one admitted Run that has
// no repository route yet, on the given task. Distinct numbers yield distinct
// batch/event/admission/run identities so several admissions can share a task.
func admittedProjection(t *testing.T, number int, task taskmodel.TaskRef) (AdmissionBatch, Run, RateBucket) {
	t.Helper()
	at := modelTestNow.Add(time.Duration(number-1) * time.Second)
	label := strconv.Itoa(number)
	ruleID := "rule-one"
	batchID := "batch-" + label
	eventID := "factory:event-" + label
	admissionID := "admission-" + label
	runID := "run-" + label
	pin := workflow.Pinned{
		ID: "full-sdlc", Revision: 3, Name: "Full SDLC", Enabled: true,
		Markdown: "# Full SDLC\n", UpdatedAt: pointerTime(at),
	}
	digest, err := pin.Digest()
	if err != nil {
		t.Fatal(err)
	}
	batch := AdmissionBatch{
		ID: batchID, Origin: AdmissionOriginEvent, EventID: eventID, EventSequence: uint64(number),
		EventSource: eventwire.SourceFactory, EventRecordDigest: strings.Repeat("a", 64),
		RegistryRevision: 2, SettingsRevision: 3, PolicyGeneration: 4, DecidedAt: at,
		Outcomes: []AdmissionOutcome{
			{Kind: AdmissionOutcomeRun, RuleID: ruleID, RuleRevision: 2, AdmissionID: admissionID, RunID: runID},
		},
	}
	run := Run{
		ID: runID,
		Causation: Causation{
			AdmissionID: admissionID, BatchID: batchID, EventID: eventID, EventSequence: uint64(number),
			EventSource: eventwire.SourceFactory, RuleID: ruleID, RuleRevision: 2, Workflow: &pin, WorkflowDigest: digest,
			PolicyRevision: 3, PolicyGeneration: 4, Task: task, RootEventID: eventID, Hop: 1,
			AncestorRuleIDs: []string{ruleID}, AdmittedAt: at,
		},
		TriggerKind: "configured-rule", DeliveryIDs: []string{eventID},
		State: StateAdmitted, CreatedAt: at, UpdatedAt: at,
		Transitions: []LifecycleTransition{{ID: runID + ":admitted", State: StateAdmitted, At: at}},
	}
	return batch, run, RateBucket{RuleID: ruleID, Minute: at.Truncate(time.Minute), Count: 1}
}

func linearTask(identifier string) taskmodel.TaskRef {
	return taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: identifier, Identifier: identifier}
}

// startingProjection extends a pending admission into a Run parked in the
// starting state with bound session, directory, and segment identity.
func startingProjection(t *testing.T, root string, number int) (AdmissionBatch, Run, RateBucket) {
	t.Helper()
	batch, run, rate := testAdmissionProjection(t, root, number, StatePending)
	startingAt := run.CreatedAt.Add(time.Second)
	run.State = StateStarting
	run.SessionName = taskSessionName(run)
	run.RunDirectory = runPath(root, run.ID)
	run.SegmentStartedAt = pointerTime(startingAt)
	run.SegmentAttempt = 0
	run.UpdatedAt = startingAt
	run.Transitions = append(run.Transitions, LifecycleTransition{ID: run.ID + ":starting", State: StateStarting, At: startingAt})
	run.DeliveredThrough = len(run.Transitions)
	return batch, run, rate
}

// postMergePendingProjection builds a Run whose segment failed to start and
// returned to post_merge_pending. Its worker identity matches the manager's
// deterministic recomputation so a fresh start reuses the same session and
// directory the canonical store treats as immutable.
func postMergePendingProjection(t *testing.T, root string, number int) (AdmissionBatch, Run, RateBucket) {
	t.Helper()
	batch, run, rate := testAdmissionProjection(t, root, number, StatePending)
	startingAt := run.CreatedAt.Add(time.Second)
	deferredAt := run.CreatedAt.Add(2 * time.Second)
	run.State = StatePostMergePending
	run.SessionName = taskSessionName(run)
	run.RunDirectory = runPath(root, run.ID)
	run.UpdatedAt = deferredAt
	run.Transitions = append(run.Transitions,
		LifecycleTransition{ID: run.ID + ":starting", State: StateStarting, At: startingAt},
		LifecycleTransition{ID: run.ID + ":post-merge", State: StatePostMergePending, At: deferredAt},
	)
	run.DeliveredThrough = len(run.Transitions)
	return batch, run, rate
}

// ---- repository resolution ---------------------------------------------

func TestManagerResolvesRepositoryRouteToPending(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := admittedProjection(t, 1, linearTask("ENG-1"))
	h.install(batch, run, rate)
	route := managerRoute(h.stateRoot)
	h.resolver.fn = func(context.Context, Run) (repositories.Route, error) { return route, nil }

	h.reconcile() // admitted -> routing
	if got := h.run(run.ID).State; got != StateRouting {
		t.Fatalf("after first pass state = %q, want routing", got)
	}
	h.reconcile() // routing -> pending
	resolved := h.run(run.ID)
	if resolved.State != StatePending {
		t.Fatalf("after second pass state = %q, want pending", resolved.State)
	}
	if resolved.Repository == nil || *resolved.Repository != route {
		t.Fatalf("repository route = %+v, want %+v", resolved.Repository, route)
	}
}

func TestManagerWaitsForSourceDispatchBeforeRouting(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := admittedProjection(t, 1, linearTask("ENG-1"))
	h.install(batch, run, rate)
	h.dispatch.dispatched = 0
	resolverCalls := 0
	h.resolver.fn = func(context.Context, Run) (repositories.Route, error) {
		resolverCalls++
		return managerRoute(h.stateRoot), nil
	}

	h.reconcile()
	if got := h.run(run.ID).State; got != StateAdmitted {
		t.Fatalf("state before source dispatch = %q, want admitted", got)
	}
	if resolverCalls != 0 {
		t.Fatalf("resolver called %d times before source dispatch", resolverCalls)
	}

	h.dispatch.dispatched = run.Causation.EventSequence
	h.reconcile()
	if got := h.run(run.ID).State; got != StateRouting {
		t.Fatalf("state after source dispatch = %q, want routing", got)
	}

	// A recovered/migrated routing Run is gated too. The production cursor is
	// monotonic, but exercising a lower fake cursor proves routing recovery does
	// not accidentally bypass the same source boundary.
	h.dispatch.dispatched = 0
	h.reconcile()
	if got := h.run(run.ID).State; got != StateRouting {
		t.Fatalf("routing state before source dispatch = %q, want routing", got)
	}
	if resolverCalls != 0 {
		t.Fatalf("resolver called %d times for undispatched routing Run", resolverCalls)
	}

	h.dispatch.dispatched = run.Causation.EventSequence
	h.reconcile()
	if got := h.run(run.ID).State; got != StatePending {
		t.Fatalf("routing state after source dispatch = %q, want pending", got)
	}
	if resolverCalls != 1 {
		t.Fatalf("resolver calls after source dispatch = %d, want 1", resolverCalls)
	}
}

func TestManagerRejectsPermanentRoutingFailure(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := admittedProjection(t, 1, linearTask("ENG-1"))
	h.install(batch, run, rate)
	h.resolver.fn = func(context.Context, Run) (repositories.Route, error) {
		return repositories.Route{}, permanentTestError{message: "repository catalog: ENG is not allowlisted"}
	}

	h.reconcile() // admitted -> routing
	h.reconcile() // routing -> rejected
	rejected := h.run(run.ID)
	if rejected.State != StateRejected {
		t.Fatalf("state = %q, want rejected", rejected.State)
	}
	if rejected.FinishedAt == nil {
		t.Fatal("rejected Run has no finish time")
	}
	if !strings.Contains(rejected.RepositoryRejection, "not allowlisted") {
		t.Fatalf("rejection reason = %q", rejected.RepositoryRejection)
	}
}

func TestManagerRetriesTransientRoutingFailure(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := admittedProjection(t, 1, linearTask("ENG-1"))
	h.install(batch, run, rate)
	attempts := 0
	route := managerRoute(h.stateRoot)
	h.resolver.fn = func(context.Context, Run) (repositories.Route, error) {
		attempts++
		if attempts == 1 {
			return repositories.Route{}, errors.New("Linear HTTP 503")
		}
		return route, nil
	}

	h.reconcile() // admitted -> routing
	h.reconcile() // routing -> admitted (transient retry)
	if got := h.run(run.ID).State; got != StateAdmitted {
		t.Fatalf("after transient failure state = %q, want admitted", got)
	}
	h.reconcile() // admitted -> routing
	h.reconcile() // routing -> pending
	resolved := h.run(run.ID)
	if resolved.State != StatePending || resolved.Repository == nil {
		t.Fatalf("after retry state = %q repository = %v", resolved.State, resolved.Repository)
	}
}

func TestManagerRejectsInvalidResolvedRoute(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := admittedProjection(t, 1, linearTask("ENG-1"))
	h.install(batch, run, rate)
	broken := managerRoute(h.stateRoot)
	broken.Origin = "https://example.com/not-canonical"
	h.resolver.fn = func(context.Context, Run) (repositories.Route, error) { return broken, nil }

	h.reconcile() // admitted -> routing
	h.reconcile() // routing -> rejected (route fails canonical validation)
	if got := h.run(run.ID).State; got != StateRejected {
		t.Fatalf("state = %q, want rejected", got)
	}
}

// ---- oldest-per-task ownership -----------------------------------------

func TestManagerPromotesOnlyOldestAdmittedOwner(t *testing.T) {
	h := newManagerHarness(t, 4)
	task := linearTask("ENG-9")
	firstBatch, first, firstRate := admittedProjection(t, 1, task)
	secondBatch, second, secondRate := admittedProjection(t, 2, task)
	h.install(firstBatch, first, firstRate)
	h.install(secondBatch, second, secondRate)
	// Block resolution so the owner parks in routing and never clears ownership.
	h.resolver.fn = func(context.Context, Run) (repositories.Route, error) {
		return repositories.Route{}, errors.New("still resolving")
	}

	h.reconcile()
	if got := h.run(first.ID).State; got != StateRouting {
		t.Fatalf("oldest owner state = %q, want routing", got)
	}
	if got := h.run(second.ID).State; got != StateAdmitted {
		t.Fatalf("younger duplicate state = %q, want admitted", got)
	}
	// Even after several passes the younger Run never overtakes ownership.
	h.reconcile()
	h.reconcile()
	if got := h.run(second.ID).State; got != StateAdmitted {
		t.Fatalf("younger duplicate advanced to %q, want admitted", got)
	}
}

// ---- start / running ----------------------------------------------------

func TestManagerStartsPendingRunToRunning(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := testAdmissionProjection(t, h.stateRoot, 1, StatePending)
	h.install(batch, run, rate)
	var startedSession, startedDir string
	h.launcher.start = func(_ context.Context, _ Run, session, dir string) error {
		startedSession, startedDir = session, dir
		return nil
	}

	h.reconcile()
	running := h.run(run.ID)
	if running.State != StateRunning {
		t.Fatalf("state = %q, want running", running.State)
	}
	if running.SessionName == "" || running.RunDirectory == "" || running.SegmentStartedAt == nil || running.StartedAt == nil {
		t.Fatalf("running Run missing worker identity: %+v", running)
	}
	if running.SessionName != startedSession || running.RunDirectory != startedDir {
		t.Fatalf("launcher identity mismatch: session %q/%q dir %q/%q", running.SessionName, startedSession, running.RunDirectory, startedDir)
	}
	if running.RunDirectory != runPath(h.stateRoot, run.ID) {
		t.Fatalf("run directory = %q, want deterministic path", running.RunDirectory)
	}
	if running.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", running.Attempts)
	}
}

func TestManagerRespectsMaxConcurrency(t *testing.T) {
	h := newManagerHarness(t, 1)
	firstBatch, first, firstRate := testAdmissionProjection(t, h.stateRoot, 1, StatePending)
	secondBatch, second, secondRate := testAdmissionProjection(t, h.stateRoot, 2, StatePending)
	h.install(firstBatch, first, firstRate)
	h.install(secondBatch, second, secondRate)
	h.launcher.exists = func(context.Context, string) (bool, error) { return true, nil }

	h.reconcile()
	started := 0
	for _, id := range []string{first.ID, second.ID} {
		if h.run(id).State == StateRunning {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("started %d runs, want 1 under max concurrency", started)
	}
}

func TestManagerFirstSegmentStartFailureIsTerminal(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := testAdmissionProjection(t, h.stateRoot, 1, StatePending)
	h.install(batch, run, rate)
	h.launcher.start = func(context.Context, Run, string, string) error { return errors.New("tmux unavailable") }

	h.reconcile()
	failed := h.run(run.ID)
	if failed.State != StateFailed {
		t.Fatalf("state = %q, want failed", failed.State)
	}
	if failed.FinishedAt == nil || !strings.Contains(failed.Detail, "start tmux session") {
		t.Fatalf("start-failure detail = %q finished = %v", failed.Detail, failed.FinishedAt)
	}
}

func TestManagerStartsPostMergePendingRun(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := postMergePendingProjection(t, h.stateRoot, 1)
	h.install(batch, run, rate)

	h.reconcile()
	if got := h.run(run.ID).State; got != StateRunning {
		t.Fatalf("post-merge start state = %q, want running", got)
	}
}

func TestManagerPostMergeStartFailureReturnsToPostMergePending(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := postMergePendingProjection(t, h.stateRoot, 1)
	h.install(batch, run, rate)
	h.launcher.start = func(context.Context, Run, string, string) error { return errors.New("tmux unavailable") }

	h.reconcile()
	deferred := h.run(run.ID)
	if deferred.State != StatePostMergePending {
		t.Fatalf("state = %q, want post_merge_pending", deferred.State)
	}
	if deferred.SegmentStartedAt != nil {
		t.Fatal("post-merge retry did not clear the segment")
	}
	if deferred.FinishedAt != nil {
		t.Fatal("post-merge retry incorrectly finished the Run")
	}
}

// ---- active reconcile + stale guard ------------------------------------

func TestManagerRecoversStartingSessionToRunning(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := startingProjection(t, h.stateRoot, 1)
	h.install(batch, run, rate)
	h.launcher.exists = func(context.Context, string) (bool, error) { return true, nil }

	h.reconcile()
	if got := h.run(run.ID).State; got != StateRunning {
		t.Fatalf("recovered state = %q, want running", got)
	}
}

func TestManagerFinishesLostSession(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningProjection(t, h.stateRoot)
	h.install(batch, run, rate)
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) { return ProcessResult{}, errors.New("no result file") }

	h.reconcile()
	failed := h.run(run.ID)
	if failed.State != StateFailed || !strings.Contains(failed.Detail, "without a process result") {
		t.Fatalf("state = %q detail = %q", failed.State, failed.Detail)
	}
}

func TestManagerRejectsStaleProcessResult(t *testing.T) {
	cases := map[string]func(run Run) ProcessResult{
		"attempts not advanced": func(run Run) ProcessResult {
			return ProcessResult{Status: string(StateSucceeded), Attempts: run.SegmentAttempt, FinishedAt: run.SegmentStartedAt.Add(time.Second)}
		},
		"finished before segment": func(run Run) ProcessResult {
			return ProcessResult{Status: string(StateSucceeded), Attempts: run.SegmentAttempt + 1, FinishedAt: run.SegmentStartedAt.Add(-time.Second)}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			h := newManagerHarness(t, 2)
			batch, run, rate := runningProjection(t, h.stateRoot)
			h.install(batch, run, rate)
			installed := h.run(run.ID)
			h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
			h.launcher.result = func(string) (ProcessResult, error) { return build(installed), nil }
			validated := false
			h.terminal.fn = func(context.Context, Run, ProcessResult) TerminalDecision {
				validated = true
				return TerminalDecision{State: StateSucceeded}
			}

			h.reconcile()
			failed := h.run(run.ID)
			if failed.State != StateFailed || !strings.Contains(failed.Detail, "stale or unbound") {
				t.Fatalf("state = %q detail = %q", failed.State, failed.Detail)
			}
			if validated {
				t.Fatal("stale result must not reach the terminal validator")
			}
		})
	}
}

// ---- non-PR terminal completion ----------------------------------------

func TestManagerAcceptsProcessFailure(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningProjection(t, h.stateRoot)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateFailed), Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.terminal.fn = func(_ context.Context, _ Run, result ProcessResult) TerminalDecision {
		return TerminalDecision{
			State:  StateFailed,
			Detail: "process failure preserved",
			Validation: CompletionValidation{
				Accepted: true, Intent: "failed", State: StateFailed, Reason: "process failure preserved",
			},
		}
	}

	h.reconcile()
	finished := h.run(run.ID)
	if finished.State != StateFailed || finished.Completion == nil || !finished.Completion.Accepted {
		t.Fatalf("completion = %+v state = %q", finished.Completion, finished.State)
	}
	if finished.TerminalRejection != "" {
		t.Fatalf("accepted completion carries rejection %q", finished.TerminalRejection)
	}
	if finished.Completion.ValidatedAt.Before(finished.CreatedAt) || finished.Completion.ValidatedAt.After(finished.UpdatedAt) {
		t.Fatal("completion validated time is outside the Run lifecycle window")
	}
}

func TestManagerAcceptsPrePullRequestBlocker(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningProjection(t, h.stateRoot)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateBlocked), Blocker: "missing_routing_metadata", Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.terminal.fn = func(_ context.Context, _ Run, result ProcessResult) TerminalDecision {
		return TerminalDecision{
			State:  StateBlocked,
			Detail: "typed pre-checkpoint blocker accepted",
			Validation: CompletionValidation{
				Accepted: true, Intent: "blocked", State: StateBlocked, Blocker: result.Blocker, Reason: "typed pre-checkpoint blocker accepted",
			},
		}
	}

	h.reconcile()
	finished := h.run(run.ID)
	if finished.State != StateBlocked || finished.Completion == nil || !finished.Completion.Accepted {
		t.Fatalf("state = %q completion = %+v", finished.State, finished.Completion)
	}
	if finished.Completion.Blocker != "missing_routing_metadata" {
		t.Fatalf("blocker = %q", finished.Completion.Blocker)
	}
}

func TestManagerRejectsInvalidSuccessFailClosed(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningProjection(t, h.stateRoot)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateSucceeded), Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
	}
	// A faithful validator refuses success without a manager-validated checkpoint.
	h.terminal.fn = func(_ context.Context, _ Run, result ProcessResult) TerminalDecision {
		return TerminalDecision{
			State:  StateFailed,
			Detail: "terminal intent rejected: success without a manager-validated ready checkpoint is not permitted",
			Validation: CompletionValidation{
				Accepted: false, Intent: "succeeded", State: StateFailed,
				Reason: "success without a manager-validated ready checkpoint is not permitted",
			},
		}
	}

	h.reconcile()
	finished := h.run(run.ID)
	if finished.State != StateFailed {
		t.Fatalf("unvalidated success state = %q, want failed", finished.State)
	}
	if finished.Completion == nil || finished.Completion.Accepted {
		t.Fatalf("unvalidated success must not be accepted: %+v", finished.Completion)
	}
	if !strings.Contains(finished.TerminalRejection, "not permitted") {
		t.Fatalf("terminal rejection = %q", finished.TerminalRejection)
	}
}

func TestManagerCannotAcceptUnacceptedSuccessRuling(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningProjection(t, h.stateRoot)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateSucceeded), Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.terminal.fn = func(context.Context, Run, ProcessResult) TerminalDecision {
		return TerminalDecision{
			State: StateSucceeded,
			Validation: CompletionValidation{
				Accepted: false, Intent: "succeeded", State: StateSucceeded, Reason: "unsafe test ruling",
			},
		}
	}

	h.reconcile()
	finished := h.run(run.ID)
	if finished.State != StateFailed || finished.Completion == nil || finished.Completion.Accepted {
		t.Fatalf("unsafe validator ruling was not reduced to failed: state=%q completion=%+v", finished.State, finished.Completion)
	}
	if finished.TerminalRejection != "terminal validator returned an unsafe ruling" {
		t.Fatalf("terminal rejection = %q", finished.TerminalRejection)
	}
}

// ---- timestamp / identity invariants -----------------------------------

func TestManagerTransitionTimestampsStrictlyAdvanceUnderFixedClock(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := admittedProjection(t, 1, linearTask("ENG-1"))
	h.install(batch, run, rate)
	// Pin the clock at the Run's own timestamp so only the monotonic nudge keeps
	// transitions strictly ordered.
	h.clock = run.CreatedAt
	route := managerRoute(h.stateRoot)
	h.resolver.fn = func(context.Context, Run) (repositories.Route, error) { return route, nil }

	h.reconcile() // admitted -> routing
	h.reconcile() // routing -> pending
	pending := h.run(run.ID)
	seen := make(map[string]bool)
	for index, transition := range pending.Transitions {
		if seen[transition.ID] {
			t.Fatalf("transition ID %q collided under a fixed clock", transition.ID)
		}
		seen[transition.ID] = true
		if index > 0 && !transition.At.After(pending.Transitions[index-1].At) {
			t.Fatalf("transition %d time %s did not advance past %s", index, transition.At, pending.Transitions[index-1].At)
		}
	}
	if len(pending.Transitions) != 3 {
		t.Fatalf("transitions = %d, want admitted+routing+pending", len(pending.Transitions))
	}
}

func TestManagerStaleTransitionFailsClosed(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := admittedProjection(t, 1, linearTask("ENG-1"))
	h.install(batch, run, rate)
	stale := h.run(run.ID)

	// A concurrent writer advances the Run to routing first.
	if err := h.manager.transition(stale, StateRouting, func(next *Run, _ time.Time) { next.Detail = "concurrent" }); err != nil {
		t.Fatalf("concurrent transition: %v", err)
	}
	// The manager's own attempt on the now-stale snapshot must fail closed.
	if err := h.manager.transition(stale, StateRouting, func(next *Run, _ time.Time) { next.Detail = "stale" }); err == nil {
		t.Fatal("stale transition on an already-advanced Run was accepted")
	}
	if got := h.run(run.ID).Detail; got != "concurrent" {
		t.Fatalf("stale transition clobbered durable state: detail = %q", got)
	}
}

func TestManagerEmitsBodyFreeTransitionDeliveries(t *testing.T) {
	h := newManagerHarness(t, 2)
	batch, run, rate := runningProjection(t, h.stateRoot)
	h.install(batch, run, rate)
	installed := h.run(run.ID)
	const sentinel = "SENTINEL-PRIVATE-BODY"
	h.launcher.exists = func(context.Context, string) (bool, error) { return false, nil }
	h.launcher.result = func(string) (ProcessResult, error) {
		return ProcessResult{Status: string(StateFailed), Detail: sentinel, Attempts: installed.SegmentAttempt + 1, FinishedAt: installed.SegmentStartedAt.Add(time.Second)}, nil
	}
	h.terminal.fn = func(context.Context, Run, ProcessResult) TerminalDecision {
		return TerminalDecision{State: StateFailed, Detail: sentinel, Validation: CompletionValidation{Accepted: true, Intent: "failed", State: StateFailed, Reason: sentinel}}
	}

	h.reconcile()
	finished := h.run(run.ID)
	for _, delivery := range finished.TransitionDeliveries {
		if delivery.EventID != RunTransitionEventID(delivery.TransitionID) {
			t.Fatalf("delivery event ID %q is not the opaque transition identity", delivery.EventID)
		}
		if strings.Contains(delivery.EventID, sentinel) || strings.Contains(delivery.TransitionID, sentinel) {
			t.Fatalf("transition delivery leaked a private body: %+v", delivery)
		}
	}
}

// ---- constructor + dormancy --------------------------------------------

func TestNewManagerRequiresCollaboratorsAndCanonicalLifecyclePortsRemainDormant(t *testing.T) {
	store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 8)
	resolver := fakeResolver{fn: func(context.Context, Run) (repositories.Route, error) { return repositories.Route{}, nil }}
	launcher := fakeLauncher{}
	terminal := fakeTerminal{fn: func(context.Context, Run, ProcessResult) TerminalDecision {
		return TerminalDecision{State: StateFailed}
	}}
	collector := &recordingCollector{}
	dispatch := &fakeDispatchGate{dispatched: ^uint64(0)}
	pullRequests := fakePullRequests{}
	stateRoot := t.TempDir()
	positive := func() int { return 1 }
	clock := func() time.Time { return modelTestNow }
	const poll = time.Minute
	const merge = 5 * time.Minute

	if _, err := NewManager(nil, dispatch, resolver, launcher, pullRequests, terminal, collector, stateRoot, positive, poll, merge, clock, discardLogger()); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := NewManager(store, nil, resolver, launcher, pullRequests, terminal, collector, stateRoot, positive, poll, merge, clock, discardLogger()); err == nil {
		t.Fatal("nil dispatch gate accepted")
	}
	if _, err := NewManager(store, dispatch, nil, launcher, pullRequests, terminal, collector, stateRoot, positive, poll, merge, clock, discardLogger()); err == nil {
		t.Fatal("nil resolver accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, nil, pullRequests, terminal, collector, stateRoot, positive, poll, merge, clock, discardLogger()); err == nil {
		t.Fatal("nil launcher accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, launcher, nil, terminal, collector, stateRoot, positive, poll, merge, clock, discardLogger()); err == nil {
		t.Fatal("nil pull request reader accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, launcher, pullRequests, nil, collector, stateRoot, positive, poll, merge, clock, discardLogger()); err == nil {
		t.Fatal("nil terminal validator accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, launcher, pullRequests, terminal, nil, stateRoot, positive, poll, merge, clock, discardLogger()); err == nil {
		t.Fatal("nil collector accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, launcher, pullRequests, terminal, collector, "relative/path", positive, poll, merge, clock, discardLogger()); err == nil {
		t.Fatal("non-canonical state root accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, launcher, pullRequests, terminal, collector, stateRoot, func() int { return 0 }, poll, merge, clock, discardLogger()); err == nil {
		t.Fatal("non-positive concurrency accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, launcher, pullRequests, terminal, collector, stateRoot, positive, 0, merge, clock, discardLogger()); err == nil {
		t.Fatal("non-positive poll interval accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, launcher, pullRequests, terminal, collector, stateRoot, positive, poll, 0, clock, discardLogger()); err == nil {
		t.Fatal("non-positive merge interval accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, launcher, pullRequests, terminal, collector, stateRoot, positive, poll, merge, nil, discardLogger()); err == nil {
		t.Fatal("nil clock accepted")
	}
	if _, err := NewManager(store, dispatch, resolver, launcher, pullRequests, terminal, collector, stateRoot, positive, poll, merge, clock, nil); err == nil {
		t.Fatal("nil logger accepted")
	}

	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	var calls []string
	dormant := map[string]bool{
		"NewManager":                       true,
		"NewGitHubCLI":                     true,
		"NewMechanicalCompletionValidator": true,
		"NewSystemCompletionEvidence":      true,
	}
	err = filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".worktrees", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(files, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		inRunsPackage := file.Name.Name == "runs"
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			// Only constructions of these canonical dormant lifecycle ports count:
			// qualified runs selectors, or bare constructors inside package runs.
			// Same-named constructors elsewhere (agentrun, projectsetup) are unrelated.
			constructs := false
			switch function := call.Fun.(type) {
			case *ast.Ident:
				constructs = inRunsPackage && dormant[function.Name]
			case *ast.SelectorExpr:
				pkg, ok := function.X.(*ast.Ident)
				constructs = ok && pkg.Name == "runs" && dormant[function.Sel.Name]
			}
			if constructs {
				position := files.Position(call.Pos())
				relative, _ := filepath.Rel(repositoryRoot, position.Filename)
				calls = append(calls, fmt.Sprintf("%s:%d", relative, position.Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("production constructs dormant canonical lifecycle port: %v", calls)
	}
}
