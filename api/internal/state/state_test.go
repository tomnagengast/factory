package state

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
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
			ID: 2, Title: "Build every route", Status: InReview, ProjectID: 1,
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
	if len(view.Tasks) != 1 || view.Tasks[0].Title != "Build every route" || view.Tasks[0].Status != InReview {
		t.Fatalf("unexpected tasks: %#v", view.Tasks)
	}
	if view.Tasks[0].UpdatedAt != events[2].At {
		t.Fatalf("task was not touched: %#v", view.Tasks[0])
	}
	if len(view.CommentsFor("task", 2)) != 1 || len(view.ArtifactsFor("task", 2)) != 1 {
		t.Fatal("relations were not projected")
	}
}

func TestCommentDeletionCascadesThroughDescendants(t *testing.T) {
	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	ancestorID, selectedID := int64(2), int64(3)
	childID, siblingID := int64(4), int64(7)
	deletedAt := at.Add(9 * time.Minute)
	events := []eventwire.Event{
		event(1, TaskCreated, at, TaskData{Title: "Task", Status: Todo, ProjectID: 99}),
		event(2, CommentCreated, at.Add(time.Minute), CommentData{
			RelationType: "task", RelationID: 1, Author: "user", Content: "Ancestor",
		}),
		event(3, CommentCreated, at.Add(2*time.Minute), CommentData{
			RelationType: "task", RelationID: 1, ParentCommentID: &ancestorID, Author: "user", Content: "Selected",
		}),
		event(4, CommentCreated, at.Add(3*time.Minute), CommentData{
			RelationType: "task", RelationID: 1, ParentCommentID: &selectedID, Author: "agent", Content: "Child",
		}),
		event(5, CommentDeleted, at.Add(4*time.Minute), IDData{ID: childID}),
		event(6, CommentCreated, at.Add(5*time.Minute), CommentData{
			RelationType: "task", RelationID: 1, ParentCommentID: &childID, Author: "user", Content: "Grandchild",
		}),
		event(7, CommentCreated, at.Add(6*time.Minute), CommentData{
			RelationType: "task", RelationID: 1, ParentCommentID: &ancestorID, Author: "user", Content: "Sibling",
		}),
		event(8, CommentCreated, at.Add(7*time.Minute), CommentData{
			RelationType: "task", RelationID: 1, ParentCommentID: &siblingID, Author: "agent", Content: "Sibling child",
		}),
		event(9, CommentCreated, at.Add(8*time.Minute), CommentData{
			RelationType: "task", RelationID: 1, Author: "user", Content: "Unrelated",
		}),
		event(10, CommentDeleted, deletedAt, IDData{ID: selectedID}),
	}

	view, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{selectedID, childID, 6} {
		comment, found := view.Comment(id)
		if !found || comment.DeletedAt == nil || *comment.DeletedAt != deletedAt || comment.UpdatedAt != deletedAt {
			t.Errorf("deleted comment %d = %#v, found = %v", id, comment, found)
		}
	}
	for _, id := range []int64{ancestorID, siblingID, 8, 9} {
		comment, found := view.Comment(id)
		if !found || comment.DeletedAt != nil {
			t.Errorf("active comment %d = %#v, found = %v", id, comment, found)
		}
	}
	active := view.CommentsFor("task", 1)
	if len(active) != 4 {
		t.Fatalf("active comments = %#v", active)
	}
	if ids := []int64{active[0].ID, active[1].ID, active[2].ID, active[3].ID}; !slices.Equal(ids, []int64{2, 7, 8, 9}) {
		t.Fatalf("active comment IDs = %v", ids)
	}
	replayed, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view, replayed) {
		t.Fatalf("replayed snapshot changed:\nfirst: %#v\nsecond: %#v", view, replayed)
	}
}

