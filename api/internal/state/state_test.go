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
		event(7, CommentCreated, at, CommentData{
			RelationType: "task", RelationID: 2, Author: "user", Content: "Approved.",
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
	selected, response, found := view.PendingHumanResponse()
	if !found || selected.ID != 3 || response.ID != 7 || response.Content != "Approved." {
		t.Fatalf("pending response = %#v, %#v, %v", selected, response, found)
	}

	events = append(events, event(8, WorkflowRunResumed, at, WorkflowRunStateData{
		RunID: 3, ResponseCommentID: 7,
	}))
	view, err = ProjectEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	run, _ = view.Run(3)
	if run.Status != "running" || run.WaitingGate != nil || run.ResponseCommentID != 7 {
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
