package state

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/tomnagengast/factory/api/internal/eventwire"
)

const (
	ProjectCreated          = "project.created"
	ProjectUpdated          = "project.updated"
	ProjectDeleted          = "project.deleted"
	TaskCreated             = "task.created"
	TaskUpdated             = "task.updated"
	TaskDeleted             = "task.deleted"
	CommentCreated          = "comment.created"
	CommentUpdated          = "comment.updated"
	CommentDeleted          = "comment.deleted"
	ArtifactCreated         = "artifact.created"
	ArtifactUpdated         = "artifact.updated"
	ArtifactDeleted         = "artifact.deleted"
	TriggerCreated          = "trigger.created"
	TriggerUpdated          = "trigger.updated"
	TriggerDeleted          = "trigger.deleted"
	WorkflowCreated         = "workflow.created"
	WorkflowDiscovered      = "workflow.discovered"
	WorkflowUpdated         = "workflow.updated"
	WorkflowDeleted         = "workflow.deleted"
	CronFired               = "cron"
	WorkflowRunStarted      = "workflow.run.started"
	WorkflowRunStepRecorded = "workflow.run.step"
	WorkflowRunCompleted    = "workflow.run.completed"
	WorkflowRunFailed       = "workflow.run.failed"
	SettingsUpdated         = "settings.updated"
)

const (
	Codex  = "codex"
	Claude = "claude"
)

type Record struct {
	ID        int64      `json:"id"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	DeletedAt *time.Time `json:"deletedAt,omitempty"`
}

type Project struct {
	Record
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Repo        *string `json:"repo,omitempty"`
	Path        string  `json:"path"`
	URL         *string `json:"url,omitempty"`
}

type TaskStatus string

const (
	Backlog    TaskStatus = "backlog"
	Todo       TaskStatus = "todo"
	InProgress TaskStatus = "in progress"
	Done       TaskStatus = "done"
	Canceled   TaskStatus = "canceled"
)

var TaskStatuses = []TaskStatus{Backlog, Todo, InProgress, Done, Canceled}

type Task struct {
	Record
	Title        string     `json:"title"`
	Description  *string    `json:"description,omitempty"`
	ParentTaskID *int64     `json:"parentTaskId,omitempty"`
	Status       TaskStatus `json:"status"`
	ProjectID    int64      `json:"projectId"`
}

type Comment struct {
	Record
	RelationType    string `json:"relationType"`
	RelationID      int64  `json:"relationId"`
	ParentCommentID *int64 `json:"parentCommentId,omitempty"`
	Author          string `json:"author"`
	Content         string `json:"content"`
}

type Artifact struct {
	Record
	Name         *string `json:"name,omitempty"`
	Type         string  `json:"type"`
	Content      string  `json:"content"`
	RelationType string  `json:"relationType"`
	RelationID   int64   `json:"relationId"`
}

type Trigger struct {
	Record
	EventType  string  `json:"eventType"`
	Schedule   *string `json:"schedule,omitempty"`
	WorkflowID int64   `json:"workflowId"`
}

type Workflow struct {
	Record
	Name        string   `json:"name"`
	Description *string  `json:"description,omitempty"`
	Path        *string  `json:"path,omitempty"`
	Scope       *string  `json:"scope,omitempty"`
	Phases      []string `json:"phases"`
	Mutating    bool     `json:"mutating"`
}

type WorkflowRun struct {
	ID             int64     `json:"id"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	TriggerID      int64     `json:"triggerId"`
	WorkflowID     int64     `json:"workflowId"`
	WorkflowName   string    `json:"workflowName"`
	WorkflowPhases []string  `json:"workflowPhases"`
	SourceEventID  int64     `json:"sourceEventId"`
	Status         string    `json:"status"`
	Output         string    `json:"output,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type WorkflowRunStep struct {
	ID        int64           `json:"id"`
	RunID     int64           `json:"runId"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	Key       string          `json:"key,omitempty"`
	Phase     string          `json:"phase,omitempty"`
	Kind      string          `json:"kind"`
	Backend   string          `json:"backend,omitempty"`
	Message   string          `json:"message"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Done      bool            `json:"done"`
}

type Settings struct {
	Harness   string `json:"harness"`
	Model     string `json:"model"`
	Reasoning string `json:"reasoning"`
}

type ModelOption struct {
	ID               string   `json:"id"`
	Reasoning        []string `json:"reasoning"`
	DefaultReasoning string   `json:"defaultReasoning"`
}

type HarnessOption struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Models []ModelOption `json:"models"`
}

var (
	DefaultSettings = Settings{Harness: Codex, Model: "gpt-5.6-sol", Reasoning: "low"}
	Harnesses       = []HarnessOption{
		{ID: Codex, Name: "Codex", Models: []ModelOption{
			{ID: "gpt-5.6-sol", Reasoning: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, DefaultReasoning: "low"},
			{ID: "gpt-5.6-terra", Reasoning: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, DefaultReasoning: "medium"},
			{ID: "gpt-5.6-luna", Reasoning: []string{"low", "medium", "high", "xhigh", "max"}, DefaultReasoning: "medium"},
			{ID: "gpt-5.5", Reasoning: []string{"low", "medium", "high", "xhigh"}, DefaultReasoning: "medium"},
			{ID: "gpt-5.4", Reasoning: []string{"low", "medium", "high", "xhigh"}, DefaultReasoning: "medium"},
			{ID: "gpt-5.4-mini", Reasoning: []string{"low", "medium", "high", "xhigh"}, DefaultReasoning: "medium"},
			{ID: "gpt-5.3-codex-spark", Reasoning: []string{"low", "medium", "high", "xhigh"}, DefaultReasoning: "high"},
		}},
		{ID: Claude, Name: "Claude Code", Models: []ModelOption{
			{ID: "sonnet", Reasoning: []string{"low", "medium", "high", "xhigh", "max"}, DefaultReasoning: "high"},
			{ID: "fable", Reasoning: []string{"low", "medium", "high", "xhigh", "max"}, DefaultReasoning: "high"},
			{ID: "opus", Reasoning: []string{"low", "medium", "high", "xhigh", "max"}, DefaultReasoning: "high"},
			{ID: "haiku", Reasoning: []string{"low", "medium", "high", "xhigh", "max"}, DefaultReasoning: "medium"},
		}},
	}
)

func ValidSettings(settings Settings) bool {
	for _, harness := range Harnesses {
		if harness.ID != settings.Harness {
			continue
		}
		for _, model := range harness.Models {
			if model.ID == settings.Model {
				return slices.Contains(model.Reasoning, settings.Reasoning)
			}
		}
	}
	return false
}

type ProjectData struct {
	ID          int64   `json:"id,omitempty"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Repo        *string `json:"repo,omitempty"`
	Path        string  `json:"path"`
	URL         *string `json:"url,omitempty"`
}

