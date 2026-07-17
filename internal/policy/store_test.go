package policy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsImmutableSnapshotAndSettingsRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	store, err := Create(path, mustConvertSources(t, populatedSources()))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("policy mode = %v, %v", info, err)
	}

	candidate := store.Snapshot().Settings()
	updated, err := store.UpdateSettings(candidate.Revision, candidate, policyTestNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation() != 2 || updated.Settings().Revision != 8 || updated.Registry().Revision != 4 || updated.TaskControl().Revision != 3 {
		t.Fatalf("updated revisions = generation %d settings %d registry %d task %d", updated.Generation(), updated.Settings().Revision, updated.Registry().Revision, updated.TaskControl().Revision)
	}
	if _, err := store.UpdateSettings(candidate.Revision, candidate, policyTestNow); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale settings error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Generation() != 2 || reopened.Snapshot().Settings().Revision != 8 {
		t.Fatalf("reopened = %#v", reopened.Snapshot().Model())
	}
	model := reopened.Snapshot().Model()
	model.Registry.Rules[0].Name = "mutated"
	if reopened.Snapshot().Registry().Rules[0].Name == "mutated" {
		t.Fatal("store snapshot exposed mutable state")
	}
}

func TestStorePreservesRegistryAndEntryRevisionSemantics(t *testing.T) {
	store := mustCreateStore(t)
	current := store.Snapshot()
	candidate := current.Registry()
	customIndex := ruleIndex(t, candidate, "custom-visible")
	labelIndex := ruleIndex(t, candidate, "linear-label")
	candidate.Rules[customIndex].Name = "Renamed only"
	candidate.Rules[labelIndex].MaxHop++
	candidate.Schedules[0].Name = "Renamed schedule"
	candidate.Schedules = append(candidate.Schedules, Schedule{
		ID: "hourly", Name: "Hourly", Enabled: true, Cron: "0 * * * *", Timezone: "UTC",
	})

	updated, err := store.UpdateRegistry(candidate.Revision, current.Settings().Revision, candidate, policyTestNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	registry := updated.Registry()
	if updated.Generation() != 2 || registry.Revision != 5 || updated.Settings().Revision != 7 || updated.TaskControl().Revision != 3 {
		t.Fatalf("domain revisions = generation %d registry %d settings %d task %d", updated.Generation(), registry.Revision, updated.Settings().Revision, updated.TaskControl().Revision)
	}
	if got := findRule(t, registry, "custom-visible").Revision; got != 2 {
		t.Fatalf("name-only rule revision = %d, want 2", got)
	}
	if got := findRule(t, registry, "linear-label").Revision; got != 2 {
		t.Fatalf("semantic rule revision = %d, want 2", got)
	}
	if registry.Schedules[0].Revision != 2 {
		t.Fatalf("name-only schedule revision = %d, want 2", registry.Schedules[0].Revision)
	}
	if len(registry.Schedules) != 2 || registry.Schedules[1].Revision != 1 {
		t.Fatalf("new schedule revisions = %#v", registry.Schedules)
	}

	stale := candidate
	stale.Revision = 4
	if _, err := store.UpdateRegistry(4, updated.Settings().Revision, stale, policyTestNow); !errors.Is(err, ErrRegistryConflict) {
		t.Fatalf("stale registry error = %v", err)
	}
	latest := store.Snapshot().Registry()
	if _, err := store.UpdateRegistry(latest.Revision, updated.Settings().Revision-1, latest, policyTestNow); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale settings dependency error = %v", err)
	}
}

