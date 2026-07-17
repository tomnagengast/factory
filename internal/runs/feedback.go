package runs

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

const protectedLinearFeedbackRuleID = "protected-linear-feedback"

// ContinueLinear durably admits one normalized human Linear comment against
// the protected workflow binding. It is intentionally separate from generic
// visible rules: a normal human comment creates exactly this continuation, and
// any explicitly configured generic comment rule is evaluated additively in
// the same atomic source admission.
func (a *Admitter) ContinueLinear(record eventwire.Record, snapshot policy.Snapshot) (Run, bool, error) {
	if a == nil || a.store == nil {
		return Run{}, false, errors.New("runs: admission store is required")
	}
	if err := snapshot.Validate(); err != nil {
		return Run{}, false, fmt.Errorf("runs: invalid feedback policy snapshot: %w", err)
	}
	if record.Sequence == 0 || record.Event.Source != eventwire.SourceLinear || record.Event.Type != "Comment" || record.Event.Action != "create" ||
		record.Event.Subject == "" || record.Event.ReceivedAt.IsZero() || record.Event.ReceivedAt.Location() != time.UTC {
		return Run{}, false, errors.New("runs: protected Linear feedback record is invalid")
	}
	provenance := record.Event.Values(eventwire.AttributeProvenance)
	if len(provenance) != 1 || provenance[0] != "human" {
		return Run{}, false, errors.New("runs: protected Linear feedback must be human-authored")
	}
	task, err := taskmodel.LegacyLinear(record.Event.Subject)
	if err != nil {
		return Run{}, false, errors.New("runs: protected Linear feedback task is invalid")
	}
	binding := snapshot.ProtectedWorkflows().LinearFeedback
	definition, found := snapshot.Workflow(binding.WorkflowID)
	if !found || !definition.Enabled {
		return Run{}, false, errors.New("runs: protected Linear feedback workflow is unavailable")
	}
	pin := workflow.Pin(workflow.Definition{
		ID: definition.ID, Revision: definition.Revision, Name: definition.Name,
		Enabled: definition.Enabled, Markdown: definition.Markdown, UpdatedAt: definition.UpdatedAt,
	})
	digest, err := pin.Digest()
	if err != nil {
		return Run{}, false, err
	}
	policyDigest, err := policy.WorkflowDigest(definition)
	if err != nil || policyDigest != digest {
		return Run{}, false, errors.New("runs: protected feedback workflow digest conflicts with its pin")
	}
	recordDigest, err := eventwire.CanonicalRecordDigest(record)
	if err != nil {
		return Run{}, false, err
	}
	admissionID := admissionDigest("factory-trigger-invocation-v1", record.Event.ID, protectedLinearFeedbackRuleID, "1")
	runID := "run-" + admissionDigest("factory-trigger-run-v1", admissionID)[:16]
	batchID := admissionDigest("factory-runs-admission-batch-v1", string(AdmissionOriginEvent), record.Event.ID)

	a.mu.Lock()
	defer a.mu.Unlock()
	storeSnapshot, err := a.store.Snapshot()
	if err != nil {
		return Run{}, false, err
	}
	model := storeSnapshot.Model()
	if durable, found, err := durableLinearFeedback(model, batchID, record, recordDigest, admissionID, runID, task, digest); found || err != nil {
		return durable, false, err
	}
	batch := AdmissionBatch{
		ID: batchID, Origin: AdmissionOriginEvent, EventID: record.Event.ID, EventSequence: record.Sequence,
		EventSource: record.Event.Source, EventRecordDigest: recordDigest,
		RegistryRevision: snapshot.Registry().Revision, SettingsRevision: snapshot.Settings().Revision,
		PolicyGeneration: snapshot.Generation(), DecidedAt: record.Event.ReceivedAt,
		Outcomes: []AdmissionOutcome{{
			Kind: AdmissionOutcomeRun, RuleID: protectedLinearFeedbackRuleID, RuleRevision: 1,
			AdmissionID: admissionID, RunID: runID,
		}},
	}
	root := record.Event.RootEventID
	if root == "" {
		root = record.Event.ID
	}
	run := Run{
		ID: runID,
		Causation: Causation{
			AdmissionID: admissionID, BatchID: batchID, EventID: record.Event.ID, EventSequence: record.Sequence,
			EventSource: record.Event.Source, RuleID: protectedLinearFeedbackRuleID, RuleRevision: 1,
			Workflow: &pin, WorkflowDigest: digest, PolicyRevision: batch.SettingsRevision,
			PolicyGeneration: batch.PolicyGeneration, Task: task, RootEventID: root,
			ParentAdmissionID: record.Event.ParentInvocationID, ParentRunID: record.Event.ParentRunID,
			Hop: record.Event.Hop + 1, AncestorRuleIDs: append(slices.Clone(record.Event.AncestorRuleIDs), protectedLinearFeedbackRuleID),
			AdmittedAt: record.Event.ReceivedAt,
		},
		TriggerKind: triggerKindComment, DeliveryIDs: []string{record.Event.ID}, State: StateAdmitted,
		CreatedAt: record.Event.ReceivedAt, UpdatedAt: record.Event.ReceivedAt,
		Transitions: []LifecycleTransition{{ID: runID + ":admitted", State: StateAdmitted, At: record.Event.ReceivedAt}},
	}

	// A customized visible comment rule is intentionally additive. It shares
	// the one source batch with the protected continuation so event identity,
	// source sequence, and record digest remain single-owned.
	rules := slices.Clone(snapshot.Registry().Rules)
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	outstandingByRule, globalOutstanding := outstanding(model.Runs)
	rolling := rollingRates(model.RateBuckets, record.Event.ReceivedAt)
	ownerExists := slices.ContainsFunc(model.Runs, func(candidate Run) bool {
		return candidate.State.Nonterminal() && candidate.Causation.Task.Equal(task)
	})
	if !ownerExists {
		outstandingByRule[protectedLinearFeedbackRuleID]++
		globalOutstanding++
	}
	rolling[protectedLinearFeedbackRuleID]++
	additional := make([]Run, 0)
	incrementCounts := map[string]int{protectedLinearFeedbackRuleID: 1}
	for _, rule := range rules {
		filter := eventwire.Filter{
			Source: rule.Filter.Source, Type: rule.Filter.Type, Action: rule.Filter.Action,
			Attributes: rule.Filter.Attributes,
		}
		if rule.Filter.Subject != nil {
			filter.Subject = *rule.Filter.Subject
		}
		if !rule.Enabled || !filter.Matches(record.Event) {
			continue
		}
		outcome := AdmissionOutcome{RuleID: rule.ID, RuleRevision: rule.Revision}
		switch {
		case slices.Contains(record.Event.AncestorRuleIDs, rule.ID):
			outcome.Kind, outcome.Reason = AdmissionOutcomeSuppressed, "ancestor-cycle"
		case record.Event.Hop >= rule.MaxHop:
			outcome.Kind, outcome.Reason = AdmissionOutcomeSuppressed, "hop-limit"
		case rolling[rule.ID] >= rule.AdmissionsHour:
			outcome.Kind, outcome.Reason = AdmissionOutcomeSuppressed, "hourly-rate-limit"
		case outstandingByRule[rule.ID] >= rule.MaxOutstanding:
			outcome.Kind, outcome.Reason = AdmissionOutcomeSuppressed, "rule-outstanding-limit"
		case globalOutstanding >= GlobalOutstandingMax:
			outcome.Kind, outcome.Reason = AdmissionOutcomeSuppressed, "global-outstanding-limit"
		default:
			genericTask, resolveErr := resolveAdmissionTask(rule.Target, record.Event)
			if resolveErr != nil {
				outcome.Kind, outcome.Reason = AdmissionOutcomeRejected, resolveErr.Error()
				break
			}
			genericWorkflow, found := snapshot.Workflow(rule.WorkflowID)
			if !found || !genericWorkflow.Enabled {
				outcome.Kind, outcome.Reason = AdmissionOutcomeRejected, "workflow-unavailable"
				break
			}
			generic, runErr := newEventRun(record, batch, rule, genericWorkflow, genericTask, record.Event.ReceivedAt)
			if runErr != nil {
				return Run{}, false, runErr
			}
			outcome.Kind, outcome.AdmissionID, outcome.RunID = AdmissionOutcomeRun, generic.Causation.AdmissionID, generic.ID
			additional = append(additional, generic)
			outstandingByRule[rule.ID]++
			globalOutstanding++
			rolling[rule.ID]++
			incrementCounts[rule.ID]++
		}
		batch.Outcomes = append(batch.Outcomes, outcome)
	}
	increments := make([]RateBucket, 0, len(incrementCounts))
	for ruleID, count := range incrementCounts {
		increments = append(increments, RateBucket{RuleID: ruleID, Minute: record.Event.ReceivedAt.Truncate(time.Minute), Count: count})
	}
	persisted, err := a.store.ApplyFeedback(batch, run, additional, increments)
	if err != nil {
		return Run{}, false, err
	}
	return persisted, true, nil
}

