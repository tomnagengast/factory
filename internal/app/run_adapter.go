package app

import (
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

// RunAdapter exposes read-only retained projections and canonical GitHub wake
// scheduling over the one Runs journal. Legacy Claim entrypoints fail closed;
// live composition must use the canonical event/native admission APIs.
type RunAdapter struct {
	store    *runs.Store
	registry func() triggerregistry.Snapshot
	notify   func()
}

func NewRunAdapter(store *runs.Store, registry func() triggerregistry.Snapshot, notify func()) (*RunAdapter, error) {
	if store == nil || registry == nil || notify == nil {
		return nil, errors.New("app run adapter: store, registry projection, and notifier are required")
	}
	return &RunAdapter{store: store, registry: registry, notify: notify}, nil
}

func (a *RunAdapter) Notify() { a.notify() }

func (a *RunAdapter) Claim(agentrun.Trigger, time.Time) (agentrun.Run, bool, error) {
	return agentrun.Run{}, false, errors.New("app run adapter: legacy direct admission is disabled")
}

func (a *RunAdapter) ClaimContinuation(claim agentrun.ContinuationClaim, _ time.Time) (agentrun.Run, bool, error) {
	task, err := taskmodel.ResolveCompatibilityIdentity(claim.Trigger.Task, claim.Trigger.IssueIdentifier)
	if err != nil {
		return agentrun.Run{}, false, err
	}
	eventID := "linear:" + claim.Trigger.DeliveryID
	model, ok := a.model()
	if !ok {
		return agentrun.Run{}, false, errors.New("app run adapter: canonical Runs are unavailable")
	}
	for _, candidate := range model.Runs {
		if candidate.Causation.EventID != eventID || candidate.Causation.EventSource != eventwire.SourceLinear || candidate.TriggerKind != "comment" ||
			!candidate.Causation.Task.Equal(task) {
			continue
		}
		if candidate.Causation.ParentRunID != "" {
			for _, owner := range model.Runs {
				if owner.ID == candidate.Causation.ParentRunID {
					return legacyRun(owner), false, nil
				}
			}
			return agentrun.Run{}, false, errors.New("app run adapter: canonical continuation owner is unavailable")
		}
		return legacyRun(candidate), false, nil
	}
	return agentrun.Run{}, false, errors.New("app run adapter: canonical Linear continuation was not admitted")
}

func (a *RunAdapter) PublicSnapshot() agentrun.PublicSnapshot {
	model, ok := a.model()
	if !ok {
		return agentrun.PublicSnapshot{Runs: []agentrun.PublicRun{}}
	}
	values := newestRuns(model.Runs)
	result := agentrun.PublicSnapshot{Total: model.TotalRuns, Runs: make([]agentrun.PublicRun, len(values))}
	for index, run := range values {
		state := legacyRunState(run.State)
		if state.Nonterminal() {
			result.Active++
		}
		result.Runs[index] = agentrun.PublicRun{
			ID: run.ID, State: state, Attempts: run.Attempts, DuplicateTriggers: run.DuplicateDeliveries,
			CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, StartedAt: cloneTime(run.StartedAt), FinishedAt: cloneTime(run.FinishedAt),
		}
	}
	return result
}

func (a *RunAdapter) ActivitySnapshot() agentrun.ActivitySnapshot {
	model, ok := a.model()
	if !ok {
		return agentrun.ActivitySnapshot{Runs: []agentrun.ActivityRun{}}
	}
	values := newestRuns(model.Runs)
	result := agentrun.ActivitySnapshot{Total: model.TotalRuns, Runs: make([]agentrun.ActivityRun, len(values))}
	for index, run := range values {
		legacy := legacyRun(run)
		if legacy.State.Nonterminal() {
			result.Active++
		}
		result.Runs[index] = agentrun.ActivityRun{
			ID: legacy.ID, Task: legacy.Task, IssueIdentifier: legacy.IssueIdentifier,
			State: legacy.State, Attempts: legacy.Attempts, DuplicateTriggers: legacy.DuplicateTriggers,
			CreatedAt: legacy.CreatedAt, UpdatedAt: legacy.UpdatedAt, StartedAt: legacy.StartedAt, FinishedAt: legacy.FinishedAt,
			Ready: legacy.Ready, MergeCommitOID: legacy.MergeCommitOID, LastGitHubCursor: legacy.LastGitHubCursor,
			LastAuthoritativeRefreshAt: legacy.LastAuthoritativeRefreshAt, NextReconcileAt: legacy.NextReconcileAt,
			ReconcileFailures: legacy.ReconcileFailures, ResumeCount: legacy.ResumeCount,
			TerminalRejection: legacy.TerminalRejection, Completion: legacy.Completion,
		}
	}
	return result
}

func (a *RunAdapter) Find(id string) (agentrun.Run, bool) {
	model, ok := a.model()
	if !ok {
		return agentrun.Run{}, false
	}
	for _, run := range model.Runs {
		if run.ID == id {
			return legacyRun(run), true
		}
	}
	return agentrun.Run{}, false
}

func (a *RunAdapter) FindObserverRun(source taskmodel.Source, taskIdentifier string, startedUnixMilli int64) (agentrun.Run, bool) {
	model, ok := a.model()
	if !ok {
		return agentrun.Run{}, false
	}
	for _, run := range model.Runs {
		if run.Causation.Task.Source != source || run.Causation.Task.Identifier != taskIdentifier || run.StartedAt == nil || run.StartedAt.UnixMilli() != startedUnixMilli {
			continue
		}
		return legacyRun(run), true
	}
	return agentrun.Run{}, false
}

func (a *RunAdapter) SchedulePullRequestReconcile(repository string, pullRequest int, headBranch, deliveryID string, cursor uint64, remediation bool, now time.Time) (bool, error) {
	scheduled, err := a.store.SchedulePullRequestReconcile(repository, pullRequest, headBranch, deliveryID, cursor, remediation, now)
	if scheduled {
		a.notify()
	}
	return scheduled, err
}

func (a *RunAdapter) RoutingSnapshot() triggerrouter.Snapshot {
	model, ok := a.model()
	if !ok {
		return triggerrouter.Snapshot{Schema: triggerrouter.SchemaVersion, Decisions: []triggerrouter.Decision{}, Invocations: []triggerrouter.Invocation{}, RateBuckets: []triggerrouter.RateBucket{}}
	}
	rules := make(map[string]triggerregistry.Rule)
	for _, rule := range a.registry().Rules {
		rules[rule.ID] = rule
	}
	decisions := make([]triggerrouter.Decision, len(model.AdmissionBatches))
	for index, batch := range model.AdmissionBatches {
		outcomes := make([]triggerrouter.Outcome, len(batch.Outcomes))
		for outcomeIndex, outcome := range batch.Outcomes {
			kind := triggerrouter.OutcomeInvocation
			if outcome.Kind == runs.AdmissionOutcomeRejected {
				kind = triggerrouter.OutcomeRejected
			} else if outcome.Kind == runs.AdmissionOutcomeSuppressed {
				kind = triggerrouter.OutcomeSuppressed
			}
			outcomes[outcomeIndex] = triggerrouter.Outcome{
				Kind: kind, RuleID: outcome.RuleID, RuleRevision: outcome.RuleRevision,
				InvocationID: outcome.AdmissionID, Reason: outcome.Reason,
			}
		}
		decisions[index] = triggerrouter.Decision{
			EventID: batch.EventID, EventSequence: batch.EventSequence, Source: batch.EventSource,
			RegistryRevision: batch.RegistryRevision, SettingsRevision: batch.SettingsRevision,
			DecidedAt: batch.DecidedAt, Outcomes: outcomes,
		}
	}
	invocations := make([]triggerrouter.Invocation, len(model.Runs))
	for index, run := range model.Runs {
		rule := rules[run.Causation.RuleID]
		if rule.ID == "" {
			rule = legacyRule(run)
		}
		invocations[index] = legacyInvocation(run, rule)
	}
	rates := make([]triggerrouter.RateBucket, len(model.RateBuckets))
	for index, rate := range model.RateBuckets {
		rates[index] = triggerrouter.RateBucket{RuleID: rate.RuleID, Minute: rate.Minute, Count: rate.Count}
	}
	return triggerrouter.Snapshot{Schema: triggerrouter.SchemaVersion, Decisions: decisions, Invocations: invocations, RateBuckets: rates}
}

func (a *RunAdapter) model() (runs.Model, bool) {
	snapshot, err := a.store.Snapshot()
	if err != nil {
		return runs.Model{}, false
	}
	return snapshot.Model(), true
}

func legacyRun(value runs.Run) agentrun.Run {
	result := agentrun.Run{
		ID: value.ID, Task: value.Causation.Task, IssueIdentifier: value.Causation.Task.Identifier,
		TriggerKind: value.TriggerKind, DeliveryIDs: slices.Clone(value.DeliveryIDs), State: legacyRunState(value.State),
		SessionName: value.SessionName, RunDirectory: value.RunDirectory, Attempts: value.Attempts,
		DuplicateTriggers: value.DuplicateDeliveries, Detail: value.Detail, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		StartedAt: cloneTime(value.StartedAt), SegmentStartedAt: cloneTime(value.SegmentStartedAt), SegmentAttempt: value.SegmentAttempt,
		FinishedAt: cloneTime(value.FinishedAt), MergeCommitOID: value.MergeCommitOID,
		LastGitHubCursor: value.GitHub.LastCursor, LastAuthoritativeRefreshAt: cloneTime(value.GitHub.LastAuthoritativeRefreshAt),
		NextReconcileAt: cloneTime(value.GitHub.NextReconcileAt), ReconcileFailures: value.GitHub.ReconcileFailures,
		RemediationRequested: value.GitHub.RemediationRequested, ResumeCount: value.ResumeCount,
		TerminalIntent: value.TerminalIntent, TerminalRejection: value.TerminalRejection,
		InvocationID: value.Causation.AdmissionID, InvocationRootEventID: value.Causation.RootEventID,
		InvocationHop: value.Causation.Hop, InvocationAncestorRuleIDs: slices.Clone(value.Causation.AncestorRuleIDs),
		PinnedWorkflow: cloneWorkflow(value.Causation.Workflow), PinnedWorkflowDigest: value.Causation.WorkflowDigest,
		PinnedPolicyRevision: value.Causation.PolicyRevision,
	}
	if value.Repository != nil {
		result.Repository = value.Repository.Repository
		result.RepositoryURL = value.Repository.Origin
		result.RepositoryPath = value.Repository.ManagedPath
		result.ManagedRoot = value.Repository.ManagedRoot
		result.BaseBranch = value.Repository.DefaultBranch
		result.Bootstrap = value.Repository.Bootstrap
		result.CloudURL = value.Repository.CloudURL
	}
	result.Transitions = make([]agentrun.Transition, len(value.Transitions))
	for index, transition := range value.Transitions {
		result.Transitions[index] = agentrun.Transition{ID: transition.ID, State: legacyRunState(transition.State), Attempts: transition.Attempts, At: transition.At}
	}
	if value.Ready != nil {
		result.Ready = &agentrun.ReadyCheckpoint{
			ContractVersion: value.Ready.ContractVersion, RunID: value.Ready.RunID, Task: value.Ready.Task,
			Repository: value.Ready.Repository, PullRequest: value.Ready.PullRequest, BaseBranch: value.Ready.BaseBranch,
			HeadBranch: value.Ready.HeadBranch, VerifiedHeadOID: value.Ready.VerifiedHeadOID,
			PullRequestUpdatedAt: value.Ready.PullRequestUpdatedAt, CreatedAt: value.Ready.CreatedAt, ValidatedAt: value.Ready.ValidatedAt,
		}
	}
	if value.Completion != nil {
		result.Completion = &agentrun.CompletionValidation{
			Accepted: value.Completion.Accepted, Intent: value.Completion.Intent, Blocker: value.Completion.Blocker,
			State: legacyRunState(value.Completion.State), Reason: value.Completion.Reason, ValidatedAt: value.Completion.ValidatedAt,
			PullRequestState: value.Completion.PullRequestState, PullRequestHead: value.Completion.PullRequestHead,
			MergeCommitOID: value.Completion.MergeCommitOID, DeploymentID: value.Completion.DeploymentID,
			DeploymentCommit: value.Completion.DeploymentCommit,
		}
	}
	return result
}

func legacyRunState(state runs.LifecycleState) agentrun.State {
	switch state {
	case runs.StateStarting:
		return agentrun.StateStarting
	case runs.StateRunning:
		return agentrun.StateRunning
	case runs.StateAwaitingHumanMerge:
		return agentrun.StateAwaitingMerge
	case runs.StatePostMergePending:
		return agentrun.StatePostMergePending
	case runs.StateSucceeded:
		return agentrun.StateSucceeded
	case runs.StateBlocked:
		return agentrun.StateBlocked
	case runs.StateFailed, runs.StateRejected:
		return agentrun.StateFailed
	default:
		return agentrun.StatePending
	}
}

func legacyInvocationState(state runs.LifecycleState) string {
	switch state {
	case runs.StateAdmitted:
		return triggerrouter.StateQueued
	case runs.StateRouting:
		return triggerrouter.StateClaiming
	case runs.StateSucceeded:
		return triggerrouter.StateSucceeded
	case runs.StateBlocked:
		return triggerrouter.StateBlocked
	case runs.StateFailed:
		return triggerrouter.StateFailed
	case runs.StateRejected:
		return triggerrouter.StateRejected
	default:
		return triggerrouter.StateClaimed
	}
}

func newestRuns(values []runs.Run) []runs.Run {
	result := slices.Clone(values)
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.After(result[j].CreatedAt)
		}
		return result[i].ID > result[j].ID
	})
	return result
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneWorkflow(value *workflow.Pinned) *workflow.Pinned {
	if value == nil {
		return nil
	}
	copy := value.Clone()
	return &copy
}