type TaskData struct {
	ID           int64      `json:"id,omitempty"`
	Title        string     `json:"title"`
	Description  *string    `json:"description,omitempty"`
	ParentTaskID *int64     `json:"parentTaskId,omitempty"`
	Status       TaskStatus `json:"status"`
	ProjectID    int64      `json:"projectId"`
}

type CommentData struct {
	ID              int64  `json:"id,omitempty"`
	RelationType    string `json:"relationType"`
	RelationID      int64  `json:"relationId"`
	ParentCommentID *int64 `json:"parentCommentId,omitempty"`
	Author          string `json:"author"`
	Content         string `json:"content"`
}

type ArtifactData struct {
	ID           int64   `json:"id,omitempty"`
	Name         *string `json:"name,omitempty"`
	Type         string  `json:"type"`
	Content      string  `json:"content"`
	RelationType string  `json:"relationType"`
	RelationID   int64   `json:"relationId"`
}

type TriggerData struct {
	ID         int64   `json:"id,omitempty"`
	EventType  string  `json:"eventType"`
	Schedule   *string `json:"schedule,omitempty"`
	WorkflowID int64   `json:"workflowId"`
}

type WorkflowData struct {
	ID          int64    `json:"id,omitempty"`
	Name        string   `json:"name"`
	Description *string  `json:"description,omitempty"`
	Path        *string  `json:"path,omitempty"`
	Scope       *string  `json:"scope,omitempty"`
	Phases      []string `json:"phases,omitempty"`
	Mutating    bool     `json:"mutating"`
}

type IDData struct {
	ID int64 `json:"id"`
}

type CronData struct {
	TriggerID int64 `json:"triggerId"`
}

type WorkflowRunData struct {
	TriggerID      int64    `json:"triggerId"`
	WorkflowID     int64    `json:"workflowId"`
	WorkflowName   string   `json:"workflowName,omitempty"`
	WorkflowPhases []string `json:"workflowPhases,omitempty"`
	SourceEventID  int64    `json:"sourceEventId"`
	Output         string   `json:"output,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type WorkflowRunStepData struct {
	RunID   int64           `json:"runId"`
	Key     string          `json:"key,omitempty"`
	Phase   string          `json:"phase,omitempty"`
	Kind    string          `json:"kind"`
	Backend string          `json:"backend,omitempty"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	Done    bool            `json:"done"`
}

