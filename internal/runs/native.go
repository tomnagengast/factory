package runs

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

const nativeTaskStartRuleID = "native-task-start"

var nativeContinuationPattern = regexp.MustCompile(`^(?:message:msg-[a-f0-9]{16}|gate:gate-[a-f0-9]{16}:(?:approved|revision_requested))$`)

type NativeAdmission struct {
	Task             taskmodel.TaskRef
	Workflow         workflow.Pinned
	WorkflowDigest   string
	PolicyRevision   uint64
	RegistryRevision uint64
	PolicyGeneration uint64
	AdmittedAt       time.Time
}

// AdmitNative durably creates the provider-neutral native start owned by one
// Factory task. Its event and Run identities intentionally match the legacy
// synthetic invocation while carrying no invented source event sequence.
func (a *Admitter) AdmitNative(admission NativeAdmission) (Run, bool, error) {
	return a.admitNative(admission, AdmissionOriginNative, "factory:native-start:"+admission.Task.ProviderID, "start")
}

// Continue durably admits one protected human-feedback continuation. The Store
// atomically coalesces it into an existing task owner when present.
func (a *Admitter) Continue(admission NativeAdmission, eventKey string) (Run, bool, error) {
	if !nativeContinuationPattern.MatchString(eventKey) {
		return Run{}, false, errors.New("runs: native continuation identity is invalid")
	}
	eventID := "factory:native-continue:" + admission.Task.ProviderID + ":" + admissionDigest(eventKey)[:16]
	return a.admitNative(admission, AdmissionOriginContinuation, eventID, eventKey)
}

func (a *Admitter) admitNative(admission NativeAdmission, origin AdmissionOrigin, eventID, invocationKey string) (Run, bool, error) {
	if a == nil || a.store == nil {
		return Run{}, false, errors.New("runs: admission store is required")
	}
	task, err := admission.Task.Normalize()
	if err != nil || task.Source != taskmodel.SourceFactory {
		return Run{}, false, errors.New("runs: native admission task is invalid")
	}
	if err := admission.Workflow.Validate(); err != nil || !admission.Workflow.Enabled || !admission.Workflow.Complete() ||
		admission.WorkflowDigest == "" || admission.PolicyGeneration == 0 || admission.AdmittedAt.IsZero() ||
		admission.AdmittedAt.Location() != time.UTC {
		return Run{}, false, errors.New("runs: native admission workflow and policy evidence are invalid")
	}
	digest, err := admission.Workflow.Digest()
	if err != nil || digest != admission.WorkflowDigest {
		return Run{}, false, errors.New("runs: native admission workflow digest conflicts")
	}
	admissionID := admissionDigest("factory-native-invocation-v1", task.OwnershipKey(), admission.WorkflowDigest, invocationKey)
	runID := "run-" + admissionDigest("factory-trigger-run-v1", admissionID)[:16]
	batchID := admissionDigest("factory-runs-admission-batch-v1", string(origin), eventID)

	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot, err := a.store.Snapshot()
	if err != nil {
		return Run{}, false, err
	}
	if durable, found, err := durableNativeRun(snapshot.Model(), batchID, eventID, origin, admissionID, runID, task, admission.WorkflowDigest); found || err != nil {
		return durable, false, err
	}

	batch := AdmissionBatch{
		ID: batchID, Origin: origin, EventID: eventID, EventSource: eventwire.SourceFactory,
		RegistryRevision: admission.RegistryRevision, SettingsRevision: admission.PolicyRevision,
		PolicyGeneration: admission.PolicyGeneration, DecidedAt: admission.AdmittedAt,
		Outcomes: []AdmissionOutcome{{
			Kind: AdmissionOutcomeRun, RuleID: nativeTaskStartRuleID, RuleRevision: 1,
			AdmissionID: admissionID, RunID: runID,
		}},
	}
	run := newNativeRun(batch, admission, task, admissionID, runID, origin)
	increment := RateBucket{RuleID: nativeTaskStartRuleID, Minute: admission.AdmittedAt.Truncate(time.Minute), Count: 1}
	if origin == AdmissionOriginContinuation {
		persisted, err := a.store.ApplyContinuation(batch, run, increment)
		return persisted, err == nil, err
	}
	if err := a.store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{increment}); err != nil {
		return Run{}, false, err
	}
	persisted, err := a.store.Snapshot()
	if err != nil {
		return Run{}, false, err
	}
	durable, found, err := durableNativeRun(persisted.Model(), batchID, eventID, origin, admissionID, runID, task, admission.WorkflowDigest)
	if err != nil || !found {
		if err == nil {
			err = errors.New("runs: persisted native admission is unavailable")
		}
		return Run{}, false, err
	}
	return durable, true, nil
}

