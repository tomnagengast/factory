package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

const migratedDirectRuleID = "migrated-direct"

type runConversionMetrics struct {
	audit CanonicalRunsAudit
}

type runSourceIndex struct {
	wireByID           map[string]eventwire.Record
	decisions          map[string]triggerrouter.Decision
	invocations        map[string]triggerrouter.Invocation
	decisionByInvoke   map[string]triggerrouter.Decision
	batchByInvoke      map[string]runs.AdmissionBatch
	legacyRuns         map[string]agentrun.Run
	runByInvocation    map[string]agentrun.Run
	transitionEvidence []string
}

func preAuditMigrationID(sourceRootDigest string) string {
	return "migration-" + digestRunParts("factory-migration-source-root-v1", sourceRootDigest)[:32]
}

func convertRunSources(
	state sourceState,
	canonical canonicalEvidence,
	migrationID string,
	sourceRootDigest string,
) (runs.Snapshot, runConversionMetrics, error) {
	if canonical.policySnapshot.Generation() == 0 || canonical.repositoryState.Generation == 0 {
		return runs.Snapshot{}, runConversionMetrics{}, errors.New("migration: canonical Run dependencies are incomplete")
	}
	if state.runs.Total < uint64(len(state.runs.Runs)) {
		return runs.Snapshot{}, runConversionMetrics{}, errors.New("migration: retained Runs exceed their lifetime total")
	}
	policySettings := canonical.policySnapshot.Settings()
	policyRegistry := canonical.policySnapshot.Registry()
	index, err := indexRunSources(
		state, canonical.policySnapshot.Generation(), policySettings.Revision, policyRegistry.Revision,
	)
	if err != nil {
		return runs.Snapshot{}, runConversionMetrics{}, err
	}

	model := runs.EmptyModel()
	model.TotalBatches = uint64(len(state.routing.Decisions))
	model.AdmissionBatches = make([]runs.AdmissionBatch, 0, len(state.routing.Decisions)+len(state.runs.Runs))
	model.Runs = make([]runs.Run, 0, len(state.routing.Invocations)+len(state.runs.Runs))
	convertedByInvocation := make(map[string]runs.Run, len(state.routing.Invocations))
	linkedEvidence := make([]string, 0, len(state.routing.Invocations))
	synthesizedEvidence := make([]string, 0, len(state.routing.Invocations))
	directEvidence := make([]string, 0, len(state.runs.Runs))
	reflectionEvidence := make([]string, 0)

	for _, invocation := range state.routing.Invocations {
		decision, found := index.decisionByInvoke[invocation.ID]
		if !found {
			return runs.Snapshot{}, runConversionMetrics{}, fmt.Errorf("migration: invocation %s is orphaned", invocation.ID)
		}
		batch := index.batchByInvoke[invocation.ID]
		legacy, linked := index.runByInvocation[invocation.ID]
		converted, synthesized, err := convertInvocationRun(invocation, decision, batch, legacy, linked, index, canonical.repositoryState)
		if err != nil {
			return runs.Snapshot{}, runConversionMetrics{}, err
		}
		convertedByInvocation[invocation.ID] = converted
		model.Runs = append(model.Runs, converted)
		if synthesized {
			synthesizedEvidence = append(synthesizedEvidence, fmt.Sprintf("%s|%s|%s", invocation.ID, converted.ID, converted.State))
		} else {
			linkedEvidence = append(linkedEvidence, invocation.ID+"|"+converted.ID)
		}
		if invocation.ReflectedAt != nil || legacy.InvocationReflectedAt != nil {
			reflectionEvidence = append(reflectionEvidence, reflectionIdentity(invocation, legacy))
		}
	}

	for _, decision := range state.routing.Decisions {
		batch, err := convertDecision(decision, index, convertedByInvocation, canonical.policySnapshot.Generation())
		if err != nil {
			return runs.Snapshot{}, runConversionMetrics{}, err
		}
		model.AdmissionBatches = append(model.AdmissionBatches, batch)
	}

	for _, legacy := range state.runs.Runs {
		if legacy.InvocationID != "" {
			if _, linked := index.invocations[legacy.InvocationID]; linked {
				continue
			}
		}
		if legacy.State.Nonterminal() {
			return runs.Snapshot{}, runConversionMetrics{}, fmt.Errorf("migration: active Run %s has no retained invocation", legacy.ID)
		}
		batch, converted, err := convertDirectRun(legacy, canonical.policySnapshot.Generation(), canonical.repositoryState)
		if err != nil {
			return runs.Snapshot{}, runConversionMetrics{}, err
		}
		model.AdmissionBatches = append(model.AdmissionBatches, batch)
		model.Runs = append(model.Runs, converted)
		model.TotalBatches++
		directEvidence = append(directEvidence, converted.ID+"|"+converted.Causation.AdmissionID)
		if legacy.InvocationReflectedAt != nil {
			reflectionEvidence = append(reflectionEvidence, fmt.Sprintf("direct|%s|%s", legacy.ID, legacy.InvocationReflectedAt.UTC().Format(time.RFC3339Nano)))
		}
	}

	model.RateBuckets = make([]runs.RateBucket, len(state.routing.RateBuckets))
	seenRates := make(map[string]bool, len(state.routing.RateBuckets))
	for position, bucket := range state.routing.RateBuckets {
		if bucket.RuleID == "" || bucket.Minute.IsZero() || bucket.Minute != bucket.Minute.UTC().Truncate(time.Minute) || bucket.Count < 1 {
			return runs.Snapshot{}, runConversionMetrics{}, errors.New("migration: routing rate bucket is invalid")
		}
		key := bucket.RuleID + "\x00" + bucket.Minute.Format(time.RFC3339)
		if seenRates[key] {
			return runs.Snapshot{}, runConversionMetrics{}, errors.New("migration: routing rate bucket is duplicated")
		}
		seenRates[key] = true
		model.RateBuckets[position] = runs.RateBucket{RuleID: bucket.RuleID, Minute: bucket.Minute, Count: bucket.Count}
	}

	synthesized := uint64(len(synthesizedEvidence))
	if synthesized > ^uint64(0)-state.runs.Total {
		return runs.Snapshot{}, runConversionMetrics{}, errors.New("migration: canonical Run lifetime total is exhausted")
	}
	model.TotalRuns = state.runs.Total + synthesized
	receipt, err := runs.NewMigrationSnapshotReceipt(
		migrationID, sourceRootDigest, model.TotalRuns,
		model.AdmissionBatches, model.Runs, model.RateBuckets,
	)
	if err != nil {
		return runs.Snapshot{}, runConversionMetrics{}, fmt.Errorf("migration: build canonical Runs receipt: %w", err)
	}
	model.Migration = receipt
	snapshot, err := runs.NewSnapshot(model)
	if err != nil {
		return runs.Snapshot{}, runConversionMetrics{}, fmt.Errorf("migration: validate canonical Runs snapshot: %w", err)
	}

	metrics, err := buildRunConversionMetrics(
		state, snapshot, linkedEvidence, synthesizedEvidence, directEvidence,
		index.transitionEvidence, reflectionEvidence,
	)
	if err != nil {
		return runs.Snapshot{}, runConversionMetrics{}, err
	}
	return snapshot, metrics, nil
}

