package state

import (
	"encoding/json"
	"slices"
	"time"
)

const (
	ProjectCreated           = "project.created"
	ProjectUpdated           = "project.updated"
	ProjectDeleted           = "project.deleted"
	TaskCreated              = "task.created"
	TaskUpdated              = "task.updated"
	TaskDeleted              = "task.deleted"
	CommentCreated           = "comment.created"
	CommentUpdated           = "comment.updated"
	CommentDeleted           = "comment.deleted"
	ReactionUpdated          = "reaction.updated"
	ArtifactCreated          = "artifact.created"
	ArtifactUpdated          = "artifact.updated"
	ArtifactDeleted          = "artifact.deleted"
	MediaCreated             = "media.created"
	TriggerCreated           = "trigger.created"
	TriggerUpdated           = "trigger.updated"
	TriggerDeleted           = "trigger.deleted"
	WorkflowCreated          = "workflow.created"
	WorkflowDiscovered       = "workflow.discovered"
	WorkflowUpdated          = "workflow.updated"
	WorkflowDeleted          = "workflow.deleted"
	CronFired                = "cron"
	WorkflowRunStarted       = "workflow.run.started"
	WorkflowRunEventRecorded = "workflow.run.event"
	WorkflowRunWaiting       = "workflow.run.waiting"
	WorkflowRunResumed       = "workflow.run.resumed"
	WorkflowRunCompleted     = "workflow.run.completed"
	WorkflowRunFailed        = "workflow.run.failed"
	SettingsUpdated          = "settings.updated"
)

const (
	Codex  = "codex"
	Claude = "claude"
)

const (
	MinWorkflowCapacity     = 0
	MaxWorkflowCapacity     = 10
	DefaultWorkflowCapacity = 6
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
	InReview   TaskStatus = "in review"
	Done       TaskStatus = "done"
	Canceled   TaskStatus = "canceled"
)

var TaskStatuses = []TaskStatus{Backlog, Todo, InProgress, InReview, Done, Canceled}

var ReactionEmojis = []string{"👍", "👎", "❤️", "🎉", "😂", "👀"}

func ValidReactionEmoji(emoji string) bool {
	return slices.Contains(ReactionEmojis, emoji)
}

type Task struct {
	Record
	Title        string     `json:"title"`
	Description  *string    `json:"description"`
	ParentTaskID *int64     `json:"parentTaskId"`
	Status       TaskStatus `json:"status"`
	ProjectID    int64      `json:"projectId"`
	Reactions    []string   `json:"reactions"`
}

type Comment struct {
	Record
	RelationType    string   `json:"relationType"`
	RelationID      int64    `json:"relationId"`
	ParentCommentID *int64   `json:"parentCommentId,omitempty"`
	Author          string   `json:"author"`
	Kind            string   `json:"kind"`
	Label           string   `json:"label,omitempty"`
	Final           bool     `json:"final"`
	Content         string   `json:"content"`
	Reactions       []string `json:"reactions"`
}

type Artifact struct {
	Record
	Name         *string `json:"name,omitempty"`
	Type         string  `json:"type"`
	Content      string  `json:"content"`
	RelationType string  `json:"relationType"`
	RelationID   int64   `json:"relationId"`
}

