package triggerrouter

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerregistry"
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
	AdmittedAt       time.Time
}

func (s *Store) AdmitNative(admission NativeAdmission) (Invocation, bool, error) {
	return s.admitNative(admission, "factory:native-start:"+admission.Task.ProviderID, "start")
}

func (s *Store) AdmitNativeContinuation(admission NativeAdmission, eventKey string) (Invocation, bool, error) {
	if !nativeContinuationPattern.MatchString(eventKey) {
		return Invocation{}, false, errors.New("trigger router: native continuation identity is invalid")
	}
	eventID := "factory:native-continue:" + admission.Task.ProviderID + ":" + digestStrings(eventKey)[:16]
	return s.admitNative(admission, eventID, eventKey)
}

func (s *Store) admitNative(admission NativeAdmission, eventID, invocationKey string) (Invocation, bool, error) {
	task, err := admission.Task.Normalize()
	if err != nil || task.Source != taskmodel.SourceFactory {
		return Invocation{}, false, errors.New("trigger router: native admission task is invalid")
	}
	if err := admission.Workflow.Validate(); err != nil || !admission.Workflow.Enabled || !admission.Workflow.Complete() || admission.WorkflowDigest == "" || admission.AdmittedAt.IsZero() {
		return Invocation{}, false, errors.New("trigger router: native admission workflow is invalid")
	}
	digest, err := admission.Workflow.Digest()
	if err != nil || digest != admission.WorkflowDigest {
		return Invocation{}, false, errors.New("trigger router: native admission workflow digest conflicts")
	}
	invocationID := digestStrings("factory-native-invocation-v1", task.OwnershipKey(), admission.WorkflowDigest, invocationKey)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned != nil {
		return Invocation{}, false, fmt.Errorf("trigger router: store is poisoned: %w", s.poisoned)
	}
	if decision, found := s.decisions[eventID]; found {
		if len(decision.Outcomes) != 1 || decision.Outcomes[0].Kind != OutcomeInvocation || decision.Outcomes[0].InvocationID != invocationID {
			return Invocation{}, false, errors.New("trigger router: native admission identity collision")
		}
		invocation, found := s.invocations[invocationID]
		if !found || !invocation.Task.Equal(task) || invocation.WorkflowDigest != admission.WorkflowDigest {
			return Invocation{}, false, errors.New("trigger router: native admission durable state conflicts")
		}
		return invocation.Clone(), false, nil
	}
	eventSequence := uint64(1)
	for _, decision := range s.decisions {
		eventSequence = max(eventSequence, decision.EventSequence+1)
	}
	rule := triggerregistry.Rule{
		ID: nativeTaskStartRuleID, Revision: 1, Name: "Native task start", Enabled: true,
		WorkflowID: admission.Workflow.ID, Target: triggerregistry.TargetPolicy{Provider: taskmodel.SourceFactory, Kind: triggerregistry.TargetEventSubject},
		MaxHop: triggerregistry.DefaultMaxHop, MaxOutstanding: triggerregistry.DefaultMaxOutstanding, AdmissionsHour: triggerregistry.DefaultAdmissionsHour,
	}
	invocation := Invocation{
		ID: invocationID, EventID: eventID, EventSequence: eventSequence, Rule: rule,
		Workflow: admission.Workflow.Clone(), WorkflowDigest: admission.WorkflowDigest, PolicyRevision: admission.PolicyRevision,
		Task: task, IssueIdentifier: task.Identifier, RootEventID: eventID, Hop: 1, AncestorRuleIDs: []string{nativeTaskStartRuleID},
		State: StateQueued, AdmittedAt: admission.AdmittedAt.UTC(), UpdatedAt: admission.AdmittedAt.UTC(),
	}
	decision := Decision{
		EventID: eventID, EventSequence: eventSequence, Source: eventwire.SourceFactory,
		RegistryRevision: admission.RegistryRevision, SettingsRevision: admission.PolicyRevision, DecidedAt: admission.AdmittedAt.UTC(),
		Outcomes: []Outcome{{Kind: OutcomeInvocation, RuleID: rule.ID, RuleRevision: rule.Revision, InvocationID: invocation.ID}},
	}
	rate := RateBucket{RuleID: rule.ID, Minute: admission.AdmittedAt.UTC().Truncate(time.Minute), Count: 1}
	op := diskOperation{Kind: operationDecisionBatch, Decisions: []Decision{decision}, Invocations: []Invocation{invocation}, RateIncrements: []RateBucket{rate}}
	if err := s.appendOperationLocked(op); err != nil {
		return Invocation{}, false, err
	}
	if err := s.applyOperationLocked(op); err != nil {
		s.poisoned = err
		return Invocation{}, false, fmt.Errorf("trigger router: apply native admission: %w", err)
	}
	if err := s.compactIfNeededLocked(); err != nil {
		return Invocation{}, false, err
	}
	return invocation.Clone(), true, nil
}

func NativeInvocationMatches(invocation Invocation, task taskmodel.TaskRef, workflowDigest string) bool {
	return invocation.Task.Equal(task) && invocation.WorkflowDigest == workflowDigest && invocation.Rule.ID == nativeTaskStartRuleID && slices.Equal(invocation.AncestorRuleIDs, []string{nativeTaskStartRuleID})
}

func NativeFeedbackInvocation(invocation Invocation) bool {
	prefix := "factory:native-continue:" + invocation.Task.ProviderID + ":"
	suffix := strings.TrimPrefix(invocation.EventID, prefix)
	return invocation.Task.Source == taskmodel.SourceFactory && invocation.Rule.ID == nativeTaskStartRuleID &&
		strings.HasPrefix(invocation.EventID, prefix) && len(suffix) == 16 && strings.Trim(suffix, "0123456789abcdef") == ""
}