func indexRunSources(
	state sourceState,
	policyGeneration uint64,
	policyRevision uint64,
	registryRevision uint64,
) (runSourceIndex, error) {
	index := runSourceIndex{
		wireByID:         make(map[string]eventwire.Record, len(state.wireRecords)),
		decisions:        make(map[string]triggerrouter.Decision, len(state.routing.Decisions)),
		invocations:      make(map[string]triggerrouter.Invocation, len(state.routing.Invocations)),
		decisionByInvoke: make(map[string]triggerrouter.Decision, len(state.routing.Invocations)),
		batchByInvoke:    make(map[string]runs.AdmissionBatch, len(state.routing.Invocations)),
		legacyRuns:       make(map[string]agentrun.Run, len(state.runs.Runs)),
		runByInvocation:  make(map[string]agentrun.Run, len(state.runs.Runs)),
	}
	for _, record := range state.wireRecords {
		if record.Sequence == 0 || index.wireByID[record.Event.ID].Sequence != 0 {
			return runSourceIndex{}, errors.New("migration: duplicate wire identity")
		}
		index.wireByID[record.Event.ID] = record
	}
	for _, invocation := range state.routing.Invocations {
		if invocation.ID == "" || index.invocations[invocation.ID].ID != "" {
			return runSourceIndex{}, errors.New("migration: duplicate invocation identity")
		}
		if _, err := normalizedLegacyTask(invocation.Task, invocation.IssueIdentifier); err != nil {
			return runSourceIndex{}, fmt.Errorf("migration: invocation %s: %w", invocation.ID, err)
		}
		index.invocations[invocation.ID] = invocation.Clone()
	}

	realSequences := make(map[uint64]string)
	for _, decision := range state.routing.Decisions {
		if decision.EventID == "" || index.decisions[decision.EventID].EventID != "" {
			return runSourceIndex{}, errors.New("migration: duplicate Decision event ID")
		}
		if decision.SettingsRevision == 0 || decision.SettingsRevision > policyRevision || decision.RegistryRevision > registryRevision {
			return runSourceIndex{}, fmt.Errorf("migration: Decision %s conflicts with canonical policy revisions", decision.EventID)
		}
		origin, err := decisionOrigin(decision, index.invocations)
		if err != nil {
			return runSourceIndex{}, err
		}
		if origin == runs.AdmissionOriginEvent {
			if previous := realSequences[decision.EventSequence]; previous != "" {
				return runSourceIndex{}, fmt.Errorf("migration: Decision event sequence %d belongs to %s and %s", decision.EventSequence, previous, decision.EventID)
			}
			record, found := index.wireByID[decision.EventID]
			if !found || record.Sequence != decision.EventSequence || record.Event.Source != decision.Source {
				return runSourceIndex{}, fmt.Errorf("migration: Decision %s conflicts with the event wire", decision.EventID)
			}
			realSequences[decision.EventSequence] = decision.EventID
		}
		batch := runs.AdmissionBatch{
			ID:     digestRunParts("factory-runs-admission-batch-v1", string(origin), decision.EventID),
			Origin: origin, EventID: decision.EventID, EventSource: decision.Source,
			RegistryRevision: decision.RegistryRevision, SettingsRevision: decision.SettingsRevision,
			PolicyGeneration: policyGeneration, DecidedAt: decision.DecidedAt.UTC(),
		}
		if origin == runs.AdmissionOriginEvent {
			batch.EventSequence = decision.EventSequence
		}
		owned := make(map[string]bool)
		for _, outcome := range decision.Outcomes {
			if outcome.Kind != triggerrouter.OutcomeInvocation {
				continue
			}
			invocation, found := index.invocations[outcome.InvocationID]
			if !found || owned[outcome.InvocationID] || index.decisionByInvoke[outcome.InvocationID].EventID != "" ||
				invocation.EventID != decision.EventID || invocation.EventSequence != decision.EventSequence ||
				invocation.Rule.ID != outcome.RuleID || invocation.Rule.Revision != outcome.RuleRevision {
				return runSourceIndex{}, errors.New("migration: orphan or multiply-owned invocation")
			}
			if err := validateInvocationIdentity(invocation, origin); err != nil {
				return runSourceIndex{}, err
			}
			owned[outcome.InvocationID] = true
			index.decisionByInvoke[outcome.InvocationID] = decision.Clone()
			index.batchByInvoke[outcome.InvocationID] = batch
		}
		index.decisions[decision.EventID] = decision.Clone()
	}
	if len(index.decisionByInvoke) != len(index.invocations) {
		return runSourceIndex{}, errors.New("migration: orphan invocation")
	}

	transitionIDs := make(map[string]bool)
	for _, legacy := range state.runs.Runs {
		if legacy.ID == "" || index.legacyRuns[legacy.ID].ID != "" {
			return runSourceIndex{}, errors.New("migration: duplicate legacy Run ID")
		}
		if _, err := normalizedLegacyTask(legacy.Task, legacy.IssueIdentifier); err != nil {
			return runSourceIndex{}, fmt.Errorf("migration: Run %s: %w", legacy.ID, err)
		}
		if legacy.InvocationID != "" {
			if previous := index.runByInvocation[legacy.InvocationID]; previous.ID != "" {
				return runSourceIndex{}, fmt.Errorf("migration: invocation %s owns Runs %s and %s", legacy.InvocationID, previous.ID, legacy.ID)
			}
			index.runByInvocation[legacy.InvocationID] = legacy
		}
		for _, transition := range legacy.Transitions {
			if transition.ID == "" || transitionIDs[transition.ID] {
				return runSourceIndex{}, fmt.Errorf("migration: Run %s has duplicate transition evidence", legacy.ID)
			}
			transitionIDs[transition.ID] = true
			record, found := index.wireByID["factory:run-transition:"+transition.ID]
			if !found || record.Sequence > state.wireDispatched || !exactTransitionEvent(record.Event, legacy, transition) {
				return runSourceIndex{}, fmt.Errorf("migration: Run %s transition %s is not exactly globally dispatched", legacy.ID, transition.ID)
			}
			index.transitionEvidence = append(index.transitionEvidence, fmt.Sprintf("%s|%d", record.Event.ID, record.Sequence))
		}
		index.legacyRuns[legacy.ID] = legacy
	}
	return index, nil
}

