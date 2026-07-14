package triggerregistry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/settings"
)

var registryTestNow = time.Date(2026, time.July, 14, 22, 0, 0, 0, time.UTC)

func TestStoreSynthesizesDefaultsUntilFirstMutation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "triggers.json")
	configuration := settings.Defaults(3)
	store, err := Open(path, Defaults(configuration, "actor-tom"), configuration)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent registry was written: %v", err)
	}
	candidate := store.Snapshot()
	candidate.Rules[0].Name = "Human comment"
	updated, err := store.Update(candidate.Revision, candidate, configuration, registryTestNow)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Revision != 1 || updated.Rules[0].Revision != 1 || updated.UpdatedAt != registryTestNow {
		t.Fatalf("updated = %#v", updated)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o, want 600", info.Mode().Perm())
	}
	reopened, err := Open(path, Defaults(configuration, "actor-tom"), configuration)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.Snapshot(); got.Revision != 1 || got.Rules[0].Name != "Human comment" {
		t.Fatalf("reopened = %#v", got)
	}
}

func TestStoreIncrementsOnlySemanticRuleAndScheduleRevisions(t *testing.T) {
	t.Parallel()
	configuration := settings.Defaults(3)
	store, err := Open(filepath.Join(t.TempDir(), "triggers.json"), Defaults(configuration, "actor-tom"), configuration)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	candidate := store.Snapshot()
	candidate.Rules[0].Name = "Comment renamed"
	candidate.Rules[1].MaxHop++
	candidate.Schedules = append(candidate.Schedules, Schedule{
		ID: "daily", Name: "Daily", Enabled: true, Cron: "0 8 * * *", Timezone: "America/Los_Angeles",
	})
	updated, err := store.Update(candidate.Revision, candidate, configuration, registryTestNow)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	comment, _ := updated.Rule("linear-comment")
	label, _ := updated.Rule("linear-label")
	if comment.Revision != 1 || label.Revision != 2 || updated.Schedules[0].Revision != 1 {
		t.Fatalf("revisions: comment=%d label=%d schedule=%d", comment.Revision, label.Revision, updated.Schedules[0].Revision)
	}

	stale := store.Snapshot()
	stale.Revision--
	if _, err := store.Update(stale.Revision, stale, configuration, registryTestNow.Add(time.Minute)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
}

func TestStoreRejectsServerOwnedMutationWithoutChangingState(t *testing.T) {
	t.Parallel()
	configuration := settings.Defaults(3)
	path := filepath.Join(t.TempDir(), "triggers.json")
	store, err := Open(path, Defaults(configuration, "actor-tom"), configuration)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	candidate := store.Snapshot()
	candidate.LegacyRollbackIncompatible = true
	if _, err := store.Update(candidate.Revision, candidate, configuration, registryTestNow); err == nil {
		t.Fatal("server-owned mutation was accepted")
	}
	if got := store.Snapshot(); got.Revision != 0 || got.LegacyRollbackIncompatible {
		t.Fatalf("state changed: %#v", got)
	}
	marked, err := store.MarkLegacyRollbackIncompatible(registryTestNow)
	if err != nil || !marked.LegacyRollbackIncompatible || marked.Revision != 1 {
		t.Fatalf("mark = %#v, %v", marked, err)
	}
	again, err := store.MarkLegacyRollbackIncompatible(registryTestNow.Add(time.Minute))
	if err != nil || again.Revision != marked.Revision || again.UpdatedAt != marked.UpdatedAt {
		t.Fatalf("idempotent mark = %#v, %v", again, err)
	}
}

func TestStoreFailsClosedOnUnknownFieldsAndOversizedFile(t *testing.T) {
	t.Parallel()
	configuration := settings.Defaults(3)
	directory := t.TempDir()
	unknown := filepath.Join(directory, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"schema":1,"revision":0,"rules":[],"schedules":[],"unknown":true}`), 0o600); err != nil {
		t.Fatalf("write unknown: %v", err)
	}
	if _, err := Open(unknown, Defaults(configuration, "actor-tom"), configuration); err == nil {
		t.Fatal("unknown field was accepted")
	}
	oversized := filepath.Join(directory, "oversized.json")
	if err := os.WriteFile(oversized, make([]byte, maxRegistryBytes+1), 0o600); err != nil {
		t.Fatalf("write oversized: %v", err)
	}
	if _, err := Open(oversized, Defaults(configuration, "actor-tom"), configuration); err == nil {
		t.Fatal("oversized registry was accepted")
	}
}
