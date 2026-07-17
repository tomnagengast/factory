package taskstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

const (
	TaskOperationPendingUnpublished = "pending-unpublished"
	TaskOperationPublished          = "published"
	TaskOperationAppliedResult      = "applied-result"
	TaskOperationAcknowledged       = "acknowledged"
)

const (
	taskFailureRevisionConflict = "revision-conflict"
	taskFailureNotFound         = "not-found"
	taskFailureTerminal         = "terminal-task"
	taskFailureRejected         = "rejected"
)

// TaskOperation is the private task transaction and outbox projection. The
// command body stays in the task journal; only Event emits to the global wire.
type TaskOperation struct {
	ID            string                `json:"id"`
	Scope         string                `json:"scope"`
	CommandHash   string                `json:"commandHash"`
	Command       CommandEnvelope       `json:"command"`
	State         string                `json:"state"`
	EventID       string                `json:"eventId"`
	EventSequence uint64                `json:"eventSequence,omitempty"`
	Result        *Result               `json:"result,omitempty"`
	Failure       *TaskOperationFailure `json:"failure,omitempty"`
	CreatedAt     time.Time             `json:"createdAt"`
	UpdatedAt     time.Time             `json:"updatedAt"`
}

type TaskOperationFailure struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Current *Task  `json:"current,omitempty"`
}

func (o TaskOperation) Clone() TaskOperation {
	o.Command = o.Command.Clone()
	if o.Result != nil {
		result := o.Result.Clone()
		o.Result = &result
	}
	if o.Failure != nil {
		failure := *o.Failure
		if failure.Current != nil {
			current := failure.Current.Clone()
			failure.Current = &current
		}
		o.Failure = &failure
	}
	return o
}

func (r Result) Clone() Result {
	r.Task = r.Task.Clone()
	if r.Message != nil {
		message := *r.Message
		r.Message = &message
	}
	if r.Link != nil {
		link := *r.Link
		r.Link = &link
	}
	if r.Gate != nil {
		gate := r.Gate.Clone()
		r.Gate = &gate
	}
	return r
}

func (c CommandEnvelope) Clone() CommandEnvelope {
	clone := c
	switch c.Kind {
	case operationCreate:
		value := *c.Create
		clone.Create = &value
	case operationUpdate:
		value := *c.Update
		clone.Update = &value
	case operationMessage:
		value := *c.Message
		clone.Message = &value
	case operationLink:
		value := *c.Link
		clone.Link = &value
	case operationGate:
		value := *c.Gate
		clone.Gate = &value
	case operationDecision:
		value := *c.Decision
		clone.Decision = &value
	case operationState:
		value := *c.State
		clone.State = &value
	case operationRouting:
		value := *c.Routing
		clone.Routing = &value
	case operationCompletion:
		value := *c.Completion
		clone.Completion = &value
	}
	return clone
}

func (o TaskOperation) Validate() error {
	if !operationIDPattern.MatchString(o.ID) || o.Scope == "" || len(o.CommandHash) != 64 || o.EventID != "factory:task:"+o.ID ||
		o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() || o.CreatedAt.Location() != time.UTC || o.UpdatedAt.Location() != time.UTC || o.UpdatedAt.Before(o.CreatedAt) {
		return errors.New("task operation identity is invalid")
	}
	scope, hash, err := o.Command.identity()
	if err != nil || scope != o.Scope || hash != o.CommandHash || taskOperationID(scope, hash) != o.ID {
		return errors.New("task operation command identity conflicts")
	}
	switch o.State {
	case TaskOperationPendingUnpublished:
		if o.EventSequence != 0 || o.Result != nil || o.Failure != nil {
			return errors.New("pending task operation carries later evidence")
		}
	case TaskOperationPublished:
		if o.EventSequence == 0 || o.Result != nil || o.Failure != nil {
			return errors.New("published task operation evidence is invalid")
		}
	case TaskOperationAppliedResult, TaskOperationAcknowledged:
		if o.EventSequence == 0 || (o.Result == nil) == (o.Failure == nil) {
			return errors.New("applied task operation requires exactly one outcome")
		}
		if o.Result != nil {
			if err := validateOperationResult(*o.Result, o.Command.Kind); err != nil {
				return err
			}
		} else if err := o.Failure.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported task operation state %q", o.State)
	}
	return nil
}