func newNativeRun(batch AdmissionBatch, admission NativeAdmission, task taskmodel.TaskRef, admissionID, runID string, origin AdmissionOrigin) Run {
	triggerKind := triggerKindConfiguredRule
	if origin == AdmissionOriginContinuation {
		triggerKind = triggerKindComment
	}
	pin := admission.Workflow.Clone()
	return Run{
		ID: runID,
		Causation: Causation{
			AdmissionID: admissionID, BatchID: batch.ID, EventID: batch.EventID, EventSource: eventwire.SourceFactory,
			RuleID: nativeTaskStartRuleID, RuleRevision: 1, Workflow: &pin, WorkflowDigest: admission.WorkflowDigest,
			PolicyRevision: admission.PolicyRevision, PolicyGeneration: admission.PolicyGeneration,
			Task: task, RootEventID: batch.EventID, Hop: 1, AncestorRuleIDs: []string{nativeTaskStartRuleID},
			AdmittedAt: admission.AdmittedAt,
		},
		TriggerKind: triggerKind, DeliveryIDs: []string{batch.EventID}, State: StateAdmitted,
		CreatedAt: admission.AdmittedAt, UpdatedAt: admission.AdmittedAt,
		Transitions: []LifecycleTransition{{ID: runID + ":admitted", State: StateAdmitted, At: admission.AdmittedAt}},
	}
}

func durableNativeRun(model Model, batchID, eventID string, origin AdmissionOrigin, admissionID, runID string, task taskmodel.TaskRef, workflowDigest string) (Run, bool, error) {
	var matched *AdmissionBatch
	consider := func(batch AdmissionBatch) error {
		if batch.ID != batchID && batch.EventID != eventID {
			return nil
		}
		if matched != nil || batch.ID != batchID || batch.EventID != eventID || batch.Origin != origin || batch.EventSource != eventwire.SourceFactory ||
			len(batch.Outcomes) != 1 || batch.Outcomes[0].Kind != AdmissionOutcomeRun || batch.Outcomes[0].RuleID != nativeTaskStartRuleID ||
			batch.Outcomes[0].RuleRevision != 1 || batch.Outcomes[0].AdmissionID != admissionID || batch.Outcomes[0].RunID != runID {
			return fmt.Errorf("%w: native admission conflicts with durable evidence", ErrIdentityCollision)
		}
		copy := batch
		matched = &copy
		return nil
	}
	if model.Migration != nil {
		for _, batch := range model.Migration.AdmissionBatches {
			if err := consider(batch); err != nil {
				return Run{}, false, err
			}
		}
	}
	for _, receipt := range model.AdmissionOperations {
		for _, batch := range receipt.AdmissionBatches {
			if err := consider(batch); err != nil {
				return Run{}, false, err
			}
		}
	}
	if matched == nil {
		return Run{}, false, nil
	}
	for _, candidate := range model.Runs {
		if candidate.ID == runID {
			if nativeRunMatches(candidate, admissionID, task, workflowDigest) {
				return candidate.Clone(), true, nil
			}
			return Run{}, false, fmt.Errorf("%w: native Run conflicts with durable evidence", ErrIdentityCollision)
		}
	}
	for _, receipt := range model.AdmissionOperations {
		for _, candidate := range receipt.Runs {
			if candidate.ID == runID && nativeRunMatches(candidate, admissionID, task, workflowDigest) {
				return candidate.Clone(), true, nil
			}
		}
	}
	return Run{}, false, fmt.Errorf("%w: native admission Run evidence is unavailable", ErrIdentityCollision)
}

func nativeRunMatches(run Run, admissionID string, task taskmodel.TaskRef, workflowDigest string) bool {
	return run.Causation.AdmissionID == admissionID && run.Causation.Task.Equal(task) && run.Causation.WorkflowDigest == workflowDigest &&
		run.Causation.RuleID == nativeTaskStartRuleID && run.Causation.RuleRevision == 1 && len(run.Causation.AncestorRuleIDs) == 1 &&
		run.Causation.AncestorRuleIDs[0] == nativeTaskStartRuleID
}