func decisionOrigin(decision triggerrouter.Decision, invocations map[string]triggerrouter.Invocation) (runs.AdmissionOrigin, error) {
	origin := runs.AdmissionOriginEvent
	originSet := false
	for _, outcome := range decision.Outcomes {
		if outcome.Kind != triggerrouter.OutcomeInvocation {
			continue
		}
		invocation, found := invocations[outcome.InvocationID]
		if !found {
			return "", fmt.Errorf("migration: Decision %s references a missing invocation", decision.EventID)
		}
		candidate := invocationOrigin(invocation)
		if originSet && origin != candidate {
			return "", fmt.Errorf("migration: Decision %s mixes admission origins", decision.EventID)
		}
		origin = candidate
		originSet = true
	}
	if origin != runs.AdmissionOriginEvent {
		if decision.Source != eventwire.SourceFactory || len(decision.Outcomes) != 1 || decision.Outcomes[0].Kind != triggerrouter.OutcomeInvocation {
			return "", fmt.Errorf("migration: synthetic Decision %s has invalid provenance", decision.EventID)
		}
	}
	return origin, nil
}

func invocationOrigin(invocation triggerrouter.Invocation) runs.AdmissionOrigin {
	if triggerrouter.NativeFeedbackInvocation(invocation) {
		return runs.AdmissionOriginContinuation
	}
	if legacyNativeInvocation(invocation) {
		return runs.AdmissionOriginNative
	}
	return runs.AdmissionOriginEvent
}

func validateInvocationIdentity(invocation triggerrouter.Invocation, origin runs.AdmissionOrigin) error {
	want := ""
	switch origin {
	case runs.AdmissionOriginEvent:
		want = digestRunParts("factory-trigger-invocation-v1", invocation.EventID, invocation.Rule.ID, strconv.FormatUint(invocation.Rule.Revision, 10))
	case runs.AdmissionOriginNative:
		if invocation.EventID != "factory:native-start:"+invocation.Task.ProviderID {
			return fmt.Errorf("migration: native invocation %s has an invalid event identity", invocation.ID)
		}
		want = digestRunParts("factory-native-invocation-v1", invocation.Task.OwnershipKey(), invocation.WorkflowDigest, "start")
	case runs.AdmissionOriginContinuation:
		// The legacy event ID retains only a truncated event-key digest, so the
		// original full idempotency key cannot be reconstructed. The durable ID
		// is preserved after NativeFeedbackInvocation validates the remaining
		// exact continuation shape.
		if !triggerrouter.NativeFeedbackInvocation(invocation) {
			return fmt.Errorf("migration: continuation invocation %s has an invalid event identity", invocation.ID)
		}
	}
	if want != "" && invocation.ID != want {
		return fmt.Errorf("migration: invocation %s deterministic identity conflicts", invocation.ID)
	}
	return nil
}

func convertDecision(
	decision triggerrouter.Decision,
	index runSourceIndex,
	converted map[string]runs.Run,
	policyGeneration uint64,
) (runs.AdmissionBatch, error) {
	origin, err := decisionOrigin(decision, index.invocations)
	if err != nil {
		return runs.AdmissionBatch{}, err
	}
	batch := runs.AdmissionBatch{
		ID:     digestRunParts("factory-runs-admission-batch-v1", string(origin), decision.EventID),
		Origin: origin, EventID: decision.EventID, EventSource: decision.Source,
		RegistryRevision: decision.RegistryRevision, SettingsRevision: decision.SettingsRevision,
		PolicyGeneration: policyGeneration, DecidedAt: decision.DecidedAt.UTC(),
		Outcomes: make([]runs.AdmissionOutcome, 0, len(decision.Outcomes)),
	}
	if origin == runs.AdmissionOriginEvent {
		batch.EventSequence = decision.EventSequence
	}
	for _, legacy := range decision.Outcomes {
		outcome := runs.AdmissionOutcome{RuleID: legacy.RuleID, RuleRevision: legacy.RuleRevision, Reason: legacy.Reason}
		switch legacy.Kind {
		case triggerrouter.OutcomeInvocation:
			convertedRun, found := converted[legacy.InvocationID]
			if !found {
				return runs.AdmissionBatch{}, fmt.Errorf("migration: Decision %s has an unconverted invocation", decision.EventID)
			}
			outcome.Kind = runs.AdmissionOutcomeRun
			outcome.AdmissionID = legacy.InvocationID
			outcome.RunID = convertedRun.ID
			outcome.Reason = ""
		case triggerrouter.OutcomeRejected:
			outcome.Kind = runs.AdmissionOutcomeRejected
		case triggerrouter.OutcomeSuppressed:
			outcome.Kind = runs.AdmissionOutcomeSuppressed
		default:
			return runs.AdmissionBatch{}, fmt.Errorf("migration: Decision %s has unknown outcome %q", decision.EventID, legacy.Kind)
		}
		batch.Outcomes = append(batch.Outcomes, outcome)
	}
	return batch, nil
}