func (f TaskOperationFailure) Validate() error {
	if !validText(f.Detail, 4096, false) {
		return errors.New("task operation failure detail is invalid")
	}
	switch f.Code {
	case taskFailureRevisionConflict:
		if f.Current == nil || f.Current.Validate() != nil {
			return errors.New("task operation revision failure is invalid")
		}
	case taskFailureNotFound, taskFailureTerminal, taskFailureRejected:
		if f.Current != nil {
			return errors.New("task operation failure carries unexpected current task")
		}
	default:
		return errors.New("task operation failure code is invalid")
	}
	return nil
}

func validateOperationResult(result Result, kind string) error {
	if result.Replayed || result.Task.Validate() != nil {
		return errors.New("task operation result is invalid")
	}
	switch kind {
	case operationMessage:
		if result.Message == nil || result.Message.Validate() != nil || result.Link != nil || result.Gate != nil {
			return errors.New("task message result is invalid")
		}
	case operationLink:
		if result.Link == nil || result.Link.Validate() != nil || result.Message != nil || result.Gate != nil {
			return errors.New("task link result is invalid")
		}
	case operationGate, operationDecision:
		if result.Gate == nil || result.Gate.Validate() != nil || result.Message != nil || result.Link != nil {
			return errors.New("task gate result is invalid")
		}
	default:
		if result.Message != nil || result.Link != nil || result.Gate != nil {
			return errors.New("task operation result carries an unexpected entity")
		}
	}
	return nil
}

func (c CommandEnvelope) identity() (string, string, error) {
	if err := c.ValidateShape(); err != nil {
		return "", "", err
	}
	var actor Actor
	var key string
	var hash string
	switch c.Kind {
	case operationCreate:
		actor, key, hash = c.Create.Actor, c.Create.IdempotencyKey, commandDigest(c.Kind, *c.Create)
	case operationUpdate:
		actor, key, hash = c.Update.Actor, c.Update.IdempotencyKey, commandDigest(c.Kind, *c.Update)
	case operationMessage:
		actor, key, hash = c.Message.Actor, c.Message.IdempotencyKey, commandDigest(c.Kind, *c.Message)
	case operationLink:
		actor, key, hash = c.Link.Actor, c.Link.IdempotencyKey, commandDigest(c.Kind, *c.Link)
	case operationGate:
		actor, key, hash = c.Gate.Actor, c.Gate.IdempotencyKey, commandDigest(c.Kind, *c.Gate)
	case operationDecision:
		actor, key, hash = c.Decision.Actor, c.Decision.IdempotencyKey, commandDigest(c.Kind, *c.Decision)
	case operationState:
		actor, key, hash = c.State.Actor, c.State.IdempotencyKey, commandDigest(c.Kind, *c.State)
	case operationRouting:
		actor, key, hash = c.Routing.Actor, c.Routing.IdempotencyKey, commandDigest(c.Kind, *c.Routing)
	case operationCompletion:
		actor, key, hash = c.Completion.Actor, c.Completion.IdempotencyKey, commandDigest(c.Kind, *c.Completion)
	}
	if err := actor.Validate(); err != nil || !validIdempotencyKey(key) {
		return "", "", errors.New("task operation idempotency identity is invalid")
	}
	return outcomeScope(actor, key), hash, nil
}

func taskOperationID(scope, hash string) string {
	digest := sha256.Sum256([]byte("factory-task-operation-v1\x00" + scope + "\x00" + hash))
	return "op-" + hex.EncodeToString(digest[:8])
}

func (o TaskOperation) Event() eventwire.Event {
	return eventwire.Event{
		ID: o.EventID, Source: eventwire.SourceFactory, Type: StagedEventType, Action: o.Command.Kind,
		Subject: o.Command.TaskID(), Attributes: map[string][]string{
			attributeOperation:            {o.ID},
			"taskSource":                  {"factory"},
			eventwire.AttributeProducer:   {"task-service"},
			eventwire.AttributeProvenance: {"factory"},
		}, ReceivedAt: o.CreatedAt,
	}
}