func TestStorePreservesTaskControlNoOpAndConflictSemantics(t *testing.T) {
	store := mustCreateStore(t)
	current := store.Snapshot()
	noOp, err := store.SetProject(current.TaskControl().Revision, "project-factory", true, policyTestNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if noOp.Generation() != current.Generation() || noOp.TaskControl().Revision != current.TaskControl().Revision {
		t.Fatalf("no-op advanced revisions: generation %d task %d", noOp.Generation(), noOp.TaskControl().Revision)
	}
	updated, err := store.SetProject(current.TaskControl().Revision, "project-network", true, policyTestNow.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation() != 2 || updated.TaskControl().Revision != 4 || len(updated.TaskControl().EnabledProjectIDs) != 2 {
		t.Fatalf("updated task control = %#v generation=%d", updated.TaskControl(), updated.Generation())
	}
	if _, err := store.SetProject(3, "project-other", true, policyTestNow); !errors.Is(err, ErrTaskControlConflict) {
		t.Fatalf("stale task-control error = %v", err)
	}
}

func TestStoreWorkflowPublicationBindingDeletionAndConflicts(t *testing.T) {
	store := mustCreateStore(t)
	current := store.Snapshot()
	candidate := Workflow{ID: "temporary", Name: "Temporary", Enabled: false, Markdown: "# Temporary\r\n"}
	published, err := store.PublishWorkflow(current.Settings().Revision, 0, candidate, policyTestNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	definition, found := published.Workflow("temporary")
	if !found || definition.Revision != 1 || definition.Markdown != "# Temporary\n" || published.Settings().Revision != 8 || published.Generation() != 2 {
		t.Fatalf("published = %#v found=%t policy=%d generation=%d", definition, found, published.Settings().Revision, published.Generation())
	}
	if _, err := store.UpdateProtectedFeedback(published.Settings().Revision, "temporary", policyTestNow); err == nil {
		t.Fatal("disabled protected workflow was accepted")
	}
	if store.Snapshot().Generation() != published.Generation() {
		t.Fatal("failed binding mutation changed generation")
	}

	definition.Enabled = true
	republished, err := store.PublishWorkflow(published.Settings().Revision, definition.Revision, definition, policyTestNow.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	definition, _ = republished.Workflow("temporary")
	if definition.Revision != 2 || republished.Settings().Revision != 9 || republished.Generation() != 3 {
		t.Fatalf("republished = %#v settings=%d generation=%d", definition, republished.Settings().Revision, republished.Generation())
	}
	bound, err := store.UpdateProtectedFeedback(republished.Settings().Revision, "temporary", policyTestNow.Add(3*time.Minute))
	if err != nil || bound.ProtectedWorkflows().LinearFeedback.WorkflowID != "temporary" || bound.Settings().Revision != 10 || bound.Generation() != 4 {
		t.Fatalf("binding = %#v, %v", bound.Model(), err)
	}
	if _, err := store.DeleteWorkflow(bound.Settings().Revision, definition.Revision, "temporary", policyTestNow); err == nil {
		t.Fatal("protected workflow deletion was accepted")
	}
	if _, err := store.PublishWorkflow(bound.Settings().Revision, definition.Revision-1, definition, policyTestNow); !errors.Is(err, ErrWorkflowConflict) {
		t.Fatalf("stale workflow error = %v", err)
	}
	if _, err := store.UpdateProtectedFeedback(bound.Settings().Revision, "custom-review", policyTestNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	current = store.Snapshot()
	deleted, err := store.DeleteWorkflow(current.Settings().Revision, definition.Revision, "temporary", policyTestNow.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := deleted.Workflow("temporary"); found {
		t.Fatal("unreferenced workflow was not deleted")
	}

	custom, _ := deleted.Workflow("custom-review")
	if _, err := store.DeleteWorkflow(deleted.Settings().Revision, custom.Revision, custom.ID, policyTestNow); err == nil {
		t.Fatal("rule-referenced workflow deletion was accepted")
	}
}

func TestStoreStrictOpenAndCreateConflictPreserveExistingArtifact(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "policy.json")
	snapshot := mustConvertSources(t, populatedSources())
	if _, err := Create(path, snapshot); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path, snapshot); err == nil {
		t.Fatal("create replaced an existing policy artifact")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("existing artifact changed: %v", err)
	}

	unknown := filepath.Join(directory, "unknown.json")
	data := append([]byte(nil), before...)
	for index := len(data) - 2; index >= 0; index-- {
		if data[index] == '}' {
			data = append(data[:index], append([]byte(",\n  \"unknown\": true\n}"), data[index+1:]...)...)
			break
		}
	}
	if err := os.WriteFile(unknown, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(unknown); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if err := os.WriteFile(unknown, append(before, []byte("{}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(unknown); err == nil {
		t.Fatal("trailing content was accepted")
	}

	if err := os.WriteFile(unknown, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unknown, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(unknown); err == nil {
		t.Fatal("public policy permissions were accepted")
	}
	symlink := filepath.Join(directory, "policy-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(symlink); err == nil {
		t.Fatal("symlinked policy artifact was accepted")
	}
}

func TestStoreConvergesAfterPostRenameDirectorySyncFailure(t *testing.T) {
	store := mustCreateStore(t)
	injected := errors.New("injected directory sync failure")
	failed := false
	store.writer = func(path string, snapshot Snapshot) (bool, error) {
		if !failed {
			failed = true
			return writeSnapshotWithDirectorySync(path, snapshot, func(*os.File) error {
				return injected
			})
		}
		return writeSnapshot(path, snapshot)
	}

	before := store.Snapshot()
	candidate := before.Settings()
	updated, err := store.UpdateSettings(candidate.Revision, candidate, policyTestNow.Add(time.Minute))
	if !errors.Is(err, injected) {
		t.Fatalf("post-rename update error = %v", err)
	}
	if updated.Generation() != before.Generation()+1 || updated.Settings().Revision != candidate.Revision+1 {
		t.Fatalf("post-rename snapshot = generation %d settings %d", updated.Generation(), updated.Settings().Revision)
	}
	if current := store.Snapshot(); current.Generation() != updated.Generation() || current.Settings().Revision != updated.Settings().Revision {
		t.Fatalf("memory diverged after rename: generation %d settings %d", current.Generation(), current.Settings().Revision)
	}
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if durable := reopened.Snapshot(); durable.Generation() != updated.Generation() || durable.Settings().Revision != updated.Settings().Revision {
		t.Fatalf("disk diverged after rename: generation %d settings %d", durable.Generation(), durable.Settings().Revision)
	}

	if _, err := store.UpdateSettings(candidate.Revision, candidate, policyTestNow.Add(2*time.Minute)); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale retry error = %v", err)
	}
	nextCandidate := store.Snapshot().Settings()
	next, err := store.UpdateSettings(nextCandidate.Revision, nextCandidate, policyTestNow.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation() != updated.Generation()+1 || next.Settings().Revision != updated.Settings().Revision+1 {
		t.Fatalf("converged update = generation %d settings %d", next.Generation(), next.Settings().Revision)
	}
}

func mustCreateStore(t *testing.T) *Store {
	t.Helper()
	store, err := Create(filepath.Join(t.TempDir(), "policy.json"), mustConvertSources(t, populatedSources()))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func ruleIndex(t *testing.T, registry Registry, id string) int {
	t.Helper()
	for index, rule := range registry.Rules {
		if rule.ID == id {
			return index
		}
	}
	t.Fatalf("rule %s not found", id)
	return -1
}
