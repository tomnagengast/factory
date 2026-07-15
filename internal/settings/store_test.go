package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/workflow"
)

var storeTestNow = time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)

func TestStoreRoundTripConflictAndMonotonicMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := Open(path, Defaults(3))
	if err != nil {
		t.Fatal(err)
	}
	candidate := store.Snapshot()
	candidate.Runtime.MaxConcurrentRuns = 4
	updated, err := store.Update(candidate.Revision, candidate, storeTestNow)
	if err != nil || updated.Revision != 1 {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	if _, err := store.Update(0, candidate, storeTestNow); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	marked, err := store.MarkWorkflowRollbackIncompatible(storeTestNow.Add(time.Minute))
	if err != nil || !marked.WorkflowRollbackIncompatible || marked.Revision != 2 {
		t.Fatalf("mark = %#v, %v", marked, err)
	}
	reopened, err := Open(path, Defaults(3))
	if err != nil || !reopened.Snapshot().WorkflowRollbackIncompatible {
		t.Fatalf("reopen = %#v, %v", reopened, err)
	}
}

func TestStoreMigratesSchema1AndPreservesBackup(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.json")
	legacy := legacySnapshot{
		Schema: 1, Revision: 7, UpdatedAt: storeTestNow,
		Triggers: Triggers{
			LinearLabel:   LinearLabelTrigger{Enabled: true, Label: "Factory", WorkflowID: "full-sdlc"},
			LinearComment: Trigger{Enabled: true, WorkflowID: "full-sdlc"},
		},
		Workflows: DefaultsLegacyWorkflows(), Agents: Defaults(3).Agents, Runtime: RuntimeSettings{MaxConcurrentRuns: 4},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, Defaults(3))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	if snapshot.Schema != SchemaVersion || snapshot.Revision != 7 || snapshot.Runtime.MaxConcurrentRuns != 4 {
		t.Fatalf("migrated snapshot = %#v", snapshot)
	}
	if snapshot.ProtectedWorkflows.LinearFeedback.WorkflowID != "full-sdlc" || snapshot.Workflows[0].Revision != 1 {
		t.Fatalf("migrated workflow = %#v", snapshot.Workflows[0])
	}
	if got := snapshot.Workflows[0].Markdown; !containsAll(got, "# Full SDLC", "## Migrated operator guidance", "Research the issue") {
		t.Fatalf("migrated Markdown missing guidance: %q", got)
	}
	backupPath := filepath.Join(directory, "settings.schema1.backup.json")
	backup, err := os.ReadFile(backupPath)
	if err != nil || string(backup) != string(data) {
		t.Fatalf("backup mismatch: %v", err)
	}
	info, err := os.Stat(backupPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, %v", info, err)
	}
	if _, err := Open(path, Defaults(3)); err != nil {
		t.Fatalf("schema 2 reopen: %v", err)
	}
}

func TestSchema1BackupConflictFailsClosed(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.json")
	legacy := legacySnapshot{
		Schema: 1, Triggers: Defaults(3).Triggers, Workflows: DefaultsLegacyWorkflows(),
		Agents: Defaults(3).Agents, Runtime: Defaults(3).Runtime,
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "settings.schema1.backup.json"), []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, Defaults(3)); err == nil {
		t.Fatal("conflicting backup did not fail")
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(data) {
		t.Fatalf("settings changed on conflict: %v", err)
	}
}

func DefaultsLegacyWorkflows() []workflow.LegacyDefinition {
	return []workflow.LegacyDefinition{{
		ID: "full-sdlc", Name: "Full SDLC", Enabled: true, Runner: "do",
		Steps: []string{"Research the issue", "Implement the approved plan"},
	}}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