// SubmitTaskOperation durably stores the private command before publication.
// Exact retries return the original transaction; a reused idempotency scope
// with a different command fails closed.
func (s *Store) SubmitTaskOperation(command CommandEnvelope, now time.Time) (TaskOperation, bool, error) {
	scope, hash, err := command.identity()
	if err != nil || now.IsZero() || now.Location() != time.UTC {
		return TaskOperation{}, false, errors.New("task store: invalid task operation submission")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkWritable(); err != nil {
		return TaskOperation{}, false, err
	}
	if id := s.operationIDs[scope]; id != "" {
		existing := s.operations[id]
		if existing.CommandHash != hash {
			return TaskOperation{}, false, ErrIdempotencyConflict
		}
		return existing.Clone(), true, nil
	}
	if _, exists := s.outcomes[scope]; exists {
		return TaskOperation{}, false, errors.New("task store: durable outcome lacks canonical task operation evidence")
	}
	operation := TaskOperation{
		ID: taskOperationID(scope, hash), Scope: scope, CommandHash: hash, Command: command.Clone(),
		State: TaskOperationPendingUnpublished, EventID: "factory:task:" + taskOperationID(scope, hash), CreatedAt: now, UpdatedAt: now,
	}
	if err := operation.Validate(); err != nil {
		return TaskOperation{}, false, err
	}
	if err := s.persistLocked(diskOperation{Kind: operationTaskSubmit, TaskOperation: &operation}); err != nil {
		return TaskOperation{}, false, transient(err)
	}
	return operation.Clone(), false, nil
}

func (s *Store) RecordTaskOperationPublished(id, eventID string, sequence uint64, at time.Time) (TaskOperation, bool, error) {
	if !operationIDPattern.MatchString(id) || eventID != "factory:task:"+id || sequence == 0 || at.IsZero() || at.Location() != time.UTC {
		return TaskOperation{}, false, errors.New("task store: invalid task publication evidence")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkWritable(); err != nil {
		return TaskOperation{}, false, err
	}
	current, found := s.operations[id]
	if !found {
		return TaskOperation{}, false, ErrNotFound
	}
	if current.State != TaskOperationPendingUnpublished {
		if current.EventID == eventID && current.EventSequence == sequence {
			return current.Clone(), false, nil
		}
		return TaskOperation{}, false, errors.New("task store: task publication conflicts with durable evidence")
	}
	next := current.Clone()
	next.State, next.EventSequence, next.UpdatedAt = TaskOperationPublished, sequence, at
	if err := next.Validate(); err != nil || !validTaskOperationAdvance(current, next) {
		return TaskOperation{}, false, errors.New("task store: invalid task publication transition")
	}
	if err := s.persistLocked(diskOperation{Kind: operationTaskUpdate, TaskOperation: &next}); err != nil {
		return TaskOperation{}, false, transient(err)
	}
	return next.Clone(), true, nil
}

// ApplyTaskOperation executes one published command through the existing task
// mutation engine, then durably attaches its exact result or typed failure to
// the same journal. A crash between those appends converges through the task
// engine's existing idempotent outcome.
func (s *Store) ApplyTaskOperation(id string, at time.Time) (Result, error) {
	if !operationIDPattern.MatchString(id) || at.IsZero() || at.Location() != time.UTC {
		return Result{}, errors.New("task store: invalid task operation apply request")
	}
	s.mu.RLock()
	current, found := s.operations[id]
	s.mu.RUnlock()
	if !found {
		return Result{}, ErrNotFound
	}
	if current.State == TaskOperationAppliedResult || current.State == TaskOperationAcknowledged {
		return current.response(false)
	}
	if current.State != TaskOperationPublished {
		return Result{}, errors.New("task store: task operation is not published")
	}

	result, applyErr := s.Execute(current.Command, current.CreatedAt)
	if IsTransient(applyErr) {
		return Result{}, applyErr
	}
	result.Replayed = false
	next := current.Clone()
	next.State, next.UpdatedAt = TaskOperationAppliedResult, at
	if applyErr == nil {
		stored := result.Clone()
		next.Result = &stored
	} else {
		failure := classifyTaskOperationFailure(applyErr)
		next.Failure = &failure
	}
	if err := next.Validate(); err != nil || !validTaskOperationAdvance(current, next) {
		return Result{}, errors.New("task store: invalid applied task operation projection")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkWritable(); err != nil {
		return Result{}, err
	}
	latest, found := s.operations[id]
	if !found {
		return Result{}, ErrNotFound
	}
	if latest.State == TaskOperationAppliedResult || latest.State == TaskOperationAcknowledged {
		return latest.response(false)
	}
	if !reflect.DeepEqual(latest, current) {
		return Result{}, errors.New("task store: task operation advanced concurrently")
	}
	if err := s.persistLocked(diskOperation{Kind: operationTaskUpdate, TaskOperation: &next}); err != nil {
		return Result{}, transient(err)
	}
	return next.response(false)
}

func (s *Store) AcknowledgeTaskOperation(id string, dispatched uint64, at time.Time) (TaskOperation, bool, error) {
	if !operationIDPattern.MatchString(id) || dispatched == 0 || at.IsZero() || at.Location() != time.UTC {
		return TaskOperation{}, false, errors.New("task store: invalid task acknowledgement")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkWritable(); err != nil {
		return TaskOperation{}, false, err
	}
	current, found := s.operations[id]
	if !found {
		return TaskOperation{}, false, ErrNotFound
	}
	if current.State == TaskOperationAcknowledged {
		return current.Clone(), false, nil
	}
	if current.State != TaskOperationAppliedResult || current.EventSequence > dispatched {
		return TaskOperation{}, false, errors.New("task store: task operation is not globally dispatchable")
	}
	next := current.Clone()
	next.State, next.UpdatedAt = TaskOperationAcknowledged, at
	if err := next.Validate(); err != nil || !validTaskOperationAdvance(current, next) {
		return TaskOperation{}, false, errors.New("task store: invalid task acknowledgement transition")
	}
	if err := s.persistLocked(diskOperation{Kind: operationTaskUpdate, TaskOperation: &next}); err != nil {
		return TaskOperation{}, false, transient(err)
	}
	return next.Clone(), true, nil
}

func (s *Store) TaskOperation(id string) (TaskOperation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	operation, found := s.operations[id]
	return operation.Clone(), found
}

func (s *Store) TaskOperations() []TaskOperation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	operations := make([]TaskOperation, 0, len(s.operations))
	for _, operation := range s.operations {
		operations = append(operations, operation.Clone())
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].ID < operations[j].ID })
	return operations
}

func (o TaskOperation) response(replayed bool) (Result, error) {
	if o.Result != nil {
		result := o.Result.Clone()
		result.Replayed = replayed
		return result, nil
	}
	if o.Failure != nil {
		return Result{}, o.Failure.Error()
	}
	return Result{}, errors.New("task store: task operation has no applied outcome")
}

func classifyTaskOperationFailure(err error) TaskOperationFailure {
	var conflict RevisionConflict
	switch {
	case errors.As(err, &conflict):
		current := conflict.Current.Clone()
		return TaskOperationFailure{Code: taskFailureRevisionConflict, Detail: err.Error(), Current: &current}
	case errors.Is(err, ErrNotFound):
		return TaskOperationFailure{Code: taskFailureNotFound, Detail: err.Error()}
	case errors.Is(err, ErrTerminalTask):
		return TaskOperationFailure{Code: taskFailureTerminal, Detail: err.Error()}
	default:
		return TaskOperationFailure{Code: taskFailureRejected, Detail: err.Error()}
	}
}

func (f TaskOperationFailure) Error() error {
	switch f.Code {
	case taskFailureRevisionConflict:
		if f.Current != nil {
			return RevisionConflict{Current: f.Current.Clone()}
		}
	case taskFailureNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, f.Detail)
	case taskFailureTerminal:
		return fmt.Errorf("%w: %s", ErrTerminalTask, f.Detail)
	}
	return errors.New(f.Detail)
}

