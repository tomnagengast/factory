package agentrun

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

const stateVersion = 2

const (
	TriggerKindLabel     = "linear-label"
	TriggerKindComment   = "linear-comment"
	TriggerKindGitHub    = "github-update"
	TriggerKindPostMerge = "post-merge"
	TriggerKindRule      = "configured-rule"
)

var (
	runIDPattern = regexp.MustCompile(`^run-[a-f0-9]{16}$`)
)

type State string

const (
	StatePending          State = "pending"
	StatePostMergePending State = "post_merge_pending"
	StateStarting         State = "starting"
	StateRunning          State = "running"
	StateAwaitingMerge    State = "awaiting_human_merge"
	StateSucceeded        State = "succeeded"
	StateBlocked          State = "blocked"
	StateFailed           State = "failed"
)

func (s State) Active() bool {
	return s.Nonterminal()
}

func (s State) Nonterminal() bool {
	return s == StatePending || s == StatePostMergePending || s == StateStarting || s == StateRunning || s == StateAwaitingMerge
}

func (s State) HasWorker() bool {
	return s == StateStarting || s == StateRunning
}

type Trigger struct {
	DeliveryID      string
	Task            taskmodel.TaskRef
	IssueIdentifier string
	Kind            string
	Repository      string
	RepositoryURL   string
	RepositoryPath  string
	ManagedRoot     string
	BaseBranch      string
	Bootstrap       bool
	CloudURL        string
}

type Transition struct {
	ID       string    `json:"id"`
	State    State     `json:"state"`
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
}

type Run struct {
	ID                         string                `json:"id"`
	Task                       taskmodel.TaskRef     `json:"task"`
	IssueIdentifier            string                `json:"issueIdentifier,omitempty"`
	Repository                 string                `json:"repository,omitempty"`
	RepositoryURL              string                `json:"repositoryUrl,omitempty"`
	RepositoryPath             string                `json:"repositoryPath,omitempty"`
	ManagedRoot                string                `json:"managedRoot,omitempty"`
	BaseBranch                 string                `json:"baseBranch,omitempty"`
	Bootstrap                  bool                  `json:"bootstrap,omitempty"`
	CloudURL                   string                `json:"cloudUrl,omitempty"`
	TriggerKind                string                `json:"triggerKind"`
	DeliveryIDs                []string              `json:"deliveryIds"`
	State                      State                 `json:"state"`
	SessionName                string                `json:"sessionName,omitempty"`
	RunDirectory               string                `json:"runDirectory,omitempty"`
	Attempts                   int                   `json:"attempts"`
	DuplicateTriggers          uint64                `json:"duplicateTriggers"`
	Detail                     string                `json:"detail,omitempty"`
	CreatedAt                  time.Time             `json:"createdAt"`
	UpdatedAt                  time.Time             `json:"updatedAt"`
	StartedAt                  *time.Time            `json:"startedAt,omitempty"`
	SegmentStartedAt           *time.Time            `json:"segmentStartedAt,omitempty"`
	SegmentAttempt             int                   `json:"segmentAttemptOffset,omitempty"`
	FinishedAt                 *time.Time            `json:"finishedAt,omitempty"`
	Transitions                []Transition          `json:"transitions,omitempty"`
	Ready                      *ReadyCheckpoint      `json:"ready,omitempty"`
	MergeCommitOID             string                `json:"mergeCommitOid,omitempty"`
	LastGitHubCursor           uint64                `json:"lastGitHubCursor,omitempty"`
	LastAuthoritativeRefreshAt *time.Time            `json:"lastAuthoritativeRefreshAt,omitempty"`
	NextReconcileAt            *time.Time            `json:"nextReconcileAt,omitempty"`
	ReconcileFailures          int                   `json:"reconcileFailures,omitempty"`
	RemediationRequested       bool                  `json:"remediationRequested,omitempty"`
	ResumeCount                int                   `json:"resumeCount,omitempty"`
	TerminalIntent             string                `json:"terminalIntent,omitempty"`
	TerminalRejection          string                `json:"terminalRejection,omitempty"`
	Completion                 *CompletionValidation `json:"completion,omitempty"`
	InvocationID               string                `json:"invocationId,omitempty"`
	InvocationRootEventID      string                `json:"invocationRootEventId,omitempty"`
	InvocationHop              int                   `json:"invocationHop,omitempty"`
	InvocationAncestorRuleIDs  []string              `json:"invocationAncestorRuleIds,omitempty"`
	PinnedWorkflow             *workflow.Pinned      `json:"pinnedWorkflow,omitempty"`
	PinnedWorkflowDigest       string                `json:"pinnedWorkflowDigest,omitempty"`
	PinnedPolicyRevision       uint64                `json:"pinnedPolicyRevision,omitempty"`
	InvocationReflectedAt      *time.Time            `json:"invocationReflectedAt,omitempty"`
}

