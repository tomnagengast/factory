package settings

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/workflow"
)

func TestReconcileCompiledDefaultsAddsMissingDefinitionIdempotently(t *testing.T) {
	defaults := Defaults(3)
	defaults.Workflows = defaults.Workflows[:1]
	store, err := Open(filepath.Join(t.TempDir(), "settings.json"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	updated, err := store.ReconcileCompiledDefaults(0, now)
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
	again, err := store.ReconcileCompiledDefaults(updated.Revision, now.Add(time.Minute))
	if err != nil || again.Revision != updated.Revision {
		t.Fatalf("idempotent reconcile: revision=%d err=%v", again.Revision, err)
	}
}

func TestReconcileCompiledDefaultsUpgradesOlderRevisions(t *testing.T) {
	defaults := Defaults(3)
	for index := range defaults.Workflows {
		defaults.Workflows[index].Revision = 1
		defaults.Workflows[index].Markdown = workflow.CanonicalizeMarkdown("# Old\n\nRetired compiled body.\n")
	}
	store, err := Open(filepath.Join(t.TempDir(), "settings.json"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.ReconcileCompiledDefaults(0, time.Now().UTC())
	if err != nil || updated.Revision != 1 {
		t.Fatalf("upgrade reconcile: revision=%d err=%v", updated.Revision, err)
	}
	for _, id := range []string{workflow.DefaultID, workflow.ProviderNeutralID} {
		definition, found := updated.Workflow(id)
		if !found || definition.Revision == 1 {
			t.Fatalf("workflow %s was not upgraded: found=%t revision=%d", id, found, definition.Revision)
		}
	}
	digest, err := workflow.Digest(mustWorkflow(t, updated, workflow.ProviderNeutralID))
	if err != nil || digest != workflow.ProviderNeutralDigest() {
		t.Fatalf("upgraded digest=%s err=%v", digest, err)
	}
}

func TestReconcileCompiledDefaultsPreservesNewerRevisions(t *testing.T) {
	defaults := Defaults(3)
	newerRevision := defaults.Workflows[0].Revision + 5
	custom := workflow.CanonicalizeMarkdown("# Newer\n\nOperator-published body.\n")
	for index := range defaults.Workflows {
		defaults.Workflows[index].Revision = newerRevision
		defaults.Workflows[index].Markdown = custom
	}
	store, err := Open(filepath.Join(t.TempDir(), "settings.json"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.ReconcileCompiledDefaults(0, time.Now().UTC())
	if err != nil || updated.Revision != 0 {
		t.Fatalf("preserve reconcile: revision=%d err=%v", updated.Revision, err)
	}
	definition := mustWorkflow(t, updated, workflow.DefaultID)
	if definition.Revision != newerRevision || definition.Markdown != custom {
		t.Fatalf("newer published workflow was overwritten: %#v", definition)
	}
}

func TestReconcileCompiledDefaultsRejectsSameRevisionDrift(t *testing.T) {
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
	if _, err := store.ReconcileCompiledDefaults(0, time.Now()); err == nil {
		t.Fatal("same-revision drift on a reserved workflow was overwritten")
	}
}

func mustWorkflow(t *testing.T, snapshot Snapshot, id string) workflow.Definition {
	t.Helper()
	definition, found := snapshot.Workflow(id)
	if !found {
		t.Fatalf("workflow %s is missing", id)
	}
	return definition
}