func TestTaskReactionsReplayDesiredStateInPaletteOrder(t *testing.T) {
	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := []eventwire.Event{
		event(1, TaskCreated, at, TaskData{Title: "First", Status: Todo, ProjectID: 99}),
		event(2, TaskCreated, at.Add(time.Minute), TaskData{Title: "Second", Status: Todo, ProjectID: 99}),
	}
	for index, emoji := range []string{"👀", "😂", "🎉", "❤️", "👎", "👍"} {
		events = append(events, event(int64(index+3), ReactionUpdated, at.Add(time.Duration(index+2)*time.Minute), ReactionUpdatedData{
			TargetType: "task", TargetID: 1, Emoji: emoji, Active: true,
		}))
	}
	events = append(events,
		event(9, ReactionUpdated, at.Add(8*time.Minute), ReactionUpdatedData{
			TargetType: "task", TargetID: 1, Emoji: "👍", Active: true,
		}),
		event(10, ReactionUpdated, at.Add(9*time.Minute), ReactionUpdatedData{
			TargetType: "task", TargetID: 1, Emoji: "👎", Active: false,
		}),
		event(11, ReactionUpdated, at.Add(10*time.Minute), ReactionUpdatedData{
			TargetType: "task", TargetID: 1, Emoji: "👎", Active: false,
		}),
		event(12, ReactionUpdated, at.Add(11*time.Minute), ReactionUpdatedData{
			TargetType: "task", TargetID: 1, Emoji: "👎", Active: true,
		}),
		event(13, ReactionUpdated, at.Add(12*time.Minute), ReactionUpdatedData{
			TargetType: "task", TargetID: 2, Emoji: "😂", Active: true,
		}),
		event(14, ReactionUpdated, at.Add(13*time.Minute), ReactionUpdatedData{
			TargetType: "task", TargetID: 1, Emoji: "👍🏻", Active: true,
		}),
		event(15, ReactionUpdated, at.Add(14*time.Minute), ReactionUpdatedData{
			TargetType: "workflow", TargetID: 1, Emoji: "👍", Active: true,
		}),
		event(16, ReactionUpdated, at.Add(15*time.Minute), ReactionUpdatedData{
			TargetType: "task", TargetID: 999, Emoji: "👍", Active: true,
		}),
		event(17, TaskDeleted, at.Add(16*time.Minute), IDData{ID: 1}),
		event(18, ReactionUpdated, at.Add(17*time.Minute), ReactionUpdatedData{
			TargetType: "task", TargetID: 1, Emoji: "🎉", Active: false,
		}),
	)

	view, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	first, found := view.Task(1)
	if !found || !slices.Equal(first.Reactions, ReactionEmojis) || first.DeletedAt == nil ||
		first.UpdatedAt != events[16].At || *first.DeletedAt != events[16].At {
		t.Fatalf("first task = %#v, found = %v", first, found)
	}
	second, found := view.Task(2)
	if !found || !slices.Equal(second.Reactions, []string{"😂"}) || second.UpdatedAt != events[12].At {
		t.Fatalf("second task = %#v, found = %v", second, found)
	}

	legacy, err := ProjectEvents(events[:2])
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Tasks[0].Reactions == nil || len(legacy.Tasks[0].Reactions) != 0 {
		t.Fatalf("legacy task reactions = %#v", legacy.Tasks[0].Reactions)
	}
	encoded, _ := json.Marshal(legacy.Tasks[0])
	if !strings.Contains(string(encoded), `"reactions":[]`) {
		t.Fatalf("legacy task JSON = %s", encoded)
	}
}