func (s *Store) applyTaskOperationLocked(operation diskOperation) error {
	if operation.Schema != 0 || operation.Checkpoint != nil || operation.Outcome != nil || operation.Task != nil || operation.Message != nil ||
		operation.Link != nil || operation.Gate != nil || operation.TaskOperation == nil || operation.LinearBinding != nil {
		return errors.New("task store: invalid task operation envelope")
	}
	next := operation.TaskOperation.Clone()
	if err := next.Validate(); err != nil {
		return fmt.Errorf("task store: invalid task operation: %w", err)
	}
	current, exists := s.operations[next.ID]
	switch operation.Kind {
	case operationTaskSubmit:
		if exists || s.operationIDs[next.Scope] != "" || next.State != TaskOperationPendingUnpublished {
			return errors.New("task store: duplicate task operation submission")
		}
	case operationTaskUpdate:
		if !exists || !validTaskOperationAdvance(current, next) {
			return errors.New("task store: invalid task operation transition")
		}
	default:
		return errors.New("task store: unsupported task operation journal kind")
	}
	if next.State == TaskOperationAppliedResult || next.State == TaskOperationAcknowledged {
		if err := s.validateAppliedTaskOperationLocked(next); err != nil {
			return err
		}
	}
	s.operations[next.ID] = next
	s.operationIDs[next.Scope] = next.ID
	return nil
}