func convertInvocationRun(
	invocation triggerrouter.Invocation,
	decision triggerrouter.Decision,
	batch runs.AdmissionBatch,
	legacy agentrun.Run,
	linked bool,
	index runSourceIndex,
	repositoriesState repositories.SourceState,
) (runs.Run, bool, error) {
	deterministicRunID := "run-" + digestRunParts("factory-trigger-run-v1", invocation.ID)[:16]
	if invocation.State == triggerrouter.StateRejected && invocation.Reason == "native-feedback-coalesced" {
		if !triggerrouter.NativeFeedbackInvocation(invocation) || linked || invocation.RunID == "" {
			return runs.Run{}, false, fmt.Errorf("migration: coalesced invocation %s is ambiguous", invocation.ID)
		}
		owner, found := index.legacyRuns[invocation.RunID]
		ownerTask, ownerTaskErr := normalizedLegacyTask(owner.Task, owner.IssueIdentifier)
		invocationTask, invocationTaskErr := normalizedLegacyTask(invocation.Task, invocation.IssueIdentifier)
		if !found || ownerTaskErr != nil || invocationTaskErr != nil || !owner.State.Nonterminal() || ownerTask != invocationTask || !slices.Contains(owner.DeliveryIDs, invocation.EventID) ||
			owner.InvocationID == "" || invocation.ReflectedAt == nil || !invocation.ReflectedAt.Equal(invocation.UpdatedAt) || !owner.UpdatedAt.Equal(invocation.UpdatedAt) {
			return runs.Run{}, false, fmt.Errorf("migration: coalesced invocation %s does not exactly match its owner", invocation.ID)
		}
		converted, err := synthesizeInvocationRun(invocation, decision, batch, deterministicRunID, runs.StateRejected)
		if err != nil {
			return runs.Run{}, false, err
		}
		converted.Causation.ParentAdmissionID = owner.InvocationID
		converted.Causation.ParentRunID = owner.ID
		converted.Detail = invocation.Reason
		converted.MigratedBaseline.RepositoryRouteUnavailable = true
		return converted, true, nil
	}

	switch invocation.State {
	case triggerrouter.StateQueued:
		if linked || invocation.RunID != "" {
			return runs.Run{}, false, fmt.Errorf("migration: queued invocation %s unexpectedly owns a Run", invocation.ID)
		}
		converted, err := synthesizeInvocationRun(invocation, decision, batch, deterministicRunID, runs.StateAdmitted)
		return converted, true, err
	case triggerrouter.StateClaiming:
		if invocation.RunID == "" || invocation.RunID != deterministicRunID {
			return runs.Run{}, false, fmt.Errorf("migration: claiming invocation %s has an invalid deterministic Run ID", invocation.ID)
		}
		if !linked {
			converted, err := synthesizeInvocationRun(invocation, decision, batch, invocation.RunID, runs.StateRouting)
			return converted, true, err
		}
		if legacy.State != agentrun.StatePending {
			return runs.Run{}, false, fmt.Errorf("migration: claiming invocation %s is linked to non-pending Run %s", invocation.ID, legacy.ID)
		}
	case triggerrouter.StateClaimed:
		if !linked {
			return runs.Run{}, false, fmt.Errorf("migration: claimed invocation %s is missing its Run", invocation.ID)
		}
	case triggerrouter.StateSucceeded, triggerrouter.StateBlocked, triggerrouter.StateFailed:
		if !linked {
			if invocation.ReflectedAt == nil || invocation.ReflectedAt.After(invocation.UpdatedAt) {
				return runs.Run{}, false, fmt.Errorf("migration: terminal invocation %s has invalid reflection evidence", invocation.ID)
			}
			state, _ := invocationLifecycleState(invocation.State)
			converted, err := synthesizeInvocationRun(invocation, decision, batch, deterministicRunID, state)
			if err == nil {
				converted.Detail = invocation.Reason
				converted.MigratedBaseline.RepositoryRouteUnavailable = true
			}
			return converted, true, err
		}
	case triggerrouter.StateRejected:
		if linked {
			return runs.Run{}, false, fmt.Errorf("migration: rejected invocation %s unexpectedly owns a distinct Run", invocation.ID)
		}
		if invocation.Reason != "repository-routing-rejected" {
			return runs.Run{}, false, fmt.Errorf("migration: rejected invocation %s has unsupported evidence", invocation.ID)
		}
		if invocation.RunID != "" && invocation.RunID != deterministicRunID {
			return runs.Run{}, false, fmt.Errorf("migration: rejected invocation %s has an invalid deterministic Run ID", invocation.ID)
		}
		converted, err := synthesizeInvocationRun(invocation, decision, batch, deterministicRunID, runs.StateRejected)
		if err == nil {
			converted.Detail = invocation.Reason
			if invocation.Reason == "repository-routing-rejected" {
				converted.RepositoryRejection = invocation.Reason
			}
			converted.MigratedBaseline.RepositoryRouteUnavailable = true
		}
		return converted, true, err
	default:
		return runs.Run{}, false, fmt.Errorf("migration: invocation %s has unsupported state %q", invocation.ID, invocation.State)
	}

	converted, err := convertLinkedRun(invocation, decision, batch, legacy, repositoriesState)
	return converted, false, err
}

func synthesizeInvocationRun(
	invocation triggerrouter.Invocation,
	decision triggerrouter.Decision,
	batch runs.AdmissionBatch,
	runID string,
	state runs.LifecycleState,
) (runs.Run, error) {
	task, err := normalizedLegacyTask(invocation.Task, invocation.IssueIdentifier)
	if err != nil {
		return runs.Run{}, fmt.Errorf("migration: invocation %s: %w", invocation.ID, err)
	}
	pin := invocation.Workflow.Clone()
	if err := validateConvertedPin(&pin, invocation.WorkflowDigest, state.Nonterminal(), false); err != nil {
		return runs.Run{}, fmt.Errorf("migration: invocation %s: %w", invocation.ID, err)
	}
	converted := runs.Run{
		ID: runID,
		Causation: runs.Causation{
			AdmissionID: invocation.ID, BatchID: batch.ID, EventID: invocation.EventID,
			EventSource: decision.Source, RuleID: invocation.Rule.ID, RuleRevision: invocation.Rule.Revision,
			Workflow: &pin, WorkflowDigest: invocation.WorkflowDigest,
			PolicyRevision: invocation.PolicyRevision, PolicyGeneration: batch.PolicyGeneration,
			Task: task, RootEventID: invocation.RootEventID,
			ParentAdmissionID: invocation.ParentInvocationID, ParentRunID: invocation.ParentRunID,
			Hop: invocation.Hop, AncestorRuleIDs: slices.Clone(invocation.AncestorRuleIDs),
			AdmittedAt: invocation.AdmittedAt.UTC(),
		},
		MigratedBaseline: &runs.MigratedBaseline{
			State: state, ObservedAt: invocation.UpdatedAt.UTC(), PriorTransitionsAcknowledged: true,
		},
		TriggerKind: synthesizedTriggerKind(batch.Origin), DeliveryIDs: []string{invocation.EventID},
		State: state, CreatedAt: invocation.AdmittedAt.UTC(), UpdatedAt: invocation.UpdatedAt.UTC(),
		Transitions: []runs.LifecycleTransition{},
	}
	if batch.Origin == runs.AdmissionOriginEvent {
		converted.Causation.EventSequence = decision.EventSequence
	}
	if state.Terminal() {
		finished := invocation.UpdatedAt.UTC()
		converted.FinishedAt = &finished
	}
	return converted, nil
}