func TestTaskCommentReactionsIgnoreRelationsButRespectCommentState(t *testing.T) {
	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	rootID, replyID := int64(2), int64(3)
	events := []eventwire.Event{
		event(1, TaskCreated, at, TaskData{Title: "Task", Status: Todo, ProjectID: 99}),
		event(2, CommentCreated, at.Add(time.Minute), CommentData{
			RelationType: "task", RelationID: 1, Author: "user", Content: "Root",
		}),
		event(3, CommentCreated, at.Add(2*time.Minute), CommentData{
			RelationType: "task", RelationID: 1, ParentCommentID: &rootID, Author: "user", Content: "Reply",
		}),
		event(4, CommentCreated, at.Add(3*time.Minute), CommentData{
			RelationType: "task", RelationID: 1, ParentCommentID: &replyID, Author: "agent", Content: "Nested",
		}),
		event(5, CommentCreated, at.Add(4*time.Minute), CommentData{
			RelationType: "workflow", RelationID: 88, Author: "user", Content: "Workflow",
		}),
		event(6, ReactionUpdated, at.Add(5*time.Minute), ReactionUpdatedData{
			TargetType: "comment", TargetID: 2, Emoji: "❤️", Active: true,
		}),
		event(7, ReactionUpdated, at.Add(6*time.Minute), ReactionUpdatedData{
			TargetType: "comment", TargetID: 3, Emoji: "👍", Active: true,
		}),
		event(8, CommentDeleted, at.Add(7*time.Minute), IDData{ID: 2}),
		event(9, ReactionUpdated, at.Add(8*time.Minute), ReactionUpdatedData{
			TargetType: "comment", TargetID: 4, Emoji: "🎉", Active: true,
		}),
		event(10, TaskDeleted, at.Add(9*time.Minute), IDData{ID: 1}),
		event(11, ReactionUpdated, at.Add(10*time.Minute), ReactionUpdatedData{
			TargetType: "comment", TargetID: 3, Emoji: "👀", Active: true,
		}),
		event(12, ReactionUpdated, at.Add(11*time.Minute), ReactionUpdatedData{
			TargetType: "comment", TargetID: 5, Emoji: "😂", Active: true,
		}),
		event(13, ReactionUpdated, at.Add(12*time.Minute), ReactionUpdatedData{
			TargetType: "comment", TargetID: 3, Emoji: "😂", Active: true,
		}),
	}

	view, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := view.Comment(2)
	reply, _ := view.Comment(3)
	nested, _ := view.Comment(4)
	workflow, _ := view.Comment(5)
	if !slices.Equal(root.Reactions, []string{"❤️"}) || root.DeletedAt == nil ||
		*root.DeletedAt != events[7].At || root.UpdatedAt != events[7].At {
		t.Fatalf("root comment = %#v", root)
	}
	if !slices.Equal(reply.Reactions, []string{"👍"}) || reply.DeletedAt == nil ||
		*reply.DeletedAt != events[7].At || reply.UpdatedAt != events[7].At {
		t.Fatalf("reply comment = %#v", reply)
	}
	if nested.Reactions == nil || len(nested.Reactions) != 0 || nested.DeletedAt == nil ||
		*nested.DeletedAt != events[7].At || nested.UpdatedAt != events[7].At {
		t.Fatalf("nested comment = %#v", nested)
	}
	if workflow.Reactions == nil || len(workflow.Reactions) != 0 || workflow.UpdatedAt != workflow.CreatedAt {
		t.Fatalf("workflow comment = %#v", workflow)
	}
}

func TestProjectEventsBuildsImmutableMedia(t *testing.T) {
	at := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	view, err := ProjectEvents([]eventwire.Event{
		event(7, MediaCreated, at, MediaData{
			Name: "screen.png", ContentType: "image/png", Size: 4,
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	media, found := view.Media(7)
	if !found || len(view.MediaFiles) != 1 || media.ID != 7 || media.Name != "screen.png" ||
		media.ContentType != "image/png" || media.Size != 4 || media.CreatedAt != at || media.UpdatedAt != at {
		t.Fatalf("projected media = %#v, found = %v, files = %#v", media, found, view.MediaFiles)
	}
}

func TestPendingWorkflowCommentSkipsAnsweredMessages(t *testing.T) {
	at := time.Now().UTC()
	parent := int64(2)
	intermediate := false
	final := true
	events := []eventwire.Event{
		event(1, WorkflowCreated, at, WorkflowData{Name: "Draft"}),
		event(2, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 1, Author: "user", Content: "Build it",
		}),
		event(3, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 1, ParentCommentID: &parent,
			Author: "agent", Kind: "reasoning", Final: &intermediate, Content: "Inspecting it",
		}),
		event(4, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 1, ParentCommentID: &parent,
			Author: "agent", Kind: "tool-use", Label: "shell", Final: &intermediate,
			Content: "workflow validate /workflows/workflow-1.js",
		}),
		event(5, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 1, ParentCommentID: &parent,
			Author: "agent", Kind: "message", Final: &final, Content: "Done",
		}),
		event(6, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 1, Author: "user", Content: "Revise it",
		}),
	}
	view, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	comment, found := view.PendingWorkflowComment()
	if !found || comment.ID != 6 {
		t.Fatalf("unexpected pending comment: %#v, %v", comment, found)
	}
	comments := view.CommentsFor("workflow", 1)
	if len(comments) != 5 || comments[0].Kind != "message" || comments[0].Final ||
		comments[1].Kind != "reasoning" || comments[1].Final ||
		comments[2].Kind != "tool-use" || comments[2].Label != "shell" || comments[2].Final ||
		comments[3].Kind != "message" || !comments[3].Final ||
		comments[4].ID != 6 {
		t.Fatalf("ordered comments = %#v", comments)
	}
}

