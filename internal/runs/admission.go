package runs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

const (
	GlobalOutstandingMax      = 100
	triggerKindConfiguredRule = "configured-rule"
)

type admissionRecord struct {
	record eventwire.Record
	digest string
}

// Admitter is the single owner of canonical admission decisions for one
// Store. Production lock order is: a caller-owned keyed lock, when present,
// then the policy.Coordinator mutex, then the Admitter mutex, then either the
// Store Snapshot RLock or one ApplyAdmissionBatch Lock. Store locks are never
// held while matching or consulting policy.
//
// A lifecycle transition may interleave between Snapshot and
// ApplyAdmissionBatch. It can only advance an existing nonterminal Run or make
// it terminal; it cannot create Runs or rate increments. Admission therefore
// remains conservative if outstanding work decreases during that interval.
// Event batches, native starts, and protected continuations all use the same
// Admitter and policy.Coordinator owner.
type Admitter struct {
	mu    sync.Mutex
	store *Store
}

func NewAdmitter(store *Store) (*Admitter, error) {
	if store == nil {
		return nil, errors.New("runs: admission store is required")
	}
	return &Admitter{store: store}, nil
}

// AdmitBatch atomically derives and appends all new event decisions, Runs,
// and rate effects for one authoritative source batch. Exact durable event
// retries are returned from their original evidence and are not re-appended.
func (a *Admitter) AdmitBatch(records []eventwire.Record, snapshot policy.Snapshot, decisionTime time.Time) ([]AdmissionBatch, error) {
	if a == nil || a.store == nil {
		return nil, errors.New("runs: admission store is required")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("runs: invalid policy snapshot: %w", err)
	}
	if decisionTime.IsZero() || decisionTime.Location() != time.UTC {
		return nil, errors.New("runs: decision time must be an explicit UTC time")
	}
	prepared := make([]admissionRecord, len(records))
	seenIDs := make(map[string]bool, len(records))
	seenSequences := make(map[uint64]bool, len(records))
	for index, record := range records {
		digest, err := eventwire.CanonicalRecordDigest(record)
		if err != nil {
			return nil, fmt.Errorf("runs: event record %d: %w", index+1, err)
		}
		if seenIDs[record.Event.ID] || seenSequences[record.Sequence] {
			return nil, fmt.Errorf("%w: duplicate event identity in source batch", ErrIdentityCollision)
		}
		seenIDs[record.Event.ID] = true
		seenSequences[record.Sequence] = true
		prepared[index] = admissionRecord{record: record, digest: digest}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	storeSnapshot, err := a.store.Snapshot()
	if err != nil {
		return nil, err
	}
	model := storeSnapshot.Model()

	results := make([]AdmissionBatch, len(prepared))
	newRecords := make([]admissionRecord, 0, len(prepared))
	newPositions := make([]int, 0, len(prepared))
	for index, candidate := range prepared {
		existing, found, err := durableEventBatch(model, candidate)
		if err != nil {
			return nil, err
		}
		if found {
			results[index] = existing
			continue
		}
		newRecords = append(newRecords, candidate)
		newPositions = append(newPositions, index)
	}
	if len(newRecords) == 0 {
		return cloneAdmissionBatches(results), nil
	}

	registry := snapshot.Registry()
	rules := slices.Clone(registry.Rules)
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	settings := snapshot.Settings()
	outstandingByRule, globalOutstanding := outstanding(model.Runs)
	rolling := rollingRates(model.RateBuckets, decisionTime)
	newBatches := make([]AdmissionBatch, 0, len(newRecords))
	newRuns := make([]Run, 0)
	increments := make(map[string]int)

	for recordIndex, candidate := range newRecords {
		record := candidate.record
		batch := AdmissionBatch{
			ID:     admissionDigest("factory-runs-admission-batch-v1", string(AdmissionOriginEvent), record.Event.ID),
			Origin: AdmissionOriginEvent, EventID: record.Event.ID, EventSequence: record.Sequence,
			EventSource: record.Event.Source, EventRecordDigest: candidate.digest,
			RegistryRevision: registry.Revision, SettingsRevision: settings.Revision,
			PolicyGeneration: snapshot.Generation(), DecidedAt: decisionTime, Outcomes: []AdmissionOutcome{},
		}
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
			case protectedTaskMutation(record.Event):
				outcome.Kind, outcome.Reason = AdmissionOutcomeSuppressed, "protected-task-operation"
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
				task, err := resolveAdmissionTask(rule.Target, record.Event)
				if err != nil {
					outcome.Kind, outcome.Reason = AdmissionOutcomeRejected, err.Error()
					break
				}
				definition, found := snapshot.Workflow(rule.WorkflowID)
				if !found || !definition.Enabled {
					outcome.Kind, outcome.Reason = AdmissionOutcomeRejected, "workflow-unavailable"
					break
				}
				run, err := newEventRun(record, batch, rule, definition, task, decisionTime)
				if err != nil {
					return nil, err
				}
				outcome.Kind, outcome.AdmissionID, outcome.RunID = AdmissionOutcomeRun, run.Causation.AdmissionID, run.ID
				newRuns = append(newRuns, run)
				outstandingByRule[rule.ID]++
				globalOutstanding++
				rolling[rule.ID]++
				increments[rule.ID]++
			}
			batch.Outcomes = append(batch.Outcomes, outcome)
		}
		newBatches = append(newBatches, batch)
		results[newPositions[recordIndex]] = batch
	}

	persistedBatches := cloneAdmissionBatches(newBatches)
	for index := range persistedBatches {
		canonicalizeAdmissionBatch(&persistedBatches[index])
	}
	slices.SortFunc(persistedBatches, compareAdmissionBatches)
	persistedRuns := cloneRuns(newRuns)
	for index := range persistedRuns {
		canonicalizeRun(&persistedRuns[index])
	}
	slices.SortFunc(persistedRuns, compareRuns)
	minute := decisionTime.Truncate(time.Minute)
	rateIncrements := make([]RateBucket, 0, len(increments))
	for ruleID, count := range increments {
		rateIncrements = append(rateIncrements, RateBucket{RuleID: ruleID, Minute: minute, Count: count})
	}
	slices.SortFunc(rateIncrements, compareRateBuckets)
	if err := a.store.ApplyAdmissionBatch(persistedBatches, persistedRuns, rateIncrements); err != nil {
		return nil, err
	}
	return cloneAdmissionBatches(results), nil
}