type InvocationClaim struct {
	RunID           string
	InvocationID    string
	EventID         string
	Task            taskmodel.TaskRef
	IssueIdentifier string
	RootEventID     string
	Hop             int
	AncestorRuleIDs []string
	Workflow        workflow.Pinned
	WorkflowDigest  string
	PolicyRevision  uint64
	Repository      RepositoryConfig
}

type ContinuationClaim struct {
	Trigger        Trigger
	Workflow       workflow.Pinned
	WorkflowDigest string
	PolicyRevision uint64
}

var ErrInvocationIssueOwned = errors.New("agent run store: issue already has an active run")

type PublicRun struct {
	ID                string     `json:"id"`
	State             State      `json:"state"`
	Attempts          int        `json:"attempts"`
	DuplicateTriggers uint64     `json:"duplicateTriggers"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
}

type Snapshot struct {
	Total  uint64
	Active int
	Runs   []Run
}

type PublicSnapshot struct {
	Total  uint64      `json:"total"`
	Active int         `json:"active"`
	Runs   []PublicRun `json:"runs"`
}

type ActivityRun struct {
	ID                         string                `json:"id"`
	Task                       taskmodel.TaskRef     `json:"task"`
	IssueIdentifier            string                `json:"issueIdentifier"`
	State                      State                 `json:"state"`
	Attempts                   int                   `json:"attempts"`
	DuplicateTriggers          uint64                `json:"duplicateTriggers"`
	CreatedAt                  time.Time             `json:"createdAt"`
	UpdatedAt                  time.Time             `json:"updatedAt"`
	StartedAt                  *time.Time            `json:"startedAt,omitempty"`
	FinishedAt                 *time.Time            `json:"finishedAt,omitempty"`
	Ready                      *ReadyCheckpoint      `json:"ready,omitempty"`
	MergeCommitOID             string                `json:"mergeCommitOid,omitempty"`
	LastGitHubCursor           uint64                `json:"lastGitHubCursor,omitempty"`
	LastAuthoritativeRefreshAt *time.Time            `json:"lastAuthoritativeRefreshAt,omitempty"`
	NextReconcileAt            *time.Time            `json:"nextReconcileAt,omitempty"`
	ReconcileFailures          int                   `json:"reconcileFailures,omitempty"`
	ResumeCount                int                   `json:"resumeCount,omitempty"`
	TerminalRejection          string                `json:"terminalRejection,omitempty"`
	Completion                 *CompletionValidation `json:"completion,omitempty"`
}

type ActivitySnapshot struct {
	Total  uint64        `json:"total"`
	Active int           `json:"active"`
	Runs   []ActivityRun `json:"runs"`
}

type diskState struct {
	Version int    `json:"version"`
	Total   uint64 `json:"total"`
	Runs    []Run  `json:"runs"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	limit int
	state diskState
}