func validTaskOperationAdvance(current, next TaskOperation) bool {
	if current.ID != next.ID || current.Scope != next.Scope || current.CommandHash != next.CommandHash || current.EventID != next.EventID ||
		!reflect.DeepEqual(current.Command, next.Command) || current.CreatedAt != next.CreatedAt || next.UpdatedAt.Before(current.UpdatedAt) {
		return false
	}
	switch current.State {
	case TaskOperationPendingUnpublished:
		return next.State == TaskOperationPublished
	case TaskOperationPublished:
		return next.State == TaskOperationAppliedResult && next.EventSequence == current.EventSequence
	case TaskOperationAppliedResult:
		return next.State == TaskOperationAcknowledged && next.EventSequence == current.EventSequence &&
			reflect.DeepEqual(next.Result, current.Result) && reflect.DeepEqual(next.Failure, current.Failure)
	default:
		return false
	}
}

func (s *Store) installCheckpointTaskOperation(operation TaskOperation) error {
	if err := operation.Validate(); err != nil || s.operations[operation.ID].ID != "" || s.operationIDs[operation.Scope] != "" {
		return errors.New("task store: invalid checkpoint task operation")
	}
	if operation.State == TaskOperationAppliedResult || operation.State == TaskOperationAcknowledged {
		if err := s.validateAppliedTaskOperationLocked(operation); err != nil {
			return err
		}
	}
	s.operations[operation.ID] = operation.Clone()
	s.operationIDs[operation.Scope] = operation.ID
	return nil
}

func (s *Store) validateAppliedTaskOperationLocked(operation TaskOperation) error {
	outcome, hasOutcome := s.outcomes[operation.Scope]
	if operation.Result != nil {
		if !hasOutcome || outcome.CommandHash != operation.CommandHash {
			return errors.New("task store: applied task operation lacks its exact outcome")
		}
		expected := resultFromOutcome(outcome)
		if !reflect.DeepEqual(*operation.Result, expected) {
			return errors.New("task store: applied task operation result conflicts with its outcome")
		}
		return s.validateTaskOperationResultLocked(operation)
	}
	if hasOutcome {
		return errors.New("task store: failed task operation conflicts with a durable outcome")
	}
	if operation.Failure != nil && operation.Failure.Current != nil {
		current, found := s.tasks[operation.Failure.Current.Ref.ProviderID]
		if !found || operation.Failure.Current.Revision > current.Revision || errTaskIdentityChanged(*operation.Failure.Current, current) != nil {
			return errors.New("task store: task operation failure conflicts with current task identity")
		}
	}
	return nil
}

func resultFromOutcome(outcome OperationOutcome) Result {
	result := Result{Task: outcome.Task.Clone()}
	if outcome.Message != nil {
		message := *outcome.Message
		result.Message = &message
	}
	if outcome.Link != nil {
		link := *outcome.Link
		result.Link = &link
	}
	if outcome.Gate != nil {
		gate := outcome.Gate.Clone()
		result.Gate = &gate
	}
	return result
}

func (s *Store) validateTaskOperationResultLocked(operation TaskOperation) error {
	result := operation.Result
	current, found := s.tasks[result.Task.Ref.ProviderID]
	if !found || result.Task.Revision > current.Revision || errTaskIdentityChanged(result.Task, current) != nil {
		return errors.New("task store: checkpoint task operation result conflicts with task state")
	}
	if result.Message != nil && !slices.Contains(s.messages[result.Message.TaskID], *result.Message) ||
		result.Link != nil && !slices.Contains(s.links[result.Link.TaskID], *result.Link) {
		return errors.New("task store: checkpoint task operation result entity is missing")
	}
	if result.Gate != nil {
		index := s.gateIndex(result.Gate.TaskID, result.Gate.ID)
		if index < 0 {
			return errors.New("task store: checkpoint task operation gate is missing")
		}
	}
	return nil
}

// TaskOutbox is the dormant single-journal submission and recovery path. Its
// handler is registered before callers can publish, so the wire cannot globally
// acknowledge a task mutation before the task journal has recorded an applied
// result or typed failure.
type TaskOutbox struct {
	store *Store
	wire  *eventwire.Wire
}

func NewTaskOutbox(store *Store, wire *eventwire.Wire) (*TaskOutbox, error) {
	if store == nil || wire == nil {
		return nil, errors.New("task outbox: store and wire are required")
	}
	outbox := &TaskOutbox{store: store, wire: wire}
	if err := wire.Handle(eventwire.Filter{Source: eventwire.SourceFactory, Type: StagedEventType}, outbox.handle); err != nil {
		return nil, err
	}
	return outbox, nil
}