func durableEventBatch(model Model, candidate admissionRecord) (AdmissionBatch, bool, error) {
	batchID := admissionDigest("factory-runs-admission-batch-v1", string(AdmissionOriginEvent), candidate.record.Event.ID)
	check := func(batch AdmissionBatch) (AdmissionBatch, bool, error) {
		overlaps := batch.ID == batchID || batch.EventID == candidate.record.Event.ID ||
			batch.EventSequence != 0 && batch.EventSequence == candidate.record.Sequence
		if !overlaps {
			return AdmissionBatch{}, false, nil
		}
		if batch.Origin != AdmissionOriginEvent || batch.ID != batchID || batch.EventID != candidate.record.Event.ID ||
			batch.EventSequence != candidate.record.Sequence || batch.EventSource != candidate.record.Event.Source ||
			batch.EventRecordDigest != candidate.digest {
			return AdmissionBatch{}, false, fmt.Errorf("%w: event %q conflicts with durable admission evidence", ErrIdentityCollision, candidate.record.Event.ID)
		}
		return cloneAdmissionBatches([]AdmissionBatch{batch})[0], true, nil
	}
	if model.Migration != nil {
		for _, batch := range model.Migration.AdmissionBatches {
			if found, ok, err := check(batch); ok || err != nil {
				return found, ok, err
			}
		}
	}
	for _, receipt := range model.AdmissionOperations {
		for _, batch := range receipt.AdmissionBatches {
			if found, ok, err := check(batch); ok || err != nil {
				return found, ok, err
			}
		}
	}
	return AdmissionBatch{}, false, nil
}