type Snapshot struct {
	Projects  []Project
	Tasks     []Task
	Comments  []Comment
	Artifacts []Artifact
	Triggers  []Trigger
	Workflows []Workflow
	Runs      []WorkflowRun
	RunSteps  []WorkflowRunStep
	Settings  Settings

	startedRuns map[[2]int64]bool
	lastCron    map[int64]time.Time
}

func ProjectEvents(events []eventwire.Event) (Snapshot, error) {
	view := Snapshot{
		startedRuns: make(map[[2]int64]bool),
		lastCron:    make(map[int64]time.Time),
		Settings:    DefaultSettings,
	}
	projectIndex := make(map[int64]int)
	taskIndex := make(map[int64]int)
	commentIndex := make(map[int64]int)
	artifactIndex := make(map[int64]int)
	triggerIndex := make(map[int64]int)
	workflowIndex := make(map[int64]int)
	runIndex := make(map[[2]int64]int)
	runStepIndex := make(map[[2]string]int)

	for _, event := range events {
		switch event.Type {
		case ProjectCreated:
			var data ProjectData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			projectIndex[event.ID] = len(view.Projects)
			view.Projects = append(view.Projects, Project{
				Record: newRecord(event), Name: data.Name, Description: data.Description,
				Repo: data.Repo, Path: data.Path, URL: data.URL,
			})
		case ProjectUpdated:
			var data ProjectData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := projectIndex[data.ID]; found {
				project := &view.Projects[index]
				project.Name, project.Description, project.Repo = data.Name, data.Description, data.Repo
				project.Path, project.URL, project.UpdatedAt = data.Path, data.URL, event.At
			}
		case ProjectDeleted:
			var data IDData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := projectIndex[data.ID]; found {
				deleteRecord(&view.Projects[index].Record, event.At)
			}

		case TaskCreated:
			var data TaskData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			taskIndex[event.ID] = len(view.Tasks)
			view.Tasks = append(view.Tasks, Task{
				Record: newRecord(event), Title: data.Title, Description: data.Description,
				ParentTaskID: data.ParentTaskID, Status: data.Status, ProjectID: data.ProjectID,
			})
		case TaskUpdated:
			var data TaskData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := taskIndex[data.ID]; found {
				task := &view.Tasks[index]
				task.Title, task.Description, task.ParentTaskID = data.Title, data.Description, data.ParentTaskID
				task.Status, task.ProjectID, task.UpdatedAt = data.Status, data.ProjectID, event.At
			}
		case TaskDeleted:
			var data IDData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := taskIndex[data.ID]; found {
				deleteRecord(&view.Tasks[index].Record, event.At)
			}

		case CommentCreated:
			var data CommentData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			commentIndex[event.ID] = len(view.Comments)
			view.Comments = append(view.Comments, Comment{
				Record: newRecord(event), RelationType: data.RelationType, RelationID: data.RelationID,
				ParentCommentID: data.ParentCommentID, Author: data.Author, Content: data.Content,
			})
		case CommentUpdated:
			var data CommentData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := commentIndex[data.ID]; found {
				comment := &view.Comments[index]
				comment.ParentCommentID, comment.Content = data.ParentCommentID, data.Content
				comment.UpdatedAt = event.At
			}
		case CommentDeleted:
			var data IDData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := commentIndex[data.ID]; found {
				deleteRecord(&view.Comments[index].Record, event.At)
			}

		case ArtifactCreated:
			var data ArtifactData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			artifactIndex[event.ID] = len(view.Artifacts)
			view.Artifacts = append(view.Artifacts, Artifact{
				Record: newRecord(event), Name: data.Name, Type: data.Type, Content: data.Content,
				RelationType: data.RelationType, RelationID: data.RelationID,
			})
		case ArtifactUpdated:
			var data ArtifactData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := artifactIndex[data.ID]; found {
				artifact := &view.Artifacts[index]
				artifact.Name, artifact.Type, artifact.Content = data.Name, data.Type, data.Content
				artifact.RelationType, artifact.RelationID = data.RelationType, data.RelationID
				artifact.UpdatedAt = event.At
			}
		case ArtifactDeleted:
			var data IDData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := artifactIndex[data.ID]; found {
				deleteRecord(&view.Artifacts[index].Record, event.At)
			}

		case TriggerCreated:
			var data TriggerData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			triggerIndex[event.ID] = len(view.Triggers)
			view.Triggers = append(view.Triggers, Trigger{
				Record: newRecord(event), EventType: data.EventType,
				Schedule: data.Schedule, WorkflowID: data.WorkflowID,
			})
		case TriggerUpdated:
			var data TriggerData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := triggerIndex[data.ID]; found {
				trigger := &view.Triggers[index]
				trigger.EventType, trigger.Schedule = data.EventType, data.Schedule
				trigger.WorkflowID, trigger.UpdatedAt = data.WorkflowID, event.At
			}
		case TriggerDeleted:
			var data IDData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := triggerIndex[data.ID]; found {
				deleteRecord(&view.Triggers[index].Record, event.At)
			}

		case WorkflowCreated, WorkflowDiscovered:
			var data WorkflowData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			name := data.Name
			if name == "" {
				name = fmt.Sprintf("Draft %d", event.ID)
			}
			workflowIndex[event.ID] = len(view.Workflows)
			view.Workflows = append(view.Workflows, Workflow{
				Record: newRecord(event), Name: name, Description: data.Description,
				Path: data.Path, Scope: data.Scope, Phases: stringSlice(data.Phases), Mutating: data.Mutating,
			})
		case WorkflowUpdated:
			var data WorkflowData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := workflowIndex[data.ID]; found {
				workflow := &view.Workflows[index]
				workflow.Name, workflow.Description, workflow.Path = data.Name, data.Description, data.Path
				workflow.Scope, workflow.Phases = data.Scope, stringSlice(data.Phases)
				workflow.Mutating, workflow.UpdatedAt = data.Mutating, event.At
			}
		case WorkflowDeleted:
			var data IDData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := workflowIndex[data.ID]; found {
				deleteRecord(&view.Workflows[index].Record, event.At)
			}

		case SettingsUpdated:
			if err := decode(event, &view.Settings); err != nil {
				return Snapshot{}, err
			}

		case CronFired:
			var data CronData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			view.lastCron[data.TriggerID] = event.At
		case WorkflowRunStarted:
			var data WorkflowRunData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			key := [2]int64{data.TriggerID, data.SourceEventID}
			view.startedRuns[key] = true
			if data.WorkflowName == "" {
				if index, found := workflowIndex[data.WorkflowID]; found {
					data.WorkflowName = view.Workflows[index].Name
					data.WorkflowPhases = view.Workflows[index].Phases
				}
			}
			runIndex[key] = len(view.Runs)
			view.Runs = append(view.Runs, WorkflowRun{
				ID: event.ID, CreatedAt: event.At, UpdatedAt: event.At,
				TriggerID: data.TriggerID, WorkflowID: data.WorkflowID,
				WorkflowName: data.WorkflowName, WorkflowPhases: stringSlice(data.WorkflowPhases),
				SourceEventID: data.SourceEventID, Status: "running",
			})
		case WorkflowRunStepRecorded:
			var data WorkflowRunStepData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			key := [2]string{fmt.Sprint(data.RunID), data.Key}
			if index, found := runStepIndex[key]; found && data.Key != "" {
				step := &view.RunSteps[index]
				if data.Phase != "" {
					step.Phase = data.Phase
				}
				step.Kind, step.Backend = data.Kind, data.Backend
				step.Message, step.Result, step.Error = data.Message, data.Result, data.Error
				step.Done, step.UpdatedAt = data.Done, event.At
				continue
			}
			if data.Phase == "" {
				data.Phase = "Run"
			}
			if data.Key != "" {
				runStepIndex[key] = len(view.RunSteps)
			}
			view.RunSteps = append(view.RunSteps, WorkflowRunStep{
				ID: event.ID, RunID: data.RunID, CreatedAt: event.At, UpdatedAt: event.At,
				Key: data.Key, Phase: data.Phase, Kind: data.Kind, Backend: data.Backend,
				Message: data.Message, Result: data.Result, Error: data.Error, Done: data.Done,
			})
		case WorkflowRunCompleted, WorkflowRunFailed:
			var data WorkflowRunData
			if err := decode(event, &data); err != nil {
				return Snapshot{}, err
			}
			if index, found := runIndex[[2]int64{data.TriggerID, data.SourceEventID}]; found {
				run := &view.Runs[index]
				run.UpdatedAt, run.Output, run.Error = event.At, data.Output, data.Error
				if event.Type == WorkflowRunCompleted {
					run.Status = "completed"
				} else {
					run.Status = "failed"
				}
			}
		}
	}
	return view, nil
}