func convertLinkedRun(
	invocation triggerrouter.Invocation,
	decision triggerrouter.Decision,
	batch runs.AdmissionBatch,
	legacy agentrun.Run,
	repositoriesState repositories.SourceState,
) (runs.Run, error) {
	if invocation.RunID != legacy.ID || legacy.InvocationID != invocation.ID {
		return runs.Run{}, fmt.Errorf("migration: invocation %s and Run %s linkage disagree", invocation.ID, legacy.ID)
	}
	task, err := normalizedLegacyTask(legacy.Task, legacy.IssueIdentifier)
	if err != nil {
		return runs.Run{}, fmt.Errorf("migration: Run %s: %w", legacy.ID, err)
	}
	invocationTask, err := normalizedLegacyTask(invocation.Task, invocation.IssueIdentifier)
	if err != nil || task != invocationTask {
		return runs.Run{}, fmt.Errorf("migration: invocation %s and Run %s task identities disagree", invocation.ID, legacy.ID)
	}
	if err := validateExactReadyTask(legacy, task); err != nil {
		return runs.Run{}, err
	}
	if legacy.InvocationRootEventID != invocation.RootEventID || legacy.InvocationHop != invocation.Hop ||
		!slices.Equal(legacy.InvocationAncestorRuleIDs, invocation.AncestorRuleIDs) ||
		!slices.Contains(legacy.DeliveryIDs, invocation.EventID) || legacy.PinnedPolicyRevision != invocation.PolicyRevision ||
		invocation.PolicyRevision != decision.SettingsRevision {
		return runs.Run{}, fmt.Errorf("migration: invocation %s and Run %s causation disagree", invocation.ID, legacy.ID)
	}
	state, err := legacyLifecycleState(legacy.State)
	if err != nil {
		return runs.Run{}, fmt.Errorf("migration: Run %s: %w", legacy.ID, err)
	}
	if err := validateLinkedTerminalState(invocation, legacy, state); err != nil {
		return runs.Run{}, err
	}
	pin, digest, err := mergeLinkedWorkflow(invocation, legacy, state.Nonterminal())
	if err != nil {
		return runs.Run{}, fmt.Errorf("migration: invocation %s and Run %s workflow: %w", invocation.ID, legacy.ID, err)
	}
	route, historical, unavailable, err := convertLegacyRoute(legacy, repositoriesState, state.Nonterminal())
	if err != nil {
		return runs.Run{}, err
	}
	if legacy.DuplicateTriggers != uint64(len(legacy.DeliveryIDs)-1) || !uniqueStrings(legacy.DeliveryIDs) {
		return runs.Run{}, fmt.Errorf("migration: Run %s delivery evidence is invalid", legacy.ID)
	}
	baseline := &runs.MigratedBaseline{
		State: state, ObservedAt: legacy.UpdatedAt.UTC(), PriorTransitionsAcknowledged: true,
		HistoricalRepository: historical, RepositoryRouteUnavailable: unavailable,
	}
	converted := runs.Run{
		ID: legacy.ID,
		Causation: runs.Causation{
			AdmissionID: invocation.ID, BatchID: batch.ID, EventID: invocation.EventID,
			EventSource: decision.Source, RuleID: invocation.Rule.ID, RuleRevision: invocation.Rule.Revision,
			Workflow: pin, WorkflowDigest: digest, PolicyRevision: invocation.PolicyRevision,
			PolicyGeneration: batch.PolicyGeneration, Task: task, RootEventID: invocation.RootEventID,
			ParentAdmissionID: invocation.ParentInvocationID, ParentRunID: invocation.ParentRunID,
			Hop: invocation.Hop, AncestorRuleIDs: slices.Clone(invocation.AncestorRuleIDs),
			AdmittedAt: invocation.AdmittedAt.UTC(),
		},
		MigratedBaseline: baseline, Repository: route, TriggerKind: legacy.TriggerKind,
		DeliveryIDs: slices.Clone(legacy.DeliveryIDs), DuplicateDeliveries: legacy.DuplicateTriggers,
		State: state, SessionName: legacy.SessionName, RunDirectory: legacy.RunDirectory,
		Attempts: legacy.Attempts, Detail: legacy.Detail, CreatedAt: legacy.CreatedAt.UTC(), UpdatedAt: legacy.UpdatedAt.UTC(),
		StartedAt: cloneRunTime(legacy.StartedAt), SegmentStartedAt: cloneRunTime(legacy.SegmentStartedAt),
		SegmentAttempt: legacy.SegmentAttempt, FinishedAt: cloneRunTime(legacy.FinishedAt),
		Transitions: []runs.LifecycleTransition{}, Ready: convertReady(legacy.Ready),
		MergeCommitOID: legacy.MergeCommitOID,
		GitHub: runs.GitHubState{
			LastCursor:                 legacy.LastGitHubCursor,
			LastAuthoritativeRefreshAt: cloneRunTime(legacy.LastAuthoritativeRefreshAt),
			NextReconcileAt:            cloneRunTime(legacy.NextReconcileAt),
			ReconcileFailures:          legacy.ReconcileFailures, RemediationRequested: legacy.RemediationRequested,
		},
		ResumeCount: legacy.ResumeCount, TerminalIntent: legacy.TerminalIntent,
		TerminalRejection: legacy.TerminalRejection, Completion: convertCompletion(legacy.Completion),
	}
	if batch.Origin == runs.AdmissionOriginEvent {
		converted.Causation.EventSequence = decision.EventSequence
	}
	return converted, nil
}

func convertDirectRun(
	legacy agentrun.Run,
	policyGeneration uint64,
	repositoriesState repositories.SourceState,
) (runs.AdmissionBatch, runs.Run, error) {
	state, err := legacyLifecycleState(legacy.State)
	if err != nil || state.Nonterminal() {
		return runs.AdmissionBatch{}, runs.Run{}, fmt.Errorf("migration: direct Run %s is not a supported terminal Run", legacy.ID)
	}
	task, err := normalizedLegacyTask(legacy.Task, legacy.IssueIdentifier)
	if err != nil {
		return runs.AdmissionBatch{}, runs.Run{}, fmt.Errorf("migration: direct Run %s: %w", legacy.ID, err)
	}
	if err := validateExactReadyTask(legacy, task); err != nil {
		return runs.AdmissionBatch{}, runs.Run{}, err
	}
	eventID := "factory:migrated-direct:" + legacy.ID
	admissionID := legacy.InvocationID
	if admissionID == "" {
		admissionID = digestRunParts("factory-runs-migrated-direct-admission-v1", legacy.ID)
	}
	batch := runs.AdmissionBatch{
		ID:     digestRunParts("factory-runs-admission-batch-v1", string(runs.AdmissionOriginMigratedDirect), eventID),
		Origin: runs.AdmissionOriginMigratedDirect, EventID: eventID, EventSource: eventwire.SourceFactory,
		PolicyGeneration: policyGeneration, SettingsRevision: legacy.PinnedPolicyRevision,
		DecidedAt: legacy.CreatedAt.UTC(),
		Outcomes: []runs.AdmissionOutcome{{
			Kind: runs.AdmissionOutcomeRun, RuleID: migratedDirectRuleID, RuleRevision: 1,
			AdmissionID: admissionID, RunID: legacy.ID,
		}},
	}
	baseline := &runs.MigratedBaseline{
		State: state, ObservedAt: legacy.UpdatedAt.UTC(), PriorTransitionsAcknowledged: true,
	}
	if legacy.InvocationReflectedAt != nil &&
		(legacy.InvocationID == "" || !legacy.InvocationReflectedAt.Equal(legacy.UpdatedAt)) {
		return runs.AdmissionBatch{}, runs.Run{}, fmt.Errorf("migration: direct Run %s has invalid reflection evidence", legacy.ID)
	}
	var pin *workflow.Pinned
	digest := legacy.PinnedWorkflowDigest
	if legacy.PinnedWorkflow == nil {
		if digest != "" {
			return runs.AdmissionBatch{}, runs.Run{}, fmt.Errorf("migration: direct Run %s has a digest without a workflow pin", legacy.ID)
		}
		baseline.WorkflowPinUnavailable = true
	} else {
		clone := legacy.PinnedWorkflow.Clone()
		pin = &clone
		if digest == "" && compactPinned(clone) {
			baseline.WorkflowDigestUnavailable = true
		}
		if err := validateConvertedPin(pin, digest, false, baseline.WorkflowDigestUnavailable); err != nil {
			return runs.AdmissionBatch{}, runs.Run{}, fmt.Errorf("migration: direct Run %s: %w", legacy.ID, err)
		}
	}
	route, historical, unavailable, err := convertLegacyRoute(legacy, repositoriesState, false)
	if err != nil {
		return runs.AdmissionBatch{}, runs.Run{}, err
	}
	baseline.HistoricalRepository = historical
	baseline.RepositoryRouteUnavailable = unavailable
	if legacy.DuplicateTriggers != uint64(len(legacy.DeliveryIDs)-1) || !uniqueStrings(legacy.DeliveryIDs) {
		return runs.AdmissionBatch{}, runs.Run{}, fmt.Errorf("migration: direct Run %s delivery evidence is invalid", legacy.ID)
	}
	converted := runs.Run{
		ID: legacy.ID,
		Causation: runs.Causation{
			AdmissionID: admissionID, BatchID: batch.ID, EventID: eventID, EventSource: eventwire.SourceFactory,
			RuleID: migratedDirectRuleID, RuleRevision: 1, Workflow: pin, WorkflowDigest: digest,
			PolicyRevision: legacy.PinnedPolicyRevision, PolicyGeneration: policyGeneration,
			Task: task, RootEventID: eventID, Hop: 0, AncestorRuleIDs: []string{}, AdmittedAt: legacy.CreatedAt.UTC(),
		},
		MigratedBaseline: baseline, Repository: route, TriggerKind: legacy.TriggerKind,
		DeliveryIDs: slices.Clone(legacy.DeliveryIDs), DuplicateDeliveries: legacy.DuplicateTriggers,
		State: state, SessionName: legacy.SessionName, RunDirectory: legacy.RunDirectory,
		Attempts: legacy.Attempts, Detail: legacy.Detail, CreatedAt: legacy.CreatedAt.UTC(), UpdatedAt: legacy.UpdatedAt.UTC(),
		StartedAt: cloneRunTime(legacy.StartedAt), SegmentStartedAt: cloneRunTime(legacy.SegmentStartedAt),
		SegmentAttempt: legacy.SegmentAttempt, FinishedAt: cloneRunTime(legacy.FinishedAt),
		Transitions: []runs.LifecycleTransition{}, Ready: convertReady(legacy.Ready),
		MergeCommitOID: legacy.MergeCommitOID,
		GitHub: runs.GitHubState{
			LastCursor:                 legacy.LastGitHubCursor,
			LastAuthoritativeRefreshAt: cloneRunTime(legacy.LastAuthoritativeRefreshAt),
			NextReconcileAt:            cloneRunTime(legacy.NextReconcileAt),
			ReconcileFailures:          legacy.ReconcileFailures, RemediationRequested: legacy.RemediationRequested,
		},
		ResumeCount: legacy.ResumeCount, TerminalIntent: legacy.TerminalIntent,
		TerminalRejection: legacy.TerminalRejection, Completion: convertCompletion(legacy.Completion),
	}
	return batch, converted, nil
}

