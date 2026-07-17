package policy

import (
	"time"

	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

type CompiledWorkflow string

const (
	CompiledFullSDLC        CompiledWorkflow = "full-sdlc"
	CompiledProviderNeutral CompiledWorkflow = "full-sdlc-provider-neutral"
)

type CompiledRule string

const (
	CompiledLinearComment CompiledRule = "linear-comment"
	CompiledLinearLabel   CompiledRule = "linear-label"
)

// RecognizeCompiledWorkflow identifies only the exact compiled definition.
// UpdatedAt is intentionally excluded by the canonical workflow digest.
func RecognizeCompiledWorkflow(definition Workflow) (CompiledWorkflow, bool) {
	digest, err := WorkflowDigest(definition)
	if err != nil {
		return "", false
	}
	for kind, expected := range map[CompiledWorkflow]Workflow{
		CompiledFullSDLC:        workflowFromSource(workflow.Default(time.Time{})),
		CompiledProviderNeutral: workflowFromSource(workflow.ProviderNeutralDefault(time.Time{})),
	} {
		expectedDigest, expectedErr := WorkflowDigest(expected)
		if expectedErr == nil && digest == expectedDigest {
			return kind, true
		}
	}
	return "", false
}

// RecognizeCompiledRule identifies a rule synthesized by the current binary
// for the supplied legacy settings and independently authoritative trigger
// actor. Customized persisted rules are intentionally not classified as
// compiled defaults.
func RecognizeCompiledRule(rule Rule, configuration settings.Snapshot, triggerActor string) (CompiledRule, bool) {
	if triggerActor == "" {
		return "", false
	}
	digest, err := RuleDigest(rule)
	if err != nil {
		return "", false
	}
	for _, expected := range triggerregistry.Defaults(configuration, triggerActor).Rules {
		converted := ruleFromSource(expected)
		expectedDigest, expectedErr := RuleDigest(converted)
		if expectedErr != nil || digest != expectedDigest {
			continue
		}
		switch expected.ID {
		case string(CompiledLinearComment):
			return CompiledLinearComment, true
		case string(CompiledLinearLabel):
			return CompiledLinearLabel, true
		}
	}
	return "", false
}

// recognizeCompiledRuleWithCorrectedActor identifies a persisted reserved
// rule whose only difference from the authoritative compiled default is its
// actor. Such a rule is ambiguous: migration cannot know whether the actor was
// intentionally customized or merely persisted from another actor identity.
func recognizeCompiledRuleWithCorrectedActor(rule Rule, configuration settings.Snapshot, triggerActor string) (CompiledRule, bool) {
	if triggerActor == "" {
		return "", false
	}
	actor, found := rule.Filter.Attributes[triggerregistry.AttributeActorID]
	if found && actor == triggerActor {
		return "", false
	}
	corrected := cloneRule(rule)
	corrected.Filter.Attributes[triggerregistry.AttributeActorID] = triggerActor
	return RecognizeCompiledRule(corrected, configuration, triggerActor)
}
