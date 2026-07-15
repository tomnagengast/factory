package taskcontrol

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreDefaultsDarkAndScopesEnablement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task-control.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.Enabled("project-factory") || len(store.Snapshot().EnabledProjectIDs) != 0 {
		t.Fatal("native tasks were enabled by default")
	}
	updated, err := store.SetProject(0, "project-factory", true, time.Now())
	if err != nil || updated.Revision != 1 || !store.Enabled("project-factory") || store.Enabled("project-network") {
		t.Fatalf("enabled snapshot=%#v err=%v", updated, err)
	}
	if _, err := store.SetProject(0, "project-network", true, time.Now()); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("revision conflict = %v", err)
	}
	reopened, err := Open(path)
	if err != nil || !reopened.Enabled("project-factory") || reopened.Enabled("project-network") {
		t.Fatalf("reopened control=%#v err=%v", reopened.Snapshot(), err)
	}
}