func validateLinkedTerminalState(invocation triggerrouter.Invocation, legacy agentrun.Run, state runs.LifecycleState) error {
	invocationState, terminalInvocation := invocationLifecycleState(invocation.State)
	if terminalInvocation && invocationState != state {
		return fmt.Errorf("migration: invocation %s and Run %s terminal states disagree", invocation.ID, legacy.ID)
	}
	if terminalInvocation {
		if invocation.Reason != legacy.Detail || invocation.ReflectedAt == nil || legacy.InvocationReflectedAt == nil ||
			!invocation.ReflectedAt.Equal(*legacy.InvocationReflectedAt) ||
			!invocation.ReflectedAt.Equal(invocation.UpdatedAt) ||
			!legacy.InvocationReflectedAt.Equal(legacy.UpdatedAt) {
			return fmt.Errorf("migration: invocation %s and Run %s reflection receipts disagree", invocation.ID, legacy.ID)
		}
	} else if legacy.InvocationReflectedAt != nil {
		return fmt.Errorf("migration: Run %s has a reflection receipt before invocation terminal state", legacy.ID)
	}
	if invocation.State == triggerrouter.StateClaimed && !state.Nonterminal() {
		return nil
	}
	if invocation.State == triggerrouter.StateClaiming && legacy.State == agentrun.StatePending {
		return nil
	}
	if invocation.State == triggerrouter.StateClaimed && state.Nonterminal() || terminalInvocation {
		return nil
	}
	return fmt.Errorf("migration: invocation %s state %q conflicts with Run %s state %q", invocation.ID, invocation.State, legacy.ID, legacy.State)
}

func mergeLinkedWorkflow(invocation triggerrouter.Invocation, legacy agentrun.Run, active bool) (*workflow.Pinned, string, error) {
	if legacy.PinnedWorkflow == nil || invocation.WorkflowDigest == "" || legacy.PinnedWorkflowDigest == "" ||
		invocation.WorkflowDigest != legacy.PinnedWorkflowDigest {
		return nil, "", errors.New("pin or digest is missing or mismatched")
	}
	if legacy.PinnedPolicyRevision != invocation.PolicyRevision {
		return nil, "", errors.New("policy revision is mismatched")
	}
	left := invocation.Workflow.Clone()
	right := legacy.PinnedWorkflow.Clone()
	if left.ID != right.ID || left.Revision != right.Revision {
		return nil, "", errors.New("pin identity is mismatched")
	}
	leftComplete, rightComplete := left.Complete(), right.Complete()
	if leftComplete && rightComplete && !reflect.DeepEqual(left, right) {
		return nil, "", errors.New("complete pins disagree")
	}
	selected := left
	if rightComplete {
		selected = right
	}
	if active && !selected.Complete() {
		return nil, "", errors.New("active pin is compacted")
	}
	if err := validateConvertedPin(&selected, invocation.WorkflowDigest, active, false); err != nil {
		return nil, "", err
	}
	return &selected, invocation.WorkflowDigest, nil
}

func validateConvertedPin(pin *workflow.Pinned, digest string, active, digestUnavailable bool) error {
	if pin == nil {
		return errors.New("workflow pin is unavailable")
	}
	if pin.Complete() {
		if digest == "" {
			return errors.New("workflow digest is unavailable")
		}
		actual, err := pin.Digest()
		if err != nil || actual != digest {
			return errors.New("workflow pin conflicts with its digest")
		}
		return nil
	}
	if active || !compactPinned(*pin) || digest == "" && !digestUnavailable {
		return errors.New("compacted workflow pin is not migration-safe")
	}
	if digest != "" && len(digest) != 64 {
		return errors.New("workflow digest is invalid")
	}
	return nil
}

func compactPinned(pin workflow.Pinned) bool {
	return workflow.ValidID(pin.ID) && pin.Name == "" && !pin.Enabled && pin.Markdown == "" && pin.UpdatedAt == nil && pin.Runner == "" && len(pin.Steps) == 0
}

