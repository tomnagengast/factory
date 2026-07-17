package runs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Trigger kinds recorded on a Run when the merge lifecycle resumes it. They
// preserve the legacy agentrun trigger vocabulary so downstream projections and
// wire events do not change.
const (
	triggerKindGitHub    = "github-update"
	triggerKindPostMerge = "post-merge"
)

// reconcileDelay is the legacy exponential backoff for a same-state retry:
// base * 2^min(failures, 4). The exponent is capped so a persistently failing
// GitHub refresh or start never overflows the delay.
func reconcileDelay(base time.Duration, failures int) time.Duration {
	if failures < 0 {
		failures = 0
	}
	if failures > 4 {
		failures = 4
	}
	return base * time.Duration(1<<failures)
}

// parkReadyRun validates a worker's ready-for-merge checkpoint against the live
// pull request and parks the Run to awaiting_human_merge. A transient GitHub
// read defers with backoff; an unresolvable checkpoint fails closed; a pull
// request that merged or closed while the checkpoint was being parked is parked
// and immediately resumed so the post-merge lifecycle is not lost.
func (m *Manager) parkReadyRun(ctx context.Context, run Run, result ProcessResult) {
	checkpoint, err := m.launcher.ReadReadyCheckpoint(run.RunDirectory)
	if err != nil {
		m.finishInvalidReady(run, result, err)
		return
	}
	if checkpoint.Task.IsZero() {
		checkpoint.Task = run.Causation.Task
	}
	checkpoint.CreatedAt = checkpoint.CreatedAt.UTC()
	if !checkpoint.PullRequestUpdatedAt.IsZero() {
		checkpoint.PullRequestUpdatedAt = checkpoint.PullRequestUpdatedAt.UTC()
	}
	if !checkpoint.ValidatedAt.IsZero() {
		checkpoint.ValidatedAt = checkpoint.ValidatedAt.UTC()
	}
	if err := m.validateCheckpoint(run, checkpoint); err != nil {
		m.finishInvalidReady(run, result, err)
		return
	}
	snapshot, err := m.pullRequests.Snapshot(ctx, checkpoint)
	if err != nil {
		m.deferReadyValidation(run, "ready checkpoint refresh failed: "+err.Error())
		return
	}
	if run.Repository != nil {
		checkpoint.Repository = run.Repository.Repository
	}
	checkpoint.PullRequestUpdatedAt = snapshot.UpdatedAt.UTC()
	switch snapshot.State {
	case "OPEN":
		if err := validateReadySnapshot(checkpoint, snapshot); err != nil {
			m.finishInvalidReady(run, result, err)
			return
		}
	case "MERGED", "CLOSED":
		if snapshot.BaseBranch != checkpoint.BaseBranch || snapshot.HeadBranch != checkpoint.HeadBranch {
			m.finishInvalidReady(run, result, errors.New("closed pull request identity does not match the ready checkpoint"))
			return
		}
	default:
		m.finishInvalidReady(run, result, fmt.Errorf("pull request state is %q", snapshot.State))
		return
	}
	if !m.markAwaitingMerge(run.ID, checkpoint, result.Attempts) {
		return
	}
	switch snapshot.State {
	case "MERGED":
		mergeCommitOID, detail := mergeResumeEvidence(snapshot, "pull request merged while checkpoint was being parked")
		m.resumeAwaiting(run.ID, triggerKindPostMerge, mergeCommitOID, detail)
	case "CLOSED":
		m.resumeAwaiting(run.ID, triggerKindPostMerge, "", "pull request closed while checkpoint was being parked")
	}
}

