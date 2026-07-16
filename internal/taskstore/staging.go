package taskstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/taskcompat"
)

const (
	StagedEventType    = "task-mutation"
	attributeOperation = "taskOperationId"
)

var operationIDPattern = regexp.MustCompile(`^op-[a-f0-9]{16}$`)

type CommandEnvelope struct {
	Kind       string             `json:"kind"`
	Create     *CreateCommand     `json:"create,omitempty"`
	Update     *UpdateCommand     `json:"update,omitempty"`
	Message    *MessageCommand    `json:"message,omitempty"`
	Link       *LinkCommand       `json:"link,omitempty"`
	Gate       *GateCommand       `json:"gate,omitempty"`
	Decision   *DecisionCommand   `json:"decision,omitempty"`
	State      *StateCommand      `json:"state,omitempty"`
	Routing    *RoutingCommand    `json:"routing,omitempty"`
	Completion *CompletionCommand `json:"completion,omitempty"`
}

type Result struct {
	Task     Task     `json:"task"`
	Message  *Message `json:"message,omitempty"`
	Link     *Link    `json:"link,omitempty"`
	Gate     *Gate    `json:"gate,omitempty"`
	Replayed bool     `json:"replayed"`
}

type StagedOperation struct {
	OperationID string
	Event       eventwire.Event
}

func CreateEnvelope(command CreateCommand) CommandEnvelope {
	return CommandEnvelope{Kind: operationCreate, Create: &command}
}

func UpdateEnvelope(command UpdateCommand) CommandEnvelope {
	return CommandEnvelope{Kind: operationUpdate, Update: &command}
}

func MessageEnvelope(command MessageCommand) CommandEnvelope {
	return CommandEnvelope{Kind: operationMessage, Message: &command}
}

func LinkEnvelope(command LinkCommand) CommandEnvelope {
	return CommandEnvelope{Kind: operationLink, Link: &command}
}

func GateEnvelope(command GateCommand) CommandEnvelope {
	return CommandEnvelope{Kind: operationGate, Gate: &command}
}

func DecisionEnvelope(command DecisionCommand) CommandEnvelope {
	return CommandEnvelope{Kind: operationDecision, Decision: &command}
}

func StateEnvelope(command StateCommand) CommandEnvelope {
	return CommandEnvelope{Kind: operationState, State: &command}
}

func RoutingEnvelope(command RoutingCommand) CommandEnvelope {
	return CommandEnvelope{Kind: operationRouting, Routing: &command}
}

func CompletionEnvelope(command CompletionCommand) CommandEnvelope {
	return CommandEnvelope{Kind: operationCompletion, Completion: &command}
}

type Stager struct {
	directory      string
	markerDataFile string
	random         io.Reader
}