func Open(path string, limit int) (*Store, error) {
	if path == "" {
		return nil, errors.New("agent run store: path is required")
	}
	if limit < 1 {
		return nil, errors.New("agent run store: limit must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("agent run store: create directory: %w", err)
	}

	s := &Store{path: path, limit: limit, state: diskState{Version: stateVersion}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent run store: read: %w", err)
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("agent run store: decode: %w", err)
	}
	if s.state.Version != 1 && s.state.Version != stateVersion {
		return nil, fmt.Errorf("agent run store: unsupported state version %d", s.state.Version)
	}
	for i := range s.state.Runs {
		if err := normalizeRunIdentity(&s.state.Runs[i]); err != nil {
			return nil, fmt.Errorf("agent run store: run %q identity: %w", s.state.Runs[i].ID, err)
		}
	}
	s.state.Version = stateVersion
	s.state.Runs = prune(s.state.Runs, limit)
	return s, nil
}

func (s *Store) Claim(trigger Trigger, now time.Time) (Run, bool, error) {
	return s.claim(trigger, workflow.Pinned{}, "", 0, now, false)
}

func (s *Store) ClaimContinuation(claim ContinuationClaim, now time.Time) (Run, bool, error) {
	return s.claim(claim.Trigger, claim.Workflow, claim.WorkflowDigest, claim.PolicyRevision, now, true)
}

func (s *Store) EnsureInvocationRun(claim InvocationClaim, now time.Time) (Run, bool, error) {
	if !runIDPattern.MatchString(claim.RunID) || claim.InvocationID == "" || claim.EventID == "" {
		return Run{}, false, errors.New("agent run store: invocation identity is invalid")
	}
	task, err := taskmodel.ResolveCompatibilityIdentity(claim.Task, claim.IssueIdentifier)
	if err != nil {
		return Run{}, false, fmt.Errorf("agent run store: invalid invocation task: %w", err)
	}
	if claim.RootEventID == "" || claim.Hop < 1 || len(claim.AncestorRuleIDs) != claim.Hop {
		return Run{}, false, errors.New("agent run store: invocation causation is invalid")
	}
	if err := validatePinnedWorkflow(claim.Workflow, claim.WorkflowDigest, claim.PolicyRevision); err != nil {
		return Run{}, false, errors.New("agent run store: pinned workflow is invalid")
	}
	if err := claim.Repository.validate(); err != nil {
		return Run{}, false, err
	}

	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.state.Runs {
		if run.ID == claim.RunID || run.InvocationID == claim.InvocationID {
			if invocationRunMatches(run, claim) {
				return cloneRun(run), false, nil
			}
			return Run{}, false, errors.New("agent run store: invocation Run identity collision")
		}
	}
	for _, run := range s.state.Runs {
		if run.Task.Equal(task) && run.State.Active() {
			return Run{}, false, ErrInvocationIssueOwned
		}
	}
	claim.Task = task
	claim.IssueIdentifier = task.Identifier
	pinnedWorkflow := claim.Workflow.Clone()
	run := Run{
		ID: claim.RunID, Task: task, IssueIdentifier: task.Identifier,
		Repository: claim.Repository.Repository, RepositoryURL: claim.Repository.RepoURL,
		RepositoryPath: claim.Repository.RepoPath, ManagedRoot: claim.Repository.ManagedRoot,
		BaseBranch: claim.Repository.BaseBranch, Bootstrap: claim.Repository.Bootstrap, CloudURL: claim.Repository.CloudURL,
		TriggerKind: TriggerKindRule, DeliveryIDs: []string{claim.EventID}, State: StatePending,
		CreatedAt: now, UpdatedAt: now, Transitions: []Transition{newTransition(claim.RunID, StatePending, 0, now)},
		InvocationID: claim.InvocationID, InvocationRootEventID: claim.RootEventID, InvocationHop: claim.Hop,
		InvocationAncestorRuleIDs: slices.Clone(claim.AncestorRuleIDs), PinnedWorkflow: &pinnedWorkflow,
		PinnedWorkflowDigest: claim.WorkflowDigest, PinnedPolicyRevision: claim.PolicyRevision,
	}
	next := s.state
	next.Total++
	next.Runs = append([]Run{run}, cloneRuns(s.state.Runs)...)
	next.Runs = prune(next.Runs, s.limit)
	if err := writeState(s.path, next); err != nil {
		return Run{}, false, err
	}
	s.state = next
	return cloneRun(run), true, nil
}

func invocationRunMatches(run Run, claim InvocationClaim) bool {
	task, err := taskmodel.ResolveCompatibilityIdentity(claim.Task, claim.IssueIdentifier)
	return err == nil && run.ID == claim.RunID && run.InvocationID == claim.InvocationID && run.Task.Equal(task) &&
		run.InvocationRootEventID == claim.RootEventID && run.InvocationHop == claim.Hop &&
		slices.Equal(run.InvocationAncestorRuleIDs, claim.AncestorRuleIDs) && run.PinnedWorkflow != nil &&
		workflowEqual(*run.PinnedWorkflow, claim.Workflow) && run.PinnedWorkflowDigest == claim.WorkflowDigest &&
		run.PinnedPolicyRevision == claim.PolicyRevision && run.Repository == claim.Repository.Repository &&
		run.RepositoryURL == claim.Repository.RepoURL && run.RepositoryPath == claim.Repository.RepoPath &&
		run.ManagedRoot == claim.Repository.ManagedRoot && run.BaseBranch == claim.Repository.BaseBranch &&
		run.Bootstrap == claim.Repository.Bootstrap && run.CloudURL == claim.Repository.CloudURL
}

func workflowEqual(left, right workflow.Pinned) bool {
	return left.ID == right.ID && left.Revision == right.Revision && left.Name == right.Name &&
		left.Enabled == right.Enabled && left.Markdown == right.Markdown && equalWorkflowTime(left.UpdatedAt, right.UpdatedAt) &&
		left.Runner == right.Runner && slices.Equal(left.Steps, right.Steps)
}

func equalWorkflowTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func validatePinnedWorkflow(pinned workflow.Pinned, digest string, policyRevision uint64) error {
	if err := pinned.Validate(); err != nil || !pinned.Enabled || !pinned.Complete() || digest == "" {
		return errors.New("pinned workflow metadata is incomplete")
	}
	actual, err := pinned.Digest()
	if err != nil {
		return err
	}
	if actual != digest {
		return errors.New("pinned workflow digest mismatch")
	}
	return nil
}

func (s *Store) MarkInvocationReflected(id string, at time.Time) error {
	return s.update(id, at, func(run *Run) error {
		if run.InvocationID == "" || run.State.Nonterminal() {
			return errors.New("cannot reflect a legacy or nonterminal Run")
		}
		if run.InvocationReflectedAt != nil {
			return nil
		}
		value := at.UTC()
		run.InvocationReflectedAt = &value
		if run.PinnedWorkflow != nil {
			compacted := run.PinnedWorkflow.Compact()
			run.PinnedWorkflow = &compacted
		}
		return nil
	})
}

func (s *Store) claim(trigger Trigger, pinned workflow.Pinned, digest string, policyRevision uint64, now time.Time, requireHistory bool) (Run, bool, error) {
	if trigger.DeliveryID == "" {
		return Run{}, false, errors.New("agent run store: delivery ID is required")
	}
	task, err := taskmodel.ResolveCompatibilityIdentity(trigger.Task, trigger.IssueIdentifier)
	if err != nil {
		return Run{}, false, fmt.Errorf("agent run store: invalid task: %w", err)
	}
	if trigger.Kind == "" {
		return Run{}, false, errors.New("agent run store: trigger kind is required")
	}

	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	hasHistory := false
	for i := range s.state.Runs {
		run := s.state.Runs[i]
		if slices.Contains(run.DeliveryIDs, trigger.DeliveryID) {
			return run, false, nil
		}
		if !run.Task.Equal(task) {
			continue
		}
		hasHistory = true
		if run.State.Active() {
			next := s.state
			next.Runs = slices.Clone(s.state.Runs)
			nextRun := &next.Runs[i]
			nextRun.DeliveryIDs = append(slices.Clone(nextRun.DeliveryIDs), trigger.DeliveryID)
			nextRun.DuplicateTriggers++
			nextRun.UpdatedAt = now
			previousState := nextRun.State
			if nextRun.State == StateAwaitingMerge && trigger.Kind == TriggerKindComment {
				resumeAwaitingRun(nextRun, trigger.Kind, "", "Linear feedback received; resuming lifecycle", now)
			}
			if nextRun.State != previousState {
				nextRun.Transitions = append(nextRun.Transitions, newTransition(nextRun.ID, nextRun.State, nextRun.Attempts, now))
			}
			if err := writeState(s.path, next); err != nil {
				return Run{}, false, err
			}
			s.state = next
			return *nextRun, false, nil
		}
	}
	if requireHistory && !hasHistory {
		return Run{}, false, nil
	}
	if requireHistory {
		if err := validatePinnedWorkflow(pinned, digest, policyRevision); err != nil {
			return Run{}, false, fmt.Errorf("agent run store: continuation workflow: %w", err)
		}
	}

	id, err := newID()
	if err != nil {
		return Run{}, false, err
	}
	run := Run{
		ID:              id,
		Task:            task,
		IssueIdentifier: task.Identifier,
		Repository:      trigger.Repository,
		RepositoryURL:   trigger.RepositoryURL,
		RepositoryPath:  trigger.RepositoryPath,
		ManagedRoot:     trigger.ManagedRoot,
		BaseBranch:      trigger.BaseBranch,
		Bootstrap:       trigger.Bootstrap,
		CloudURL:        trigger.CloudURL,
		TriggerKind:     trigger.Kind,
		DeliveryIDs:     []string{trigger.DeliveryID},
		State:           StatePending,
		CreatedAt:       now,
		UpdatedAt:       now,
		Transitions:     []Transition{newTransition(id, StatePending, 0, now)},
	}
	if requireHistory {
		pinned = pinned.Clone()
		run.PinnedWorkflow = &pinned
		run.PinnedWorkflowDigest = digest
		run.PinnedPolicyRevision = policyRevision
	}
	next := s.state
	next.Total++
	next.Runs = append([]Run{run}, next.Runs...)
	next.Runs = prune(next.Runs, s.limit)
	if err := writeState(s.path, next); err != nil {
		return Run{}, false, err
	}
	s.state = next
	return run, true, nil
}

func (s *Store) MarkStarting(id, sessionName, runDirectory string, now time.Time) error {
	if sessionName == "" || runDirectory == "" {
		return errors.New("agent run store: session name and run directory are required")
	}
	return s.update(id, now, func(run *Run) error {
		if run.State != StatePending && run.State != StatePostMergePending {
			return fmt.Errorf("cannot start run in state %q", run.State)
		}
		run.State = StateStarting
		run.SessionName = sessionName
		run.RunDirectory = runDirectory
		startedAt := now.UTC()
		run.SegmentStartedAt = &startedAt
		run.SegmentAttempt = run.Attempts
		run.NextReconcileAt = nil
		run.Detail = ""
		return nil
	})
}

func (s *Store) MarkRunning(id string, attempts int, now time.Time) error {
	return s.update(id, now, func(run *Run) error {
		if run.State != StateStarting && run.State != StateRunning {
			return fmt.Errorf("cannot mark run running from state %q", run.State)
		}
		run.State = StateRunning
		if attempts > run.Attempts {
			run.Attempts = attempts
		}
		if run.StartedAt == nil {
			startedAt := now.UTC()
			run.StartedAt = &startedAt
		}
		run.NextReconcileAt = nil
		run.ReconcileFailures = 0
		run.Detail = ""
		return nil
	})
}

func (s *Store) DeferReadyValidation(id, detail string, next time.Time, now time.Time) error {
	return s.update(id, now, func(run *Run) error {
		if run.State != StateStarting && run.State != StateRunning {
			return fmt.Errorf("cannot defer ready validation from state %q", run.State)
		}
		next = next.UTC()
		run.NextReconcileAt = &next
		refreshedAt := now.UTC()
		run.LastAuthoritativeRefreshAt = &refreshedAt
		run.ReconcileFailures++
		run.Detail = detail
		return nil
	})
}

func (s *Store) RetryPostMergeStart(id, detail string, next time.Time, now time.Time) error {
	return s.update(id, now, func(run *Run) error {
		if run.State != StateStarting || run.TriggerKind != TriggerKindPostMerge || run.Ready == nil {
			return fmt.Errorf("cannot retry post-merge start from state %q", run.State)
		}
		next = next.UTC()
		run.State = StatePostMergePending
		run.SessionName = ""
		run.SegmentStartedAt = nil
		run.NextReconcileAt = &next
		run.ReconcileFailures++
		run.Detail = detail
		return nil
	})
}

func (s *Store) MarkAwaitingMerge(id string, checkpoint ReadyCheckpoint, next time.Time, attempts int, now time.Time) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	return s.update(id, now, func(run *Run) error {
		if run.State != StateStarting && run.State != StateRunning {
			return fmt.Errorf("cannot await merge from state %q", run.State)
		}
		if checkpoint.RunID != run.ID {
			return errors.New("ready checkpoint belongs to another run")
		}
		checkpoint.ValidatedAt = now.UTC()
		next = next.UTC()
		run.State = StateAwaitingMerge
		run.Ready = &checkpoint
		run.NextReconcileAt = &next
		refreshedAt := now.UTC()
		run.LastAuthoritativeRefreshAt = &refreshedAt
		run.ReconcileFailures = 0
		run.RemediationRequested = false
		run.Attempts = max(run.Attempts, attempts)
		run.Detail = "waiting for human merge"
		run.TerminalIntent = ""
		run.TerminalRejection = ""
		return nil
	})
}