// reconcileAwaitingMerge polls a parked Run's pull request. An open, unchanged
// pull request defers with a fresh merge-poll timer; a changed pull request or a
// pending remediation resumes to pending; a merged or closed pull request
// resumes the post-merge lifecycle; a transient error or unknown state defers
// with exponential backoff.
func (m *Manager) reconcileAwaitingMerge(ctx context.Context, run Run) {
	if run.Ready == nil {
		m.finishAwaitingWithoutReady(run)
		return
	}
	if run.GitHub.NextReconcileAt != nil && m.now().Before(*run.GitHub.NextReconcileAt) {
		return
	}
	snapshot, err := m.pullRequests.Snapshot(ctx, *run.Ready)
	if err != nil {
		m.deferMergeReconcile(run, "GitHub refresh failed: "+err.Error(), true)
		return
	}
	switch snapshot.State {
	case "OPEN":
		updated := !run.Ready.PullRequestUpdatedAt.IsZero() && snapshot.UpdatedAt.After(run.Ready.PullRequestUpdatedAt)
		if run.GitHub.RemediationRequested || snapshot.SafeguardRegression || updated || snapshot.IsDraft ||
			snapshot.HeadBranch != run.Ready.HeadBranch || snapshot.HeadOID != run.Ready.VerifiedHeadOID {
			m.resumeAwaiting(run.ID, triggerKindGitHub, "", "pull request changed; resuming remediation")
			return
		}
		m.deferMergeReconcile(run, "waiting for human merge", false)
	case "MERGED":
		mergeCommitOID, detail := mergeResumeEvidence(snapshot, "pull request merged; resuming post-merge lifecycle")
		if snapshot.HeadOID != run.Ready.VerifiedHeadOID {
			detail = "merged pull request requires authoritative blocker review"
		}
		m.resumeAwaiting(run.ID, triggerKindPostMerge, mergeCommitOID, detail)
	case "CLOSED":
		m.resumeAwaiting(run.ID, triggerKindPostMerge, "", "pull request closed without merge; resuming blocker report")
	default:
		m.deferMergeReconcile(run, "unknown pull request state: "+snapshot.State, true)
	}
}

// mergeResumeEvidence keeps malformed provider data from wedging an awaiting
// Run. A missing or invalid merge commit cannot become canonical merge identity,
// but the Run still resumes so the mechanical post-merge validator can emit the
// authoritative typed blocker.
func mergeResumeEvidence(snapshot PullRequestSnapshot, matchedDetail string) (string, string) {
	if !gitOIDPattern.MatchString(snapshot.MergeCommitOID) {
		return "", "merged pull request requires authoritative blocker review"
	}
	return snapshot.MergeCommitOID, matchedDetail
}

// markAwaitingMerge parks a starting|running Run into awaiting_human_merge with
// the validated checkpoint, a merge-poll timer, and reset failure/remediation
// counters. It reloads the Run so the transition is derived from fresh durable
// state and returns whether the park succeeded.
func (m *Manager) markAwaitingMerge(runID string, checkpoint ReadyCheckpoint, attempts int) bool {
	current, ok := m.reload(runID)
	if !ok || (current.State != StateStarting && current.State != StateRunning) {
		return false
	}
	err := m.transition(current, StateAwaitingHumanMerge, func(next *Run, at time.Time) {
		parked := checkpoint
		parked.ValidatedAt = at
		next.Ready = &parked
		reconcile := at.Add(m.mergeInterval)
		next.GitHub.NextReconcileAt = &reconcile
		refreshed := at
		next.GitHub.LastAuthoritativeRefreshAt = &refreshed
		next.GitHub.ReconcileFailures = 0
		next.GitHub.RemediationRequested = false
		if attempts > next.Attempts {
			next.Attempts = attempts
		}
		next.Detail = "waiting for human merge"
		next.TerminalIntent = ""
		next.TerminalRejection = ""
	})
	if err != nil {
		m.logger.Error("park run", "run_id", runID, "error", err)
		return false
	}
	return true
}

