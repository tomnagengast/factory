package taskstore

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

const (
	SchemaVersion = 1

	StateOpen       = "open"
	StateInProgress = "in_progress"
	StateCompleted  = "completed"
	StateCanceled   = "canceled"

	ApprovalGated     = "gated"
	ApprovalAutomatic = "automatic"

	AuthorHuman  = "human"
	AuthorAgent  = "agent"
	AuthorSystem = "system"

	GateOpen              = "open"
	GateApproved          = "approved"
	GateRevisionRequested = "revision_requested"

	DecisionApprove = "approve"
	DecisionRevise  = "revise"
)

var (
	providerIDPattern = regexp.MustCompile(`^task-[a-f0-9]{16}$`)
	entityIDPattern   = regexp.MustCompile(`^(?:msg|link|gate)-[a-f0-9]{16}$`)
	projectIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

type Actor struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type Task struct {
	Ref              taskmodel.TaskRef `json:"ref"`
	Sequence         uint64            `json:"sequence"`
	Title            string            `json:"title"`
	Description      string            `json:"description,omitempty"`
	ProjectID        string            `json:"projectId"`
	ApprovalMode     string            `json:"approvalMode"`
	State            string            `json:"state"`
	Revision         uint64            `json:"revision"`
	CreatedBy        Actor             `json:"createdBy"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	CompletedAt      *time.Time        `json:"completedAt,omitempty"`
	Routing          *RoutingSnapshot  `json:"routing,omitempty"`
	Completion       *Completion       `json:"completion,omitempty"`
	MessageCount     uint64            `json:"messageCount"`
	LinkCount        uint64            `json:"linkCount"`
	GateCount        uint64            `json:"gateCount"`
	LatestHumanAt    *time.Time        `json:"latestHumanAt,omitempty"`
	LatestActivityAt time.Time         `json:"latestActivityAt"`
}

type Message struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"taskId"`
	Ordinal   uint64    `json:"ordinal"`
	ParentID  string    `json:"parentId,omitempty"`
	Body      string    `json:"body"`
	Author    Actor     `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
}

type Link struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"taskId"`
	Label     string    `json:"label"`
	URL       string    `json:"url"`
	Actor     Actor     `json:"actor"`
	CreatedAt time.Time `json:"createdAt"`
}

type Gate struct {
	ID          string        `json:"id"`
	TaskID      string        `json:"taskId"`
	Kind        string        `json:"kind"`
	Mode        string        `json:"mode"`
	Status      string        `json:"status"`
	ArtifactURL string        `json:"artifactUrl,omitempty"`
	OpenedBy    Actor         `json:"openedBy"`
	OpenedAt    time.Time     `json:"openedAt"`
	Decision    *GateDecision `json:"decision,omitempty"`
}

type GateDecision struct {
	Action    string    `json:"action"`
	Reason    string    `json:"reason,omitempty"`
	Actor     Actor     `json:"actor"`
	DecidedAt time.Time `json:"decidedAt"`
}

type RoutingSnapshot struct {
	ProjectID      string    `json:"projectId"`
	Repository     string    `json:"repository"`
	RepositoryURL  string    `json:"repositoryUrl"`
	RepositoryPath string    `json:"repositoryPath"`
	BaseBranch     string    `json:"baseBranch"`
	WorkflowID     string    `json:"workflowId"`
	WorkflowDigest string    `json:"workflowDigest"`
	AdmittedAt     time.Time `json:"admittedAt"`
}

type Completion struct {
	RunID       string    `json:"runId"`
	EvidenceRef string    `json:"evidenceRef"`
	CompletedAt time.Time `json:"completedAt"`
}

type OperationOutcome struct {
	Scope       string   `json:"scope"`
	CommandHash string   `json:"commandHash"`
	Kind        string   `json:"kind"`
	Task        *Task    `json:"task,omitempty"`
	Message     *Message `json:"message,omitempty"`
	Link        *Link    `json:"link,omitempty"`
	Gate        *Gate    `json:"gate,omitempty"`
}

type Snapshot struct {
	Schema       int                `json:"schema"`
	NextSequence uint64             `json:"nextSequence"`
	Tasks        []Task             `json:"tasks"`
	Messages     []Message          `json:"messages"`
	Links        []Link             `json:"links"`
	Gates        []Gate             `json:"gates"`
	Outcomes     []OperationOutcome `json:"outcomes"`
}

type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type MessagePage struct {
	Messages  []Message `json:"messages"`
	NextAfter uint64    `json:"nextAfter,omitempty"`
}

type Status struct {
	Healthy       bool   `json:"healthy"`
	Poisoned      bool   `json:"poisoned"`
	Tasks         int    `json:"tasks"`
	Messages      uint64 `json:"messages"`
	PendingStages int    `json:"pendingStages"`
}

func (a Actor) Validate() error {
	if !validText(a.ID, 256, false) {
		return errors.New("actor ID is invalid")
	}
	switch a.Kind {
	case AuthorHuman, AuthorAgent, AuthorSystem:
		return nil
	default:
		return errors.New("actor kind is invalid")
	}
}

func (t Task) Validate() error {
	if err := t.Ref.Validate(); err != nil || t.Ref.Source != taskmodel.SourceFactory || !providerIDPattern.MatchString(t.Ref.ProviderID) {
		return errors.New("task identity is invalid")
	}
	if t.Sequence == 0 || t.Ref.Identifier != fmt.Sprintf("FAC-%d", t.Sequence) {
		return errors.New("task sequence is invalid")
	}
	if !validText(t.Title, 200, false) || !validText(t.Description, 64<<10, true) || !projectIDPattern.MatchString(t.ProjectID) {
		return errors.New("task content is invalid")
	}
	if t.ApprovalMode != ApprovalGated && t.ApprovalMode != ApprovalAutomatic {
		return errors.New("task approval mode is invalid")
	}
	if !validState(t.State) || t.Revision == 0 || t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() || t.LatestActivityAt.IsZero() || t.UpdatedAt.Before(t.CreatedAt) {
		return errors.New("task lifecycle is invalid")
	}
	if !t.LatestActivityAt.Equal(t.UpdatedAt) {
		return errors.New("task latest activity conflicts with update time")
	}
	if err := t.CreatedBy.Validate(); err != nil {
		return err
	}
	if (t.State == StateCompleted) != (t.CompletedAt != nil) {
		return errors.New("task completion time conflicts with state")
	}
	if t.Routing != nil {
		if err := t.Routing.Validate(); err != nil {
			return err
		}
	}
	if t.Completion != nil {
		if t.State != StateCompleted || !validText(t.Completion.RunID, 128, false) || !validText(t.Completion.EvidenceRef, 2048, false) || t.Completion.CompletedAt.IsZero() || t.CompletedAt == nil || !t.Completion.CompletedAt.Equal(*t.CompletedAt) {
			return errors.New("task completion evidence is invalid")
		}
	}
	return nil
}

func (r RoutingSnapshot) Validate() error {
	if !projectIDPattern.MatchString(r.ProjectID) || !validText(r.Repository, 256, false) || !validText(r.RepositoryURL, 2048, false) || !validText(r.RepositoryPath, 4096, false) || !validText(r.BaseBranch, 255, false) || !validText(r.WorkflowID, 128, false) || !validText(r.WorkflowDigest, 128, false) || r.AdmittedAt.IsZero() {
		return errors.New("task routing snapshot is invalid")
	}
	return nil
}

func (m Message) Validate() error {
	if !entityIDPattern.MatchString(m.ID) || !providerIDPattern.MatchString(m.TaskID) || m.Ordinal == 0 || !validText(m.Body, 32<<10, false) || m.CreatedAt.IsZero() {
		return errors.New("task message is invalid")
	}
	if m.ParentID != "" && !entityIDPattern.MatchString(m.ParentID) {
		return errors.New("task message parent is invalid")
	}
	return m.Author.Validate()
}

func (l Link) Validate() error {
	if !entityIDPattern.MatchString(l.ID) || !providerIDPattern.MatchString(l.TaskID) || !validText(l.Label, 160, false) || l.CreatedAt.IsZero() {
		return errors.New("task link is invalid")
	}
	parsed, err := url.Parse(l.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("task link URL is invalid")
	}
	return l.Actor.Validate()
}

func (g Gate) Validate() error {
	if !entityIDPattern.MatchString(g.ID) || !providerIDPattern.MatchString(g.TaskID) || !validText(g.Kind, 80, false) || g.OpenedAt.IsZero() {
		return errors.New("task gate is invalid")
	}
	if g.Mode != ApprovalGated && g.Mode != ApprovalAutomatic {
		return errors.New("task gate mode is invalid")
	}
	if g.Status != GateOpen && g.Status != GateApproved && g.Status != GateRevisionRequested {
		return errors.New("task gate status is invalid")
	}
	if g.ArtifactURL != "" {
		parsed, err := url.Parse(g.ArtifactURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return errors.New("task gate artifact URL is invalid")
		}
	}
	if err := g.OpenedBy.Validate(); err != nil {
		return err
	}
	if (g.Status == GateOpen) != (g.Decision == nil) {
		return errors.New("task gate decision conflicts with status")
	}
	if g.Decision != nil {
		if g.Decision.Action != DecisionApprove && g.Decision.Action != DecisionRevise || g.Decision.DecidedAt.IsZero() || !validText(g.Decision.Reason, 4096, true) {
			return errors.New("task gate decision is invalid")
		}
		if err := g.Decision.Actor.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (t Task) Clone() Task {
	t.CompletedAt = cloneTime(t.CompletedAt)
	t.LatestHumanAt = cloneTime(t.LatestHumanAt)
	if t.Routing != nil {
		value := *t.Routing
		t.Routing = &value
	}
	if t.Completion != nil {
		value := *t.Completion
		t.Completion = &value
	}
	return t
}

func (g Gate) Clone() Gate {
	if g.Decision != nil {
		value := *g.Decision
		g.Decision = &value
	}
	return g
}

func (o OperationOutcome) Clone() OperationOutcome {
	if o.Task != nil {
		value := o.Task.Clone()
		o.Task = &value
	}
	if o.Message != nil {
		value := *o.Message
		o.Message = &value
	}
	if o.Link != nil {
		value := *o.Link
		o.Link = &value
	}
	if o.Gate != nil {
		value := o.Gate.Clone()
		o.Gate = &value
	}
	return o
}

func (s Snapshot) Clone() Snapshot {
	clone := s
	clone.Tasks = make([]Task, len(s.Tasks))
	for i := range s.Tasks {
		clone.Tasks[i] = s.Tasks[i].Clone()
	}
	clone.Messages = slices.Clone(s.Messages)
	clone.Links = slices.Clone(s.Links)
	clone.Gates = make([]Gate, len(s.Gates))
	for i := range s.Gates {
		clone.Gates[i] = s.Gates[i].Clone()
	}
	clone.Outcomes = make([]OperationOutcome, len(s.Outcomes))
	for i := range s.Outcomes {
		clone.Outcomes[i] = s.Outcomes[i].Clone()
	}
	return clone
}

func validState(value string) bool {
	return value == StateOpen || value == StateInProgress || value == StateCompleted || value == StateCanceled
}

func validText(value string, maximum int, emptyAllowed bool) bool {
	if value == "" {
		return emptyAllowed
	}
	if value != strings.TrimSpace(value) || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return false
		}
	}
	return true
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