func workflowPin(run runs.Run) workflow.Pinned {
	if run.Causation.Workflow == nil {
		return workflow.Pinned{}
	}
	return run.Causation.Workflow.Clone()
}

func legacyRule(run runs.Run) triggerregistry.Rule {
	return triggerregistry.Rule{
		ID: run.Causation.RuleID, Revision: run.Causation.RuleRevision, Name: run.Causation.RuleID, Enabled: true,
		WorkflowID: workflowPin(run).ID,
		Target:     triggerregistry.TargetPolicy{Provider: run.Causation.Task.Source, Kind: triggerregistry.TargetEventSubject},
		MaxHop:     triggerregistry.DefaultMaxHop, MaxOutstanding: triggerregistry.DefaultMaxOutstanding,
		AdmissionsHour: triggerregistry.DefaultAdmissionsHour,
	}
}

func legacyInvocation(run runs.Run, rule triggerregistry.Rule) triggerrouter.Invocation {
	return triggerrouter.Invocation{
		ID: run.Causation.AdmissionID, EventID: run.Causation.EventID, EventSequence: run.Causation.EventSequence,
		Rule: rule, Workflow: workflowPin(run), WorkflowDigest: run.Causation.WorkflowDigest,
		PolicyRevision: run.Causation.PolicyRevision, Task: run.Causation.Task,
		IssueIdentifier: run.Causation.Task.Identifier, RootEventID: run.Causation.RootEventID,
		ParentInvocationID: run.Causation.ParentAdmissionID, ParentRunID: run.Causation.ParentRunID,
		Hop: run.Causation.Hop, AncestorRuleIDs: slices.Clone(run.Causation.AncestorRuleIDs),
		State: legacyInvocationState(run.State), RunID: run.ID, Reason: run.Detail,
		AdmittedAt: run.Causation.AdmittedAt, UpdatedAt: run.UpdatedAt, ReflectedAt: cloneTime(run.FinishedAt),
	}
}