// resumeAwaiting resumes a parked Run to pending (a GitHub change) or
// post_merge_pending (merge or close). It keeps the deterministic SessionName the
// store treats as immutable, retains the ready checkpoint, increments the resume
// count, records the merge commit when present, and clears the reconcile timer so
// the Run is immediately eligible to start again.
func (m *Manager) resumeAwaiting(runID, kind, mergeCommitOID, detail string) {
	current, ok := m.reload(runID)
	if !ok || current.State != StateAwaitingHumanMerge {
		return
	}
	target := StatePending
	if kind == triggerKindPostMerge {
		target = StatePostMergePending
	}
	if err := m.transition(current, target, func(next *Run, at time.Time) {
		next.TriggerKind = kind
		if mergeCommitOID != "" {
			next.MergeCommitOID = mergeCommitOID
		}
		next.GitHub.NextReconcileAt = nil
		refreshed := at
		next.GitHub.LastAuthoritativeRefreshAt = &refreshed
		next.GitHub.ReconcileFailures = 0
		next.GitHub.RemediationRequested = false
		next.ResumeCount = current.ResumeCount + 1
		next.Detail = truncateText(detail, maximumTextBytes)
	}); err != nil {
		m.logger.Error("resume awaiting run", "run_id", runID, "error", err)
	}
}

// repark returns a still-parkable Run (one that still holds its ready checkpoint)
// to awaiting_human_merge after a validator rejected its terminal intent. The
// ready checkpoint is unchanged; a durable unaccepted completion preserves the
// rejection evidence and a resume-count backoff timer defers the next poll. A
// later accepted terminal may overwrite the unaccepted completion.
func (m *Manager) repark(run Run, result ProcessResult, decision TerminalDecision) {
	current, ok := m.reload(run.ID)
	if !ok || (current.State != StateStarting && current.State != StateRunning) || current.Ready == nil {
		return
	}
	if err := m.transition(current, StateAwaitingHumanMerge, func(next *Run, at time.Time) {
		// next.Ready is the unchanged current checkpoint (cloned into next), so the
		// store's Ready-replacement rule sees a DeepEqual checkpoint.
		reconcile := at.Add(reconcileDelay(m.mergeInterval, current.ResumeCount))
		next.GitHub.NextReconcileAt = &reconcile
		refreshed := at
		next.GitHub.LastAuthoritativeRefreshAt = &refreshed
		next.GitHub.RemediationRequested = false
		if result.Attempts > next.Attempts {
			next.Attempts = result.Attempts
		}
		validation := decision.Validation
		next.TerminalIntent = truncateText(validation.Intent, 256)
		next.TerminalRejection = truncateText(validation.Reason, maximumTextBytes)
		next.Detail = truncateText("terminal intent rejected: "+validation.Reason, maximumTextBytes)
		next.Completion = reparkCompletion(validation, current.Ready.VerifiedHeadOID, at)
	}); err != nil {
		m.logger.Error("repark rejected terminal", "run_id", run.ID, "error", err)
	}
}

// reparkCompletion builds the durable unaccepted completion for a re-parked Run.
// The Run holds a ready checkpoint but is not merged, so merge identity is
// dropped and a pull request head is retained only when it matches the verified
// checkpoint head; this keeps the completion within the store's cross-field
// rules while preserving the rejection intent and reason.
func reparkCompletion(validation CompletionValidation, verifiedHeadOID string, at time.Time) *CompletionValidation {
	completion := CompletionValidation{
		Accepted:         false,
		Intent:           validation.Intent,
		Blocker:          validation.Blocker,
		State:            StateFailed,
		Reason:           validation.Reason,
		ValidatedAt:      at,
		PullRequestState: validation.PullRequestState,
	}
	if validation.PullRequestHead == verifiedHeadOID {
		completion.PullRequestHead = validation.PullRequestHead
	}
	return &completion
}

// deferReadyValidation records a same-state backoff wake for a worker whose
// ready checkpoint could not be authoritatively refreshed. It stays in the
// starting|running state and increments the failure count.
func (m *Manager) deferReadyValidation(run Run, detail string) {
	current, ok := m.reload(run.ID)
	if !ok || (current.State != StateStarting && current.State != StateRunning) {
		return
	}
	at := m.advance(current.UpdatedAt)
	reconcile := at.Add(reconcileDelay(m.pollInterval, current.GitHub.ReconcileFailures))
	bounded := truncateText(detail, maximumTextBytes)
	if err := m.store.ScheduleReconcile(ReconcileSchedule{
		RunID: current.ID, ExpectedUpdatedAt: current.UpdatedAt, At: at, NextReconcileAt: reconcile,
		FailureMode: ReconcileFailuresIncrement, AuthoritativeRefresh: true, Detail: &bounded,
	}); err != nil {
		m.logger.Error("defer ready checkpoint validation", "run_id", run.ID, "error", err)
	}
}

