package policy

import (
	"reflect"
	"slices"
	"testing"

	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

func TestCompatibilityViewsPreservePublicDomains(t *testing.T) {
	snapshot := mustConvertSources(t, populatedSources())
	configuration := SettingsView(snapshot)
	if err := configuration.Validate(); err != nil {
		t.Fatalf("settings view: %v", err)
	}
	if configuration.Revision != snapshot.Settings().Revision || len(configuration.Workflows) != len(snapshot.Workflows()) {
		t.Fatalf("settings view = %#v", configuration)
	}
	if configuration.ProtectedWorkflows.LinearFeedback.WorkflowID != workflow.ProviderNeutralID ||
		configuration.Triggers.LinearComment.WorkflowID != workflow.ProviderNeutralID {
		t.Fatalf("protected compatibility bindings = %#v", configuration)
	}
	if configuration.Triggers.LinearLabel.WorkflowID != workflow.ProviderNeutralID ||
		configuration.Triggers.LinearLabel.Label != "FACTORY" {
		t.Fatalf("label compatibility trigger = %#v", configuration.Triggers.LinearLabel)
	}

	registry := RegistryView(snapshot)
	if err := registry.Validate(configuration); err != nil {
		t.Fatalf("registry view: %v", err)
	}
	if registry.Revision != snapshot.Registry().Revision || len(registry.Rules) != len(snapshot.Registry().Rules) ||
		len(registry.Schedules) != len(snapshot.Registry().Schedules) {
		t.Fatalf("registry view = %#v", registry)
	}
	label, found := registry.Rule(string(CompiledLinearLabel))
	if !found || label.Filter.Attributes[triggerregistry.AttributeAddedLabel] != "FACTORY" {
		t.Fatalf("label rule = %#v, found=%t", label, found)
	}
	if candidate := RegistryCandidate(registry); !reflect.DeepEqual(candidate, snapshot.Registry()) {
		t.Fatalf("registry candidate = %#v, want %#v", candidate, snapshot.Registry())
	}
	definition, _ := configuration.Workflow("custom-review")
	if candidate := WorkflowCandidate(definition); candidate != (Workflow{
		ID: definition.ID, Revision: definition.Revision, Name: definition.Name,
		Enabled: definition.Enabled, Markdown: definition.Markdown, UpdatedAt: definition.UpdatedAt,
	}) {
		t.Fatalf("workflow candidate = %#v", candidate)
	}

	control := TaskControlView(snapshot)
	if control.Revision != snapshot.TaskControl().Revision ||
		!reflect.DeepEqual(control.EnabledProjectIDs, snapshot.TaskControl().EnabledProjectIDs) {
		t.Fatalf("task-control view = %#v", control)
	}
}

func TestSettingsViewDisablesAbsentLegacyLabelTrigger(t *testing.T) {
	sources := populatedSources()
	sources.Registry.Rules = slices.DeleteFunc(sources.Registry.Rules, func(rule triggerregistry.Rule) bool {
		return rule.ID == string(CompiledLinearLabel)
	})
	snapshot := mustConvertSources(t, sources)
	configuration := SettingsView(snapshot)
	if configuration.Triggers.LinearLabel.Enabled {
		t.Fatalf("absent label rule became enabled: %#v", configuration.Triggers.LinearLabel)
	}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("settings view: %v", err)
	}
}

func TestCompatibilityViewsDoNotAliasCanonicalSnapshot(t *testing.T) {
	snapshot := mustConvertSources(t, populatedSources())
	configuration := SettingsView(snapshot)
	registry := RegistryView(snapshot)
	control := TaskControlView(snapshot)

	configuration.Workflows[0].Name = "changed"
	registry.Rules[0].Filter.Attributes[triggerregistry.AttributeActorID] = "changed"
	registry.Schedules[0].Attributes["kind"][0] = "changed"
	control.EnabledProjectIDs[0] = "changed"

	if SettingsView(snapshot).Workflows[0].Name == "changed" ||
		RegistryView(snapshot).Rules[0].Filter.Attributes[triggerregistry.AttributeActorID] == "changed" ||
		RegistryView(snapshot).Schedules[0].Attributes["kind"][0] == "changed" ||
		TaskControlView(snapshot).EnabledProjectIDs[0] == "changed" {
		t.Fatal("compatibility view aliases canonical policy")
	}
}
