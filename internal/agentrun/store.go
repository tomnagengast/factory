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
	"sync"
	"time"
)

const stateVersion = 1

const (
	TriggerKindLabel     = "linear-label"
	TriggerKindComment   = "linear-comment"
	TriggerKindGitHub    = "github-update"
	TriggerKindPostMerge = "post-merge"
)

var issueIdentifierPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*$`)

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
	IssueIdentifier string
	Kind            string
}

type Transition struct {
	ID       string    `json:"id"`
	State    State     `json:"state"`
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
}

type Run struct {
	ID                         string                `json:"id"`
	IssueIdentifier            string                `json:"issueIdentifier"`
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
}

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
	if s.state.Version != stateVersion {
		return nil, fmt.Errorf("agent run store: unsupported state version %d", s.state.Version)
	}
	s.state.Runs = prune(s.state.Runs, limit)
	return s, nil
}

func (s *Store) Claim(trigger Trigger, now time.Time) (Run, bool, error) {
	return s.claim(trigger, now, false)
}

func (s *Store) ClaimContinuation(trigger Trigger, now time.Time) (Run, bool, error) {
	return s.claim(trigger, now, true)
}

func (s *Store) claim(trigger Trigger, now time.Time, requireHistory bool) (Run, bool, error) {
	if trigger.DeliveryID == "" {
		return Run{}, false, errors.New("agent run store: delivery ID is required")
	}
	if !issueIdentifierPattern.MatchString(trigger.IssueIdentifier) {
		return Run{}, false, fmt.Errorf("agent run store: invalid issue identifier %q", trigger.IssueIdentifier)
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
		if run.IssueIdentifier != trigger.IssueIdentifier {
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

	id, err := newID()
	if err != nil {
		return Run{}, false, err
	}
	run := Run{
		ID:              id,
		IssueIdentifier: trigger.IssueIdentifier,
		TriggerKind:     trigger.Kind,
		DeliveryIDs:     []string{trigger.DeliveryID},
		State:           StatePending,
		CreatedAt:       now,
		UpdatedAt:       now,
		Transitions:     []Transition{newTransition(id, StatePending, 0, now)},
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
	return issueIdentifierPattern.MatchString(value)
}

func (s *Store) FindStarted(issueIdentifier string, startedUnixMilli int64) (Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var matched Run
	found := false
	for _, run := range s.state.Runs {
		if run.IssueIdentifier != issueIdentifier || run.StartedAt == nil || run.StartedAt.UnixMilli() != startedUnixMilli {
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
	return run
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