func (o *TaskOutbox) Execute(ctx context.Context, command CommandEnvelope, now time.Time) (Result, error) {
	if o == nil || o.store == nil || o.wire == nil {
		return Result{}, errors.New("task outbox: unavailable")
	}
	operation, _, err := o.store.SubmitTaskOperation(command, now)
	if err != nil {
		return Result{}, err
	}
	if operation.State == TaskOperationAcknowledged {
		return operation.response(true)
	}
	if operation.State == TaskOperationPendingUnpublished {
		if _, _, err := o.wire.Publish(ctx, operation.Event()); err != nil {
			return Result{}, err
		}
	}
	if err := o.Reconcile(ctx); err != nil {
		return Result{}, err
	}
	operation, found := o.store.TaskOperation(operation.ID)
	if !found || operation.State != TaskOperationAcknowledged {
		return Result{}, errors.New("task outbox: operation did not reach global acknowledgement")
	}
	// The legacy coordinator synchronously dispatches and then rereads the
	// durable idempotent outcome, so its successful API response reports a
	// replay even on the first call. Preserve that public behavior while the
	// task operation keeps its original non-replayed result as journal evidence.
	return operation.response(true)
}

func (o *TaskOutbox) Reconcile(ctx context.Context) error {
	if o == nil || o.store == nil || o.wire == nil {
		return errors.New("task outbox: unavailable")
	}
	if err := o.wire.CatchUp(ctx); err != nil {
		return err
	}
	for _, operation := range o.store.TaskOperations() {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch operation.State {
		case TaskOperationPendingUnpublished:
			if _, _, err := o.wire.Publish(ctx, operation.Event()); err != nil {
				return err
			}
		case TaskOperationPublished:
			if _, err := o.store.ApplyTaskOperation(operation.ID, operation.UpdatedAt); err != nil {
				latest, _ := o.store.TaskOperation(operation.ID)
				if latest.State != TaskOperationAppliedResult && latest.State != TaskOperationAcknowledged {
					return err
				}
			}
		}
		latest, found := o.store.TaskOperation(operation.ID)
		if !found || latest.State != TaskOperationAppliedResult {
			continue
		}
		status := o.wire.Status()
		if status.Dispatched < latest.EventSequence {
			continue
		}
		at := latest.UpdatedAt
		if _, _, err := o.store.AcknowledgeTaskOperation(latest.ID, status.Dispatched, at); err != nil {
			return err
		}
	}
	return nil
}

func (o *TaskOutbox) handle(_ context.Context, record eventwire.Record) error {
	values := record.Event.Values(attributeOperation)
	if len(values) != 1 || !operationIDPattern.MatchString(values[0]) || record.Sequence == 0 {
		return eventwire.Permanent(errors.New("task outbox: operation identity is invalid"))
	}
	operation, found := o.store.TaskOperation(values[0])
	if !found || !taskOperationRecordMatches(operation, record) {
		return eventwire.Permanent(errors.New("task outbox: event conflicts with private operation"))
	}
	if _, _, err := o.store.RecordTaskOperationPublished(operation.ID, record.Event.ID, record.Sequence, record.Event.ReceivedAt); err != nil {
		return err
	}
	if _, err := o.store.ApplyTaskOperation(operation.ID, record.Event.ReceivedAt); err != nil {
		latest, _ := o.store.TaskOperation(operation.ID)
		if latest.State != TaskOperationAppliedResult && latest.State != TaskOperationAcknowledged {
			return err
		}
	}
	return nil
}

func taskOperationRecordMatches(operation TaskOperation, record eventwire.Record) bool {
	expected := operation.Event()
	return record.Event.ID == expected.ID && record.Event.Source == expected.Source && record.Event.Type == expected.Type &&
		record.Event.Action == expected.Action && record.Event.Subject == expected.Subject && record.Event.ReceivedAt.Equal(expected.ReceivedAt) &&
		(record.Event.RootEventID == "" || record.Event.RootEventID == expected.ID) && reflect.DeepEqual(record.Event.Attributes, expected.Attributes) &&
		len(record.Event.Channels) == 0 && record.Event.ParentInvocationID == "" && record.Event.ParentRunID == "" && record.Event.Hop == 0 &&
		len(record.Event.AncestorRuleIDs) == 0
}