func outstanding(values []Run) (map[string]int, int) {
	byRule := make(map[string]int)
	global := 0
	for _, run := range values {
		if run.State.Nonterminal() {
			byRule[run.Causation.RuleID]++
			global++
		}
	}
	return byRule, global
}

func rollingRates(values []RateBucket, now time.Time) map[string]int {
	cutoff := now.Add(-time.Hour).Truncate(time.Minute)
	rolling := make(map[string]int)
	for _, bucket := range values {
		if !bucket.Minute.Before(cutoff) {
			rolling[bucket.RuleID] += bucket.Count
		}
	}
	return rolling
}

func protectedTaskMutation(event eventwire.Event) bool {
	return event.Source == eventwire.SourceFactory && event.Type == "task-mutation"
}

func resolveAdmissionTask(target policy.TargetPolicy, event eventwire.Event) (taskmodel.TaskRef, error) {
	if target.Provider == "" {
		target.Provider = taskmodel.SourceLinear
	}
	if target.Provider != taskmodel.SourceLinear {
		return taskmodel.TaskRef{}, errors.New("target-provider-invalid")
	}
	var value string
	switch target.Kind {
	case policy.TargetFixedIssue:
		value = target.Value
	case policy.TargetEventSubject:
		value = event.Subject
	case policy.TargetEventAttribute:
		values := event.Values(target.Value)
		if len(values) != 1 {
			return taskmodel.TaskRef{}, errors.New("target-attribute-cardinality")
		}
		value = values[0]
	default:
		return taskmodel.TaskRef{}, errors.New("target-policy-invalid")
	}
	value = strings.ToUpper(strings.TrimSpace(value))
	if !taskmodel.ValidLinearIdentifier(value) {
		return taskmodel.TaskRef{}, errors.New("target-issue-invalid")
	}
	return taskmodel.LegacyLinear(value)
}

func newEventRun(record eventwire.Record, batch AdmissionBatch, rule policy.Rule, definition policy.Workflow, task taskmodel.TaskRef, now time.Time) (Run, error) {
	admissionID := admissionDigest("factory-trigger-invocation-v1", record.Event.ID, rule.ID, strconv.FormatUint(rule.Revision, 10))
	runID := "run-" + admissionDigest("factory-trigger-run-v1", admissionID)[:16]
	pin := workflow.Pin(workflow.Definition{
		ID: definition.ID, Revision: definition.Revision, Name: definition.Name,
		Enabled: definition.Enabled, Markdown: definition.Markdown, UpdatedAt: definition.UpdatedAt,
	})
	digest, err := pin.Digest()
	if err != nil {
		return Run{}, fmt.Errorf("runs: digest workflow snapshot: %w", err)
	}
	policyDigest, err := policy.WorkflowDigest(definition)
	if err != nil || policyDigest != digest {
		return Run{}, errors.New("runs: policy workflow digest conflicts with pinned workflow")
	}
	root := record.Event.RootEventID
	if root == "" {
		root = record.Event.ID
	}
	ancestors := append(slices.Clone(record.Event.AncestorRuleIDs), rule.ID)
	return Run{
		ID: runID,
		Causation: Causation{
			AdmissionID: admissionID, BatchID: batch.ID, EventID: record.Event.ID,
			EventSequence: record.Sequence, EventSource: record.Event.Source,
			RuleID: rule.ID, RuleRevision: rule.Revision, Workflow: &pin, WorkflowDigest: digest,
			PolicyRevision: batch.SettingsRevision, PolicyGeneration: batch.PolicyGeneration,
			Task: task, RootEventID: root, ParentAdmissionID: record.Event.ParentInvocationID,
			ParentRunID: record.Event.ParentRunID, Hop: record.Event.Hop + 1,
			AncestorRuleIDs: ancestors, AdmittedAt: now,
		},
		TriggerKind: triggerKindConfiguredRule, DeliveryIDs: []string{record.Event.ID},
		State: StateAdmitted, CreatedAt: now, UpdatedAt: now,
		Transitions: []LifecycleTransition{{ID: runID + ":admitted", State: StateAdmitted, At: now}},
	}, nil
}

func admissionDigest(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
