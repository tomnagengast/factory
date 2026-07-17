package policy

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

var policyTestNow = time.Date(2026, time.July, 16, 20, 0, 0, 0, time.UTC)

func TestConvertSourcesPreservesIndependentDomainsAndCustomPolicy(t *testing.T) {
	sources := populatedSources()
	originalSettings := sources.Settings.Clone()
	originalRegistry := sources.Registry.Clone()
	originalControl := sources.TaskControl
	originalControl.EnabledProjectIDs = append([]string(nil), sources.TaskControl.EnabledProjectIDs...)

	snapshot := mustConvertSources(t, sources)
	if snapshot.Generation() != 1 || snapshot.Settings().Revision != 7 || snapshot.Registry().Revision != 4 || snapshot.TaskControl().Revision != 3 {
		t.Fatalf("revisions = generation %d, settings %d, registry %d, task %d", snapshot.Generation(), snapshot.Settings().Revision, snapshot.Registry().Revision, snapshot.TaskControl().Revision)
	}
	custom, found := snapshot.Workflow("custom-review")
	if !found || custom.Revision != 3 || custom.Markdown != "# Custom review\n" {
		t.Fatalf("custom workflow = %#v, found=%t", custom, found)
	}
	if _, found := snapshot.Workflow(workflow.DefaultID); found {
		t.Fatal("exact compiled full-sdlc duplicate was preserved")
	}
	providerNeutral, found := snapshot.Workflow(workflow.ProviderNeutralID)
	if !found || len(snapshot.Workflows()) != 2 {
		t.Fatalf("provider-neutral consolidation = %#v, found=%t", snapshot.Workflows(), found)
	}
	if kind, recognized := RecognizeCompiledWorkflow(providerNeutral); !recognized || kind != CompiledProviderNeutral {
		t.Fatalf("provider-neutral workflow recognition = %q, %t", kind, recognized)
	}
	if got := snapshot.ProtectedWorkflows().LinearFeedback.WorkflowID; got != workflow.ProviderNeutralID {
		t.Fatalf("protected feedback workflow = %q", got)
	}
	registry := snapshot.Registry()
	if _, found := ruleByID(registry, string(CompiledLinearComment)); found {
		t.Fatal("exact compiled generic comment rule was preserved")
	}
	label := findRule(t, registry, string(CompiledLinearLabel))
	if label.WorkflowID != workflow.ProviderNeutralID {
		t.Fatalf("compiled label workflow = %q", label.WorkflowID)
	}
	customRule := findRule(t, registry, "custom-visible")
	if customRule.Revision != 2 || customRule.WorkflowID != custom.ID {
		t.Fatalf("custom rule = %#v", customRule)
	}
	if len(registry.Schedules) != 1 || registry.Schedules[0].Revision != 2 {
		t.Fatalf("schedules = %#v", registry.Schedules)
	}
	if got := snapshot.TaskControl().EnabledProjectIDs; !reflect.DeepEqual(got, []string{"project-factory"}) {
		t.Fatalf("enabled projects = %#v", got)
	}

	if !reflect.DeepEqual(sources.Settings, originalSettings) || !reflect.DeepEqual(*sources.Registry, originalRegistry) || !reflect.DeepEqual(sources.TaskControl, originalControl) {
		t.Fatal("source conversion mutated a legacy owner")
	}
}

func TestConvertSourcesSynthesizesAbsentRegistryWithoutWritingDefaults(t *testing.T) {
	sources := populatedSources()
	sources.Registry = nil
	snapshot := mustConvertSources(t, sources)
	registry := snapshot.Registry()
	if registry.Revision != 0 || len(registry.Rules) != 1 || len(registry.Schedules) != 0 {
		t.Fatalf("implicit registry = %#v", registry)
	}
	if rule := registry.Rules[0]; rule.ID != string(CompiledLinearLabel) || rule.WorkflowID != workflow.ProviderNeutralID {
		t.Fatalf("implicit label rule = %#v", rule)
	}
}

func TestConvertSourcesSynthesizesProviderNeutralDefault(t *testing.T) {
	sources := populatedSources()
	sources.Settings.Workflows = slices.DeleteFunc(sources.Settings.Workflows, func(definition workflow.Definition) bool {
		return definition.ID == workflow.ProviderNeutralID
	})
	sources.Settings.ProtectedWorkflows.LinearFeedback.WorkflowID = workflow.DefaultID
	snapshot := mustConvertSources(t, sources)
	definition, found := snapshot.Workflow(workflow.ProviderNeutralID)
	if !found || len(snapshot.Workflows()) != 2 {
		t.Fatalf("synthesized workflows = %#v, found=%t", snapshot.Workflows(), found)
	}
	if kind, recognized := RecognizeCompiledWorkflow(definition); !recognized || kind != CompiledProviderNeutral {
		t.Fatalf("synthesized workflow recognition = %q, %t", kind, recognized)
	}
}