func (s Snapshot) Project(id int64) (Project, bool) {
	for _, project := range s.Projects {
		if project.ID == id {
			return project, true
		}
	}
	return Project{}, false
}

func (s Snapshot) Task(id int64) (Task, bool) {
	for _, task := range s.Tasks {
		if task.ID == id {
			return task, true
		}
	}
	return Task{}, false
}

func (s Snapshot) Comment(id int64) (Comment, bool) {
	for _, comment := range s.Comments {
		if comment.ID == id {
			return comment, true
		}
	}
	return Comment{}, false
}

func (s Snapshot) Trigger(id int64) (Trigger, bool) {
	for _, trigger := range s.Triggers {
		if trigger.ID == id {
			return trigger, true
		}
	}
	return Trigger{}, false
}

func (s Snapshot) Artifact(id int64) (Artifact, bool) {
	for _, artifact := range s.Artifacts {
		if artifact.ID == id {
			return artifact, true
		}
	}
	return Artifact{}, false
}

func (s Snapshot) Workflow(id int64) (Workflow, bool) {
	for _, workflow := range s.Workflows {
		if workflow.ID == id {
			return workflow, true
		}
	}
	return Workflow{}, false
}

func (s Snapshot) WorkflowByPath(path string) (Workflow, bool) {
	for _, workflow := range s.Workflows {
		if workflow.Path != nil && *workflow.Path == path {
			return workflow, true
		}
	}
	return Workflow{}, false
}

