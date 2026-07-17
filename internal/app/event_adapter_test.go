package app

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/linearidentity"
	"github.com/tomnagengast/factory/internal/server"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/taskstore"
)

var (
	_ server.EventStore                = (*ActivityAdapter)(nil)
	_ server.LinearIdentityBinder      = (*LinearIdentityAdapter)(nil)
	_ taskservice.LinearIdentityBinder = (*LinearIdentityAdapter)(nil)
	_ server.GitHubEventStore          = GitHubProjection{}
	_ server.LinearCommentStore        = LinearProjection{}
)

func TestEventAdaptersWriteOnlyCanonicalOwners(t *testing.T) {
	root := filepath.Join(t.TempDir(), "activity")
	if err := eventwire.MaterializeActivity(root, eventwire.ActivityProjection{Schema: eventwire.ActivitySchemaVersion, Events: []eventwire.ActivityRecord{}}, map[string][]byte{}); err != nil {
		t.Fatal(err)
	}
	store, err := eventwire.OpenActivityStore(root, 10)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewActivityAdapter(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 17, 6, 30, 0, 0, time.UTC)
	if err := adapter.StagePayload("linear-1", []byte(`{"issue":"ENG-47"}`)); err != nil {
		t.Fatal(err)
	}
	if added, err := adapter.AddStaged("linear-1", activity.Event{Type: "Comment", Action: "create", ReceivedAt: now}); err != nil || !added {
		t.Fatalf("add staged activity: added=%v err=%v", added, err)
	}
	if snapshot := adapter.Snapshot(); snapshot.Total != 1 || len(snapshot.Events) != 1 || snapshot.Events[0].Type != "Comment" {
		t.Fatalf("activity snapshot = %#v", snapshot)
	}

	tasks, err := taskstore.Create(filepath.Join(t.TempDir(), "tasks.jsonl"), taskstore.Snapshot{
		Schema: taskstore.SchemaVersion, NextSequence: 1, Tasks: []taskstore.Task{}, Messages: []taskstore.Message{},
		Links: []taskstore.Link{}, Gates: []taskstore.Gate{}, Outcomes: []taskstore.OperationOutcome{},
		Operations: []taskstore.TaskOperation{}, LinearBindings: []taskstore.LinearBinding{},
	})
	if err != nil {
		t.Fatal(err)
	}
	identities, err := NewLinearIdentityAdapter(tasks)
	if err != nil {
		t.Fatal(err)
	}
	uuid := "01234567-89ab-4def-8123-456789abcdef"
	if added, err := identities.Bind("eng-47", uuid); err != nil || !added {
		t.Fatalf("bind identity: added=%v err=%v", added, err)
	}
	if _, err := identities.Bind("ENG-47", "11234567-89ab-4def-8123-456789abcdef"); !errors.Is(err, linearidentity.ErrConflict) {
		t.Fatalf("identity conflict = %v", err)
	}
}

func TestProviderProjectionCursorIsInMemoryAndMonotonic(t *testing.T) {
	cursor := NewProviderProjectionCursor(3, 7)
	github := GitHubProjection{Cursor: cursor}
	linear := LinearProjection{Cursor: cursor}
	if added, err := github.AddAt(4, githubhook.Event{}); err != nil || !added || github.Total() != 4 {
		t.Fatalf("GitHub projection: added=%v total=%d err=%v", added, github.Total(), err)
	}
	if added, err := github.AddAt(2, githubhook.Event{}); err != nil || added || github.Total() != 4 {
		t.Fatalf("GitHub cursor regressed: added=%v total=%d err=%v", added, github.Total(), err)
	}
	if added, err := linear.AddAt(8, linearhook.Event{}); err != nil || !added || linear.Total() != 8 {
		t.Fatalf("Linear projection: added=%v total=%d err=%v", added, linear.Total(), err)
	}
}
