package state

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tomnagengast/factory/api/internal/eventwire"
)

func TestProjectEventsBuildsDomainState(t *testing.T) {
	at := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	events := []eventwire.Event{
		event(1, ProjectCreated, at, ProjectData{Name: "Factory", Path: "/factory"}),
		event(2, TaskCreated, at.Add(time.Minute), TaskData{
			Title: "Build routes", Status: Todo, ProjectID: 1,
		}),
		event(3, TaskUpdated, at.Add(2*time.Minute), TaskData{
			ID: 2, Title: "Build every route", Status: InProgress, ProjectID: 1,
		}),
		event(4, CommentCreated, at.Add(3*time.Minute), CommentData{
			RelationType: "task", RelationID: 2, Author: "user", Content: "Keep it small.",
		}),
		event(5, ArtifactCreated, at.Add(4*time.Minute), ArtifactData{
			Type: "link", Content: "https://example.com", RelationType: "task", RelationID: 2,
		}),
	}
	view, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Projects) != 1 || view.Projects[0].ID != 1 {
		t.Fatalf("unexpected projects: %#v", view.Projects)
	}
	if len(view.Tasks) != 1 || view.Tasks[0].Title != "Build every route" || view.Tasks[0].Status != InProgress {
		t.Fatalf("unexpected tasks: %#v", view.Tasks)
	}
	if view.Tasks[0].UpdatedAt != events[2].At {
		t.Fatalf("task was not touched: %#v", view.Tasks[0])
	}
	if len(view.CommentsFor("task", 2)) != 1 || len(view.ArtifactsFor("task", 2)) != 1 {
		t.Fatal("relations were not projected")
	}
}

func TestPendingWorkflowCommentSkipsAnsweredMessages(t *testing.T) {
	at := time.Now().UTC()
	parent := int64(2)
	events := []eventwire.Event{
		event(1, WorkflowCreated, at, WorkflowData{Name: "Draft"}),
		event(2, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 1, Author: "user", Content: "Build it",
		}),
		event(3, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 1, ParentCommentID: &parent,
			Author: "agent", Content: "Done",
		}),
		event(4, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 1, Author: "user", Content: "Revise it",
		}),
	}
	view, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	comment, found := view.PendingWorkflowComment()
	if !found || comment.ID != 4 {
		t.Fatalf("unexpected pending comment: %#v, %v", comment, found)
	}
}

func TestRunAndCronMarkersAreProjected(t *testing.T) {
	at := time.Now().UTC()
	events := []eventwire.Event{
		event(1, CronFired, at, CronData{TriggerID: 8}),
		event(2, WorkflowRunStarted, at, WorkflowRunData{TriggerID: 8, SourceEventID: 1}),
	}
	view, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if !view.RunStarted(8, 1) {
		t.Fatal("run marker missing")
	}
	if last, found := view.LastCron(8); !found || !last.Equal(at) {
		t.Fatalf("cron marker missing: %v, %v", last, found)
	}
}

func event(id int64, eventType string, at time.Time, data any) eventwire.Event {
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return eventwire.Event{ID: id, Type: eventType, At: at, Data: encoded}
}