func convertLegacyRoute(
	legacy agentrun.Run,
	state repositories.SourceState,
	requireActive bool,
) (*repositories.Route, *runs.HistoricalRepository, bool, error) {
	if legacy.Repository == "" {
		if legacy.RepositoryURL != "" || legacy.RepositoryPath != "" || legacy.ManagedRoot != "" || legacy.BaseBranch != "" || legacy.Bootstrap || legacy.CloudURL != "" {
			return nil, nil, false, fmt.Errorf("migration: Run %s has a partial repository route", legacy.ID)
		}
		if requireActive {
			return nil, nil, false, fmt.Errorf("migration: active Run %s is missing its repository route", legacy.ID)
		}
		return nil, nil, true, nil
	}
	for _, record := range state.Records {
		if record.Project.IsZero() || !record.Routable() || record.Repository != legacy.Repository || record.Origin != legacy.RepositoryURL ||
			record.ManagedPath != legacy.RepositoryPath || record.ManagedRoot != legacy.ManagedRoot ||
			record.DefaultBranch != legacy.BaseBranch || record.Bootstrap != legacy.Bootstrap || record.CloudURL != legacy.CloudURL {
			continue
		}
		route := record.Route()
		return &route, nil, false, nil
	}
	if requireActive {
		return nil, nil, false, fmt.Errorf("migration: active Run %s repository route is not exactly admitted", legacy.ID)
	}
	return nil, &runs.HistoricalRepository{
		Repository: legacy.Repository, Origin: legacy.RepositoryURL,
		ManagedPath: legacy.RepositoryPath, ManagedRoot: legacy.ManagedRoot,
		DefaultBranch: legacy.BaseBranch, Bootstrap: legacy.Bootstrap, CloudURL: legacy.CloudURL,
	}, false, nil
}

func legacyLifecycleState(state agentrun.State) (runs.LifecycleState, error) {
	switch state {
	case agentrun.StatePending:
		return runs.StatePending, nil
	case agentrun.StatePostMergePending:
		return runs.StatePostMergePending, nil
	case agentrun.StateStarting:
		return runs.StateStarting, nil
	case agentrun.StateRunning:
		return runs.StateRunning, nil
	case agentrun.StateAwaitingMerge:
		return runs.StateAwaitingHumanMerge, nil
	case agentrun.StateSucceeded:
		return runs.StateSucceeded, nil
	case agentrun.StateBlocked:
		return runs.StateBlocked, nil
	case agentrun.StateFailed:
		return runs.StateFailed, nil
	default:
		return "", fmt.Errorf("unsupported lifecycle state %q", state)
	}
}

func invocationLifecycleState(state string) (runs.LifecycleState, bool) {
	switch state {
	case triggerrouter.StateSucceeded:
		return runs.StateSucceeded, true
	case triggerrouter.StateBlocked:
		return runs.StateBlocked, true
	case triggerrouter.StateFailed:
		return runs.StateFailed, true
	case triggerrouter.StateRejected:
		return runs.StateRejected, true
	default:
		return "", false
	}
}

func synthesizedTriggerKind(origin runs.AdmissionOrigin) string {
	if origin == runs.AdmissionOriginContinuation {
		return agentrun.TriggerKindComment
	}
	return agentrun.TriggerKindRule
}

func normalizedLegacyTask(task taskmodel.TaskRef, issueIdentifier string) (taskmodel.TaskRef, error) {
	resolved, err := taskmodel.ResolveCompatibilityIdentity(task, issueIdentifier)
	if err != nil {
		return taskmodel.TaskRef{}, fmt.Errorf("invalid task identity: %w", err)
	}
	return resolved, nil
}

func validateExactReadyTask(legacy agentrun.Run, task taskmodel.TaskRef) error {
	if legacy.Ready == nil || legacy.Ready.Task.IsZero() {
		return nil
	}
	readyTask, err := legacy.Ready.Task.Normalize()
	if err != nil || readyTask != task {
		return fmt.Errorf("migration: Run %s ready task identity disagrees", legacy.ID)
	}
	return nil
}

func convertReady(value *agentrun.ReadyCheckpoint) *runs.ReadyCheckpoint {
	if value == nil {
		return nil
	}
	return &runs.ReadyCheckpoint{
		ContractVersion: value.ContractVersion, RunID: value.RunID, Task: value.Task,
		Repository: value.Repository, PullRequest: value.PullRequest, BaseBranch: value.BaseBranch,
		HeadBranch: value.HeadBranch, VerifiedHeadOID: value.VerifiedHeadOID,
		PullRequestUpdatedAt: value.PullRequestUpdatedAt, CreatedAt: value.CreatedAt, ValidatedAt: value.ValidatedAt,
	}
}

func convertCompletion(value *agentrun.CompletionValidation) *runs.CompletionValidation {
	if value == nil {
		return nil
	}
	state, err := legacyLifecycleState(value.State)
	if err != nil {
		state = runs.LifecycleState(value.State)
	}
	return &runs.CompletionValidation{
		Accepted: value.Accepted, Intent: value.Intent, Blocker: value.Blocker, State: state,
		Reason: value.Reason, ValidatedAt: value.ValidatedAt, PullRequestState: value.PullRequestState,
		PullRequestHead: value.PullRequestHead, MergeCommitOID: value.MergeCommitOID,
		DeploymentID: value.DeploymentID, DeploymentCommit: value.DeploymentCommit,
	}
}

func cloneRunTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := value.UTC()
	return &clone
}

func exactTransitionEvent(event eventwire.Event, legacy agentrun.Run, transition agentrun.Transition) bool {
	if event.ID != "factory:run-transition:"+transition.ID || event.Source != eventwire.SourceFactory ||
		event.Type != "agent-run" || event.Action != string(transition.State) || event.Subject != legacy.IssueIdentifier ||
		!event.ReceivedAt.Equal(transition.At) || !exactAttribute(event, "runId", legacy.ID) ||
		!exactAttribute(event, "attempts", strconv.Itoa(transition.Attempts)) ||
		!exactAttribute(event, "taskSource", string(legacy.Task.Source)) ||
		!exactAttribute(event, "taskProviderId", legacy.Task.ProviderID) ||
		!exactAttribute(event, "taskIdentifier", legacy.Task.Identifier) ||
		!exactAttribute(event, eventwire.AttributeProducer, "agent-collector") ||
		!exactAttribute(event, eventwire.AttributeProvenance, "factory") {
		return false
	}
	if legacy.InvocationID == "" {
		return event.RootEventID == event.ID && event.ParentInvocationID == "" && event.ParentRunID == "" && event.Hop == 0 && len(event.AncestorRuleIDs) == 0
	}
	return event.RootEventID == legacy.InvocationRootEventID && event.ParentInvocationID == legacy.InvocationID &&
		event.ParentRunID == legacy.ID && event.Hop == legacy.InvocationHop &&
		slices.Equal(event.AncestorRuleIDs, legacy.InvocationAncestorRuleIDs)
}

func exactAttribute(event eventwire.Event, key, value string) bool {
	return slices.Equal(event.Attributes[key], []string{value})
}

