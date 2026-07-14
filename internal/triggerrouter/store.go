package triggerrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

const (
	operationCheckpoint    = "checkpoint"
	operationDecisionBatch = "decision-batch"
	operationTransition    = "transition"
)

type transition struct {
	InvocationID string     `json:"invocationId"`
	State        string     `json:"state"`
	RunID        string     `json:"runId,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	At           time.Time  `json:"at"`
	ReflectedAt  *time.Time `json:"reflectedAt,omitempty"`
}

type diskOperation struct {
	Kind           string       `json:"kind"`
	Schema         int          `json:"schema,omitempty"`
	Checkpoint     *Snapshot    `json:"checkpoint,omitempty"`
	Decisions      []Decision   `json:"decisions,omitempty"`
	Invocations    []Invocation `json:"invocations,omitempty"`
	RateIncrements []RateBucket `json:"rateIncrements,omitempty"`
	Transition     *transition  `json:"transition,omitempty"`
}

type Store struct {
	mu             sync.RWMutex
	path           string
	decisions      map[string]Decision
	invocations    map[string]Invocation
	rates          map[string]RateBucket
	operationUnits int
	poisoned       error
	write          func(*os.File, []byte) (int, error)
	sync           func(*os.File) error
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("trigger router: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("trigger router: create directory: %w", err)
	}
	store := &Store{path: path}
	store.resetProjection()
	store.write = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	store.sync = func(file *os.File) error { return file.Sync() }
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := store.writeCheckpointLocked(); err != nil {
			return nil, err
		}
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trigger router: read: %w", err)
	}
	complete := len(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if index := bytes.LastIndexByte(data, '\n'); index >= 0 {
			complete = index + 1
		} else {
			complete = 0
		}
		if err := os.Truncate(path, int64(complete)); err != nil {
			return nil, fmt.Errorf("trigger router: truncate incomplete tail: %w", err)
		}
	}
	if err := store.replay(data[:complete]); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) resetProjection() {
	s.decisions = make(map[string]Decision)
	s.invocations = make(map[string]Invocation)
	s.rates = make(map[string]RateBucket)
	s.operationUnits = 0
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *Store) Invocation(id string) (Invocation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	invocation, found := s.invocations[id]
	return invocation.Clone(), found
}

func (s *Store) ClaimedInvocation(invocationID, runID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	invocation, found := s.invocations[invocationID]
	return found && invocation.State == StateClaimed && invocation.RunID == runID
}

func (s *Store) Prune(retainedEventIDs map[string]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned != nil {
		return fmt.Errorf("trigger router: store is poisoned: %w", s.poisoned)
	}
	removeEvents := make(map[string]bool)
	removeInvocations := make(map[string]bool)
	for eventID, decision := range s.decisions {
		if retainedEventIDs[eventID] {
			continue
		}
		terminal := true
		for _, outcome := range decision.Outcomes {
			if outcome.Kind != OutcomeInvocation {
				continue
			}
			invocation, found := s.invocations[outcome.InvocationID]
			if !found || invocation.Nonterminal() {
				terminal = false
				break
			}
		}
		if terminal {
			removeEvents[eventID] = true
			for _, outcome := range decision.Outcomes {
				if outcome.Kind == OutcomeInvocation {
					removeInvocations[outcome.InvocationID] = true
				}
			}
		}
	}
	if len(removeEvents) == 0 {
		return nil
	}
	oldDecisions, oldInvocations := s.decisions, s.invocations
	nextDecisions := make(map[string]Decision, len(s.decisions)-len(removeEvents))
	for id, decision := range s.decisions {
		if !removeEvents[id] {
			nextDecisions[id] = decision
		}
	}
	nextInvocations := make(map[string]Invocation, len(s.invocations)-len(removeInvocations))
	for id, invocation := range s.invocations {
		if !removeInvocations[id] {
			nextInvocations[id] = invocation
		}
	}
	s.decisions, s.invocations = nextDecisions, nextInvocations
	if err := s.writeCheckpointLocked(); err != nil {
		s.decisions, s.invocations = oldDecisions, oldInvocations
		return err
	}
	return nil
}

func (s *Store) TransitionInvocation(id, state, runID, reason string, reflectedAt *time.Time, now time.Time) (Invocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned != nil {
		return Invocation{}, fmt.Errorf("trigger router: store is poisoned: %w", s.poisoned)
	}
	current, found := s.invocations[id]
	if !found {
		return Invocation{}, errors.New("trigger router: invocation not found")
	}
	now = now.UTC()
	if reflectedAt != nil {
		value := reflectedAt.UTC()
		reflectedAt = &value
	}
	next := transition{InvocationID: id, State: state, RunID: runID, Reason: reason, At: now, ReflectedAt: reflectedAt}
	if err := validateTransition(current, next); err != nil {
		return Invocation{}, err
	}
	op := diskOperation{Kind: operationTransition, Transition: &next}
	if err := s.appendOperationLocked(op); err != nil {
		return Invocation{}, err
	}
	if err := s.applyOperationLocked(op); err != nil {
		s.poisoned = err
		return Invocation{}, err
	}
	if err := s.compactIfNeededLocked(); err != nil {
		return Invocation{}, err
	}
	return s.invocations[id].Clone(), nil
}

func validateTransition(current Invocation, next transition) error {
	allowed := false
	switch current.State {
	case StateQueued:
		allowed = next.State == StateClaiming || next.State == StateRejected
	case StateClaiming:
		allowed = next.State == StateClaimed || next.State == StateRejected
	case StateClaimed:
		allowed = next.State == StateSucceeded || next.State == StateBlocked || next.State == StateFailed || next.State == StateRejected
	default:
		allowed = false
	}
	if !allowed {
		return fmt.Errorf("trigger router: invalid invocation transition %s -> %s", current.State, next.State)
	}
	if (next.State == StateClaiming || next.State == StateClaimed) && next.RunID == "" {
		return errors.New("trigger router: claiming transition requires Run ID")
	}
	if next.State == StateClaimed && current.RunID != "" && current.RunID != next.RunID {
		return errors.New("trigger router: claimed Run ID conflicts with claim intent")
	}
	if (next.State == StateSucceeded || next.State == StateBlocked || next.State == StateFailed) && next.ReflectedAt == nil {
		return errors.New("trigger router: terminal Run outcome must be reflected")
	}
	return nil
}

func (s *Store) replay(data []byte) error {
	lines := bytes.Split(data, []byte{'\n'})
	foundCheckpoint := false
	for _, raw := range lines {
		if len(raw) == 0 {
			continue
		}
		var operation diskOperation
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&operation); err != nil {
			return fmt.Errorf("trigger router: decode operation: %w", err)
		}
		if operation.Kind == operationCheckpoint {
			if foundCheckpoint || operation.Schema != SchemaVersion || operation.Checkpoint == nil {
				return errors.New("trigger router: invalid checkpoint")
			}
			foundCheckpoint = true
		}
		if !foundCheckpoint {
			return errors.New("trigger router: operation precedes checkpoint")
		}
		if err := s.applyOperationLocked(operation); err != nil {
			return err
		}
	}
	if !foundCheckpoint {
		return errors.New("trigger router: checkpoint is missing")
	}
	return nil
}

func (s *Store) applyOperationLocked(operation diskOperation) error {
	switch operation.Kind {
	case operationCheckpoint:
		if operation.Checkpoint == nil || operation.Checkpoint.Schema != SchemaVersion {
			return errors.New("trigger router: invalid checkpoint projection")
		}
		s.resetProjection()
		for _, decision := range operation.Checkpoint.Decisions {
			if _, exists := s.decisions[decision.EventID]; exists || decision.EventID == "" || decision.EventSequence == 0 {
				return errors.New("trigger router: invalid checkpoint decision")
			}
			s.decisions[decision.EventID] = decision.Clone()
		}
		for _, invocation := range operation.Checkpoint.Invocations {
			if _, exists := s.invocations[invocation.ID]; exists || invocation.ID == "" || invocation.EventID == "" {
				return errors.New("trigger router: invalid checkpoint invocation")
			}
			s.invocations[invocation.ID] = invocation.Clone()
		}
		for _, bucket := range operation.Checkpoint.RateBuckets {
			if bucket.RuleID == "" || bucket.Minute.IsZero() || bucket.Count < 1 {
				return errors.New("trigger router: invalid checkpoint rate bucket")
			}
			key := rateKey(bucket.RuleID, bucket.Minute)
			if _, exists := s.rates[key]; exists {
				return errors.New("trigger router: duplicate checkpoint rate bucket")
			}
			bucket.Minute = bucket.Minute.UTC().Truncate(time.Minute)
			s.rates[key] = bucket
		}
		if err := s.validateProjectionLocked(); err != nil {
			return err
		}
		s.operationUnits = s.liveUnitsLocked()
	case operationDecisionBatch:
		if len(operation.Decisions) == 0 {
			return errors.New("trigger router: empty decision batch")
		}
		invocationByID := make(map[string]Invocation, len(operation.Invocations))
		for _, invocation := range operation.Invocations {
			if invocation.ID == "" || invocation.EventID == "" || invocation.State != StateQueued {
				return errors.New("trigger router: invalid admitted invocation")
			}
			if _, exists := invocationByID[invocation.ID]; exists {
				return errors.New("trigger router: duplicate invocation in batch")
			}
			invocationByID[invocation.ID] = invocation
		}
		seenDecisions := make(map[string]bool, len(operation.Decisions))
		referencedInvocations := make(map[string]bool, len(operation.Invocations))
		admittedByRule := make(map[string]int)
		for _, decision := range operation.Decisions {
			if seenDecisions[decision.EventID] {
				return errors.New("trigger router: duplicate decision in batch")
			}
			seenDecisions[decision.EventID] = true
			if _, exists := s.decisions[decision.EventID]; exists {
				return errors.New("trigger router: duplicate durable decision")
			}
			if err := validateDecision(decision); err != nil {
				return errors.New("trigger router: invalid decision")
			}
			for _, outcome := range decision.Outcomes {
				if outcome.Kind == OutcomeInvocation {
					invocation, found := invocationByID[outcome.InvocationID]
					if !found || referencedInvocations[outcome.InvocationID] || invocation.EventID != decision.EventID || invocation.EventSequence != decision.EventSequence || invocation.Rule.ID != outcome.RuleID || invocation.Rule.Revision != outcome.RuleRevision {
						return errors.New("trigger router: decision invocation mismatch")
					}
					referencedInvocations[outcome.InvocationID] = true
					admittedByRule[outcome.RuleID]++
				}
			}
			s.decisions[decision.EventID] = decision.Clone()
		}
		if len(referencedInvocations) != len(invocationByID) {
			return errors.New("trigger router: orphan invocation in batch")
		}
		for id, invocation := range invocationByID {
			if _, exists := s.invocations[id]; exists {
				return errors.New("trigger router: duplicate durable invocation")
			}
			s.invocations[id] = invocation.Clone()
		}
		incrementedByRule := make(map[string]int, len(operation.RateIncrements))
		for _, increment := range operation.RateIncrements {
			if increment.RuleID == "" || increment.Minute.IsZero() || increment.Count < 1 {
				return errors.New("trigger router: invalid rate increment")
			}
			if incrementedByRule[increment.RuleID] != 0 {
				return errors.New("trigger router: duplicate rate increment")
			}
			incrementedByRule[increment.RuleID] = increment.Count
			key := rateKey(increment.RuleID, increment.Minute)
			bucket := s.rates[key]
			bucket.RuleID, bucket.Minute = increment.RuleID, increment.Minute.UTC().Truncate(time.Minute)
			bucket.Count += increment.Count
			s.rates[key] = bucket
		}
		if !equalCounts(admittedByRule, incrementedByRule) {
			return errors.New("trigger router: rate increments do not match admissions")
		}
		s.operationUnits += len(operation.Decisions) + len(operation.Invocations) + len(operation.RateIncrements)
	case operationTransition:
		if operation.Transition == nil {
			return errors.New("trigger router: transition is missing")
		}
		current, found := s.invocations[operation.Transition.InvocationID]
		if !found {
			return errors.New("trigger router: transition invocation is missing")
		}
		if err := validateTransition(current, *operation.Transition); err != nil {
			return err
		}
		current.State = operation.Transition.State
		current.RunID = operation.Transition.RunID
		current.Reason = operation.Transition.Reason
		current.UpdatedAt = operation.Transition.At.UTC()
		if operation.Transition.ReflectedAt != nil {
			value := operation.Transition.ReflectedAt.UTC()
			current.ReflectedAt = &value
		}
		if !current.Nonterminal() {
			compactTerminalInvocation(&current)
		}
		s.invocations[current.ID] = current
		s.operationUnits++
	default:
		return fmt.Errorf("trigger router: unknown operation %q", operation.Kind)
	}
	return nil
}

func (s *Store) validateProjectionLocked() error {
	referenced := make(map[string]bool, len(s.invocations))
	for _, decision := range s.decisions {
		if err := validateDecision(decision); err != nil {
			return errors.New("trigger router: invalid checkpoint decision")
		}
		for _, outcome := range decision.Outcomes {
			if outcome.Kind != OutcomeInvocation {
				continue
			}
			invocation, found := s.invocations[outcome.InvocationID]
			if !found || referenced[outcome.InvocationID] || invocation.EventID != decision.EventID || invocation.EventSequence != decision.EventSequence || invocation.Rule.ID != outcome.RuleID || invocation.Rule.Revision != outcome.RuleRevision {
				return errors.New("trigger router: invalid checkpoint decision invocation")
			}
			referenced[outcome.InvocationID] = true
		}
	}
	if len(referenced) != len(s.invocations) {
		return errors.New("trigger router: orphan checkpoint invocation")
	}
	return nil
}

func validateDecision(decision Decision) error {
	if decision.EventID == "" || decision.EventSequence == 0 || !eventwire.ValidSource(decision.Source) || decision.DecidedAt.IsZero() {
		return errors.New("decision identity is invalid")
	}
	seenRules := make(map[string]bool, len(decision.Outcomes))
	for _, outcome := range decision.Outcomes {
		if outcome.RuleID == "" || outcome.RuleRevision == 0 || seenRules[outcome.RuleID] {
			return errors.New("decision outcome identity is invalid")
		}
		seenRules[outcome.RuleID] = true
		switch outcome.Kind {
		case OutcomeInvocation:
			if outcome.InvocationID == "" || outcome.Reason != "" {
				return errors.New("invocation outcome is invalid")
			}
		case OutcomeRejected, OutcomeSuppressed:
			if outcome.InvocationID != "" || outcome.Reason == "" {
				return errors.New("non-invocation outcome is invalid")
			}
		default:
			return errors.New("decision outcome kind is invalid")
		}
	}
	return nil
}

func equalCounts(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, count := range left {
		if right[key] != count {
			return false
		}
	}
	return true
}

func compactTerminalInvocation(invocation *Invocation) {
	invocation.Rule = triggerregistry.Rule{ID: invocation.Rule.ID, Revision: invocation.Rule.Revision}
	invocation.Workflow = settings.Workflow{ID: invocation.Workflow.ID}
}

func (s *Store) appendOperationLocked(operation diskOperation) error {
	data, err := json.Marshal(operation)
	if err != nil {
		return fmt.Errorf("trigger router: encode operation: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("trigger router: open append: %w", err)
	}
	defer file.Close()
	offset, err := file.Seek(0, 2)
	if err != nil {
		return fmt.Errorf("trigger router: seek append: %w", err)
	}
	written, writeErr := s.write(file, data)
	if writeErr == nil && written != len(data) {
		writeErr = errors.New("trigger router: short write")
	}
	if writeErr == nil {
		writeErr = s.sync(file)
	}
	if writeErr == nil {
		return nil
	}
	if rollbackErr := rollbackAppend(file, offset); rollbackErr != nil {
		s.poisoned = errors.Join(writeErr, rollbackErr)
		return fmt.Errorf("trigger router: append failed and rollback failed: %w", s.poisoned)
	}
	return fmt.Errorf("trigger router: append: %w", writeErr)
}

func rollbackAppend(file *os.File, offset int64) error {
	if err := file.Truncate(offset); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Store) compactIfNeededLocked() error {
	live := s.liveUnitsLocked()
	if s.operationUnits-live <= live {
		return nil
	}
	return s.writeCheckpointLocked()
}

func (s *Store) writeCheckpointLocked() error {
	snapshot := s.snapshotLocked()
	operation := diskOperation{Kind: operationCheckpoint, Schema: SchemaVersion, Checkpoint: &snapshot}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".trigger-routing-*")
	if err != nil {
		return fmt.Errorf("trigger router: create checkpoint: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("trigger router: set checkpoint permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	if err := encoder.Encode(operation); err != nil {
		temp.Close()
		return fmt.Errorf("trigger router: encode checkpoint: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("trigger router: sync checkpoint: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("trigger router: close checkpoint: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("trigger router: replace checkpoint: %w", err)
	}
	s.operationUnits = s.liveUnitsLocked()
	return nil
}

func (s *Store) snapshotLocked() Snapshot {
	snapshot := Snapshot{Schema: SchemaVersion}
	for _, decision := range s.decisions {
		snapshot.Decisions = append(snapshot.Decisions, decision.Clone())
	}
	for _, invocation := range s.invocations {
		snapshot.Invocations = append(snapshot.Invocations, invocation.Clone())
	}
	for _, bucket := range s.rates {
		snapshot.RateBuckets = append(snapshot.RateBuckets, bucket)
	}
	sortSnapshot(&snapshot)
	return snapshot
}

func (s *Store) liveUnitsLocked() int {
	return len(s.decisions) + len(s.invocations) + len(s.rates)
}

func rateKey(ruleID string, minute time.Time) string {
	return ruleID + "\x00" + minute.UTC().Truncate(time.Minute).Format(time.RFC3339)
}