func (s *Store) DeferMergeReconcile(id, detail string, next time.Time, failed bool, now time.Time) error {
	return s.update(id, now, func(run *Run) error {
		if run.State != StateAwaitingMerge {
			return fmt.Errorf("cannot defer merge reconcile from state %q", run.State)
		}
		next = next.UTC()
		run.NextReconcileAt = &next
		run.Detail = detail
		refreshedAt := now.UTC()
		run.LastAuthoritativeRefreshAt = &refreshedAt
		if failed {
			run.ReconcileFailures++
		} else {
			run.ReconcileFailures = 0
		}
		return nil
	})
}

func (s *Store) ReparkRejected(id string, checkpoint ReadyCheckpoint, next time.Time, attempts int, validation CompletionValidation, now time.Time) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	return s.update(id, now, func(run *Run) error {
		if run.State != StateStarting && run.State != StateRunning {
			return fmt.Errorf("cannot repark rejected terminal intent from state %q", run.State)
		}
		if checkpoint.RunID != run.ID {
			return errors.New("ready checkpoint belongs to another run")
		}
		next = next.UTC()
		run.State = StateAwaitingMerge
		run.Ready = &checkpoint
		run.NextReconcileAt = &next
		refreshedAt := now.UTC()
		run.LastAuthoritativeRefreshAt = &refreshedAt
		run.Attempts = max(run.Attempts, attempts)
		run.RemediationRequested = false
		run.Detail = "terminal intent rejected: " + validation.Reason
		run.TerminalIntent = validation.Intent
		run.TerminalRejection = validation.Reason
		run.Completion = &validation
		return nil
	})
}