func reflectionIdentity(invocation triggerrouter.Invocation, legacy agentrun.Run) string {
	invoked, run := "", ""
	if invocation.ReflectedAt != nil {
		invoked = invocation.ReflectedAt.UTC().Format(time.RFC3339Nano)
	}
	if legacy.InvocationReflectedAt != nil {
		run = legacy.InvocationReflectedAt.UTC().Format(time.RFC3339Nano)
	}
	return invocation.ID + "|" + legacy.ID + "|" + invoked + "|" + run
}

func buildRunConversionMetrics(
	state sourceState,
	snapshot runs.Snapshot,
	linked, synthesized, direct, transitions, reflections []string,
) (runConversionMetrics, error) {
	decisions := slices.Clone(state.routing.Decisions)
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].EventID < decisions[j].EventID })
	invocations := make([]triggerrouter.Invocation, len(state.routing.Invocations))
	for index, value := range state.routing.Invocations {
		invocations[index] = value.Clone()
	}
	sort.Slice(invocations, func(i, j int) bool { return invocations[i].ID < invocations[j].ID })
	rates := slices.Clone(state.routing.RateBuckets)
	sort.Slice(rates, func(i, j int) bool {
		if rates[i].RuleID != rates[j].RuleID {
			return rates[i].RuleID < rates[j].RuleID
		}
		return rates[i].Minute.Before(rates[j].Minute)
	})
	legacyRuns := slices.Clone(state.runs.Runs)
	sort.Slice(legacyRuns, func(i, j int) bool { return legacyRuns[i].ID < legacyRuns[j].ID })

	decisionDigest, err := digestJSON(decisions)
	if err != nil {
		return runConversionMetrics{}, err
	}
	invocationDigest, err := digestJSON(invocations)
	if err != nil {
		return runConversionMetrics{}, err
	}
	rateDigest, err := digestJSON(rates)
	if err != nil {
		return runConversionMetrics{}, err
	}
	runDigest, err := digestJSON(legacyRuns)
	if err != nil {
		return runConversionMetrics{}, err
	}

	model := snapshot.Model()
	active, pins, routes := make([]string, 0), make([]string, 0), make([]string, 0)
	for _, run := range model.Runs {
		if run.State.Nonterminal() && run.State != runs.StateAdmitted {
			active = append(active, run.Causation.Task.OwnershipKey()+"|"+run.ID+"|"+string(run.State))
		}
		if run.Causation.Workflow != nil {
			pins = append(pins, fmt.Sprintf("%s|%s|%d|%s", run.ID, run.Causation.Workflow.ID, run.Causation.Workflow.Revision, run.Causation.WorkflowDigest))
		}
		switch {
		case run.Repository != nil:
			routes = append(routes, fmt.Sprintf("%s|current|%s|%s|%s", run.ID, run.Repository.ProjectID, run.Repository.Repository, run.Repository.ManagedPath))
		case run.MigratedBaseline != nil && run.MigratedBaseline.HistoricalRepository != nil:
			routes = append(routes, fmt.Sprintf("%s|historical|%s|%s", run.ID, run.MigratedBaseline.HistoricalRepository.Repository, run.MigratedBaseline.HistoricalRepository.ManagedPath))
		case run.MigratedBaseline != nil && run.MigratedBaseline.RepositoryRouteUnavailable:
			routes = append(routes, run.ID+"|unavailable")
		}
	}

	canonicalBatchDigest, err := digestJSON(model.AdmissionBatches)
	if err != nil {
		return runConversionMetrics{}, err
	}
	snapshotDigest, err := snapshot.Digest()
	if err != nil {
		return runConversionMetrics{}, err
	}
	audit := CanonicalRunsAudit{
		Schema: runs.SchemaVersion, Digest: snapshotDigest, MigrationOperationID: model.Migration.OperationID,
		SourceDecisions: uint64(len(decisions)), SourceInvocations: uint64(len(invocations)),
		SourceRateBuckets: uint64(len(rates)), SourceRunsRetained: uint64(len(legacyRuns)), SourceRunsLifetime: state.runs.Total,
		SourceDecisionDigest: decisionDigest, SourceInvocationDigest: invocationDigest,
		SourceRateDigest: rateDigest, SourceRunDigest: runDigest,
		LinkedPairs: uint64(len(linked)), LinkedPairDigest: digestStringEvidence("linked", linked),
		SynthesizedRuns: uint64(len(synthesized)), SynthesizedRunDigest: digestStringEvidence("synthesized", synthesized),
		DirectRuns: uint64(len(direct)), DirectRunDigest: digestStringEvidence("direct", direct),
		TransitionReceipts: uint64(len(transitions)), TransitionReceiptDigest: digestStringEvidence("transitions", transitions),
		ReflectionReceipts: uint64(len(reflections)), ReflectionReceiptDigest: digestStringEvidence("reflections", reflections),
		ActiveOwnership: uint64(len(active)), ActiveOwnershipDigest: digestStringEvidence("ownership", active),
		WorkflowPins: uint64(len(pins)), WorkflowPinDigest: digestStringEvidence("pins", pins),
		RepositoryRoutes: uint64(len(routes)), RepositoryRouteDigest: digestStringEvidence("routes", routes),
		CanonicalBatchesRetained: uint64(len(model.AdmissionBatches)), CanonicalBatchesLifetime: model.TotalBatches,
		BatchLifetimeMigrationBaseline: true,
		CanonicalRunsRetained:          uint64(len(model.Runs)), CanonicalRunsLifetime: model.TotalRuns,
		CanonicalRateBuckets: uint64(len(model.RateBuckets)), CanonicalBatchDigest: canonicalBatchDigest,
		CanonicalRunDigest: model.Migration.CanonicalRunsDigest, CanonicalRateDigest: model.Migration.RateBucketDigest,
	}
	return runConversionMetrics{audit: audit}, nil
}

func canonicalRunsEvidence(snapshot runs.Snapshot, metrics runConversionMetrics) (CanonicalRunsAudit, error) {
	if err := snapshot.Validate(); err != nil {
		return CanonicalRunsAudit{}, fmt.Errorf("migration: revalidate canonical Runs evidence: %w", err)
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return CanonicalRunsAudit{}, err
	}
	model := snapshot.Model()
	if metrics.audit.Schema != runs.SchemaVersion || metrics.audit.Digest != digest || model.Migration == nil ||
		metrics.audit.MigrationOperationID != model.Migration.OperationID ||
		metrics.audit.CanonicalRunsLifetime != model.TotalRuns || metrics.audit.CanonicalRunsRetained != uint64(len(model.Runs)) ||
		metrics.audit.CanonicalBatchesLifetime != model.TotalBatches || metrics.audit.CanonicalBatchesRetained != uint64(len(model.AdmissionBatches)) {
		return CanonicalRunsAudit{}, errors.New("migration: canonical Runs evidence conflicts with its snapshot")
	}
	return metrics.audit, nil
}

func digestStringEvidence(domain string, values []string) string {
	values = slices.Clone(values)
	slices.Sort(values)
	parts := append([]string{"factory-migration-runs-evidence-v1", domain}, values...)
	return digestRunParts(parts...)
}

func digestRunParts(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