func durableLinearFeedback(model Model, batchID string, record eventwire.Record, recordDigest, admissionID, runID string, task taskmodel.TaskRef, workflowDigest string) (Run, bool, error) {
	candidate := admissionRecord{record: record, digest: recordDigest}
	batch, found, err := durableEventBatch(model, candidate)
	if err != nil || !found {
		return Run{}, found, err
	}
	wantOutcome := AdmissionOutcome{
		Kind: AdmissionOutcomeRun, RuleID: protectedLinearFeedbackRuleID, RuleRevision: 1, AdmissionID: admissionID, RunID: runID,
	}
	if batch.ID != batchID || !slices.Contains(batch.Outcomes, wantOutcome) {
		return Run{}, false, fmt.Errorf("%w: protected feedback conflicts with durable admission evidence", ErrIdentityCollision)
	}
	for _, run := range model.Runs {
		if run.ID != runID {
			continue
		}
		if run.Causation.AdmissionID != admissionID || !run.Causation.Task.Equal(task) || run.Causation.WorkflowDigest != workflowDigest ||
			run.Causation.RuleID != protectedLinearFeedbackRuleID || run.Causation.EventSequence != record.Sequence {
			return Run{}, false, fmt.Errorf("%w: protected feedback Run conflicts with durable evidence", ErrIdentityCollision)
		}
		return run.Clone(), true, nil
	}
	return Run{}, false, fmt.Errorf("%w: protected feedback Run is unavailable", ErrIdentityCollision)
}
