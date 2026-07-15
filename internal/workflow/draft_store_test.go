package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDraftStorePersistsAndConflicts(t *testing.T) {
	now := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "workflow-drafts.json")
	store, err := OpenDraftStore(path)
	if err != nil {
		t.Fatal(err)
	}
	draft := Draft{WorkflowID: "draft-a", Revision: 1, Name: "Draft A", Markdown: "# Draft", UpdatedAt: now}
	if _, err := store.Create(draft); err != nil {
		t.Fatal(err)
	}
	draft.Markdown = "# Updated"
	saved, err := store.Save(draft.WorkflowID, 1, 0, draft, now.Add(time.Minute))
	if err != nil || saved.Revision != 2 {
		t.Fatalf("save = %#v, %v", saved, err)
	}
	if _, err := store.Save(draft.WorkflowID, 1, 0, draft, now); !errors.Is(err, ErrDraftConflict) {
		t.Fatalf("stale save error = %v", err)
	}
	reopened, err := OpenDraftStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, found := reopened.Draft(draft.WorkflowID)
	if !found || got.Markdown != "# Updated" || got.Revision != 2 {
		t.Fatalf("reopened draft = %#v, %t", got, found)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("draft mode = %o", info.Mode().Perm())
	}
}

func TestMissingDraftStoreIsEmptyAndCorruptionIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow-drafts.json")
	store, err := OpenDraftStore(path)
	if err != nil || len(store.Snapshot().Drafts) != 0 {
		t.Fatalf("empty open = %#v, %v", store, err)
	}
	if err := os.WriteFile(path, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDraftStore(path); err == nil {
		t.Fatal("corrupt draft store opened")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{invalid" {
		t.Fatalf("corrupt data changed: %q, %v", data, err)
	}
}
