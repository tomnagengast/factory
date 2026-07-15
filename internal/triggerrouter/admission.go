package triggerrouter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

func (s *Store) ApplyDecisionBatch(records []eventwire.Record, registry triggerregistry.Snapshot, configuration settings.Snapshot, now time.Time) ([]Decision, error) {
	if err := registry.Validate(configuration); err != nil {
		return nil, fmt.Errorf("trigger router: invalid registry snapshot: %w", err)
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned != nil {
		return nil, fmt.Errorf("trigger router: store is poisoned: %w", s.poisoned)
	}
	s.expireRatesLocked(now)

	rules := slices.Clone(registry.Rules)
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	outstandingByRule, globalOutstanding := s.outstandingLocked()
	rolling := s.rollingRatesLocked(now)
	decisions := make([]Decision, 0, len(records))
	newDecisions := make([]Decision, 0, len(records))
	newInvocations := make([]Invocation, 0)
	increments := make(map[string]int)

	for _, record := range records {
		if existing, found := s.decisions[record.Event.ID]; found {
			if existing.EventSequence != record.Sequence {
				return nil, fmt.Errorf("trigger router: event %q sequence conflicts with durable decision", record.Event.ID)
			}
			decisions = append(decisions, existing.Clone())
			continue
		}
		decision := Decision{
			EventID: record.Event.ID, EventSequence: record.Sequence, Source: record.Event.Source,
			RegistryRevision: registry.Revision, SettingsRevision: configuration.Revision, DecidedAt: now,
			Outcomes: []Outcome{},
		}
		for _, rule := range rules {
			if !rule.Enabled || !rule.Filter.Matches(record.Event) {
				continue
			}
			outcome := Outcome{RuleID: rule.ID, RuleRevision: rule.Revision}
			switch {
			case protectedTaskMutation(record.Event):
				outcome.Kind, outcome.Reason = OutcomeSuppressed, "protected-task-operation"
			case slices.Contains(record.Event.AncestorRuleIDs, rule.ID):
				outcome.Kind, outcome.Reason = OutcomeSuppressed, "ancestor-cycle"
			case record.Event.Hop >= rule.MaxHop:
				outcome.Kind, outcome.Reason = OutcomeSuppressed, "hop-limit"
			case rolling[rule.ID] >= rule.AdmissionsHour:
				outcome.Kind, outcome.Reason = OutcomeSuppressed, "hourly-rate-limit"
			case outstandingByRule[rule.ID] >= rule.MaxOutstanding:
				outcome.Kind, outcome.Reason = OutcomeSuppressed, "rule-outstanding-limit"
			case globalOutstanding >= GlobalOutstandingMax:
				outcome.Kind, outcome.Reason = OutcomeSuppressed, "global-outstanding-limit"
			default:
				task, err := resolveTask(rule.Target, record.Event)
				if err != nil {
					outcome.Kind, outcome.Reason = OutcomeRejected, err.Error()
					break
				}
				workflow, found := configuration.Workflow(rule.WorkflowID)
				if !found || !workflow.Enabled {
					outcome.Kind, outcome.Reason = OutcomeRejected, "workflow-unavailable"
					break
				}
				invocation, err := newInvocation(record, rule, workflow, configuration.Revision, task, now)
				if err != nil {
					return nil, err
				}
				outcome.Kind, outcome.InvocationID = OutcomeInvocation, invocation.ID
				newInvocations = append(newInvocations, invocation)
				outstandingByRule[rule.ID]++
				globalOutstanding++
				rolling[rule.ID]++
				increments[rule.ID]++
			}
			decision.Outcomes = append(decision.Outcomes, outcome)
		}
		decisions = append(decisions, decision)
		newDecisions = append(newDecisions, decision)
	}
	if len(newDecisions) == 0 {
		return cloneDecisions(decisions), nil
	}
	minute := now.Truncate(time.Minute)
	rateIncrements := make([]RateBucket, 0, len(increments))
	for ruleID, count := range increments {
		rateIncrements = append(rateIncrements, RateBucket{RuleID: ruleID, Minute: minute, Count: count})
	}
	sort.Slice(rateIncrements, func(i, j int) bool { return rateIncrements[i].RuleID < rateIncrements[j].RuleID })
	op := diskOperation{Kind: operationDecisionBatch, Decisions: newDecisions, Invocations: newInvocations, RateIncrements: rateIncrements}
	if err := s.appendOperationLocked(op); err != nil {
		return nil, err
	}
	if err := s.applyOperationLocked(op); err != nil {
		s.poisoned = err
		return nil, fmt.Errorf("trigger router: apply persisted decision batch: %w", err)
	}
	if err := s.compactIfNeededLocked(); err != nil {
		return nil, err
	}
	return cloneDecisions(decisions), nil
}

func protectedTaskMutation(event eventwire.Event) bool {
	return event.Source == eventwire.SourceFactory && event.Type == "task-mutation"
}

func (s *Store) outstandingLocked() (map[string]int, int) {
	byRule := make(map[string]int)
	global := 0
	for _, invocation := range s.invocations {
		if invocation.Nonterminal() {
			byRule[invocation.Rule.ID]++
			global++
		}
	}
	return byRule, global
}

func (s *Store) rollingRatesLocked(now time.Time) map[string]int {
	cutoff := now.Add(-time.Hour).Truncate(time.Minute)
	result := make(map[string]int)
	for _, bucket := range s.rates {
		if !bucket.Minute.Before(cutoff) {
			result[bucket.RuleID] += bucket.Count
		}
	}
	return result
}

func (s *Store) expireRatesLocked(now time.Time) {
	cutoff := now.Add(-time.Hour).Truncate(time.Minute)
	for key, bucket := range s.rates {
		if bucket.Minute.Before(cutoff) {
			delete(s.rates, key)
		}
	}
}

func resolveTask(target triggerregistry.TargetPolicy, event eventwire.Event) (taskmodel.TaskRef, error) {
	target = target.Canonical()
	if target.Provider != taskmodel.SourceLinear {
		return taskmodel.TaskRef{}, fmt.Errorf("target-provider-invalid")
	}
	var value string
	switch target.Kind {
	case triggerregistry.TargetFixedIssue:
		value = target.Value
	case triggerregistry.TargetEventSubject:
		value = event.Subject
	case triggerregistry.TargetEventAttribute:
		values := event.Values(target.Value)
		if len(values) != 1 {
			return taskmodel.TaskRef{}, fmt.Errorf("target-attribute-cardinality")
		}
		value = values[0]
	default:
		return taskmodel.TaskRef{}, fmt.Errorf("target-policy-invalid")
	}
	value = strings.ToUpper(strings.TrimSpace(value))
	if !agentrun.ValidIssueIdentifier(value) {
		return taskmodel.TaskRef{}, fmt.Errorf("target-issue-invalid")
	}
	return taskmodel.LegacyLinear(value)
}

func resolveIssue(target triggerregistry.TargetPolicy, event eventwire.Event) (string, error) {
	task, err := resolveTask(target, event)
	if err != nil {
		return "", err
	}
	return task.Identifier, nil
}

func newInvocation(record eventwire.Record, rule triggerregistry.Rule, definition workflow.Definition, policyRevision uint64, task taskmodel.TaskRef, now time.Time) (Invocation, error) {
	id := digestStrings("factory-trigger-invocation-v1", record.Event.ID, rule.ID, fmt.Sprintf("%d", rule.Revision))
	pinned := workflow.Pin(definition)
	digest, err := pinned.Digest()
	if err != nil {
		return Invocation{}, fmt.Errorf("trigger router: digest workflow snapshot: %w", err)
	}
	root := record.Event.RootEventID
	if root == "" {
		root = record.Event.ID
	}
	ancestors := append(slices.Clone(record.Event.AncestorRuleIDs), rule.ID)
	rule = rule.Clone()
	rule.Target = rule.Target.Canonical()
	return Invocation{
		ID: id, EventID: record.Event.ID, EventSequence: record.Sequence, Rule: rule,
		Workflow: pinned, WorkflowDigest: digest, PolicyRevision: policyRevision, Task: task, IssueIdentifier: task.Identifier,
		RootEventID: root, ParentInvocationID: record.Event.ParentInvocationID, ParentRunID: record.Event.ParentRunID,
		Hop: record.Event.Hop + 1, AncestorRuleIDs: ancestors, State: StateQueued, AdmittedAt: now, UpdatedAt: now,
	}, nil
}

func digestStrings(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func cloneDecisions(decisions []Decision) []Decision {
	cloned := make([]Decision, len(decisions))
	for i, decision := range decisions {
		cloned[i] = decision.Clone()
	}
	return cloned
}
