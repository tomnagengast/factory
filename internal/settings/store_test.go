package settings

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreDefaultsUpdateAndReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data", "settings.json")
	store, err := Open(path, Defaults(3))
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	if got := store.Snapshot(); got.Revision != 0 || got.Runtime.MaxConcurrentRuns != 3 {
		t.Fatalf("default snapshot = %#v", got)
	}
	now := time.Date(2026, time.July, 13, 4, 0, 0, 0, time.UTC)
	candidate := store.Snapshot()
	candidate.Runtime.MaxConcurrentRuns = 4
	updated, err := store.Update(candidate.Revision, candidate, now)
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if updated.Revision != 1 || updated.UpdatedAt != now || updated.Runtime.MaxConcurrentRuns != 4 {
		t.Fatalf("updated snapshot = %#v", updated)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat settings: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %o, want 600", info.Mode().Perm())
	}
	reopened, err := Open(path, Defaults(2))
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	if got := reopened.Snapshot(); got.Revision != 1 || got.Runtime.MaxConcurrentRuns != 4 {
		t.Fatalf("reopened snapshot = %#v", got)
	}
}

func TestStoreRejectsConflictAndInvalidCandidateWithoutChangingState(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "settings.json"), Defaults(3))
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	candidate := store.Snapshot()
	candidate.Agents.Principal.Model = "bad model"
	if _, err := store.Update(0, candidate, time.Now()); err == nil {
		t.Fatal("invalid update succeeded")
	}
	if got := store.Snapshot(); got.Revision != 0 || got.Agents.Principal.Model != "gpt-5.6-sol" {
		t.Fatalf("state changed after invalid update: %#v", got)
	}
	if _, err := store.Update(1, store.Snapshot(), time.Now()); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestStoreRejectsUnknownFieldsAndInvalidPersistedState(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	unknown := filepath.Join(directory, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"schema":1,"unknown":true}`), 0o600); err != nil {
		t.Fatalf("write unknown state: %v", err)
	}
	if _, err := Open(unknown, Defaults(3)); err == nil {
		t.Fatal("unknown field was accepted")
	}
	invalid := filepath.Join(directory, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"schema":2}`), 0o600); err != nil {
		t.Fatalf("write invalid state: %v", err)
	}
	if _, err := Open(invalid, Defaults(3)); err == nil {
		t.Fatal("invalid persisted state was accepted")
	}
}

func TestStoreConcurrentSnapshotsAndUpdates(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "settings.json"), Defaults(3))
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 50 {
				_ = store.Snapshot()
			}
		}()
	}
	for range 10 {
		current := store.Snapshot()
		if _, err := store.Update(current.Revision, current, time.Now()); err != nil {
			t.Fatalf("update settings: %v", err)
		}
	}
	wait.Wait()
	if got := store.Snapshot().Revision; got != 10 {
		t.Fatalf("revision = %d, want 10", got)
	}
}
