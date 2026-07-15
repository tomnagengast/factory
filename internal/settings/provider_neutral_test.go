package settings

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/workflow"
)

func TestReconcileProviderNeutralAddsExactDefinitionIdempotently(t *testing.T) {
	defaults := Defaults(3)
	defaults.Workflows = defaults.Workflows[:1]
	store, err := Open(filepath.Join(t.TempDir(), "settings.json"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	updated, err := store.ReconcileProviderNeutral(0, now)
	if err != nil || updated.Revision != 1 {
		t.Fatalf("reconcile: revision=%d err=%v", updated.Revision, err)
	}
	definition, found := updated.Workflow(workflow.ProviderNeutralID)
	if !found {
		t.Fatal("provider-neutral workflow was not added")
	}
	digest, err := workflow.Digest(definition)
	if err != nil || digest != workflow.ProviderNeutralDigest() {
		t.Fatalf("reconciled digest=%s err=%v", digest, err)
	}
	again, err := store.ReconcileProviderNeutral(updated.Revision, now.Add(time.Minute))
	if err != nil || again.Revision != updated.Revision {
		t.Fatalf("idempotent reconcile: revision=%d err=%v", again.Revision, err)
	}
}

func TestReconcileProviderNeutralRejectsCustomizedReservedID(t *testing.T) {
	defaults := Defaults(3)
	for index := range defaults.Workflows {
		if defaults.Workflows[index].ID == workflow.ProviderNeutralID {
			defaults.Workflows[index].Markdown += "\nCustomized.\n"
		}
	}
	store, err := Open(filepath.Join(t.TempDir(), "settings.json"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileProviderNeutral(0, time.Now()); err == nil {
		t.Fatal("customized reserved workflow was overwritten")
	}
}