type Media struct {
	Record
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

type Trigger struct {
	Record
	EventType  string  `json:"eventType"`
	Schedule   *string `json:"schedule,omitempty"`
	WorkflowID int64   `json:"workflowId"`
	Enabled    bool    `json:"enabled"`
}

type Workflow struct {
	Record
	Name        string   `json:"name"`
	Description *string  `json:"description,omitempty"`
	Path        *string  `json:"path,omitempty"`
	Scope       *string  `json:"scope,omitempty"`
	Phases      []string `json:"phases"`
	Mutating    bool     `json:"mutating"`
	RunCount    int      `json:"runCount"`
	TaskCount   int      `json:"taskCount"`
}

type WorkflowRun struct {
	ID                int64           `json:"id"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
	TriggerID         int64           `json:"triggerId"`
	WorkflowID        int64           `json:"workflowId"`
	WorkflowName      string          `json:"workflowName"`
	WorkflowPhases    []string        `json:"workflowPhases"`
	SourceEventID     int64           `json:"sourceEventId"`
	TaskID            int64           `json:"taskId,omitempty"`
	Status            string          `json:"status"`
	WaitingGate       *WorkflowGate   `json:"waitingGate,omitempty"`
	GateCommentID     int64           `json:"gateCommentId,omitempty"`
	ResponseCommentID int64           `json:"responseCommentId,omitempty"`
	Output            string          `json:"output,omitempty"`
	Error             string          `json:"error,omitempty"`
	Directory         string          `json:"-"`
	Source            string          `json:"-"`
	Settings          *Settings       `json:"-"`
	Arguments         json.RawMessage `json:"-"`
}

type WorkflowGate struct {
	Workflow string          `json:"workflow"`
	Phase    string          `json:"phase,omitempty"`
	StepID   int64           `json:"stepId"`
	Key      string          `json:"key"`
	AgentID  string          `json:"agentId,omitempty"`
	Message  string          `json:"message"`
	Schema   json.RawMessage `json:"schema,omitempty"`
}

type WorkflowRuntimeEvent struct {
	Sequence    int64           `json:"sequence"`
	At          time.Time       `json:"at"`
	Type        string          `json:"type"`
	Workflow    string          `json:"workflow"`
	Phase       string          `json:"phase,omitempty"`
	StepID      int64           `json:"stepId,omitempty"`
	Key         string          `json:"key,omitempty"`
	AgentID     string          `json:"agentId,omitempty"`
	Backend     string          `json:"backend,omitempty"`
	Kind        string          `json:"kind,omitempty"`
	Message     string          `json:"message,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	Tokens      int64           `json:"tokens,omitempty"`
	Concurrency int             `json:"concurrency,omitempty"`
	Budget      *int64          `json:"budget,omitempty"`
}

type WorkflowRunEvent struct {
	Raw        json.RawMessage `json:"-"`
	ID         int64           `json:"id"`
	RunID      int64           `json:"runId"`
	RecordedAt time.Time       `json:"recordedAt"`
	WorkflowRuntimeEvent
}

type Settings struct {
	Harness          string `json:"harness"`
	Model            string `json:"model"`
	Reasoning        string `json:"reasoning"`
	WorkflowCapacity int    `json:"workflowCapacity"`
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
	DefaultSettings = Settings{
		Harness: Codex, Model: "gpt-5.6-sol", Reasoning: "low",
		WorkflowCapacity: DefaultWorkflowCapacity,
	}
	Harnesses = []HarnessOption{
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
	if settings.WorkflowCapacity < MinWorkflowCapacity ||
		settings.WorkflowCapacity > MaxWorkflowCapacity {
		return false
	}
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
	Kind            string `json:"kind,omitempty"`
	Label           string `json:"label,omitempty"`
	Final           *bool  `json:"final,omitempty"`
	Content         string `json:"content"`
}

type ReactionUpdatedData struct {
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
	Emoji      string `json:"emoji"`
	Active     bool   `json:"active"`
}

type ArtifactData struct {
	ID           int64   `json:"id,omitempty"`
	Name         *string `json:"name,omitempty"`
	Type         string  `json:"type"`
	Content      string  `json:"content"`
	RelationType string  `json:"relationType"`
	RelationID   int64   `json:"relationId"`
}

type MediaData struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

type TriggerData struct {
	ID         int64   `json:"id,omitempty"`
	EventType  string  `json:"eventType"`
	Schedule   *string `json:"schedule,omitempty"`
	WorkflowID int64   `json:"workflowId"`
	Enabled    bool    `json:"enabled"`
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
	TriggerID      int64           `json:"triggerId"`
	WorkflowID     int64           `json:"workflowId"`
	WorkflowName   string          `json:"workflowName,omitempty"`
	WorkflowPhases []string        `json:"workflowPhases,omitempty"`
	SourceEventID  int64           `json:"sourceEventId"`
	TaskID         int64           `json:"taskId,omitempty"`
	Directory      string          `json:"directory,omitempty"`
	Source         string          `json:"source,omitempty"`
	Settings       *Settings       `json:"settings,omitempty"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	Output         string          `json:"output,omitempty"`
	Error          string          `json:"error,omitempty"`
}

type WorkflowRunStateData struct {
	RunID             int64         `json:"runId"`
	Gate              *WorkflowGate `json:"gate,omitempty"`
	GateCommentID     int64         `json:"gateCommentId,omitempty"`
	ResponseCommentID int64         `json:"responseCommentId,omitempty"`
}

type WorkflowRunEventData struct {
	RunID int64           `json:"runId"`
	Event json.RawMessage `json:"event"`
}

type Snapshot struct {
	Projects   []Project
	Tasks      []Task
	Comments   []Comment
	Artifacts  []Artifact
	MediaFiles []Media
	Triggers   []Trigger
	Workflows  []Workflow
	Runs       []WorkflowRun
	RunEvents  []WorkflowRunEvent
	Settings   Settings
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

func (s Snapshot) Media(id int64) (Media, bool) {
	for _, media := range s.MediaFiles {
		if media.ID == id {
			return media, true
		}
	}
	return Media{}, false
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

func (s Snapshot) EventsFor(runID int64) []WorkflowRunEvent {
	events := make([]WorkflowRunEvent, 0)
	for _, event := range s.RunEvents {
		if event.RunID == runID {
			events = append(events, event)
		}
	}
	return events
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
