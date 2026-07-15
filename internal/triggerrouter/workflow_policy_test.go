package triggerrouter

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

func TestWorkflowPolicyProtectsBindingsAndDisabledRuleReferences(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	now := time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC)
	configurationStore, err := settings.Open(filepath.Join(directory, "settings.json"), settings.Defaults(3))
	if err != nil {
		t.Fatal(err)
	}
	registryStore, err := triggerregistry.Open(
		filepath.Join(directory, "triggers.json"),
		triggerregistry.Defaults(configurationStore.Snapshot(), "human"),
		configurationStore.Snapshot(),
	)
	if err != nil {
		t.Fatal(err)
	}
	routing, _ := Open(filepath.Join(directory, "routing.jsonl"))
	journal, _ := eventwire.Open(filepath.Join(directory, "events.jsonl"), 20, nil)
	raw, _ := eventwire.New(journal)
	policy, err := NewCoordinatedWire(raw, registryStore, configurationStore, routing, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	newDefinition := workflow.Definition{
		ID: "secondary", Name: "Secondary", Enabled: false, Markdown: "# Secondary\n",
	}
	published, err := policy.PublishWorkflow(0, 0, newDefinition, now)
	if err != nil {
		t.Fatal(err)
	}
	secondary, found := published.Workflow("secondary")
	if !found || secondary.Revision != 1 || secondary.Enabled {
		t.Fatalf("secondary = %#v, found=%t", secondary, found)
	}
	if _, err := policy.UpdateProtectedFeedback(published.Revision, "secondary", now); !errors.Is(err, ErrPolicyValidation) {
		t.Fatalf("disabled protected binding error = %v", err)
	}

	registry := registryStore.Snapshot()
	registry.Rules[0].Enabled = false
	registry.Rules[0].WorkflowID = "secondary"
	if _, err := policy.UpdateRegistry(registry.Revision, published.Revision, registry, now); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.DeleteWorkflow(published.Revision, secondary.Revision, secondary.ID, now); !errors.Is(err, ErrPolicyValidation) {
		t.Fatalf("disabled-rule delete error = %v", err)
	}

	primary, _ := published.Workflow(workflow.DefaultID)
	primary.Enabled = false
	if _, err := policy.PublishWorkflow(published.Revision, primary.Revision, primary, now); !errors.Is(err, ErrPolicyValidation) {
		t.Fatalf("protected disable error = %v", err)
	}
	if status := policy.Status(); status.Pending != 0 {
		t.Fatalf("wire status = %#v", status)
	}
	if err := policy.CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
}