func TestConvertSourcesPreservesCustomizedVisibleCommentRule(t *testing.T) {
	sources := populatedSources()
	comment := &sources.Registry.Rules[ruleSourceIndex(t, sources.Registry.Rules, string(CompiledLinearComment))]
	comment.Name = "Visible Linear comment"
	comment.WorkflowID = "custom-review"
	expected := ruleFromSource(*comment)

	snapshot := mustConvertSources(t, sources)
	preserved := findRule(t, snapshot.Registry(), string(CompiledLinearComment))
	if !reflect.DeepEqual(preserved, expected) {
		t.Fatalf("custom visible comment = %#v, want %#v", preserved, expected)
	}
}

func TestConvertSourcesRequiresActorOnlyForAbsentRegistry(t *testing.T) {
	sources := populatedSources()
	sources.TriggerActorID = "different-current-actor"
	if _, err := ConvertSources(sources); err != nil {
		t.Fatalf("explicit registry unexpectedly required actor: %v", err)
	}
	sources.TriggerActorID = ""
	sources.Registry = nil
	if _, err := ConvertSources(sources); err == nil {
		t.Fatal("implicit registry accepted without actor")
	}
}

func TestConvertSourcesRejectsCustomizedReservedWorkflowAndInvalidControl(t *testing.T) {
	sources := populatedSources()
	sources.Settings.Workflows[0].Markdown += "\nCustomized.\n"
	if _, err := ConvertSources(sources); !errors.Is(err, ErrReservedWorkflowConflict) {
		t.Fatalf("reserved conflict error = %v", err)
	}

	sources = populatedSources()
	for index := range sources.Settings.Workflows {
		if sources.Settings.Workflows[index].ID == workflow.ProviderNeutralID {
			sources.Settings.Workflows[index].Name = "Customized provider neutral"
		}
	}
	if _, err := ConvertSources(sources); !errors.Is(err, ErrReservedWorkflowConflict) {
		t.Fatalf("provider-neutral reserved conflict error = %v", err)
	}

	sources = populatedSources()
	comment := &sources.Registry.Rules[ruleSourceIndex(t, sources.Registry.Rules, string(CompiledLinearComment))]
	comment.Name = "Customized reserved admission"
	if _, err := ConvertSources(sources); !errors.Is(err, ErrReservedWorkflowConflict) {
		t.Fatalf("reserved rule reference error = %v", err)
	}

	sources = populatedSources()
	sources.TaskControl.Version = 2
	if _, err := ConvertSources(sources); err == nil {
		t.Fatal("unknown task-control source version was accepted")
	}
}

func populatedSources() Sources {
	configuration := settings.Defaults(3)
	configuration.Revision = 7
	configuration.UpdatedAt = policyTestNow
	configuration.ProtectedWorkflows.LinearFeedback.WorkflowID = workflow.ProviderNeutralID
	configuration.Workflows = append(configuration.Workflows, workflow.Definition{
		ID: "custom-review", Revision: 3, Name: "Custom review", Enabled: true,
		Markdown: "# Custom review\n", UpdatedAt: policyTestNow,
	})
	registry := triggerregistry.Defaults(configuration, "actor-tom")
	registry.Revision = 4
	registry.UpdatedAt = policyTestNow
	registry.Rules = append(registry.Rules, triggerregistry.Rule{
		ID: "custom-visible", Revision: 2, Name: "Visible custom", Enabled: true,
		Filter: triggerregistry.Filter{
			Source: eventwire.SourceLinear, Type: "Issue", Action: "update",
			Attributes: map[string]string{triggerregistry.AttributeActorID: "actor-tom"},
		},
		WorkflowID: "custom-review",
		Target:     triggerregistry.TargetPolicy{Provider: taskmodel.SourceLinear, Kind: triggerregistry.TargetEventSubject},
		MaxHop:     4, MaxOutstanding: 10, AdmissionsHour: 120,
	})
	registry.Schedules = []triggerregistry.Schedule{{
		ID: "daily-audit", Revision: 2, Name: "Daily audit", Enabled: true,
		Cron: "0 8 * * *", Timezone: "UTC", Subject: "ENG-47",
		Attributes: map[string][]string{"kind": {"audit"}},
	}}
	return Sources{
		Settings: configuration, Registry: &registry,
		TaskControl: taskcontrol.Snapshot{
			Version: 1, Revision: 3, UpdatedAt: policyTestNow,
			EnabledProjectIDs: []string{"project-factory"},
		},
		TriggerActorID: "actor-tom",
	}
}

func mustConvertSources(t *testing.T, sources Sources) Snapshot {
	t.Helper()
	snapshot, err := ConvertSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func findRule(t *testing.T, registry Registry, id string) Rule {
	t.Helper()
	for _, rule := range registry.Rules {
		if rule.ID == id {
			return rule
		}
	}
	t.Fatalf("rule %s not found", id)
	return Rule{}
}

func ruleByID(registry Registry, id string) (Rule, bool) {
	for _, rule := range registry.Rules {
		if rule.ID == id {
			return rule, true
		}
	}
	return Rule{}, false
}

func ruleSourceIndex(t *testing.T, rules []triggerregistry.Rule, id string) int {
	t.Helper()
	for index, rule := range rules {
		if rule.ID == id {
			return index
		}
	}
	t.Fatalf("source rule %s not found", id)
	return -1
}