func (s Snapshot) WorkflowByName(name string) (Workflow, bool) {
	for _, workflow := range s.Workflows {
		if workflow.Name == name && workflow.DeletedAt == nil {
			return workflow, true
		}
	}
	return Workflow{}, false
}

func (s Snapshot) Run(id int64) (WorkflowRun, bool) {
	for _, run := range s.Runs {
		if run.ID == id {
			return run, true
		}
	}
	return WorkflowRun{}, false
}

func (s Snapshot) StepsFor(runID int64) []WorkflowRunStep {
	steps := make([]WorkflowRunStep, 0)
	for _, step := range s.RunSteps {
		if step.RunID == runID {
			steps = append(steps, step)
		}
	}
	return steps
}

func (s Snapshot) CommentsFor(relationType string, relationID int64) []Comment {
	comments := make([]Comment, 0)
	for _, comment := range s.Comments {
		if comment.RelationType == relationType && comment.RelationID == relationID && comment.DeletedAt == nil {
			comments = append(comments, comment)
		}
	}
	return comments
}

func (s Snapshot) ArtifactsFor(relationType string, relationID int64) []Artifact {
	artifacts := make([]Artifact, 0)
	for _, artifact := range s.Artifacts {
		if artifact.RelationType == relationType && artifact.RelationID == relationID && artifact.DeletedAt == nil {
			artifacts = append(artifacts, artifact)
		}
	}
	return artifacts
}

func (s Snapshot) PendingWorkflowComment() (Comment, bool) {
	answered := make(map[int64]bool)
	for _, comment := range s.Comments {
		if comment.Author == "agent" && comment.ParentCommentID != nil {
			answered[*comment.ParentCommentID] = true
		}
	}
	for _, comment := range s.Comments {
		if comment.DeletedAt != nil || comment.Author != "user" || comment.RelationType != "workflow" || answered[comment.ID] {
			continue
		}
		if workflow, found := s.Workflow(comment.RelationID); found && workflow.DeletedAt == nil {
			return comment, true
		}
	}
	return Comment{}, false
}

func (s Snapshot) RunStarted(triggerID, sourceEventID int64) bool {
	return s.startedRuns[[2]int64{triggerID, sourceEventID}]
}

func (s Snapshot) LastCron(triggerID int64) (time.Time, bool) {
	value, found := s.lastCron[triggerID]
	return value, found
}

func newRecord(event eventwire.Event) Record {
	return Record{ID: event.ID, CreatedAt: event.At, UpdatedAt: event.At}
}

func deleteRecord(record *Record, at time.Time) {
	record.UpdatedAt = at
	record.DeletedAt = &at
}

func decode(event eventwire.Event, target any) error {
	if err := json.Unmarshal(event.Data, target); err != nil {
		return fmt.Errorf("decode %s event %d: %w", event.Type, event.ID, err)
	}
	return nil
}

func stringSlice(values []string) []string {
	return append([]string{}, values...)
}
