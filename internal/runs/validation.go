package runs

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/workflow"
)

const (
	readyContractVersion = 1
	maximumHop           = 32
	maximumTextBytes     = 4096
)

var (
	ruleIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	repositoryPattern = regexp.MustCompile(`^[a-z0-9_.-]+/[a-z0-9_.-]+$`)
	projectIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	branchPattern     = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	cloudLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	gitOIDPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func validateModel(model Model) error {
	if model.Schema != SchemaVersion {
		return fmt.Errorf("runs: schema is %d, want %d", model.Schema, SchemaVersion)
	}
	if model.TotalBatches < uint64(len(model.AdmissionBatches)) {
		return errors.New("runs: total admission batches is below the retained count")
	}
	if model.TotalRuns < uint64(len(model.Runs)) {
		return errors.New("runs: total Runs is below the retained count")
	}
	if !canonicalBatchOrder(model.AdmissionBatches) || !canonicalRunOrder(model.Runs) || !canonicalRateOrder(model.RateBuckets) {
		return errors.New("runs: projection ordering is not canonical")
	}

	batches := make(map[string]AdmissionBatch, len(model.AdmissionBatches))
	events := make(map[string]string, len(model.AdmissionBatches))
	sequences := make(map[uint64]string, len(model.AdmissionBatches))
	runnable := make(map[string]AdmissionOutcome)
	runOutcome := make(map[string]string)
	for index, batch := range model.AdmissionBatches {
		if err := validateAdmissionBatch(batch); err != nil {
			return fmt.Errorf("runs: admission batch %d: %w", index+1, err)
		}
		if _, duplicate := batches[batch.ID]; duplicate {
			return fmt.Errorf("runs: admission batch ID %q is duplicated", batch.ID)
		}
		batches[batch.ID] = batch
		if previous, duplicate := events[batch.EventID]; duplicate {
			return fmt.Errorf("runs: event ID %q belongs to admission batches %q and %q", batch.EventID, previous, batch.ID)
		}
		events[batch.EventID] = batch.ID
		if batch.EventSequence != 0 {
			if previous, duplicate := sequences[batch.EventSequence]; duplicate {
				return fmt.Errorf("runs: event sequence %d belongs to admission batches %q and %q", batch.EventSequence, previous, batch.ID)
			}
			sequences[batch.EventSequence] = batch.ID
		}
		for _, outcome := range batch.Outcomes {
			if outcome.Kind != AdmissionOutcomeRun {
				continue
			}
			if _, duplicate := runnable[outcome.AdmissionID]; duplicate {
				return fmt.Errorf("runs: admission ID %q is duplicated", outcome.AdmissionID)
			}
			if _, duplicate := runOutcome[outcome.RunID]; duplicate {
				return fmt.Errorf("runs: Run ID %q is referenced by multiple outcomes", outcome.RunID)
			}
			runnable[outcome.AdmissionID] = outcome
			runOutcome[outcome.RunID] = batch.ID
		}
	}

	seenRuns := make(map[string]bool, len(model.Runs))
	seenAdmissions := make(map[string]bool, len(model.Runs))
	for index, run := range model.Runs {
		if err := validateRun(run); err != nil {
			return fmt.Errorf("runs: Run %d: %w", index+1, err)
		}
		if seenRuns[run.ID] {
			return fmt.Errorf("runs: Run ID %q is duplicated", run.ID)
		}
		seenRuns[run.ID] = true
		if seenAdmissions[run.Causation.AdmissionID] {
			return fmt.Errorf("runs: admission ID %q owns multiple Runs", run.Causation.AdmissionID)
		}
		seenAdmissions[run.Causation.AdmissionID] = true
		batch, found := batches[run.Causation.BatchID]
		outcome, linked := runnable[run.Causation.AdmissionID]
		if !found || !linked || outcome.RunID != run.ID || runOutcome[run.ID] != batch.ID ||
			run.Causation.EventID != batch.EventID || run.Causation.EventSequence != batch.EventSequence ||
			run.Causation.EventSource != batch.EventSource || run.Causation.PolicyGeneration != batch.PolicyGeneration ||
			run.Causation.PolicyRevision != batch.SettingsRevision ||
			outcome.RuleID != run.Causation.RuleID || outcome.RuleRevision != run.Causation.RuleRevision ||
			run.Causation.AdmittedAt.Before(batch.DecidedAt) {
			return fmt.Errorf("runs: Run %q admission linkage conflicts", run.ID)
		}
	}
	if len(seenRuns) != len(runnable) {
		return errors.New("runs: runnable admission outcome is orphaned")
	}
	if err := validateTaskOwnership(model.Runs); err != nil {
		return err
	}

	seenRates := make(map[string]bool, len(model.RateBuckets))
	for _, bucket := range model.RateBuckets {
		if !ruleIDPattern.MatchString(bucket.RuleID) || bucket.Minute.IsZero() || bucket.Minute != bucket.Minute.UTC().Truncate(time.Minute) || bucket.Count < 1 {
			return errors.New("runs: invalid rate bucket")
		}
		key := rateKey(bucket.RuleID, bucket.Minute)
		if seenRates[key] {
			return errors.New("runs: duplicate rate bucket")
		}
		seenRates[key] = true
	}
	return nil
}

func validateAdmissionBatch(batch AdmissionBatch) error {
	if !validIdentity(batch.ID) || !validIdentity(batch.EventID) || !validSource(batch.EventSource) || batch.DecidedAt.IsZero() || batch.DecidedAt.Location() != time.UTC {
		return errors.New("admission identity is invalid")
	}
	switch batch.Origin {
	case AdmissionOriginEvent:
		if batch.EventSequence == 0 {
			return errors.New("event admission requires a source sequence")
		}
	case AdmissionOriginNative, AdmissionOriginContinuation, AdmissionOriginMigratedDirect:
		if batch.EventSequence != 0 {
			return errors.New("non-event admission cannot invent a source sequence")
		}
	default:
		return fmt.Errorf("unsupported admission origin %q", batch.Origin)
	}
	seenRules := make(map[string]bool, len(batch.Outcomes))
	for _, outcome := range batch.Outcomes {
		if !ruleIDPattern.MatchString(outcome.RuleID) || outcome.RuleRevision == 0 || seenRules[outcome.RuleID] {
			return errors.New("admission outcome rule identity is invalid")
		}
		seenRules[outcome.RuleID] = true
		switch outcome.Kind {
		case AdmissionOutcomeRun:
			if !validIdentity(outcome.AdmissionID) || !validIdentity(outcome.RunID) || outcome.Reason != "" {
				return errors.New("runnable admission outcome is invalid")
			}
		case AdmissionOutcomeRejected, AdmissionOutcomeSuppressed:
			if outcome.AdmissionID != "" || outcome.RunID != "" || !validText(outcome.Reason, maximumTextBytes) {
				return errors.New("non-runnable admission outcome is invalid")
			}
		default:
			return fmt.Errorf("unsupported admission outcome %q", outcome.Kind)
		}
	}
	return nil
}

func validateRun(run Run) error {
	if !validIdentity(run.ID) || !validText(run.TriggerKind, 128) || run.Attempts < 0 || run.SegmentAttempt < 0 || run.SegmentAttempt > run.Attempts ||
		run.ResumeCount < 0 || run.GitHub.ReconcileFailures < 0 || !validOptionalText(run.Detail, maximumTextBytes) ||
		!validOptionalText(run.RepositoryRejection, maximumTextBytes) || !validOptionalText(run.TerminalIntent, 256) ||
		!validOptionalText(run.TerminalRejection, maximumTextBytes) {
		return errors.New("Run lifecycle metadata is invalid")
	}
	if err := validateMigratedBaseline(run); err != nil {
		return err
	}
	if err := validateCausation(run.Causation, run.State, run.MigratedBaseline); err != nil {
		return err
	}
	if !validLifecycleState(run.State) {
		return fmt.Errorf("unsupported lifecycle state %q", run.State)
	}
	if len(run.DeliveryIDs) == 0 || run.DuplicateDeliveries != uint64(len(run.DeliveryIDs)-1) || !sortStringsUnique(run.DeliveryIDs) {
		return errors.New("Run delivery identities are invalid")
	}
	for _, deliveryID := range run.DeliveryIDs {
		if !validIdentity(deliveryID) {
			return errors.New("Run delivery identity is invalid")
		}
	}
	if run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() || run.CreatedAt.Location() != time.UTC || run.UpdatedAt.Location() != time.UTC ||
		run.CreatedAt.Before(run.Causation.AdmittedAt) || run.UpdatedAt.Before(run.CreatedAt) {
		return errors.New("Run lifecycle timestamps are invalid")
	}
	for _, value := range []*time.Time{run.StartedAt, run.SegmentStartedAt, run.FinishedAt, run.GitHub.LastAuthoritativeRefreshAt, run.GitHub.NextReconcileAt} {
		if value != nil && (value.IsZero() || value.Location() != time.UTC || value.Before(run.CreatedAt)) {
			return errors.New("Run optional timestamp is invalid")
		}
	}
	if run.State.Terminal() != (run.FinishedAt != nil) {
		return errors.New("Run terminal state and finish time conflict")
	}
	if run.StartedAt != nil && run.FinishedAt != nil && run.FinishedAt.Before(*run.StartedAt) {
		return errors.New("Run finished before it started")
	}
	if run.SessionName != "" && !validText(run.SessionName, 256) {
		return errors.New("Run session name is invalid")
	}
	if run.RunDirectory != "" && !canonicalAbsolutePath(run.RunDirectory) {
		return errors.New("Run directory is not a canonical absolute path")
	}
	if (run.State == StateStarting || run.State == StateRunning) && (run.SessionName == "" || run.RunDirectory == "" || run.SegmentStartedAt == nil) {
		return errors.New("worker lifecycle requires session and segment identity")
	}
	if run.Repository == nil {
		if run.State != StateAdmitted && run.State != StateRouting && run.State != StateRejected {
			if (run.MigratedBaseline == nil || !run.State.Terminal()) && !acceptedPrePullRequestBlocker(run) {
				return errors.New("runnable lifecycle is missing a repository route")
			}
		}
	} else if err := validateRoute(*run.Repository); err != nil {
		return err
	}
	if run.RepositoryRejection != "" && run.State != StateRejected {
		return errors.New("repository rejection is only valid on a rejected Run")
	}
	if run.State == StateRejected && run.Repository == nil && run.RepositoryRejection == "" && run.Detail == "" {
		return errors.New("rejected Run requires a durable reason")
	}
	if run.MergeCommitOID != "" && !gitOIDPattern.MatchString(run.MergeCommitOID) {
		return errors.New("Run merge commit is invalid")
	}
	if err := validateTransitions(run); err != nil {
		return err
	}
	if run.Ready != nil {
		if err := validateReady(*run.Ready, run); err != nil {
			return err
		}
	}
	if run.Completion != nil {
		if err := validateCompletion(*run.Completion, run); err != nil {
			return err
		}
		if run.Completion.ValidatedAt.Before(run.CreatedAt) || run.Completion.ValidatedAt.After(run.UpdatedAt) {
			return errors.New("Run completion timestamp conflicts with the Run")
		}
	}
	return nil
}

func validateCausation(c Causation, state LifecycleState, baseline *MigratedBaseline) error {
	if !validIdentity(c.AdmissionID) || !validIdentity(c.BatchID) || !validIdentity(c.EventID) || !validSource(c.EventSource) ||
		!ruleIDPattern.MatchString(c.RuleID) || c.RuleRevision == 0 || c.Task.Validate() != nil || !validIdentity(c.RootEventID) ||
		c.Hop < 0 || c.Hop > maximumHop || len(c.AncestorRuleIDs) != c.Hop || c.AdmittedAt.IsZero() || c.AdmittedAt.Location() != time.UTC {
		return errors.New("Run causation identity is invalid")
	}
	if c.ParentAdmissionID != "" && !validIdentity(c.ParentAdmissionID) || c.ParentRunID != "" && !validIdentity(c.ParentRunID) {
		return errors.New("Run parent causation is invalid")
	}
	seen := make(map[string]bool, len(c.AncestorRuleIDs))
	for _, ruleID := range c.AncestorRuleIDs {
		if !ruleIDPattern.MatchString(ruleID) || seen[ruleID] {
			return errors.New("Run ancestor path is invalid")
		}
		seen[ruleID] = true
	}
	if c.Hop > 0 && c.AncestorRuleIDs[len(c.AncestorRuleIDs)-1] != c.RuleID {
		return errors.New("Run ancestor path does not end at its admitting rule")
	}
	if c.Workflow == nil {
		if c.WorkflowDigest != "" || state.Nonterminal() || baseline == nil || !baseline.WorkflowPinUnavailable {
			return errors.New("nonterminal Run requires an immutable workflow pin")
		}
		return nil
	}
	if compactWorkflow(*c.Workflow) {
		if state.Nonterminal() {
			return errors.New("nonterminal Run cannot use a compacted workflow pin")
		}
		if c.WorkflowDigest == "" {
			if baseline == nil || !baseline.WorkflowDigestUnavailable {
				return errors.New("Run compacted workflow digest is unavailable without migrated evidence")
			}
			return nil
		}
		if !digestPattern.MatchString(c.WorkflowDigest) {
			return errors.New("Run workflow digest is invalid")
		}
		return nil
	}
	if !digestPattern.MatchString(c.WorkflowDigest) {
		return errors.New("Run workflow digest is invalid")
	}
	if err := c.Workflow.Validate(); err != nil || !c.Workflow.Enabled || !c.Workflow.Complete() {
		return errors.New("Run workflow pin is invalid")
	}
	digest, err := c.Workflow.Digest()
	if err != nil || digest != c.WorkflowDigest {
		return errors.New("Run workflow pin conflicts with its digest")
	}
	return nil
}

func compactWorkflow(pin workflow.Pinned) bool {
	return workflow.ValidID(pin.ID) && pin.Name == "" && !pin.Enabled && pin.Markdown == "" && pin.UpdatedAt == nil && pin.Runner == "" && len(pin.Steps) == 0
}

func validateTransitions(run Run) error {
	baseline := run.MigratedBaseline
	if len(run.Transitions) == 0 {
		if baseline == nil || baseline.State != run.State || baseline.ObservedAt != run.UpdatedAt {
			return errors.New("Run transition history is incomplete")
		}
		return nil
	}
	if run.Transitions[len(run.Transitions)-1].State != run.State {
		return errors.New("Run transition history is incomplete")
	}
	if baseline == nil && run.Transitions[0].At != run.CreatedAt {
		return errors.New("Run transition history is incomplete")
	}
	seen := make(map[string]bool, len(run.Transitions))
	previousAttempts := -1
	for index, transition := range run.Transitions {
		if !validIdentity(transition.ID) || seen[transition.ID] || !validLifecycleState(transition.State) || transition.Attempts < previousAttempts ||
			transition.Attempts < 0 || transition.Attempts > run.Attempts || transition.At.IsZero() || transition.At.Location() != time.UTC ||
			transition.At.Before(run.CreatedAt) || transition.At.After(run.UpdatedAt) {
			return errors.New("Run transition history is invalid")
		}
		seen[transition.ID] = true
		previousAttempts = transition.Attempts
		if index == 0 && baseline == nil {
			if transition.State != StateAdmitted && transition.State != StatePending {
				return errors.New("Run transition history has an invalid initial state")
			}
			continue
		}
		var previousState LifecycleState
		var previousAt time.Time
		if index > 0 {
			previousState = run.Transitions[index-1].State
			previousAt = run.Transitions[index-1].At
		} else {
			previousState = baseline.State
			previousAt = baseline.ObservedAt
		}
		if !transition.At.After(previousAt) || !legalTransition(previousState, transition.State) {
			return fmt.Errorf("illegal lifecycle transition %s -> %s", previousState, transition.State)
		}
	}
	return nil
}

func legalTransition(from, to LifecycleState) bool {
	switch from {
	case StateAdmitted:
		return to == StateRouting || to == StateRejected
	case StateRouting:
		return to == StateAdmitted || to == StatePending || to == StateRejected
	case StatePending:
		return to == StateStarting || to == StateSucceeded || to == StateBlocked || to == StateFailed || to == StateRejected
	case StatePostMergePending:
		return to == StateStarting || to == StateSucceeded || to == StateBlocked || to == StateFailed
	case StateStarting:
		return to == StateRunning || to == StatePostMergePending || to == StateAwaitingHumanMerge || to == StateSucceeded || to == StateBlocked || to == StateFailed
	case StateRunning:
		return to == StateAwaitingHumanMerge || to == StateSucceeded || to == StateBlocked || to == StateFailed
	case StateAwaitingHumanMerge:
		return to == StatePending || to == StatePostMergePending || to == StateSucceeded || to == StateBlocked || to == StateFailed
	default:
		return false
	}
}

func validateRoute(route repositories.Route) error {
	if !projectIDPattern.MatchString(route.ProjectID) || !repositoryPattern.MatchString(route.Repository) ||
		route.Origin != "git@github.com:"+route.Repository+".git" || !canonicalAbsolutePath(route.ManagedPath) ||
		!canonicalAbsolutePath(route.ManagedRoot) || route.ManagedPath == route.ManagedRoot || !pathWithin(route.ManagedRoot, route.ManagedPath) ||
		!validBranch(route.DefaultBranch) {
		return errors.New("Run repository route is invalid")
	}
	if route.CloudURL != "" {
		parsed, err := url.Parse(route.CloudURL)
		if err != nil {
			return errors.New("Run repository Cloud URL is invalid")
		}
		host := parsed.Hostname()
		label := strings.TrimSuffix(host, ".nags.cloud")
		if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
			parsed.Path != "" && parsed.Path != "/" || label == host || !cloudLabelPattern.MatchString(label) || host != strings.ToLower(host) {
			return errors.New("Run repository Cloud URL is invalid")
		}
	}
	return nil
}

func validateReady(ready ReadyCheckpoint, run Run) error {
	if ready.ContractVersion != readyContractVersion || ready.RunID != run.ID || !repositoryPattern.MatchString(ready.Repository) ||
		ready.PullRequest < 1 || !validBranch(ready.BaseBranch) || !validBranch(ready.HeadBranch) || !gitOIDPattern.MatchString(ready.VerifiedHeadOID) ||
		ready.CreatedAt.IsZero() || ready.CreatedAt.Location() != time.UTC || !ready.PullRequestUpdatedAt.IsZero() && ready.PullRequestUpdatedAt.Location() != time.UTC ||
		!ready.ValidatedAt.IsZero() && ready.ValidatedAt.Location() != time.UTC {
		return errors.New("Run ready checkpoint is invalid")
	}
	if !ready.Task.IsZero() {
		if ready.Task.Validate() != nil || !ready.Task.Equal(run.Causation.Task) {
			return errors.New("Run ready checkpoint task conflicts")
		}
		prefix, err := ready.Task.BranchPrefix()
		if err != nil || !strings.HasPrefix(ready.HeadBranch, prefix) {
			return errors.New("Run ready checkpoint branch conflicts with its task")
		}
	}
	if run.Repository == nil || ready.Repository != run.Repository.Repository || ready.BaseBranch != run.Repository.DefaultBranch {
		return errors.New("Run ready checkpoint conflicts with its repository route")
	}
	if run.State == StateAwaitingHumanMerge && run.SegmentStartedAt != nil && ready.CreatedAt.Before(*run.SegmentStartedAt) {
		return errors.New("Run ready checkpoint predates its lifecycle segment")
	}
	if ready.CreatedAt.Before(run.CreatedAt) || !ready.ValidatedAt.IsZero() && ready.ValidatedAt.Before(ready.CreatedAt) {
		return errors.New("Run ready checkpoint timestamps conflict")
	}
	return nil
}

func validateCompletion(completion CompletionValidation, run Run) error {
	if !validText(completion.Intent, 256) || !completion.State.Terminal() || !validText(completion.Reason, maximumTextBytes) ||
		completion.ValidatedAt.IsZero() || completion.ValidatedAt.Location() != time.UTC || !validOptionalText(completion.Blocker, 256) ||
		!validOptionalText(completion.PullRequestState, 64) || !validOptionalText(completion.DeploymentID, 256) {
		return errors.New("Run completion validation is invalid")
	}
	for _, oid := range []string{completion.PullRequestHead, completion.MergeCommitOID, completion.DeploymentCommit} {
		if oid != "" && !gitOIDPattern.MatchString(oid) {
			return errors.New("Run completion Git identity is invalid")
		}
	}
	if completion.Accepted {
		if !run.State.Terminal() || completion.State != run.State || completion.Intent != string(completion.State) {
			return errors.New("accepted Run completion conflicts with terminal lifecycle")
		}
		if run.Ready != nil && completion.PullRequestHead != run.Ready.VerifiedHeadOID {
			return errors.New("accepted Run completion conflicts with verified head")
		}
		if run.MergeCommitOID != "" && completion.MergeCommitOID != run.MergeCommitOID {
			return errors.New("accepted Run completion conflicts with merge identity")
		}
	}
	return nil
}

func validateMigratedBaseline(run Run) error {
	baseline := run.MigratedBaseline
	if baseline == nil {
		return nil
	}
	if !baseline.PriorTransitionsAcknowledged || !validLifecycleState(baseline.State) || baseline.ObservedAt.IsZero() ||
		baseline.ObservedAt.Location() != time.UTC || baseline.ObservedAt.Before(run.CreatedAt) || baseline.ObservedAt.After(run.UpdatedAt) ||
		baseline.WorkflowPinUnavailable && baseline.WorkflowDigestUnavailable ||
		baseline.WorkflowPinUnavailable != (run.Causation.Workflow == nil) ||
		baseline.WorkflowDigestUnavailable != (run.Causation.Workflow != nil && compactWorkflow(*run.Causation.Workflow) && run.Causation.WorkflowDigest == "") ||
		baseline.HistoricalRepository != nil && run.Repository != nil {
		return errors.New("Run migrated baseline is invalid")
	}
	if baseline.HistoricalRepository != nil {
		repository := baseline.HistoricalRepository
		if !repositoryPattern.MatchString(repository.Repository) || !validOptionalText(repository.Origin, maximumTextBytes) ||
			repository.ManagedPath != "" && !canonicalAbsolutePath(repository.ManagedPath) ||
			repository.ManagedRoot != "" && !canonicalAbsolutePath(repository.ManagedRoot) ||
			repository.DefaultBranch != "" && !validBranch(repository.DefaultBranch) ||
			repository.CloudURL != "" && !validText(repository.CloudURL, maximumTextBytes) {
			return errors.New("Run historical repository evidence is invalid")
		}
		if (repository.ManagedPath == "") != (repository.ManagedRoot == "") ||
			repository.ManagedPath != "" && (repository.ManagedPath == repository.ManagedRoot || !pathWithin(repository.ManagedRoot, repository.ManagedPath)) {
			return errors.New("Run historical repository evidence is invalid")
		}
	}
	return nil
}

func acceptedPrePullRequestBlocker(run Run) bool {
	if run.State != StateBlocked || run.Completion == nil || !run.Completion.Accepted || run.Ready != nil {
		return false
	}
	switch run.Completion.Blocker {
	case "missing_routing_metadata", "approval_denied", "authority_unavailable", "decision_required":
		return true
	default:
		return false
	}
}

func validateTaskOwnership(runs []Run) error {
	byTask := make(map[string][]Run)
	for _, run := range runs {
		if run.State.Nonterminal() {
			key := run.Causation.Task.OwnershipKey()
			byTask[key] = append(byTask[key], run)
		}
	}
	for _, taskRuns := range byTask {
		slices.SortFunc(taskRuns, compareOwnershipOrder)
		owner := ""
		for index, run := range taskRuns {
			if run.State == StateAdmitted {
				continue
			}
			if owner != "" || index != 0 {
				return fmt.Errorf("runs: task ownership is not held by the oldest nonterminal Run")
			}
			owner = run.ID
		}
	}
	return nil
}

func compareOwnershipOrder(left, right Run) int {
	if comparison := left.Causation.AdmittedAt.Compare(right.Causation.AdmittedAt); comparison != 0 {
		return comparison
	}
	if comparison := left.CreatedAt.Compare(right.CreatedAt); comparison != 0 {
		return comparison
	}
	return strings.Compare(left.ID, right.ID)
}

func validLifecycleState(state LifecycleState) bool {
	switch state {
	case StateAdmitted, StateRouting, StatePending, StatePostMergePending, StateStarting, StateRunning, StateAwaitingHumanMerge,
		StateSucceeded, StateBlocked, StateFailed, StateRejected:
		return true
	default:
		return false
	}
}

func canonicalBatchOrder(values []AdmissionBatch) bool {
	return slices.IsSortedFunc(values, func(left, right AdmissionBatch) int {
		if comparison := left.DecidedAt.Compare(right.DecidedAt); comparison != 0 {
			return comparison
		}
		return strings.Compare(left.ID, right.ID)
	})
}

func canonicalRunOrder(values []Run) bool {
	return slices.IsSortedFunc(values, func(left, right Run) int {
		if comparison := left.CreatedAt.Compare(right.CreatedAt); comparison != 0 {
			return comparison
		}
		return strings.Compare(left.ID, right.ID)
	})
}

func canonicalRateOrder(values []RateBucket) bool {
	return slices.IsSortedFunc(values, func(left, right RateBucket) int {
		if comparison := strings.Compare(left.RuleID, right.RuleID); comparison != 0 {
			return comparison
		}
		return left.Minute.Compare(right.Minute)
	})
}

func sortStringsUnique(values []string) bool {
	for index, value := range values {
		if index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func validSource(source eventwire.Source) bool {
	return eventwire.ValidSource(source)
}

func validIdentity(value string) bool {
	return validText(value, 256)
}

func validText(value string, maximum int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validOptionalText(value string, maximum int) bool {
	return value == "" || validText(value, maximum)
}

func canonicalAbsolutePath(value string) bool {
	return utf8.ValidString(value) && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validBranch(value string) bool {
	return value != "" && len(value) <= 255 && branchPattern.MatchString(value) && !strings.HasPrefix(value, "/") &&
		!strings.HasSuffix(value, "/") && !strings.Contains(value, "..") && !strings.Contains(value, "//")
}

func rateKey(ruleID string, minute time.Time) string {
	return ruleID + "\x00" + minute.UTC().Truncate(time.Minute).Format(time.RFC3339)
}