func (s *Store) ResumeAwaiting(id, kind, mergeCommitOID, detail string, now time.Time) error {
	return s.update(id, now, func(run *Run) error {
		if run.State != StateAwaitingMerge {
			return fmt.Errorf("cannot resume merge lifecycle from state %q", run.State)
		}
		resumeAwaitingRun(run, kind, mergeCommitOID, detail, now)
		return nil
	})
}

func resumeAwaitingRun(run *Run, kind, mergeCommitOID, detail string, now time.Time) {
	run.State = StatePending
	if kind == TriggerKindPostMerge {
		run.State = StatePostMergePending
	}
	run.TriggerKind = kind
	run.MergeCommitOID = mergeCommitOID
	run.NextReconcileAt = nil
	refreshedAt := now.UTC()
	run.LastAuthoritativeRefreshAt = &refreshedAt
	run.ReconcileFailures = 0
	run.RemediationRequested = false
	run.ResumeCount++
	run.SessionName = ""
	run.Detail = detail
}

func (s *Store) SchedulePullRequestReconcile(repository string, pullRequest int, headBranch, deliveryID string, cursor uint64, remediation bool, now time.Time) (bool, error) {
	if !repositoryPattern.MatchString(repository) || (pullRequest < 1 && !validBranch(headBranch)) || deliveryID == "" {
		return false, errors.New("schedule pull request reconcile: invalid wake")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.state
	next.Runs = cloneRuns(s.state.Runs)
	for i := range next.Runs {
		run := &next.Runs[i]
		if run.State != StateAwaitingMerge || run.Ready == nil || run.Ready.Repository != repository {
			continue
		}
		if pullRequest > 0 && run.Ready.PullRequest != pullRequest {
			continue
		}
		if headBranch != "" && run.Ready.HeadBranch != headBranch {
			continue
		}
		if !slices.Contains(run.DeliveryIDs, deliveryID) {
			run.DeliveryIDs = append(run.DeliveryIDs, deliveryID)
			run.DuplicateTriggers++
		}
		at := now.UTC()
		run.NextReconcileAt = &at
		run.LastGitHubCursor = max(run.LastGitHubCursor, cursor)
		run.RemediationRequested = run.RemediationRequested || remediation
		run.UpdatedAt = at
		if err := writeState(s.path, next); err != nil {
			return false, err
		}
		s.state = next
		return true, nil
	}
	return false, nil
}

func (s *Store) Finish(id string, state State, attempts int, detail string, now time.Time) error {
	return s.finish(id, state, attempts, detail, nil, now)
}

func (s *Store) FinishValidated(id string, state State, attempts int, detail string, validation CompletionValidation, now time.Time) error {
	return s.finish(id, state, attempts, detail, &validation, now)
}

func (s *Store) finish(id string, state State, attempts int, detail string, validation *CompletionValidation, now time.Time) error {
	if state != StateSucceeded && state != StateBlocked && state != StateFailed {
		return fmt.Errorf("agent run store: invalid terminal state %q", state)
	}
	return s.update(id, now, func(run *Run) error {
		if !run.State.Active() {
			return fmt.Errorf("cannot finish run in state %q", run.State)
		}
		finishedAt := now.UTC()
		run.State = state
		run.Attempts = max(run.Attempts, attempts)
		run.Detail = detail
		run.FinishedAt = &finishedAt
		run.Completion = validation
		if validation != nil {
			run.TerminalIntent = validation.Intent
			if validation.Accepted {
				run.TerminalRejection = ""
			} else {
				run.TerminalRejection = validation.Reason
			}
		}
		return nil
	})
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	runs := cloneRuns(s.state.Runs)
	active := 0
	for _, run := range runs {
		if run.State.Active() {
			active++
		}
	}
	return Snapshot{Total: s.state.Total, Active: active, Runs: runs}
}

func (s *Store) Find(id string) (Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, run := range s.state.Runs {
		if run.ID == id {
			run = cloneRun(run)
			return run, true
		}
	}
	return Run{}, false
}

func ValidIssueIdentifier(value string) bool {
	return taskmodel.ValidLinearIdentifier(value)
}

func (s *Store) FindStarted(issueIdentifier string, startedUnixMilli int64) (Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var matched Run
	found := false
	for _, run := range s.state.Runs {
		if run.Task.Source != taskmodel.SourceLinear || !strings.EqualFold(run.Task.Identifier, issueIdentifier) || run.StartedAt == nil || run.StartedAt.UnixMilli() != startedUnixMilli {
			continue
		}
		if !found || run.CreatedAt.After(matched.CreatedAt) {
			matched = run
			found = true
		}
	}
	if found {
		matched = cloneRun(matched)
	}
	return matched, found
}

func (s *Store) PublicSnapshot() PublicSnapshot {
	snapshot := s.Snapshot()
	runs := make([]PublicRun, len(snapshot.Runs))
	for i, run := range snapshot.Runs {
		runs[i] = PublicRun{
			ID:                run.ID,
			State:             run.State,
			Attempts:          run.Attempts,
			DuplicateTriggers: run.DuplicateTriggers,
			CreatedAt:         run.CreatedAt,
			UpdatedAt:         run.UpdatedAt,
			StartedAt:         run.StartedAt,
			FinishedAt:        run.FinishedAt,
		}
	}
	return PublicSnapshot{Total: snapshot.Total, Active: snapshot.Active, Runs: runs}
}

func (s *Store) ActivitySnapshot() ActivitySnapshot {
	snapshot := s.Snapshot()
	runs := make([]ActivityRun, len(snapshot.Runs))
	for i, run := range snapshot.Runs {
		runs[i] = ActivityRun{
			ID:                         run.ID,
			Task:                       run.Task,
			IssueIdentifier:            run.IssueIdentifier,
			State:                      run.State,
			Attempts:                   run.Attempts,
			DuplicateTriggers:          run.DuplicateTriggers,
			CreatedAt:                  run.CreatedAt,
			UpdatedAt:                  run.UpdatedAt,
			StartedAt:                  run.StartedAt,
			FinishedAt:                 run.FinishedAt,
			Ready:                      cloneReady(run.Ready),
			MergeCommitOID:             run.MergeCommitOID,
			LastGitHubCursor:           run.LastGitHubCursor,
			LastAuthoritativeRefreshAt: cloneTime(run.LastAuthoritativeRefreshAt),
			NextReconcileAt:            cloneTime(run.NextReconcileAt),
			ReconcileFailures:          run.ReconcileFailures,
			ResumeCount:                run.ResumeCount,
			TerminalRejection:          run.TerminalRejection,
			Completion:                 cloneCompletion(run.Completion),
		}
	}
	return ActivitySnapshot{Total: snapshot.Total, Active: snapshot.Active, Runs: runs}
}

func (s *Store) update(id string, now time.Time, mutate func(*Run) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.state
	next.Runs = cloneRuns(s.state.Runs)
	for i := range next.Runs {
		if next.Runs[i].ID != id {
			continue
		}
		previousState := next.Runs[i].State
		if err := mutate(&next.Runs[i]); err != nil {
			return fmt.Errorf("agent run store: update %s: %w", id, err)
		}
		next.Runs[i].UpdatedAt = now.UTC()
		if next.Runs[i].State != previousState {
			next.Runs[i].Transitions = append(next.Runs[i].Transitions,
				newTransition(next.Runs[i].ID, next.Runs[i].State, next.Runs[i].Attempts, now),
			)
		}
		if err := writeState(s.path, next); err != nil {
			return err
		}
		s.state = next
		return nil
	}
	return fmt.Errorf("agent run store: run %s not found", id)
}

func (s *Store) AcknowledgeTransitions(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state
	next.Runs = cloneRuns(s.state.Runs)
	changed := false
	for i := range next.Runs {
		kept := next.Runs[i].Transitions[:0]
		for _, transition := range next.Runs[i].Transitions {
			if _, ok := wanted[transition.ID]; ok {
				changed = true
				continue
			}
			kept = append(kept, transition)
		}
		next.Runs[i].Transitions = kept
	}
	if !changed {
		return nil
	}
	next.Runs = prune(next.Runs, s.limit)
	if err := writeState(s.path, next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func prune(runs []Run, limit int) []Run {
	if len(runs) <= limit {
		return runs
	}
	kept := make([]Run, 0, limit)
	for _, run := range runs {
		if len(kept) < limit || run.State.Active() || len(run.Transitions) > 0 {
			kept = append(kept, run)
		}
	}
	return kept
}

func newTransition(runID string, state State, attempts int, at time.Time) Transition {
	return Transition{
		ID:       fmt.Sprintf("%s:%s:%d", runID, state, at.UTC().UnixNano()),
		State:    state,
		Attempts: attempts,
		At:       at.UTC(),
	}
}

func cloneRuns(runs []Run) []Run {
	cloned := make([]Run, len(runs))
	for i, run := range runs {
		cloned[i] = cloneRun(run)
	}
	return cloned
}

func cloneRun(run Run) Run {
	run.DeliveryIDs = slices.Clone(run.DeliveryIDs)
	run.Transitions = slices.Clone(run.Transitions)
	run.Ready = cloneReady(run.Ready)
	run.SegmentStartedAt = cloneTime(run.SegmentStartedAt)
	run.LastAuthoritativeRefreshAt = cloneTime(run.LastAuthoritativeRefreshAt)
	run.NextReconcileAt = cloneTime(run.NextReconcileAt)
	run.Completion = cloneCompletion(run.Completion)
	run.InvocationAncestorRuleIDs = slices.Clone(run.InvocationAncestorRuleIDs)
	if run.PinnedWorkflow != nil {
		pinned := run.PinnedWorkflow.Clone()
		run.PinnedWorkflow = &pinned
	}
	run.InvocationReflectedAt = cloneTime(run.InvocationReflectedAt)
	return run
}

func normalizeRunIdentity(run *Run) error {
	resolved, err := taskmodel.ResolveCompatibilityIdentity(run.Task, run.IssueIdentifier)
	if err != nil {
		return err
	}
	run.Task = resolved
	run.IssueIdentifier = resolved.Identifier
	return nil
}

func cloneCompletion(value *CompletionValidation) *CompletionValidation {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneReady(value *ReadyCheckpoint) *ReadyCheckpoint {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func newID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("agent run store: generate ID: %w", err)
	}
	return "run-" + hex.EncodeToString(value[:]), nil
}

func writeState(path string, value diskState) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".agent-runs-*")
	if err != nil {
		return fmt.Errorf("agent run store: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("agent run store: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		temp.Close()
		return fmt.Errorf("agent run store: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("agent run store: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("agent run store: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("agent run store: replace: %w", err)
	}
	return nil
}