// deferMergeReconcile records a same-state merge-poll wake for a parked Run. A
// failed poll increments the failure count and backs off exponentially; an
// open-and-waiting poll resets the count and uses the flat merge interval.
func (m *Manager) deferMergeReconcile(run Run, detail string, failed bool) {
	current, ok := m.reload(run.ID)
	if !ok || current.State != StateAwaitingHumanMerge {
		return
	}
	at := m.advance(current.UpdatedAt)
	mode := ReconcileFailuresReset
	reconcile := at.Add(m.mergeInterval)
	if failed {
		mode = ReconcileFailuresIncrement
		reconcile = at.Add(reconcileDelay(m.mergeInterval, current.GitHub.ReconcileFailures))
	}
	bounded := truncateText(detail, maximumTextBytes)
	if err := m.store.ScheduleReconcile(ReconcileSchedule{
		RunID: current.ID, ExpectedUpdatedAt: current.UpdatedAt, At: at, NextReconcileAt: reconcile,
		FailureMode: mode, AuthoritativeRefresh: true, Detail: &bounded,
	}); err != nil {
		m.logger.Error("defer merge reconciliation", "run_id", run.ID, "error", err)
	}
}

// finishInvalidReady fails a Run whose ready checkpoint could not be validated,
// mirroring the legacy invalid-checkpoint finish.
func (m *Manager) finishInvalidReady(run Run, result ProcessResult, cause error) {
	m.finishActive(run.ID, StateFailed, "invalid ready checkpoint: "+cause.Error(), result.Attempts, nil)
}

// finishAwaitingWithoutReady fails a parked Run that lost its ready checkpoint.
// This is a defensive fail-closed path: canonical parking always sets Ready.
func (m *Manager) finishAwaitingWithoutReady(run Run) {
	current, ok := m.reload(run.ID)
	if !ok || current.State != StateAwaitingHumanMerge {
		return
	}
	if err := m.transition(current, StateFailed, func(next *Run, at time.Time) {
		next.Detail = "awaiting merge without a ready checkpoint"
		finished := at
		next.FinishedAt = &finished
	}); err != nil {
		m.logger.Error("finish invalid awaiting run", "run_id", run.ID, "error", err)
	}
}

// validateCheckpoint fail-closes a worker's ready checkpoint against the Run's
// immutable task and repository route before it is parked. It reuses the store's
// authoritative validateReady against a prospective awaiting projection, so the
// manager rejects exactly what the store would reject on the park transition.
func (m *Manager) validateCheckpoint(run Run, checkpoint ReadyCheckpoint) error {
	if run.Repository == nil {
		return errors.New("run has no repository route")
	}
	if run.SegmentStartedAt == nil || checkpoint.CreatedAt.Before(*run.SegmentStartedAt) {
		return errors.New("checkpoint predates the current lifecycle segment")
	}
	prospective := run.Clone()
	prospective.State = StateAwaitingHumanMerge
	prospective.Ready = &checkpoint
	return validateReady(checkpoint, prospective, false)
}

// validateReadySnapshot proves an open pull request still matches the verified
// checkpoint before parking. It mirrors legacy agentrun.validateReadySnapshot.
func validateReadySnapshot(checkpoint ReadyCheckpoint, snapshot PullRequestSnapshot) error {
	if snapshot.State != "OPEN" {
		return fmt.Errorf("pull request state is %q, want OPEN", snapshot.State)
	}
	if snapshot.IsDraft {
		return errors.New("pull request is still a draft")
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