func TestHistoricalAgentCommentStillAnswersWorkflowMessage(t *testing.T) {
	at := time.Now().UTC()
	parent := int64(2)
	view, err := ProjectEvents([]eventwire.Event{
		event(1, WorkflowCreated, at, WorkflowData{Name: "Draft"}),
		event(2, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 1, Author: "user", Content: "Build it",
		}),
		{
			ID: 3, Type: CommentCreated, At: at,
			Data: json.RawMessage(`{"relationType":"workflow","relationId":1,"parentCommentId":2,"author":"agent","content":"Done"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := view.PendingWorkflowComment(); found {
		t.Fatal("historical agent response did not answer its parent")
	}
	comment, found := view.Comment(3)
	if !found || comment.Kind != "message" || !comment.Final ||
		comment.ParentCommentID == nil || *comment.ParentCommentID != parent {
		t.Fatalf("historical agent comment = %#v, found = %v", comment, found)
	}
}

func TestDeletedAgentRepliesDoNotAnswerComments(t *testing.T) {
	t.Run("workflow conversation", func(t *testing.T) {
		at := time.Now().UTC()
		parentID := int64(2)
		view, err := ProjectEvents([]eventwire.Event{
			event(1, WorkflowCreated, at, WorkflowData{Name: "Draft"}),
			event(2, CommentCreated, at, CommentData{
				RelationType: "workflow", RelationID: 1, Author: "user", Content: "Build it",
			}),
			event(3, CommentCreated, at, CommentData{
				RelationType: "workflow", RelationID: 1, ParentCommentID: &parentID,
				Author: "agent", Content: "Done",
			}),
			event(4, CommentDeleted, at, IDData{ID: 3}),
		})
		if err != nil {
			t.Fatal(err)
		}
		comment, found := view.PendingWorkflowComment()
		if !found || comment.ID != parentID {
			t.Fatalf("pending workflow comment = %#v, found = %v", comment, found)
		}
	})

	t.Run("waiting workflow run", func(t *testing.T) {
		at := time.Now().UTC()
		gateID, responseID := int64(4), int64(6)
		view, err := ProjectEvents([]eventwire.Event{
			event(1, ProjectCreated, at, ProjectData{Name: "Factory", Path: "/factory"}),
			event(2, TaskCreated, at, TaskData{Title: "Review it", Status: InReview, ProjectID: 1}),
			event(3, WorkflowRunStarted, at, WorkflowRunData{
				TriggerID: 8, WorkflowID: 7, SourceEventID: 2, TaskID: 2,
			}),
			event(4, CommentCreated, at, CommentData{
				RelationType: "task", RelationID: 2, Author: "agent", Content: "Approve it?",
			}),
			event(5, WorkflowRunWaiting, at, WorkflowRunStateData{
				RunID: 3, GateCommentID: gateID, Gate: &WorkflowGate{Key: "gate", Message: "Approve it?"},
			}),
			event(6, CommentCreated, at, CommentData{
				RelationType: "task", RelationID: 2, ParentCommentID: &gateID,
				Author: "user", Content: "Approved",
			}),
			event(7, CommentCreated, at, CommentData{
				RelationType: "task", RelationID: 2, ParentCommentID: &responseID,
				Author: "agent", Content: "Acknowledged",
			}),
			event(8, CommentDeleted, at, IDData{ID: 7}),
		})
		if err != nil {
			t.Fatal(err)
		}
		run, comment, found := view.PendingHumanResponse()
		if !found || run.ID != 3 || comment.ID != responseID {
			t.Fatalf("pending human response = %#v, %#v, found = %v", run, comment, found)
		}
	})
}

func TestHistoricalAgentCommentFinalityIsLimitedToWorkflowReplies(t *testing.T) {
	at := time.Now().UTC()
	parent := int64(1)
	view, err := ProjectEvents([]eventwire.Event{
		event(1, CommentCreated, at, CommentData{
			RelationType: "task", RelationID: 9, Author: "user", Content: "Review it",
		}),
		event(2, CommentCreated, at, CommentData{
			RelationType: "task", RelationID: 9, ParentCommentID: &parent,
			Author: "agent", Content: "Reviewed",
		}),
		event(3, CommentCreated, at, CommentData{
			RelationType: "workflow", RelationID: 8, Author: "agent", Content: "Unparented",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{2, 3} {
		comment, found := view.Comment(id)
		if !found || comment.Final {
			t.Fatalf("historical comment %d = %#v, found = %v", id, comment, found)
		}
	}
}

func TestRunAndCronMarkersAreProjected(t *testing.T) {
	at := time.Now().UTC()
	events := []eventwire.Event{
		event(1, CronFired, at, CronData{TriggerID: 8}),
		event(2, WorkflowRunStarted, at, WorkflowRunData{
			TriggerID: 8, WorkflowID: 7, WorkflowName: "review",
			WorkflowPhases: []string{"Review"}, SourceEventID: 1,
		}),
		event(3, WorkflowRunEventRecorded, at.Add(time.Second), WorkflowRunEventData{
			RunID: 2, Event: json.RawMessage(
				`{"sequence":1,"at":"2026-07-17T12:00:01Z","type":"step.started","workflow":"review","phase":"Review","stepId":1,"kind":"agent","message":"Review it"}`,
			),
		}),
		event(4, WorkflowRunEventRecorded, at.Add(2*time.Second), WorkflowRunEventData{
			RunID: 2, Event: json.RawMessage(
				`{"sequence":2,"at":"2026-07-17T12:00:02Z","type":"step.completed","workflow":"review","phase":"Review","stepId":1,"kind":"agent","result":"done"}`,
			),
		}),
		event(5, WorkflowRunCompleted, at.Add(3*time.Second), WorkflowRunData{
			TriggerID: 8, WorkflowID: 7, SourceEventID: 1, Output: "complete",
		}),
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
	if len(view.Runs) != 1 || view.Runs[0].Status != "completed" || view.Runs[0].Output != "complete" {
		t.Fatalf("run missing: %#v", view.Runs)
	}
	runEvents := view.EventsFor(2)
	if len(runEvents) != 2 || runEvents[0].Type != "step.started" ||
		runEvents[1].Sequence != 2 || string(runEvents[1].Result) != `"done"` {
		t.Fatalf("run events missing: %#v", runEvents)
	}
}

func TestWorkflowRunTaskIDReplay(t *testing.T) {
	at := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		events      []eventwire.Event
		wantTaskID  int64
		wantDeleted bool
	}{
		{
			name: "task creation",
			events: []eventwire.Event{
				event(1, ProjectCreated, at, ProjectData{Name: "Factory", Path: "/factory"}),
				event(2, TaskCreated, at, TaskData{Title: "Create", Status: Todo, ProjectID: 1}),
				event(3, WorkflowRunStarted, at, WorkflowRunData{TriggerID: 8, WorkflowID: 7, SourceEventID: 2}),
			},
			wantTaskID: 2,
		},
		{
			name: "task update",
			events: []eventwire.Event{
				event(1, ProjectCreated, at, ProjectData{Name: "Factory", Path: "/factory"}),
				event(2, TaskCreated, at, TaskData{Title: "Update", Status: Todo, ProjectID: 1}),
				event(3, TaskUpdated, at, TaskData{ID: 2, Title: "Updated", Status: InReview, ProjectID: 1}),
				event(4, WorkflowRunStarted, at, WorkflowRunData{TriggerID: 8, WorkflowID: 7, SourceEventID: 3}),
			},
			wantTaskID: 2,
		},
		{
			name: "task deletion",
			events: []eventwire.Event{
				event(1, ProjectCreated, at, ProjectData{Name: "Factory", Path: "/factory"}),
				event(2, TaskCreated, at, TaskData{Title: "Delete", Status: Todo, ProjectID: 1}),
				event(3, TaskDeleted, at, IDData{ID: 2}),
				event(4, WorkflowRunStarted, at, WorkflowRunData{TriggerID: 8, WorkflowID: 7, SourceEventID: 3}),
			},
			wantTaskID:  2,
			wantDeleted: true,
		},
		{
			name: "explicit task ID",
			events: []eventwire.Event{
				event(1, ProjectCreated, at, ProjectData{Name: "Factory", Path: "/factory"}),
				event(2, TaskCreated, at, TaskData{Title: "Explicit", Status: Todo, ProjectID: 1}),
				event(3, "release.ready", at, map[string]int64{"id": 99}),
				event(4, WorkflowRunStarted, at, WorkflowRunData{TriggerID: 8, WorkflowID: 7, SourceEventID: 3, TaskID: 2}),
			},
			wantTaskID: 2,
		},
		{
			name: "cron event",
			events: []eventwire.Event{
				event(1, CronFired, at, CronData{TriggerID: 8}),
				event(2, WorkflowRunStarted, at, WorkflowRunData{TriggerID: 8, WorkflowID: 7, SourceEventID: 1}),
			},
		},
		{
			name: "custom event",
			events: []eventwire.Event{
				event(1, "release.ready", at, map[string]int64{"id": 2}),
				event(2, WorkflowRunStarted, at, WorkflowRunData{TriggerID: 8, WorkflowID: 7, SourceEventID: 1}),
			},
		},
		{
			name: "missing task",
			events: []eventwire.Event{
				event(1, TaskUpdated, at, TaskData{ID: 99, Title: "Missing", Status: Todo, ProjectID: 1}),
				event(2, WorkflowRunStarted, at, WorkflowRunData{TriggerID: 8, WorkflowID: 7, SourceEventID: 1}),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view, err := ProjectEvents(test.events)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Runs) != 1 || view.Runs[0].TaskID != test.wantTaskID {
				t.Fatalf("runs = %#v, want task ID %d", view.Runs, test.wantTaskID)
			}
			if test.wantDeleted {
				task, found := view.Task(test.wantTaskID)
				if !found || task.DeletedAt == nil {
					t.Fatalf("deleted task = %#v, found = %v", task, found)
				}
			}
		})
	}
}

func TestWorkflowUsageIsProjectedFromRunStarts(t *testing.T) {
	at := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	events := []eventwire.Event{
		event(1, WorkflowDiscovered, at, WorkflowData{Name: "active"}),
		event(2, WorkflowDiscovered, at, WorkflowData{Name: "less-used"}),
		event(3, WorkflowDiscovered, at, WorkflowData{Name: "unused"}),
		event(4, TaskCreated, at, TaskData{Title: "First", Status: Todo, ProjectID: 99}),
		event(5, TaskUpdated, at, TaskData{ID: 4, Title: "First revised", Status: Todo, ProjectID: 99}),
		event(6, TaskCreated, at, TaskData{Title: "Second", Status: Todo, ProjectID: 99}),
		event(7, TaskDeleted, at, IDData{ID: 6}),
		event(8, WorkflowRunStarted, at, WorkflowRunData{
			TriggerID: 20, WorkflowID: 1, SourceEventID: 4, TaskID: 4,
		}),
		event(9, WorkflowRunStarted, at, WorkflowRunData{
			TriggerID: 20, WorkflowID: 1, SourceEventID: 5,
		}),
		event(10, WorkflowRunStarted, at, WorkflowRunData{
			TriggerID: 20, WorkflowID: 1, SourceEventID: 6, TaskID: 6,
		}),
		event(11, WorkflowRunStarted, at, WorkflowRunData{
			TriggerID: 20, WorkflowID: 1, SourceEventID: 7,
		}),
		event(12, WorkflowRunStarted, at, WorkflowRunData{
			TriggerID: 21, WorkflowID: 1, SourceEventID: 100,
		}),
		event(13, WorkflowRunStarted, at, WorkflowRunData{
			TriggerID: 22, WorkflowID: 2, SourceEventID: 101,
		}),
		event(14, WorkflowRunWaiting, at.Add(time.Second), WorkflowRunStateData{RunID: 9}),
		event(15, WorkflowRunCompleted, at.Add(2*time.Second), WorkflowRunData{
			TriggerID: 20, WorkflowID: 1, SourceEventID: 6,
		}),
		event(16, WorkflowRunFailed, at.Add(3*time.Second), WorkflowRunData{
			TriggerID: 20, WorkflowID: 1, SourceEventID: 7, Error: "failed",
		}),
	}

	view, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}

	want := map[int64]struct {
		runs  int
		tasks int
	}{
		1: {runs: 5, tasks: 2},
		2: {runs: 1, tasks: 0},
		3: {runs: 0, tasks: 0},
	}
	for _, workflow := range view.Workflows {
		expected := want[workflow.ID]
		if workflow.RunCount != expected.runs || workflow.TaskCount != expected.tasks {
			t.Errorf("workflow %d usage = %d runs, %d tasks; want %d runs, %d tasks",
				workflow.ID, workflow.RunCount, workflow.TaskCount, expected.runs, expected.tasks)
		}
	}
	for runID, status := range map[int64]string{8: "running", 9: "waiting", 10: "completed", 11: "failed", 12: "running"} {
		run, found := view.Run(runID)
		if !found || run.Status != status {
			t.Errorf("run %d = %#v, found = %v; want status %q", runID, run, found, status)
		}
	}
}

func TestWaitingRunFindsAUserTaskCommentAndResumes(t *testing.T) {
	at := time.Now().UTC()
	settings := DefaultSettings
	events := []eventwire.Event{
		event(1, ProjectCreated, at, ProjectData{Name: "Factory", Path: "/factory"}),
		event(2, TaskCreated, at, TaskData{
			Title: "Review it", Status: InReview, ProjectID: 1,
		}),
		event(3, WorkflowRunStarted, at, WorkflowRunData{
			TriggerID: 8, WorkflowID: 7, WorkflowName: "review",
			SourceEventID: 2, TaskID: 2, Directory: "/factory", Source: "/review.js",
			Settings: &settings, Arguments: json.RawMessage(`{"runId":3}`),
		}),
		event(4, WorkflowRunEventRecorded, at, WorkflowRunEventData{
			RunID: 3, Event: json.RawMessage(
				`{"sequence":1,"at":"2026-07-19T12:00:00Z","type":"runtime.suspended","workflow":"review","phase":"Review","stepId":1,"key":"gate-key","backend":"human","kind":"gate","message":"Approve it?"}`,
			),
		}),
		event(5, CommentCreated, at, CommentData{
			RelationType: "task", RelationID: 2, Author: "agent", Content: "Approve it?",
		}),
		event(6, WorkflowRunWaiting, at, WorkflowRunStateData{
			RunID: 3, GateCommentID: 5, Gate: &WorkflowGate{
				Workflow: "review", Phase: "Review", StepID: 1,
				Key: "gate-key", Message: "Approve it?",
			},
		}),
		event(7, ReactionUpdated, at, ReactionUpdatedData{
			TargetType: "comment", TargetID: 5, Emoji: "👍", Active: true,
		}),
	}
	view, err := ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	run, found := view.Run(3)
	if !found || run.Status != "waiting" || run.TaskID != 2 ||
		run.WaitingGate == nil || run.WaitingGate.Key != "gate-key" ||
		run.Settings == nil || run.Settings.Harness != Codex ||
		string(run.Arguments) != `{"runId":3}` {
		t.Fatalf("waiting run = %#v, found = %v", run, found)
	}
	if _, _, found := view.PendingHumanResponse(); found {
		t.Fatal("reaction satisfied a waiting human gate")
	}

	events = append(events, event(8, CommentCreated, at, CommentData{
		RelationType: "task", RelationID: 2, Author: "user", Content: "Approved.",
	}))
	view, err = ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	selected, response, found := view.PendingHumanResponse()
	if !found || selected.ID != 3 || response.ID != 8 || response.Content != "Approved." {
		t.Fatalf("pending response = %#v, %#v, %v", selected, response, found)
	}

	events = append(events, event(9, WorkflowRunResumed, at, WorkflowRunStateData{
		RunID: 3, ResponseCommentID: 8,
	}))
	view, err = ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	run, _ = view.Run(3)
	if run.Status != "running" || run.WaitingGate != nil || run.ResponseCommentID != 8 {
		t.Fatalf("resumed run = %#v", run)
	}
	if _, _, found := view.PendingHumanResponse(); found {
		t.Fatal("resumed response remained pending")
	}
}

func TestSettingsDefaultAndReplay(t *testing.T) {
	view, err := ProjectEvents(nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.Settings != DefaultSettings {
		t.Fatalf("default settings = %#v", view.Settings)
	}
	selected := Settings{
		Harness: Claude, Model: "sonnet", Reasoning: "high", WorkflowCapacity: 4,
	}
	view, err = ProjectEvents([]eventwire.Event{
		event(1, SettingsUpdated, time.Now().UTC(), selected),
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Settings != selected || !ValidSettings(view.Settings) {
		t.Fatalf("replayed settings = %#v", view.Settings)
	}
	for _, capacity := range []int{MinWorkflowCapacity, MaxWorkflowCapacity} {
		valid := selected
		valid.WorkflowCapacity = capacity
		if !ValidSettings(valid) {
			t.Fatalf("workflow capacity %d was rejected", capacity)
		}
	}
	if ValidSettings(Settings{
		Harness: Claude, Model: "gpt-5.6-sol", Reasoning: "high", WorkflowCapacity: 4,
	}) {
		t.Fatal("cross-harness model was accepted")
	}
	for _, capacity := range []int{-1, 11} {
		invalid := selected
		invalid.WorkflowCapacity = capacity
		if ValidSettings(invalid) {
			t.Fatalf("workflow capacity %d was accepted", capacity)
		}
	}
}

func TestSettingsReplayDefaultsMissingWorkflowCapacity(t *testing.T) {
	at := time.Now().UTC()
	view, err := ProjectEvents([]eventwire.Event{{
		ID: 1, Type: SettingsUpdated, At: at,
		Data: json.RawMessage(`{"harness":"claude","model":"sonnet","reasoning":"high"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Settings.Harness != Claude ||
		view.Settings.WorkflowCapacity != DefaultWorkflowCapacity {
		t.Fatalf("legacy settings = %#v", view.Settings)
	}
}

func TestTriggerEnabledStateReplaysUpdates(t *testing.T) {
	at := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	schedule := "0 9 * * 1-5"
	view, err := ProjectEvents([]eventwire.Event{
		event(1, TriggerCreated, at, TriggerData{
			EventType: CronFired, Schedule: &schedule, WorkflowID: 8, Enabled: true,
		}),
		event(2, TriggerUpdated, at.Add(time.Minute), TriggerData{
			ID: 1, EventType: CronFired, Schedule: &schedule, WorkflowID: 8, Enabled: false,
		}),
		event(3, TriggerUpdated, at.Add(2*time.Minute), TriggerData{
			ID: 1, EventType: CronFired, Schedule: &schedule, WorkflowID: 8, Enabled: true,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	trigger, found := view.Trigger(1)
	if !found || !trigger.Enabled || trigger.UpdatedAt != at.Add(2*time.Minute) ||
		trigger.EventType != CronFired || trigger.Schedule == nil || *trigger.Schedule != schedule {
		t.Fatalf("replayed trigger = %#v, found = %v", trigger, found)
	}
}

func TestTriggerReplayDefaultsHistoricalEventsToEnabled(t *testing.T) {
	at := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	view, err := ProjectEvents([]eventwire.Event{
		{
			ID: 1, Type: TriggerCreated, At: at,
			Data: json.RawMessage(`{"eventType":"release.ready","workflowId":8}`),
		},
		{
			ID: 2, Type: TriggerUpdated, At: at.Add(time.Minute),
			Data: json.RawMessage(`{"id":1,"eventType":"release.shipped","workflowId":8}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	trigger, found := view.Trigger(1)
	if !found || !trigger.Enabled || trigger.EventType != "release.shipped" {
		t.Fatalf("historical trigger = %#v, found = %v", trigger, found)
	}
}

func event(id int64, eventType string, at time.Time, data any) eventwire.Event {
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return eventwire.Event{ID: id, Type: eventType, At: at, Data: encoded}
}
