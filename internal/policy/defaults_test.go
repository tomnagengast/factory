package policy

import (
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

func TestRecognizeCompiledWorkflowsByCanonicalDigest(t *testing.T) {
	tests := []struct {
		definition workflow.Definition
		kind       CompiledWorkflow
		digest     string
	}{
		{
			definition: workflow.Default(time.Now()), kind: CompiledFullSDLC,
			digest: "2f244db491b286e9d5047874f63c286875bfb19fa3c66e163c70d4f6d186ccac",
		},
		{
			definition: workflow.ProviderNeutralDefault(time.Now()), kind: CompiledProviderNeutral,
			digest: "cf0ffc5caad35dfa6c30cb356974865aff39228af4a167972ff94b91a0812da7",
		},
	}
	for _, test := range tests {
		converted := workflowFromSource(test.definition)
		got, recognized := RecognizeCompiledWorkflow(converted)
		if !recognized || got != test.kind {
			t.Fatalf("recognition = %q, %t; want %q", got, recognized, test.kind)
		}
		canonicalDigest, err := WorkflowDigest(converted)
		legacyDigest, legacyErr := workflow.Digest(test.definition)
		if err != nil || legacyErr != nil || canonicalDigest != legacyDigest || canonicalDigest != test.digest {
			t.Fatalf("canonical digest = %q, %v; legacy = %q, %v; want %q", canonicalDigest, err, legacyDigest, legacyErr, test.digest)
		}

		converted.Markdown += "\nCustomized.\n"
		if _, recognized := RecognizeCompiledWorkflow(converted); recognized {
			t.Fatalf("customized %s recognized as compiled", test.kind)
		}
	}
}

func TestRecognizeCompiledRulesByCanonicalDigest(t *testing.T) {
	configuration := settings.Defaults(3)
	for _, source := range triggerregistry.Defaults(configuration, "actor-tom").Rules {
		converted := ruleFromSource(source)
		kind, recognized := RecognizeCompiledRule(converted, configuration, "actor-tom")
		if !recognized || string(kind) != source.ID {
			t.Fatalf("rule %s recognition = %q, %t", source.ID, kind, recognized)
		}
		converted.Name += " customized"
		if _, recognized := RecognizeCompiledRule(converted, configuration, "actor-tom"); recognized {
			t.Fatalf("customized rule %s recognized as compiled", source.ID)
		}
	}
	comment := ruleFromSource(triggerregistry.Defaults(configuration, "actor-tom").Rules[0])
	if _, recognized := RecognizeCompiledRule(comment, configuration, "different-actor"); recognized {
		t.Fatal("rule with a different compiled actor was recognized")
	}
}
