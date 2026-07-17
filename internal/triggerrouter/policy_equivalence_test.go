package triggerrouter

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

func TestCanonicalPolicyAdmissionMatchesLegacyCoordinatedWireForPreservedRules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 23, 0, 0, 0, time.UTC)
	configuration, registry, control := admissionEquivalenceSources(now)
	canonical, err := policy.ConvertSources(policy.Sources{
		Settings: configuration, Registry: &registry, TaskControl: control, TriggerActorID: "actor-tom",
	})
	if err != nil {
		t.Fatal(err)
	}

	configurationStore, err := settings.Open(filepath.Join(root, "legacy", "settings.json"), configuration)
	if err != nil {
		t.Fatal(err)
	}
	registryStore, err := triggerregistry.Open(filepath.Join(root, "legacy", "triggers.json"), registry, configuration)
	if err != nil {
		t.Fatal(err)
	}
	legacyRouting, err := Open(filepath.Join(root, "legacy", "routing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := eventwire.Open(filepath.Join(root, "legacy", "events.jsonl"), 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := eventwire.New(journal)
	if err != nil {
		t.Fatal(err)
	}
	legacyWire, err := NewCoordinatedWire(raw, registryStore, configurationStore, legacyRouting, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	events := []eventwire.Event{
		{ID: "linear:match", Source: eventwire.SourceLinear, Type: "Issue", Action: "update", Subject: "ENG-47", Attributes: map[string][]string{eventwire.AttributeActorID: {"actor-tom"}}, ReceivedAt: now},
		{ID: "linear:wrong-actor", Source: eventwire.SourceLinear, Type: "Issue", Action: "update", Subject: "ENG-47", Attributes: map[string][]string{eventwire.AttributeActorID: {"actor-other"}}, ReceivedAt: now},
		{ID: "linear:wrong-action", Source: eventwire.SourceLinear, Type: "Issue", Action: "create", Subject: "ENG-47", Attributes: map[string][]string{eventwire.AttributeActorID: {"actor-tom"}}, ReceivedAt: now},
	}
	records, err := legacyWire.PublishBatch(context.Background(), events)
	if err != nil {
		t.Fatal(err)
	}

	canonicalStore, err := policy.Create(filepath.Join(root, "canonical", "policy.json"), canonical)
	if err != nil {
		t.Fatal(err)
	}
	canonicalCoordinator, err := policy.NewCoordinator(canonicalStore, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	canonicalRouting, err := Open(filepath.Join(root, "canonical", "routing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := canonicalCoordinator.Admit(func(snapshot policy.Snapshot) error {
		_, applyErr := canonicalRouting.ApplyDecisionBatch(
			records,
			policy.RegistryView(snapshot),
			policy.SettingsView(snapshot),
			now,
		)
		return applyErr
	}); err != nil {
		t.Fatal(err)
	}

	legacySnapshot := legacyRouting.Snapshot()
	canonicalSnapshot := canonicalRouting.Snapshot()
	if !reflect.DeepEqual(canonicalSnapshot, legacySnapshot) {
		t.Fatalf("canonical admission = %#v, want legacy %#v", canonicalSnapshot, legacySnapshot)
	}
	if len(canonicalSnapshot.Decisions) != 3 || len(canonicalSnapshot.Decisions[0].Outcomes) != 1 ||
		canonicalSnapshot.Decisions[0].Outcomes[0].Kind != OutcomeInvocation ||
		len(canonicalSnapshot.Decisions[1].Outcomes) != 0 || len(canonicalSnapshot.Decisions[2].Outcomes) != 0 {
		t.Fatalf("matching characterization = %#v", canonicalSnapshot.Decisions)
	}
	if len(canonicalSnapshot.Invocations) != 1 || canonicalSnapshot.Invocations[0].Task.Identifier != "ENG-47" ||
		canonicalSnapshot.Invocations[0].Workflow.ID != "custom-review" {
		t.Fatalf("canonical invocation = %#v", canonicalSnapshot.Invocations)
	}
}

func TestCanonicalPolicyIntentionallyRejectsActorOnlyReservedRuleAcceptedByLegacyAdmission(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 16, 23, 0, 0, 0, time.UTC)
	configuration := settings.Defaults(3)
	registry := triggerregistry.Defaults(configuration, "actor-stale")
	if err := registry.Validate(configuration); err != nil {
		t.Fatalf("legacy registry validation: %v", err)
	}
	legacy, err := Open(filepath.Join(t.TempDir(), "routing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	record := eventwire.Record{Sequence: 1, Event: eventwire.Event{
		ID: "linear:legacy-comment", Source: eventwire.SourceLinear, Type: "Comment", Action: "create", Subject: "ENG-47",
		Attributes: map[string][]string{eventwire.AttributeActorID: {"actor-stale"}, eventwire.AttributeProvenance: {"human"}}, ReceivedAt: now,
	}}
	decisions, err := legacy.ApplyDecisionBatch([]eventwire.Record{record}, registry, configuration, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || len(decisions[0].Outcomes) != 1 || decisions[0].Outcomes[0].Kind != OutcomeInvocation {
		t.Fatalf("legacy admission = %#v", decisions)
	}

	_, err = policy.ConvertSources(policy.Sources{
		Settings: configuration, Registry: &registry,
		TaskControl: taskcontrol.Snapshot{Version: 1}, TriggerActorID: "actor-current",
	})
	if !errors.Is(err, policy.ErrReservedWorkflowConflict) {
		t.Fatalf("canonical actor-only ambiguity error = %v", err)
	}
}

func admissionEquivalenceSources(now time.Time) (settings.Snapshot, triggerregistry.Snapshot, taskcontrol.Snapshot) {
	configuration := settings.Defaults(3)
	configuration.Revision = 7
	configuration.UpdatedAt = now
	configuration.WorkflowRollbackIncompatible = true
	configuration.Workflows = append(configuration.Workflows, workflow.Definition{
		ID: "custom-review", Revision: 3, Name: "Custom review", Enabled: true,
		Markdown: "# Custom review\n", UpdatedAt: now,
	})
	registry := triggerregistry.Snapshot{
		Schema: triggerregistry.SchemaVersion, Revision: 4, UpdatedAt: now,
		Rules: []triggerregistry.Rule{{
			ID: "custom-visible", Revision: 2, Name: "Visible custom", Enabled: true,
			Filter: triggerregistry.Filter{
				Source: eventwire.SourceLinear, Type: "Issue", Action: "update",
				Attributes: map[string]string{eventwire.AttributeActorID: "actor-tom"},
			},
			WorkflowID: "custom-review",
			Target:     triggerregistry.TargetPolicy{Provider: taskmodel.SourceLinear, Kind: triggerregistry.TargetEventSubject},
			MaxHop:     4, MaxOutstanding: 10, AdmissionsHour: 120,
		}},
		Schedules: []triggerregistry.Schedule{},
	}
	control := taskcontrol.Snapshot{Version: 1, Revision: 3, UpdatedAt: now, EnabledProjectIDs: []string{"project-factory"}}
	return configuration, registry, control
}
