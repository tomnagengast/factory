package taskstore

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/taskcompat"
	"github.com/tomnagengast/factory/internal/taskmodel"
)

const (
	operationCheckpoint = "checkpoint"
	operationCreate     = "create"
	operationUpdate     = "update"
	operationMessage    = "message"
	operationLink       = "link"
	operationGate       = "gate"
	operationDecision   = "decision"
	operationState      = "state"
	operationRouting    = "routing"
	operationCompletion = "completion"
)

var (
	ErrIdempotencyConflict = errors.New("task store: idempotency key conflicts with an existing command")
	ErrNotFound            = errors.New("task store: task not found")
	ErrTerminalTask        = errors.New("task store: terminal task cannot be mutated")
)

type RevisionConflict struct {
	Current Task
}

func (e RevisionConflict) Error() string { return "task store: revision conflict" }

type transientError struct{ error }

func (e transientError) Unwrap() error { return e.error }

func transient(err error) error {
	if err == nil {
		return nil
	}
	return transientError{error: err}
}

func IsTransient(err error) bool {
	var target transientError
	return errors.As(err, &target)
}

type CreateCommand struct {
	Actor          Actor  `json:"actor"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	ProjectID      string `json:"projectId"`
	ApprovalMode   string `json:"approvalMode"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type UpdateCommand struct {
	Actor            Actor  `json:"actor"`
	TaskID           string `json:"taskId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	Title            string `json:"title"`
	Description      string `json:"description,omitempty"`
	ApprovalMode     string `json:"approvalMode"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type MessageCommand struct {
	Actor            Actor  `json:"actor"`
	TaskID           string `json:"taskId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	ParentID         string `json:"parentId,omitempty"`
	Body             string `json:"body"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type LinkCommand struct {
	Actor            Actor  `json:"actor"`
	TaskID           string `json:"taskId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	Label            string `json:"label"`
	URL              string `json:"url"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type GateCommand struct {
	Actor            Actor  `json:"actor"`
	TaskID           string `json:"taskId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	Kind             string `json:"kind"`
	Mode             string `json:"mode"`
	ArtifactURL      string `json:"artifactUrl,omitempty"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type DecisionCommand struct {
	Actor            Actor  `json:"actor"`
	TaskID           string `json:"taskId"`
	GateID           string `json:"gateId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	Action           string `json:"action"`
	Reason           string `json:"reason,omitempty"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type StateCommand struct {
	Actor            Actor  `json:"actor"`
	TaskID           string `json:"taskId"`
	ExpectedRevision uint64 `json:"expectedRevision"`
	State            string `json:"state"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type RoutingCommand struct {
	Actor            Actor           `json:"actor"`
	TaskID           string          `json:"taskId"`
	ExpectedRevision uint64          `json:"expectedRevision"`
	Routing          RoutingSnapshot `json:"routing"`
	IdempotencyKey   string          `json:"idempotencyKey"`
}

type CompletionCommand struct {
	Actor            Actor      `json:"actor"`
	TaskID           string     `json:"taskId"`
	ExpectedRevision uint64     `json:"expectedRevision"`
	Completion       Completion `json:"completion"`
	IdempotencyKey   string     `json:"idempotencyKey"`
}

type diskOperation struct {
	Kind       string            `json:"kind"`
	Schema     int               `json:"schema,omitempty"`
	Checkpoint *Snapshot         `json:"checkpoint,omitempty"`
	Outcome    *OperationOutcome `json:"outcome,omitempty"`
	Task       *Task             `json:"task,omitempty"`
	Message    *Message          `json:"message,omitempty"`
	Link       *Link             `json:"link,omitempty"`
	Gate       *Gate             `json:"gate,omitempty"`
}

type Store struct {
	mu           sync.RWMutex
	path         string
	tasks        map[string]Task
	messages     map[string][]Message
	links        map[string][]Link
	gates        map[string][]Gate
	outcomes     map[string]OperationOutcome
	nextSequence uint64
	poisoned     error
	random       io.Reader
	write        func(*os.File, []byte) (int, error)
	sync         func(*os.File) error
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("task store: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("task store: create directory: %w", err)
	}
	s := &Store{path: path, random: rand.Reader}
	s.write = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	s.sync = func(file *os.File) error { return file.Sync() }
	s.reset()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.writeCheckpointLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("task store: read: %w", err)
	}
	complete := len(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if index := bytes.LastIndexByte(data, '\n'); index >= 0 {
			complete = index + 1
		} else {
			complete = 0
		}
		if err := os.Truncate(path, int64(complete)); err != nil {
			return nil, fmt.Errorf("task store: truncate incomplete tail: %w", err)
		}
		file, err := os.OpenFile(path, os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("task store: reopen truncated store: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("task store: sync truncated store: %w", err)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("task store: close truncated store: %w", err)
		}
	}
	if err := s.replay(data[:complete]); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) reset() {
	s.tasks = make(map[string]Task)
	s.messages = make(map[string][]Message)
	s.links = make(map[string][]Link)
	s.gates = make(map[string][]Gate)
	s.outcomes = make(map[string]OperationOutcome)
	s.nextSequence = 1
}

func (s *Store) Create(command CreateCommand, now time.Time) (Task, bool, error) {
	if err := command.Actor.Validate(); err != nil || !validIdempotencyKey(command.IdempotencyKey) {
		return Task{}, false, errors.New("task store: invalid create command")
	}
	probe := Task{Ref: taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0000000000000000", Identifier: "FAC-1"}, Sequence: 1, Title: command.Title, Description: command.Description, ProjectID: command.ProjectID, ApprovalMode: command.ApprovalMode, State: StateOpen, Revision: 1, CreatedBy: command.Actor, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), LatestActivityAt: now.UTC()}
	if err := probe.Validate(); err != nil {
		return Task{}, false, fmt.Errorf("task store: invalid create command: %w", err)
	}
	hash := commandDigest(operationCreate, command)
	scope := outcomeScope(command.Actor, command.IdempotencyKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	if outcome, found, err := s.replayOutcome(scope, hash); found || err != nil {
		if err != nil {
			return Task{}, false, err
		}
		return outcome.Task.Clone(), true, nil
	}
	if err := s.checkWritable(); err != nil {
		return Task{}, false, err
	}
	providerID, err := s.newEntityID("task")
	if err != nil {
		return Task{}, false, transient(err)
	}
	sequence := s.nextSequence
	now = now.UTC()
	task := probe
	task.Ref = taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: providerID, Identifier: "FAC-" + strconv.FormatUint(sequence, 10)}
	task.Sequence = sequence
	task.CreatedAt, task.UpdatedAt, task.LatestActivityAt = now, now, now
	outcome := OperationOutcome{Scope: scope, CommandHash: hash, Kind: operationCreate, Task: taskPointer(task)}
	op := diskOperation{Kind: operationCreate, Task: taskPointer(task), Outcome: &outcome}
	if err := s.persistLocked(op); err != nil {
		return Task{}, false, transient(err)
	}
	return task.Clone(), false, nil
}

func (s *Store) Update(command UpdateCommand, now time.Time) (Task, bool, error) {
	return s.mutateTask(operationUpdate, command.Actor, command.TaskID, command.ExpectedRevision, command.IdempotencyKey, commandDigest(operationUpdate, command), now, func(current Task) (Task, *Message, *Link, *Gate, error) {
		current.Title, current.Description, current.ApprovalMode = command.Title, command.Description, command.ApprovalMode
		return current, nil, nil, nil, current.Validate()
	})
}

func (s *Store) AddMessage(command MessageCommand, now time.Time) (Task, Message, bool, error) {
	var created Message
	task, replayed, err := s.mutateTask(operationMessage, command.Actor, command.TaskID, command.ExpectedRevision, command.IdempotencyKey, commandDigest(operationMessage, command), now, func(current Task) (Task, *Message, *Link, *Gate, error) {
		id, err := s.newEntityID("msg")
		if err != nil {
			return Task{}, nil, nil, nil, err
		}
		if command.ParentID != "" && !s.messageExists(current.Ref.ProviderID, command.ParentID) {
			return Task{}, nil, nil, nil, errors.New("task store: reply parent not found")
		}
		created = Message{ID: id, TaskID: current.Ref.ProviderID, Ordinal: current.MessageCount + 1, ParentID: command.ParentID, Body: command.Body, Author: command.Actor, CreatedAt: now.UTC()}
		if err := created.Validate(); err != nil {
			return Task{}, nil, nil, nil, err
		}
		current.MessageCount++
		if command.Actor.Kind == AuthorHuman {
			at := now.UTC()
			current.LatestHumanAt = &at
		}
		return current, &created, nil, nil, nil
	})
	if err != nil {
		return Task{}, Message{}, false, err
	}
	if replayed {
		outcome := s.outcome(command.Actor, command.IdempotencyKey)
		return task, *outcome.Message, true, nil
	}
	return task, created, false, nil
}

func (s *Store) AddLink(command LinkCommand, now time.Time) (Task, Link, bool, error) {
	var created Link
	task, replayed, err := s.mutateTask(operationLink, command.Actor, command.TaskID, command.ExpectedRevision, command.IdempotencyKey, commandDigest(operationLink, command), now, func(current Task) (Task, *Message, *Link, *Gate, error) {
		id, err := s.newEntityID("link")
		if err != nil {
			return Task{}, nil, nil, nil, err
		}
		created = Link{ID: id, TaskID: current.Ref.ProviderID, Label: command.Label, URL: command.URL, Actor: command.Actor, CreatedAt: now.UTC()}
		if err := created.Validate(); err != nil {
			return Task{}, nil, nil, nil, err
		}
		current.LinkCount++
		return current, nil, &created, nil, nil
	})
	if err != nil {
		return Task{}, Link{}, false, err
	}
	if replayed {
		outcome := s.outcome(command.Actor, command.IdempotencyKey)
		return task, *outcome.Link, true, nil
	}
	return task, created, false, nil
}

func (s *Store) OpenGate(command GateCommand, now time.Time) (Task, Gate, bool, error) {
	var created Gate
	task, replayed, err := s.mutateTask(operationGate, command.Actor, command.TaskID, command.ExpectedRevision, command.IdempotencyKey, commandDigest(operationGate, command), now, func(current Task) (Task, *Message, *Link, *Gate, error) {
		id, err := s.newEntityID("gate")
		if err != nil {
			return Task{}, nil, nil, nil, err
		}
		mode := command.Mode
		if mode == "" {
			mode = current.ApprovalMode
		}
		created = Gate{ID: id, TaskID: current.Ref.ProviderID, Kind: command.Kind, Mode: mode, Status: GateOpen, ArtifactURL: command.ArtifactURL, OpenedBy: command.Actor, OpenedAt: now.UTC()}
		if mode == ApprovalAutomatic {
			created.Status = GateApproved
			created.Decision = &GateDecision{Action: DecisionApprove, Actor: Actor{ID: "factory", Kind: AuthorSystem}, DecidedAt: now.UTC()}
		}
		if err := created.Validate(); err != nil {
			return Task{}, nil, nil, nil, err
		}
		current.GateCount++
		return current, nil, nil, &created, nil
	})
	if err != nil {
		return Task{}, Gate{}, false, err
	}
	if replayed {
		outcome := s.outcome(command.Actor, command.IdempotencyKey)
		return task, outcome.Gate.Clone(), true, nil
	}
	return task, created, false, nil
}

func (s *Store) DecideGate(command DecisionCommand, now time.Time) (Task, Gate, bool, error) {
	var decided Gate
	task, replayed, err := s.mutateTask(operationDecision, command.Actor, command.TaskID, command.ExpectedRevision, command.IdempotencyKey, commandDigest(operationDecision, command), now, func(current Task) (Task, *Message, *Link, *Gate, error) {
		index := s.gateIndex(current.Ref.ProviderID, command.GateID)
		if index < 0 {
			return Task{}, nil, nil, nil, errors.New("task store: gate not found")
		}
		decided = s.gates[current.Ref.ProviderID][index].Clone()
		if decided.Mode != ApprovalGated || decided.Status != GateOpen || command.Actor.Kind != AuthorHuman {
			return Task{}, nil, nil, nil, errors.New("task store: gate cannot be decided")
		}
		status := GateApproved
		if command.Action == DecisionRevise {
			status = GateRevisionRequested
		}
		decided.Status = status
		decided.Decision = &GateDecision{Action: command.Action, Reason: command.Reason, Actor: command.Actor, DecidedAt: now.UTC()}
		if err := decided.Validate(); err != nil {
			return Task{}, nil, nil, nil, err
		}
		at := now.UTC()
		current.LatestHumanAt = &at
		return current, nil, nil, &decided, nil
	})
	if err != nil {
		return Task{}, Gate{}, false, err
	}
	if replayed {
		outcome := s.outcome(command.Actor, command.IdempotencyKey)
		return task, outcome.Gate.Clone(), true, nil
	}
	return task, decided, false, nil
}

func (s *Store) ChangeState(command StateCommand, now time.Time) (Task, bool, error) {
	return s.mutateTask(operationState, command.Actor, command.TaskID, command.ExpectedRevision, command.IdempotencyKey, commandDigest(operationState, command), now, func(current Task) (Task, *Message, *Link, *Gate, error) {
		if !validStateTransition(current.State, command.State) {
			return Task{}, nil, nil, nil, errors.New("task store: invalid state transition")
		}
		current.State = command.State
		current.CompletedAt = nil
		if command.State == StateCompleted {
			at := now.UTC()
			current.CompletedAt = &at
		}
		return current, nil, nil, nil, nil
	})
}

func (s *Store) SetRouting(command RoutingCommand, now time.Time) (Task, bool, error) {
	return s.mutateTask(operationRouting, command.Actor, command.TaskID, command.ExpectedRevision, command.IdempotencyKey, commandDigest(operationRouting, command), now, func(current Task) (Task, *Message, *Link, *Gate, error) {
		if current.Routing != nil || current.State != StateOpen || command.Routing.ProjectID != current.ProjectID {
			return Task{}, nil, nil, nil, errors.New("task store: routing cannot be changed")
		}
		if err := command.Routing.Validate(); err != nil {
			return Task{}, nil, nil, nil, err
		}
		value := command.Routing
		current.Routing = &value
		current.State = StateInProgress
		return current, nil, nil, nil, nil
	})
}

func (s *Store) Complete(command CompletionCommand, now time.Time) (Task, bool, error) {
	return s.mutateTask(operationCompletion, command.Actor, command.TaskID, command.ExpectedRevision, command.IdempotencyKey, commandDigest(operationCompletion, command), now, func(current Task) (Task, *Message, *Link, *Gate, error) {
		if current.State != StateInProgress || current.Routing == nil || current.Completion != nil || command.Completion.RunID == "" || command.Completion.EvidenceRef == "" {
			return Task{}, nil, nil, nil, errors.New("task store: completion cannot be recorded")
		}
		value := command.Completion
		value.CompletedAt = now.UTC()
		current.Completion = &value
		current.State = StateCompleted
		at := now.UTC()
		current.CompletedAt = &at
		return current, nil, nil, nil, nil
	})
}

func (s *Store) mutateTask(kind string, actor Actor, taskID string, expected uint64, key, hash string, now time.Time, mutate func(Task) (Task, *Message, *Link, *Gate, error)) (Task, bool, error) {
	if err := actor.Validate(); err != nil || !providerIDPattern.MatchString(taskID) || expected == 0 || !validIdempotencyKey(key) {
		return Task{}, false, errors.New("task store: invalid mutation command")
	}
	scope := outcomeScope(actor, key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if outcome, found, err := s.replayOutcome(scope, hash); found || err != nil {
		if err != nil {
			return Task{}, false, err
		}
		return outcome.Task.Clone(), true, nil
	}
	if err := s.checkWritable(); err != nil {
		return Task{}, false, transient(err)
	}
	current, found := s.tasks[taskID]
	if !found {
		return Task{}, false, ErrNotFound
	}
	if current.Terminal() {
		return Task{}, false, ErrTerminalTask
	}
	if current.Revision != expected {
		return Task{}, false, RevisionConflict{Current: current.Clone()}
	}
	next, message, link, gate, err := mutate(current.Clone())
	if err != nil {
		return Task{}, false, err
	}
	now = now.UTC()
	next.Revision = current.Revision + 1
	next.UpdatedAt, next.LatestActivityAt = now, now
	if err := next.Validate(); err != nil {
		return Task{}, false, err
	}
	outcome := OperationOutcome{Scope: scope, CommandHash: hash, Kind: kind, Task: taskPointer(next), Message: message, Link: link, Gate: gate}
	op := diskOperation{Kind: kind, Task: taskPointer(next), Message: message, Link: link, Gate: gate, Outcome: &outcome}
	if err := s.persistLocked(op); err != nil {
		return Task{}, false, transient(err)
	}
	return next.Clone(), false, nil
}

func (s *Store) Find(taskID string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, found := s.tasks[taskID]
	return task.Clone(), found
}

func (s *Store) FindIdentifier(identifier string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identifier = strings.ToUpper(strings.TrimSpace(identifier))
	for _, task := range s.tasks {
		if task.Ref.Identifier == identifier {
			return task.Clone(), true
		}
	}
	return Task{}, false
}

func (s *Store) List(cursor string, limit int) (TaskPage, error) {
	if limit < 1 || limit > 200 {
		return TaskPage{}, errors.New("task store: list limit must be between 1 and 200")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks := make([]Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		tasks = append(tasks, task.Clone())
	}
	sort.Slice(tasks, func(i, j int) bool {
		if !tasks[i].LatestActivityAt.Equal(tasks[j].LatestActivityAt) {
			return tasks[i].LatestActivityAt.After(tasks[j].LatestActivityAt)
		}
		return tasks[i].Sequence > tasks[j].Sequence
	})
	start := 0
	if cursor != "" {
		for start < len(tasks) && taskCursor(tasks[start]) != cursor {
			start++
		}
		if start == len(tasks) {
			return TaskPage{}, errors.New("task store: invalid list cursor")
		}
		start++
	}
	end := min(start+limit, len(tasks))
	page := TaskPage{Tasks: slices.Clone(tasks[start:end])}
	if end < len(tasks) && end > start {
		page.NextCursor = taskCursor(tasks[end-1])
	}
	return page, nil
}

func (s *Store) Messages(taskID string, after uint64, limit int) (MessagePage, error) {
	if limit < 1 || limit > 500 {
		return MessagePage{}, errors.New("task store: message limit must be between 1 and 500")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, found := s.tasks[taskID]; !found {
		return MessagePage{}, ErrNotFound
	}
	messages := s.messages[taskID]
	start := sort.Search(len(messages), func(i int) bool { return messages[i].Ordinal > after })
	end := min(start+limit, len(messages))
	page := MessagePage{Messages: slices.Clone(messages[start:end])}
	if end < len(messages) && end > start {
		page.NextAfter = messages[end-1].Ordinal
	}
	return page, nil
}

func (s *Store) Links(taskID string) ([]Link, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, found := s.tasks[taskID]; !found {
		return nil, ErrNotFound
	}
	return slices.Clone(s.links[taskID]), nil
}

func (s *Store) Gates(taskID string) ([]Gate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, found := s.tasks[taskID]; !found {
		return nil, ErrNotFound
	}
	values := make([]Gate, len(s.gates[taskID]))
	for i := range values {
		values[i] = s.gates[taskID][i].Clone()
	}
	return values, nil
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *Store) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := Status{Healthy: s.poisoned == nil, Poisoned: s.poisoned != nil, Tasks: len(s.tasks)}
	for _, messages := range s.messages {
		status.Messages += uint64(len(messages))
	}
	return status
}

func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkWritable(); err != nil {
		return err
	}
	return s.writeCheckpointLocked()
}

func (s *Store) replay(data []byte) error {
	foundCheckpoint := false
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		if len(raw) == 0 {
			continue
		}
		var operation diskOperation
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&operation); err != nil {
			return fmt.Errorf("task store: decode operation: %w", err)
		}
		if operation.Kind == operationCheckpoint {
			if foundCheckpoint || operation.Schema != SchemaVersion || operation.Checkpoint == nil || operation.Checkpoint.Schema != SchemaVersion {
				return errors.New("task store: invalid checkpoint")
			}
			foundCheckpoint = true
		}
		if !foundCheckpoint {
			return errors.New("task store: operation precedes checkpoint")
		}
		if err := s.applyLocked(operation); err != nil {
			return err
		}
	}
	if !foundCheckpoint {
		return errors.New("task store: checkpoint is missing")
	}
	return nil
}

func (s *Store) persistLocked(operation diskOperation) error {
	if err := s.appendLocked(operation); err != nil {
		return err
	}
	if err := s.applyLocked(operation); err != nil {
		s.poisoned = err
		return fmt.Errorf("task store: apply persisted operation: %w", err)
	}
	return nil
}

func (s *Store) applyLocked(operation diskOperation) error {
	if operation.Kind == operationCheckpoint {
		return s.applyCheckpointLocked(operation)
	}
	if operation.Task == nil || operation.Outcome == nil || operation.Outcome.Task == nil || operation.Outcome.Kind != operation.Kind || operation.Outcome.Scope == "" || operation.Outcome.CommandHash == "" || !reflect.DeepEqual(*operation.Task, *operation.Outcome.Task) {
		return errors.New("task store: invalid operation envelope")
	}
	if _, exists := s.outcomes[operation.Outcome.Scope]; exists {
		return errors.New("task store: duplicate operation outcome")
	}
	next := operation.Task.Clone()
	if err := next.Validate(); err != nil {
		return fmt.Errorf("task store: invalid task operation: %w", err)
	}
	current, exists := s.tasks[next.Ref.ProviderID]
	if operation.Kind == operationCreate {
		if exists || next.Sequence != s.nextSequence || operation.Message != nil || operation.Link != nil || operation.Gate != nil {
			return errors.New("task store: invalid create operation")
		}
		s.nextSequence++
	} else {
		if !exists || next.Revision != current.Revision+1 || errTaskIdentityChanged(current, next) != nil {
			return errors.New("task store: invalid task revision")
		}
		if err := validateAdvance(current, next, operation); err != nil {
			return err
		}
	}
	s.tasks[next.Ref.ProviderID] = next
	switch operation.Kind {
	case operationMessage:
		if operation.Message == nil || operation.Outcome.Message == nil || *operation.Message != *operation.Outcome.Message || operation.Message.TaskID != next.Ref.ProviderID || operation.Message.Ordinal != uint64(len(s.messages[next.Ref.ProviderID])+1) || s.messageExists(next.Ref.ProviderID, operation.Message.ID) {
			return errors.New("task store: invalid message operation")
		}
		if err := operation.Message.Validate(); err != nil {
			return err
		}
		s.messages[next.Ref.ProviderID] = append(s.messages[next.Ref.ProviderID], *operation.Message)
	case operationLink:
		if operation.Link == nil || operation.Outcome.Link == nil || *operation.Link != *operation.Outcome.Link || operation.Link.TaskID != next.Ref.ProviderID {
			return errors.New("task store: invalid link operation")
		}
		if err := operation.Link.Validate(); err != nil || s.linkExists(next.Ref.ProviderID, operation.Link.ID) {
			return errors.New("task store: invalid link operation")
		}
		s.links[next.Ref.ProviderID] = append(s.links[next.Ref.ProviderID], *operation.Link)
	case operationGate:
		if operation.Gate == nil || operation.Outcome.Gate == nil || !reflect.DeepEqual(*operation.Gate, *operation.Outcome.Gate) || operation.Gate.TaskID != next.Ref.ProviderID || s.gateIndex(next.Ref.ProviderID, operation.Gate.ID) >= 0 {
			return errors.New("task store: invalid gate operation")
		}
		if err := operation.Gate.Validate(); err != nil {
			return err
		}
		s.gates[next.Ref.ProviderID] = append(s.gates[next.Ref.ProviderID], operation.Gate.Clone())
	case operationDecision:
		if operation.Gate == nil || operation.Outcome.Gate == nil || !reflect.DeepEqual(*operation.Gate, *operation.Outcome.Gate) {
			return errors.New("task store: invalid gate decision operation")
		}
		index := s.gateIndex(next.Ref.ProviderID, operation.Gate.ID)
		if index < 0 || s.gates[next.Ref.ProviderID][index].Status != GateOpen || operation.Gate.Status == GateOpen {
			return errors.New("task store: invalid gate decision transition")
		}
		if err := operation.Gate.Validate(); err != nil {
			return err
		}
		s.gates[next.Ref.ProviderID][index] = operation.Gate.Clone()
	default:
		if operation.Message != nil || operation.Link != nil || operation.Gate != nil || operation.Outcome.Message != nil || operation.Outcome.Link != nil || operation.Outcome.Gate != nil {
			return errors.New("task store: unexpected operation entity")
		}
	}
	s.outcomes[operation.Outcome.Scope] = operation.Outcome.Clone()
	return nil
}

func (s *Store) applyCheckpointLocked(operation diskOperation) error {
	if operation.Checkpoint == nil || operation.Checkpoint.Schema != SchemaVersion || operation.Checkpoint.NextSequence == 0 {
		return errors.New("task store: invalid checkpoint projection")
	}
	s.reset()
	sequences := make(map[uint64]bool, len(operation.Checkpoint.Tasks))
	for _, task := range operation.Checkpoint.Tasks {
		if err := task.Validate(); err != nil || s.tasks[task.Ref.ProviderID].Revision != 0 || task.Sequence >= operation.Checkpoint.NextSequence || sequences[task.Sequence] {
			return errors.New("task store: invalid checkpoint task")
		}
		sequences[task.Sequence] = true
		s.tasks[task.Ref.ProviderID] = task.Clone()
	}
	if uint64(len(sequences))+1 != operation.Checkpoint.NextSequence {
		return errors.New("task store: checkpoint task sequence has a gap")
	}
	s.nextSequence = operation.Checkpoint.NextSequence
	for _, message := range operation.Checkpoint.Messages {
		if err := message.Validate(); err != nil || s.tasks[message.TaskID].Revision == 0 || message.Ordinal != uint64(len(s.messages[message.TaskID])+1) || s.messageExists(message.TaskID, message.ID) {
			return errors.New("task store: invalid checkpoint message")
		}
		s.messages[message.TaskID] = append(s.messages[message.TaskID], message)
	}
	for _, link := range operation.Checkpoint.Links {
		if err := link.Validate(); err != nil || s.tasks[link.TaskID].Revision == 0 || s.linkExists(link.TaskID, link.ID) {
			return errors.New("task store: invalid checkpoint link")
		}
		s.links[link.TaskID] = append(s.links[link.TaskID], link)
	}
	for _, gate := range operation.Checkpoint.Gates {
		if err := gate.Validate(); err != nil || s.tasks[gate.TaskID].Revision == 0 || s.gateIndex(gate.TaskID, gate.ID) >= 0 {
			return errors.New("task store: invalid checkpoint gate")
		}
		s.gates[gate.TaskID] = append(s.gates[gate.TaskID], gate.Clone())
	}
	for _, outcome := range operation.Checkpoint.Outcomes {
		if outcome.Scope == "" || outcome.CommandHash == "" || outcome.Task == nil || s.outcomes[outcome.Scope].Scope != "" {
			return errors.New("task store: invalid checkpoint outcome")
		}
		current := s.tasks[outcome.Task.Ref.ProviderID]
		if err := outcome.Task.Validate(); err != nil || current.Revision == 0 || outcome.Task.Revision > current.Revision || errTaskIdentityChanged(*outcome.Task, current) != nil {
			return errors.New("task store: invalid checkpoint outcome task")
		}
		if err := s.validateCheckpointOutcomeEntities(outcome); err != nil {
			return err
		}
		s.outcomes[outcome.Scope] = outcome.Clone()
	}
	for id, task := range s.tasks {
		if task.MessageCount != uint64(len(s.messages[id])) || task.LinkCount != uint64(len(s.links[id])) || task.GateCount != uint64(len(s.gates[id])) {
			return errors.New("task store: checkpoint counters conflict")
		}
	}
	return nil
}

func (s *Store) validateCheckpointOutcomeEntities(outcome OperationOutcome) error {
	taskID := outcome.Task.Ref.ProviderID
	invalid := func() error { return errors.New("task store: invalid checkpoint outcome entity") }
	switch outcome.Kind {
	case operationCreate, operationUpdate, operationState, operationRouting, operationCompletion:
		if outcome.Message != nil || outcome.Link != nil || outcome.Gate != nil {
			return invalid()
		}
	case operationMessage:
		if outcome.Message == nil || outcome.Link != nil || outcome.Gate != nil || outcome.Message.TaskID != taskID {
			return invalid()
		}
		messages := s.messages[taskID]
		index := int(outcome.Message.Ordinal - 1)
		if index < 0 || index >= len(messages) || messages[index] != *outcome.Message || outcome.Task.MessageCount != outcome.Message.Ordinal {
			return invalid()
		}
	case operationLink:
		if outcome.Link == nil || outcome.Message != nil || outcome.Gate != nil || outcome.Link.TaskID != taskID {
			return invalid()
		}
		found := false
		for _, link := range s.links[taskID] {
			if link == *outcome.Link {
				found = true
				break
			}
		}
		if !found {
			return invalid()
		}
	case operationGate, operationDecision:
		if outcome.Gate == nil || outcome.Message != nil || outcome.Link != nil || outcome.Gate.TaskID != taskID {
			return invalid()
		}
		index := s.gateIndex(taskID, outcome.Gate.ID)
		if index < 0 {
			return invalid()
		}
		current := s.gates[taskID][index]
		if outcome.Kind == operationDecision {
			if !reflect.DeepEqual(current, *outcome.Gate) {
				return invalid()
			}
		} else {
			expected := outcome.Gate.Clone()
			expected.Status, expected.Decision = current.Status, current.Decision
			if !reflect.DeepEqual(expected, current) {
				return invalid()
			}
		}
	default:
		return errors.New("task store: invalid checkpoint outcome kind")
	}
	return nil
}

func validateAdvance(current, next Task, operation diskOperation) error {
	if next.UpdatedAt.Before(current.UpdatedAt) || !next.LatestActivityAt.Equal(next.UpdatedAt) {
		return errors.New("task store: mutation time regressed")
	}
	expected := current.Clone()
	expected.Revision = next.Revision
	expected.UpdatedAt = next.UpdatedAt
	expected.LatestActivityAt = next.LatestActivityAt
	switch operation.Kind {
	case operationUpdate:
		expected.Title, expected.Description, expected.ApprovalMode = next.Title, next.Description, next.ApprovalMode
	case operationMessage:
		expected.MessageCount = current.MessageCount + 1
		if operation.Message == nil {
			return errors.New("task store: message operation is missing its entity")
		}
		if operation.Message.Author.Kind == AuthorHuman {
			at := operation.Message.CreatedAt
			expected.LatestHumanAt = &at
		}
	case operationLink:
		expected.LinkCount = current.LinkCount + 1
	case operationGate:
		expected.GateCount = current.GateCount + 1
	case operationDecision:
		if operation.Gate == nil || operation.Gate.Decision == nil || operation.Gate.Decision.Actor.Kind != AuthorHuman {
			return errors.New("task store: gate decision is not human-attributed")
		}
		at := operation.Gate.Decision.DecidedAt
		expected.LatestHumanAt = &at
	case operationState:
		if !validStateTransition(current.State, next.State) {
			return errors.New("task store: invalid state transition")
		}
		expected.State = next.State
		expected.CompletedAt = cloneTime(next.CompletedAt)
	case operationRouting:
		if current.State != StateOpen || next.State != StateInProgress || current.Routing != nil || next.Routing == nil || next.Routing.ProjectID != current.ProjectID {
			return errors.New("task store: invalid routing transition")
		}
		expected.State = next.State
		if next.Routing != nil {
			value := *next.Routing
			expected.Routing = &value
		}
	case operationCompletion:
		if current.State != StateInProgress || next.State != StateCompleted || current.Routing == nil || current.Completion != nil || next.Completion == nil || next.CompletedAt == nil {
			return errors.New("task store: invalid completion transition")
		}
		expected.State = next.State
		expected.CompletedAt = cloneTime(next.CompletedAt)
		if next.Completion != nil {
			value := *next.Completion
			expected.Completion = &value
		}
	default:
		return errors.New("task store: unknown mutation operation")
	}
	if !reflect.DeepEqual(expected, next) {
		return errors.New("task store: mutation changed fields outside its authority")
	}
	return nil
}

func errTaskIdentityChanged(current, next Task) error {
	if current.Ref != next.Ref || current.Sequence != next.Sequence || current.ProjectID != next.ProjectID || current.CreatedBy != next.CreatedBy || !current.CreatedAt.Equal(next.CreatedAt) {
		return errors.New("task identity changed")
	}
	return nil
}

func (s *Store) appendLocked(operation diskOperation) error {
	if err := taskcompat.Ensure(s.path); err != nil {
		return fmt.Errorf("task store: establish compatibility boundary: %w", err)
	}
	data, err := json.Marshal(operation)
	if err != nil {
		return fmt.Errorf("task store: encode operation: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("task store: open append: %w", err)
	}
	defer file.Close()
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("task store: seek append: %w", err)
	}
	written, writeErr := s.write(file, data)
	if writeErr == nil && written != len(data) {
		writeErr = errors.New("task store: short write")
	}
	if writeErr == nil {
		writeErr = s.sync(file)
	}
	if writeErr == nil {
		return nil
	}
	if rollbackErr := file.Truncate(offset); rollbackErr == nil {
		rollbackErr = file.Sync()
		if rollbackErr == nil {
			return fmt.Errorf("task store: append: %w", writeErr)
		}
		s.poisoned = errors.Join(writeErr, rollbackErr)
	} else {
		s.poisoned = errors.Join(writeErr, rollbackErr)
	}
	return fmt.Errorf("task store: append failed and rollback failed: %w", s.poisoned)
}

func (s *Store) writeCheckpointLocked() error {
	if err := taskcompat.Ensure(s.path); err != nil {
		return fmt.Errorf("task store: establish compatibility boundary: %w", err)
	}
	snapshot := s.snapshotLocked()
	operation := diskOperation{Kind: operationCheckpoint, Schema: SchemaVersion, Checkpoint: &snapshot}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".tasks-*")
	if err != nil {
		return fmt.Errorf("task store: create checkpoint: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("task store: set checkpoint permissions: %w", err)
	}
	if err := json.NewEncoder(temp).Encode(operation); err != nil {
		temp.Close()
		return fmt.Errorf("task store: encode checkpoint: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("task store: sync checkpoint: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("task store: close checkpoint: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("task store: replace checkpoint: %w", err)
	}
	directory, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return fmt.Errorf("task store: open checkpoint directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("task store: sync checkpoint directory: %w", err)
	}
	return nil
}

func (s *Store) snapshotLocked() Snapshot {
	snapshot := Snapshot{Schema: SchemaVersion, NextSequence: s.nextSequence}
	for _, task := range s.tasks {
		snapshot.Tasks = append(snapshot.Tasks, task.Clone())
	}
	sort.Slice(snapshot.Tasks, func(i, j int) bool { return snapshot.Tasks[i].Sequence < snapshot.Tasks[j].Sequence })
	for _, task := range snapshot.Tasks {
		snapshot.Messages = append(snapshot.Messages, slices.Clone(s.messages[task.Ref.ProviderID])...)
		snapshot.Links = append(snapshot.Links, slices.Clone(s.links[task.Ref.ProviderID])...)
		for _, gate := range s.gates[task.Ref.ProviderID] {
			snapshot.Gates = append(snapshot.Gates, gate.Clone())
		}
	}
	for _, outcome := range s.outcomes {
		snapshot.Outcomes = append(snapshot.Outcomes, outcome.Clone())
	}
	sort.Slice(snapshot.Outcomes, func(i, j int) bool { return snapshot.Outcomes[i].Scope < snapshot.Outcomes[j].Scope })
	return snapshot
}

func (s *Store) checkWritable() error {
	if s.poisoned != nil {
		return fmt.Errorf("task store: store is poisoned: %w", s.poisoned)
	}
	return nil
}

func (s *Store) replayOutcome(scope, hash string) (OperationOutcome, bool, error) {
	outcome, found := s.outcomes[scope]
	if !found {
		return OperationOutcome{}, false, nil
	}
	if outcome.CommandHash != hash {
		return OperationOutcome{}, true, ErrIdempotencyConflict
	}
	return outcome.Clone(), true, nil
}

func (s *Store) outcome(actor Actor, key string) OperationOutcome {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.outcomes[outcomeScope(actor, key)].Clone()
}

func (s *Store) newEntityID(prefix string) (string, error) {
	var value [8]byte
	if _, err := io.ReadFull(s.random, value[:]); err != nil {
		return "", transient(fmt.Errorf("task store: generate %s ID: %w", prefix, err))
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}

func (s *Store) messageExists(taskID, id string) bool {
	for _, message := range s.messages[taskID] {
		if message.ID == id {
			return true
		}
	}
	return false
}

func (s *Store) linkExists(taskID, id string) bool {
	for _, link := range s.links[taskID] {
		if link.ID == id {
			return true
		}
	}
	return false
}

func (s *Store) gateIndex(taskID, id string) int {
	for index, gate := range s.gates[taskID] {
		if gate.ID == id {
			return index
		}
	}
	return -1
}

func commandDigest(kind string, command any) string {
	data, _ := json.Marshal(command)
	digest := sha256.Sum256(append([]byte(kind+"\x00"), data...))
	return hex.EncodeToString(digest[:])
}

func outcomeScope(actor Actor, key string) string { return actor.ID + "\x00" + key }

func validIdempotencyKey(value string) bool {
	return validText(value, 256, false) && !strings.ContainsAny(value, "\n\r\t")
}

func validStateTransition(current, next string) bool {
	if current == next {
		return false
	}
	switch current {
	case StateOpen:
		return next == StateCanceled
	case StateInProgress:
		return next == StateCanceled
	default:
		return false
	}
}

func taskCursor(task Task) string {
	return task.LatestActivityAt.UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatUint(task.Sequence, 10)
}

func taskPointer(task Task) *Task {
	value := task.Clone()
	return &value
}