func NewStager(directory, markerDataFile string) (*Stager, error) {
	if directory == "" || markerDataFile == "" {
		return nil, errors.New("task staging: directory and marker data file are required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("task staging: create directory: %w", err)
	}
	return &Stager{directory: filepath.Clean(directory), markerDataFile: filepath.Clean(markerDataFile), random: rand.Reader}, nil
}

func (s *Stager) Stage(command CommandEnvelope, now time.Time) (StagedOperation, error) {
	if err := command.ValidateShape(); err != nil {
		return StagedOperation{}, err
	}
	if err := taskcompat.Ensure(s.markerDataFile); err != nil {
		return StagedOperation{}, fmt.Errorf("task staging: establish compatibility boundary: %w", err)
	}
	operationID, err := randomOperationID(s.random)
	if err != nil {
		return StagedOperation{}, err
	}
	path := s.path(operationID)
	temp, err := os.CreateTemp(s.directory, ".task-operation-*")
	if err != nil {
		return StagedOperation{}, fmt.Errorf("task staging: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return StagedOperation{}, fmt.Errorf("task staging: set permissions: %w", err)
	}
	if err := json.NewEncoder(temp).Encode(command); err != nil {
		temp.Close()
		return StagedOperation{}, fmt.Errorf("task staging: encode command: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return StagedOperation{}, fmt.Errorf("task staging: sync command: %w", err)
	}
	if err := temp.Close(); err != nil {
		return StagedOperation{}, fmt.Errorf("task staging: close command: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return StagedOperation{}, fmt.Errorf("task staging: publish command: %w", err)
	}
	directory, err := os.Open(s.directory)
	if err != nil {
		return StagedOperation{}, fmt.Errorf("task staging: open directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return StagedOperation{}, fmt.Errorf("task staging: sync directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return StagedOperation{}, fmt.Errorf("task staging: close directory: %w", err)
	}
	event := eventwire.Event{
		ID: "factory:task:" + operationID, Source: eventwire.SourceFactory, Type: StagedEventType, Action: command.Kind,
		Subject: command.TaskID(), Attributes: map[string][]string{
			attributeOperation:            {operationID},
			"taskSource":                  {"factory"},
			eventwire.AttributeProducer:   {"task-service"},
			eventwire.AttributeProvenance: {"factory"},
		}, ReceivedAt: now.UTC(),
	}
	if err := event.Validate(); err != nil {
		_ = s.Cancel(operationID)
		return StagedOperation{}, err
	}
	return StagedOperation{OperationID: operationID, Event: event}, nil
}

func (s *Stager) Cancel(operationID string) error {
	if !operationIDPattern.MatchString(operationID) {
		return errors.New("task staging: invalid operation ID")
	}
	removed, err := s.remove(s.path(operationID))
	if err != nil {
		return err
	}
	if removed {
		return s.syncDirectory()
	}
	return nil
}

// Recover removes command files that have no pending wire record. Pending
// records retain their bodies until CatchUp dispatches them successfully.
func (s *Stager) Recover(dispatched uint64, records []eventwire.Record) error {
	pending := make(map[string]struct{})
	for _, record := range records {
		if record.Sequence <= dispatched || record.Event.Source != eventwire.SourceFactory || record.Event.Type != StagedEventType {
			continue
		}
		values := record.Event.Values(attributeOperation)
		if len(values) == 1 && operationIDPattern.MatchString(values[0]) && record.Event.ID == "factory:task:"+values[0] {
			pending[values[0]] = struct{}{}
		}
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return fmt.Errorf("task staging: read recovery directory: %w", err)
	}
	removed := false
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("task staging: unsafe recovery entry %s", entry.Name())
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".task-operation-") {
			deleted, err := s.remove(filepath.Join(s.directory, name))
			if err != nil {
				return err
			}
			removed = removed || deleted
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			return fmt.Errorf("task staging: unknown recovery entry %s", name)
		}
		operationID := strings.TrimSuffix(name, ".json")
		if !operationIDPattern.MatchString(operationID) {
			return fmt.Errorf("task staging: invalid recovery entry %s", name)
		}
		if _, keep := pending[operationID]; keep {
			continue
		}
		deleted, err := s.remove(filepath.Join(s.directory, name))
		if err != nil {
			return err
		}
		removed = removed || deleted
	}
	if removed {
		return s.syncDirectory()
	}
	return nil
}

func (s *Stager) remove(path string) (bool, error) {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("task staging: remove: %w", err)
	}
	return true, nil
}

func (s *Stager) syncDirectory() error {
	directory, err := os.Open(s.directory)
	if err != nil {
		return fmt.Errorf("task staging: open directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("task staging: sync directory: %w", err)
	}
	return nil
}

func (s *Stager) Status() Status {
	status := Status{Healthy: true}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		status.Healthy = false
		return status
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			status.PendingStages++
		}
	}
	return status
}

func (s *Stager) path(operationID string) string {
	return filepath.Join(s.directory, operationID+".json")
}

type Dispatcher struct {
	store  *Store
	stager *Stager
}

func NewDispatcher(store *Store, stager *Stager) (*Dispatcher, error) {
	if store == nil || stager == nil {
		return nil, errors.New("task dispatcher: store and stager are required")
	}
	return &Dispatcher{store: store, stager: stager}, nil
}

func (d *Dispatcher) Apply(_ context.Context, record eventwire.Record) (Result, error) {
	if record.Event.Source != eventwire.SourceFactory || record.Event.Type != StagedEventType {
		return Result{}, eventwire.Permanent(errors.New("task dispatcher: event is not a task mutation"))
	}
	values := record.Event.Values(attributeOperation)
	if len(values) != 1 || !operationIDPattern.MatchString(values[0]) || record.Event.ID != "factory:task:"+values[0] {
		return Result{}, eventwire.Permanent(errors.New("task dispatcher: operation identity is invalid"))
	}
	operationID := values[0]
	file, err := os.Open(d.stager.path(operationID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, eventwire.Permanent(fmt.Errorf("task dispatcher: staged command is missing: %w", err))
		}
		return Result{}, fmt.Errorf("task dispatcher: open staged command: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return Result{}, eventwire.Permanent(errors.New("task dispatcher: staged command permissions or size are invalid"))
	}
	var command CommandEnvelope
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		return Result{}, eventwire.Permanent(fmt.Errorf("task dispatcher: decode staged command: %w", err))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Result{}, eventwire.Permanent(errors.New("task dispatcher: staged command has trailing content"))
	}
	if err := command.ValidateShape(); err != nil || command.Kind != record.Event.Action || command.TaskID() != record.Event.Subject {
		return Result{}, eventwire.Permanent(errors.New("task dispatcher: staged command conflicts with event metadata"))
	}
	result, err := d.store.Execute(command, record.Event.ReceivedAt)
	if err != nil {
		if !IsTransient(err) {
			return Result{}, eventwire.Permanent(err)
		}
		return Result{}, err
	}
	if err := d.stager.Cancel(operationID); err != nil {
		return Result{}, fmt.Errorf("task dispatcher: clean applied command: %w", err)
	}
	return result, nil
}

type Publisher interface {
	Publish(context.Context, eventwire.Event) (eventwire.Record, bool, error)
}

type Coordinator struct {
	store     *Store
	stager    *Stager
	publisher Publisher
}

func NewCoordinator(store *Store, stager *Stager, publisher Publisher) (*Coordinator, error) {
	if store == nil || stager == nil || publisher == nil {
		return nil, errors.New("task coordinator: store, stager, and publisher are required")
	}
	return &Coordinator{store: store, stager: stager, publisher: publisher}, nil
}

func (c *Coordinator) Execute(ctx context.Context, command CommandEnvelope, now time.Time) (Result, error) {
	staged, err := c.stager.Stage(command, now)
	if err != nil {
		return Result{}, err
	}
	if _, _, err := c.publisher.Publish(ctx, staged.Event); err != nil {
		return Result{}, err
	}
	result, err := c.store.Execute(command, now)
	if err != nil {
		if !IsTransient(err) {
			_ = c.stager.Cancel(staged.OperationID)
		}
		return Result{}, fmt.Errorf("task coordinator: read applied outcome: %w", err)
	}
	if err := c.stager.Cancel(staged.OperationID); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Store) Execute(command CommandEnvelope, now time.Time) (Result, error) {
	if err := command.ValidateShape(); err != nil {
		return Result{}, err
	}
	switch command.Kind {
	case operationCreate:
		task, replayed, err := s.Create(*command.Create, now)
		return Result{Task: task, Replayed: replayed}, err
	case operationUpdate:
		task, replayed, err := s.Update(*command.Update, now)
		return Result{Task: task, Replayed: replayed}, err
	case operationMessage:
		task, message, replayed, err := s.AddMessage(*command.Message, now)
		return Result{Task: task, Message: &message, Replayed: replayed}, err
	case operationLink:
		task, link, replayed, err := s.AddLink(*command.Link, now)
		return Result{Task: task, Link: &link, Replayed: replayed}, err
	case operationGate:
		task, gate, replayed, err := s.OpenGate(*command.Gate, now)
		return Result{Task: task, Gate: &gate, Replayed: replayed}, err
	case operationDecision:
		task, gate, replayed, err := s.DecideGate(*command.Decision, now)
		return Result{Task: task, Gate: &gate, Replayed: replayed}, err
	case operationState:
		task, replayed, err := s.ChangeState(*command.State, now)
		return Result{Task: task, Replayed: replayed}, err
	case operationRouting:
		task, replayed, err := s.SetRouting(*command.Routing, now)
		return Result{Task: task, Replayed: replayed}, err
	case operationCompletion:
		task, replayed, err := s.Complete(*command.Completion, now)
		return Result{Task: task, Replayed: replayed}, err
	default:
		return Result{}, errors.New("task dispatcher: unsupported command")
	}
}

func (c CommandEnvelope) ValidateShape() error {
	count := 0
	for _, present := range []bool{c.Create != nil, c.Update != nil, c.Message != nil, c.Link != nil, c.Gate != nil, c.Decision != nil, c.State != nil, c.Routing != nil, c.Completion != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return errors.New("task staging: command must contain exactly one operation")
	}
	valid := c.Kind == operationCreate && c.Create != nil ||
		c.Kind == operationUpdate && c.Update != nil ||
		c.Kind == operationMessage && c.Message != nil ||
		c.Kind == operationLink && c.Link != nil ||
		c.Kind == operationGate && c.Gate != nil ||
		c.Kind == operationDecision && c.Decision != nil ||
		c.Kind == operationState && c.State != nil ||
		c.Kind == operationRouting && c.Routing != nil ||
		c.Kind == operationCompletion && c.Completion != nil
	if !valid {
		return errors.New("task staging: command kind conflicts with payload")
	}
	return nil
}

func (c CommandEnvelope) TaskID() string {
	switch c.Kind {
	case operationUpdate:
		return c.Update.TaskID
	case operationMessage:
		return c.Message.TaskID
	case operationLink:
		return c.Link.TaskID
	case operationGate:
		return c.Gate.TaskID
	case operationDecision:
		return c.Decision.TaskID
	case operationState:
		return c.State.TaskID
	case operationRouting:
		return c.Routing.TaskID
	case operationCompletion:
		return c.Completion.TaskID
	default:
		return ""
	}
}

func randomOperationID(random io.Reader) (string, error) {
	var value [8]byte
	if _, err := io.ReadFull(random, value[:]); err != nil {
		return "", fmt.Errorf("task staging: generate operation ID: %w", err)
	}
	return "op-" + hex.EncodeToString(value[:]), nil
}

func BodyFreeEvent(event eventwire.Event, forbidden ...string) bool {
	data, err := json.Marshal(event)
	if err != nil {
		return false
	}
	encoded := string(data)
	for _, value := range forbidden {
		if value != "" && strings.Contains(encoded, value) {
			return false
		}
	}
	return true
}
