package runs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	operationCheckpoint     = "checkpoint"
	operationAdmissionBatch = "admission-batch"
	operationTransition     = "lifecycle-transition"
	maxJournalBytes         = 64 << 20
)

var (
	ErrDuplicateAdmissionBatch = errors.New("runs: duplicate admission batch")
	ErrIdentityCollision       = errors.New("runs: durable identity collision")
)

type diskOperation struct {
	Kind             string           `json:"kind"`
	Version          int              `json:"version"`
	Sequence         uint64           `json:"sequence"`
	Schema           int              `json:"schema,omitempty"`
	Checkpoint       *Model           `json:"checkpoint,omitempty"`
	AdmissionBatches []AdmissionBatch `json:"admissionBatches,omitempty"`
	Runs             []Run            `json:"runs,omitempty"`
	RateIncrements   []RateBucket     `json:"rateIncrements,omitempty"`
	Transition       *Run             `json:"transition,omitempty"`
}

type checkpointWriter func(*storeLocation, diskOperation, bool, func(*os.File) error) (bool, error)
type operationApplier func(Model, diskOperation) (Snapshot, error)

type storeLocation struct {
	directory *os.Root
	name      string
}

// Store is a dormant projection journal. Its exported mutations accept only
// already-decided admission batches and already-constructed lifecycle
// projections; it performs no matching, routing, publication, or ownership
// arbitration.
type Store struct {
	mu                        sync.RWMutex
	location                  *storeLocation
	retention                 int
	state                     Snapshot
	operationsSinceCheckpoint int
	poisoned                  error
	write                     func(*os.File, []byte) (int, error)
	syncFile                  func(*os.File) error
	rollback                  func(*os.File, int64) error
	checkpoint                checkpointWriter
	apply                     operationApplier
}

// Create installs one initial checkpoint without replacing an existing
// artifact. Generation construction must supply a new disposable destination.
func Create(trustedRoot, path string, initial Snapshot, retention int) (*Store, error) {
	if err := validateStoreArguments(trustedRoot, path, retention); err != nil {
		return nil, err
	}
	if err := initial.Validate(); err != nil {
		return nil, fmt.Errorf("runs: invalid initial snapshot: %w", err)
	}
	location, err := openStoreLocation(trustedRoot, path, true)
	if err != nil {
		return nil, err
	}
	installed := false
	defer func() {
		if !installed {
			location.Close()
		}
	}()
	model := initial.Model()
	operation := diskOperation{
		Kind: operationCheckpoint, Version: JournalVersion, Sequence: model.JournalSequence,
		Schema: SchemaVersion, Checkpoint: &model,
	}
	replaced, err := writeCheckpoint(location, operation, true, func(directory *os.File) error { return directory.Sync() })
	if err != nil {
		return nil, err
	}
	if !replaced {
		return nil, errors.New("runs: create completed without installing artifact")
	}
	installed = true
	return newStore(location, retention, initial), nil
}

// Open strictly replays an existing canonical journal. It recovers only an
// incomplete final line; every complete malformed or semantically invalid
// operation fails the open.
func Open(trustedRoot, path string, retention int) (*Store, error) {
	if err := validateStoreArguments(trustedRoot, path, retention); err != nil {
		return nil, err
	}
	location, err := openStoreLocation(trustedRoot, path, false)
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			location.Close()
		}
	}()
	data, err := readJournal(location, true)
	if err != nil {
		return nil, err
	}
	state, operations, err := replay(data)
	if err != nil {
		return nil, err
	}
	store := newStore(location, retention, state)
	store.operationsSinceCheckpoint = operations
	succeeded = true
	return store, nil
}

func newStore(location *storeLocation, retention int, state Snapshot) *Store {
	store := &Store{location: location, retention: retention, state: state}
	store.write = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	store.syncFile = func(file *os.File) error { return file.Sync() }
	store.rollback = rollbackAppend
	store.checkpoint = writeCheckpoint
	store.apply = applyOperation
	return store
}

// Close releases the anchored directory handle. A closed Store refuses all
// later reads and mutations.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.location == nil {
		return nil
	}
	err := s.location.Close()
	s.location = nil
	return err
}

func (s *Store) Snapshot() (Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.healthyLocked(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{model: cloneModel(s.state.model)}, nil
}

// ApplyAdmissionBatch atomically appends already-decided batches, their new
// Runs, and matching rate increments. It deliberately does not decide what
// the batches contain.
func (s *Store) ApplyAdmissionBatch(batches []AdmissionBatch, runs []Run, increments []RateBucket) error {
	batches = cloneAdmissionBatches(batches)
	for index := range batches {
		canonicalizeAdmissionBatch(&batches[index])
	}
	slices.SortFunc(batches, compareAdmissionBatches)
	runs = cloneRuns(runs)
	for index := range runs {
		canonicalizeRun(&runs[index])
	}
	slices.SortFunc(runs, compareRuns)
	increments = slices.Clone(increments)
	for index := range increments {
		increments[index].Minute = increments[index].Minute.UTC().Truncate(time.Minute)
	}
	slices.SortFunc(increments, func(left, right RateBucket) int {
		if left.RuleID != right.RuleID {
			if left.RuleID < right.RuleID {
				return -1
			}
			return 1
		}
		return left.Minute.Compare(right.Minute)
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.healthyLocked(); err != nil {
		return err
	}
	sequence, err := nextSequence(s.state.model.JournalSequence)
	if err != nil {
		return err
	}
	operation := diskOperation{
		Kind: operationAdmissionBatch, Version: JournalVersion, Sequence: sequence,
		AdmissionBatches: batches, Runs: runs, RateIncrements: increments,
	}
	if err := validateOperationShape(operation); err != nil {
		return err
	}
	if _, err := applyOperation(s.state.model, operation); err != nil {
		return err
	}
	if err := s.appendOperationLocked(operation); err != nil {
		return err
	}
	next, err := s.apply(s.state.model, operation)
	if err != nil {
		s.poisoned = err
		return fmt.Errorf("runs: apply persisted admission batch: %w", err)
	}
	s.state = next
	s.operationsSinceCheckpoint++
	return s.compactIfNeededLocked()
}

// Transition appends one legal lifecycle transition as a complete next Run
// projection. Immutable admission and causation identity cannot change.
func (s *Store) Transition(next Run) error {
	next = cloneRun(next)
	canonicalizeRun(&next)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.healthyLocked(); err != nil {
		return err
	}
	sequence, err := nextSequence(s.state.model.JournalSequence)
	if err != nil {
		return err
	}
	operation := diskOperation{Kind: operationTransition, Version: JournalVersion, Sequence: sequence, Transition: &next}
	if _, err := applyOperation(s.state.model, operation); err != nil {
		return err
	}
	if err := s.appendOperationLocked(operation); err != nil {
		return err
	}
	state, err := s.apply(s.state.model, operation)
	if err != nil {
		s.poisoned = err
		return fmt.Errorf("runs: apply persisted lifecycle transition: %w", err)
	}
	s.state = state
	s.operationsSinceCheckpoint++
	return s.compactIfNeededLocked()
}

// Compact replaces the journal with one checkpoint, retaining every batch
// that owns a nonterminal Run and the newest configured number of remaining
// admission batches. Runs are retained and evicted with their owning batch.
// A nonzero rate cutoff also expires older rate buckets.
func (s *Store) Compact(expireRatesBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.healthyLocked(); err != nil {
		return err
	}
	return s.writeCheckpointLocked(expireRatesBefore)
}

func (s *Store) healthyLocked() error {
	if s.location == nil {
		return errors.New("runs: store is closed")
	}
	if s.poisoned != nil {
		return fmt.Errorf("runs: store is poisoned: %w", s.poisoned)
	}
	return nil
}

func applyOperation(current Model, operation diskOperation) (Snapshot, error) {
	if err := validateOperationShape(operation); err != nil {
		return Snapshot{}, err
	}
	if operation.Sequence != current.JournalSequence+1 {
		return Snapshot{}, fmt.Errorf("runs: journal sequence is %d, want %d", operation.Sequence, current.JournalSequence+1)
	}
	switch operation.Kind {
	case operationAdmissionBatch:
		return applyAdmissionBatch(current, operation)
	case operationTransition:
		return applyTransition(current, operation)
	default:
		return Snapshot{}, fmt.Errorf("runs: cannot apply operation %q", operation.Kind)
	}
}

func applyAdmissionBatch(current Model, operation diskOperation) (Snapshot, error) {
	matchedBatches := 0
	for _, batch := range operation.AdmissionBatches {
		for _, existing := range current.AdmissionBatches {
			if existing.ID == batch.ID {
				matchedBatches++
				existingRuns := runsForBatch(current.Runs, existing.ID)
				candidateRuns := runsForBatch(operation.Runs, batch.ID)
				if !reflect.DeepEqual(existing, batch) || !reflect.DeepEqual(existingRuns, candidateRuns) {
					return Snapshot{}, fmt.Errorf("%w: admission batch %q", ErrIdentityCollision, batch.ID)
				}
				continue
			}
			if existing.EventID == batch.EventID || batch.EventSequence != 0 && existing.EventSequence == batch.EventSequence {
				return Snapshot{}, fmt.Errorf("%w: event %q", ErrIdentityCollision, batch.EventID)
			}
		}
	}
	if matchedBatches > 0 {
		if matchedBatches != len(operation.AdmissionBatches) {
			return Snapshot{}, fmt.Errorf("%w: admission operation partially overlaps durable batches", ErrIdentityCollision)
		}
		if err := validateRateIncrements(operation.AdmissionBatches, operation.RateIncrements); err != nil {
			return Snapshot{}, fmt.Errorf("%w: persisted admission operation rates differ", ErrIdentityCollision)
		}
		if err := validateAdmissionOperationProjection(operation); err != nil {
			return Snapshot{}, fmt.Errorf("%w: persisted admission operation projection differs", ErrIdentityCollision)
		}
		return Snapshot{}, ErrDuplicateAdmissionBatch
	}
	for _, candidate := range operation.Runs {
		for _, existing := range current.Runs {
			if existing.ID == candidate.ID || existing.Causation.AdmissionID == candidate.Causation.AdmissionID {
				return Snapshot{}, fmt.Errorf("%w: Run %q", ErrIdentityCollision, candidate.ID)
			}
		}
	}
	if err := validateRateIncrements(operation.AdmissionBatches, operation.RateIncrements); err != nil {
		return Snapshot{}, err
	}
	if err := validateAdmissionOperationProjection(operation); err != nil {
		return Snapshot{}, err
	}
	if uint64(len(operation.AdmissionBatches)) > math.MaxUint64-current.TotalBatches || uint64(len(operation.Runs)) > math.MaxUint64-current.TotalRuns {
		return Snapshot{}, errors.New("runs: lifetime totals exhausted")
	}
	next := cloneModel(current)
	next.JournalSequence = operation.Sequence
	next.TotalBatches += uint64(len(operation.AdmissionBatches))
	next.TotalRuns += uint64(len(operation.Runs))
	next.AdmissionBatches = append(next.AdmissionBatches, cloneAdmissionBatches(operation.AdmissionBatches)...)
	next.Runs = append(next.Runs, cloneRuns(operation.Runs)...)
	rates := make(map[string]RateBucket, len(next.RateBuckets)+len(operation.RateIncrements))
	for _, bucket := range next.RateBuckets {
		rates[rateKey(bucket.RuleID, bucket.Minute)] = bucket
	}
	for _, increment := range operation.RateIncrements {
		key := rateKey(increment.RuleID, increment.Minute)
		bucket := rates[key]
		bucket.RuleID = increment.RuleID
		bucket.Minute = increment.Minute.UTC().Truncate(time.Minute)
		if increment.Count > math.MaxInt-bucket.Count {
			return Snapshot{}, errors.New("runs: rate bucket count exhausted")
		}
		bucket.Count += increment.Count
		rates[key] = bucket
	}
	next.RateBuckets = next.RateBuckets[:0]
	for _, bucket := range rates {
		next.RateBuckets = append(next.RateBuckets, bucket)
	}
	return NewSnapshot(next)
}

func validateAdmissionOperationProjection(operation diskOperation) error {
	candidate := Model{
		Schema: SchemaVersion, TotalBatches: uint64(len(operation.AdmissionBatches)), TotalRuns: uint64(len(operation.Runs)),
		AdmissionBatches: cloneAdmissionBatches(operation.AdmissionBatches), Runs: cloneRuns(operation.Runs),
		RateBuckets: slices.Clone(operation.RateIncrements),
	}
	if _, err := NewSnapshot(candidate); err != nil {
		return fmt.Errorf("runs: invalid admission operation projection: %w", err)
	}
	return nil
}

func runsForBatch(runs []Run, batchID string) []Run {
	result := make([]Run, 0)
	for _, run := range runs {
		if run.Causation.BatchID == batchID {
			result = append(result, run)
		}
	}
	return result
}

func validateRateIncrements(batches []AdmissionBatch, increments []RateBucket) error {
	wanted := make(map[string]int)
	var minute time.Time
	for _, batch := range batches {
		batchMinute := batch.DecidedAt.UTC().Truncate(time.Minute)
		if minute.IsZero() {
			minute = batchMinute
		} else if minute != batchMinute {
			return errors.New("runs: atomic admission batches must share one rate minute")
		}
		for _, outcome := range batch.Outcomes {
			if outcome.Kind == AdmissionOutcomeRun {
				wanted[outcome.RuleID]++
			}
		}
	}
	got := make(map[string]int, len(increments))
	for _, increment := range increments {
		if !ruleIDPattern.MatchString(increment.RuleID) || increment.Minute.IsZero() || increment.Minute != increment.Minute.UTC().Truncate(time.Minute) ||
			increment.Minute != minute || increment.Count < 1 || got[increment.RuleID] != 0 {
			return errors.New("runs: invalid rate increment")
		}
		got[increment.RuleID] = increment.Count
	}
	if !reflect.DeepEqual(wanted, got) {
		return errors.New("runs: rate increments do not match runnable admissions")
	}
	return nil
}

func applyTransition(current Model, operation diskOperation) (Snapshot, error) {
	nextRun := cloneRun(*operation.Transition)
	index := -1
	var currentRun Run
	for candidateIndex, run := range current.Runs {
		if run.ID == nextRun.ID {
			index = candidateIndex
			currentRun = run
			break
		}
	}
	if index < 0 {
		return Snapshot{}, fmt.Errorf("runs: Run %q not found", nextRun.ID)
	}
	if currentRun.ID != nextRun.ID || !reflect.DeepEqual(currentRun.Causation, nextRun.Causation) ||
		!reflect.DeepEqual(currentRun.MigratedBaseline, nextRun.MigratedBaseline) || currentRun.CreatedAt != nextRun.CreatedAt {
		return Snapshot{}, errors.New("runs: lifecycle transition changed immutable admission identity")
	}
	if len(nextRun.Transitions) != len(currentRun.Transitions)+1 || !slices.Equal(nextRun.Transitions[:len(currentRun.Transitions)], currentRun.Transitions) {
		return Snapshot{}, errors.New("runs: lifecycle transition must append exactly one history record")
	}
	transition := nextRun.Transitions[len(nextRun.Transitions)-1]
	if !legalTransition(currentRun.State, nextRun.State) || transition.State != nextRun.State {
		return Snapshot{}, fmt.Errorf("runs: illegal lifecycle transition %s -> %s", currentRun.State, nextRun.State)
	}
	if !nextRun.UpdatedAt.After(currentRun.UpdatedAt) || transition.At != nextRun.UpdatedAt {
		return Snapshot{}, errors.New("runs: lifecycle transition time must advance UpdatedAt")
	}
	if err := validateTransitionDelta(currentRun, nextRun, transition); err != nil {
		return Snapshot{}, err
	}
	if currentRun.Repository != nil && !reflect.DeepEqual(currentRun.Repository, nextRun.Repository) {
		return Snapshot{}, errors.New("runs: lifecycle transition changed immutable repository route")
	}
	if currentRun.Repository == nil && nextRun.Repository != nil && (currentRun.State != StateRouting || nextRun.State != StatePending) {
		return Snapshot{}, errors.New("runs: repository route can only resolve while routing")
	}
	next := cloneModel(current)
	next.JournalSequence = operation.Sequence
	next.Runs[index] = nextRun
	return NewSnapshot(next)
}

func validateTransitionDelta(current, next Run, transition LifecycleTransition) error {
	if !stringsSubset(current.DeliveryIDs, next.DeliveryIDs) || next.DuplicateDeliveries < current.DuplicateDeliveries {
		return errors.New("runs: lifecycle transition rewrote delivery evidence")
	}
	if next.Attempts < current.Attempts || transition.Attempts != next.Attempts || next.ResumeCount < current.ResumeCount ||
		next.GitHub.LastCursor < current.GitHub.LastCursor {
		return errors.New("runs: lifecycle transition decreased durable counters")
	}
	if current.StartedAt != nil && !equalOptionalTime(current.StartedAt, next.StartedAt) ||
		!monotonicOptionalTime(current.GitHub.LastAuthoritativeRefreshAt, next.GitHub.LastAuthoritativeRefreshAt) {
		return errors.New("runs: lifecycle transition rewrote durable timestamps")
	}
	if current.StartedAt == nil && next.StartedAt != nil && (next.State != StateRunning || !next.StartedAt.Equal(next.UpdatedAt)) {
		return errors.New("runs: lifecycle transition introduced an invalid start timestamp")
	}
	if current.RunDirectory != "" && next.RunDirectory != current.RunDirectory ||
		current.RunDirectory == "" && next.RunDirectory != "" && next.State != StateStarting {
		return errors.New("runs: lifecycle transition rewrote Run directory identity")
	}
	if current.SessionName != next.SessionName {
		setting := current.SessionName == "" && next.SessionName != "" && next.State == StateStarting
		clearing := current.SessionName != "" && next.SessionName == "" && (next.State == StatePending || next.State == StatePostMergePending)
		if !setting && !clearing {
			return errors.New("runs: lifecycle transition rewrote session identity")
		}
	}
	if !equalOptionalTime(current.SegmentStartedAt, next.SegmentStartedAt) {
		setting := next.SegmentStartedAt != nil && next.State == StateStarting
		clearing := current.SegmentStartedAt != nil && next.SegmentStartedAt == nil && next.State == StatePostMergePending
		if !setting && !clearing {
			return errors.New("runs: lifecycle transition rewrote segment identity")
		}
	}
	if current.SegmentAttempt != next.SegmentAttempt && (next.State != StateStarting || next.SegmentAttempt < current.SegmentAttempt) {
		return errors.New("runs: lifecycle transition rewrote segment attempt identity")
	}
	if current.Ready != nil && !reflect.DeepEqual(current.Ready, next.Ready) ||
		current.Ready == nil && next.Ready != nil && next.State != StateAwaitingHumanMerge {
		return errors.New("runs: lifecycle transition rewrote ready checkpoint")
	}
	if current.MergeCommitOID != "" && next.MergeCommitOID != current.MergeCommitOID {
		return errors.New("runs: lifecycle transition rewrote merge identity")
	}
	if current.MergeCommitOID == "" && next.MergeCommitOID != "" {
		postMergeResume := current.State == StateAwaitingHumanMerge && next.State == StatePostMergePending
		acceptedTerminal := next.State.Terminal() && next.Completion != nil && next.Completion.Accepted && next.Completion.MergeCommitOID == next.MergeCommitOID
		if !postMergeResume && !acceptedTerminal {
			return errors.New("runs: lifecycle transition introduced merge identity outside post-merge evidence")
		}
	}
	if current.FinishedAt != nil && !equalOptionalTime(current.FinishedAt, next.FinishedAt) {
		return errors.New("runs: lifecycle transition rewrote terminal timestamp")
	}
	if current.FinishedAt == nil && next.FinishedAt != nil && !next.FinishedAt.Equal(next.UpdatedAt) {
		return errors.New("runs: lifecycle transition finish timestamp must match transition time")
	}
	if current.Completion != nil && current.Completion.Accepted && !reflect.DeepEqual(current.Completion, next.Completion) {
		return errors.New("runs: lifecycle transition rewrote accepted completion")
	}
	return nil
}

func stringsSubset(current, next []string) bool {
	index := 0
	for _, value := range next {
		if index < len(current) && current[index] == value {
			index++
		}
	}
	return index == len(current)
}

func monotonicOptionalTime(current, next *time.Time) bool {
	if current == nil {
		return true
	}
	return next != nil && !next.Before(*current)
}

func equalOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func validateOperationShape(operation diskOperation) error {
	if operation.Version != JournalVersion {
		return fmt.Errorf("runs: journal version is %d, want %d", operation.Version, JournalVersion)
	}
	switch operation.Kind {
	case operationCheckpoint:
		if operation.Schema != SchemaVersion || operation.Checkpoint == nil || len(operation.AdmissionBatches) != 0 || operation.Transition != nil || len(operation.Runs) != 0 || len(operation.RateIncrements) != 0 ||
			operation.Checkpoint.Schema != operation.Schema || operation.Checkpoint.JournalSequence != operation.Sequence {
			return errors.New("runs: invalid checkpoint operation")
		}
	case operationAdmissionBatch:
		if operation.Sequence == 0 || operation.Schema != 0 || operation.Checkpoint != nil || len(operation.AdmissionBatches) == 0 || operation.Transition != nil {
			return errors.New("runs: invalid admission-batch operation")
		}
	case operationTransition:
		if operation.Sequence == 0 || operation.Schema != 0 || operation.Checkpoint != nil || len(operation.AdmissionBatches) != 0 || operation.Transition == nil || len(operation.Runs) != 0 || len(operation.RateIncrements) != 0 {
			return errors.New("runs: invalid lifecycle-transition operation")
		}
	default:
		return fmt.Errorf("runs: unknown journal operation %q", operation.Kind)
	}
	return nil
}

func replay(data []byte) (Snapshot, int, error) {
	lines := bytes.Split(data, []byte{'\n'})
	foundCheckpoint := false
	operations := 0
	var state Snapshot
	for _, raw := range lines {
		if len(raw) == 0 {
			continue
		}
		operation, err := decodeOperation(raw)
		if err != nil {
			return Snapshot{}, 0, err
		}
		if !foundCheckpoint {
			if operation.Kind != operationCheckpoint {
				return Snapshot{}, 0, errors.New("runs: operation precedes checkpoint")
			}
			if err := validateOperationShape(operation); err != nil {
				return Snapshot{}, 0, err
			}
			original := cloneModel(*operation.Checkpoint)
			canonical := cloneModel(original)
			canonicalizeModel(&canonical)
			if !reflect.DeepEqual(original, canonical) {
				return Snapshot{}, 0, errors.New("runs: checkpoint projection is not canonical")
			}
			state, err = NewSnapshot(original)
			if err != nil {
				return Snapshot{}, 0, fmt.Errorf("runs: invalid checkpoint projection: %w", err)
			}
			foundCheckpoint = true
			continue
		}
		if operation.Kind == operationCheckpoint {
			return Snapshot{}, 0, errors.New("runs: duplicate checkpoint")
		}
		canonical := canonicalOperation(operation)
		if !reflect.DeepEqual(operation, canonical) {
			return Snapshot{}, 0, fmt.Errorf("runs: journal operation %d is not canonical", operation.Sequence)
		}
		state, err = applyOperation(state.model, operation)
		if err != nil {
			return Snapshot{}, 0, fmt.Errorf("runs: replay operation %d: %w", operation.Sequence, err)
		}
		operations++
	}
	if !foundCheckpoint {
		return Snapshot{}, 0, errors.New("runs: journal checkpoint is missing")
	}
	return state, operations, nil
}

func decodeOperation(data []byte) (diskOperation, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var operation diskOperation
	if err := decoder.Decode(&operation); err != nil {
		return diskOperation{}, fmt.Errorf("runs: decode journal operation: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return diskOperation{}, errors.New("runs: decode journal operation: trailing content")
	}
	return operation, nil
}

func canonicalOperation(operation diskOperation) diskOperation {
	canonical := operation
	if operation.AdmissionBatches != nil {
		canonical.AdmissionBatches = cloneAdmissionBatches(operation.AdmissionBatches)
		for index := range canonical.AdmissionBatches {
			canonicalizeAdmissionBatch(&canonical.AdmissionBatches[index])
		}
		slices.SortFunc(canonical.AdmissionBatches, compareAdmissionBatches)
	}
	if operation.Runs != nil {
		canonical.Runs = cloneRuns(operation.Runs)
		for index := range canonical.Runs {
			canonicalizeRun(&canonical.Runs[index])
		}
		slices.SortFunc(canonical.Runs, compareRuns)
	}
	if operation.RateIncrements != nil {
		canonical.RateIncrements = slices.Clone(operation.RateIncrements)
		for index := range canonical.RateIncrements {
			canonical.RateIncrements[index].Minute = canonical.RateIncrements[index].Minute.UTC().Truncate(time.Minute)
		}
		slices.SortFunc(canonical.RateIncrements, func(left, right RateBucket) int {
			if left.RuleID != right.RuleID {
				if left.RuleID < right.RuleID {
					return -1
				}
				return 1
			}
			return left.Minute.Compare(right.Minute)
		})
	}
	if operation.Transition != nil {
		transition := cloneRun(*operation.Transition)
		canonicalizeRun(&transition)
		canonical.Transition = &transition
	}
	return canonical
}

func compareAdmissionBatches(left, right AdmissionBatch) int {
	if comparison := left.DecidedAt.Compare(right.DecidedAt); comparison != 0 {
		return comparison
	}
	if left.ID < right.ID {
		return -1
	}
	if left.ID > right.ID {
		return 1
	}
	return 0
}

func compareRuns(left, right Run) int {
	if comparison := left.CreatedAt.Compare(right.CreatedAt); comparison != 0 {
		return comparison
	}
	if left.ID < right.ID {
		return -1
	}
	if left.ID > right.ID {
		return 1
	}
	return 0
}

func (s *Store) appendOperationLocked(operation diskOperation) error {
	data, err := json.Marshal(operation)
	if err != nil {
		return fmt.Errorf("runs: encode journal operation: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxJournalBytes {
		return errors.New("runs: journal operation is too large")
	}
	info, file, err := openArtifact(s.location, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("runs: journal changed while opening for append")
	}
	if info.Size() > int64(maxJournalBytes-len(data)) {
		return errors.New("runs: journal is too large to append")
	}
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("runs: seek journal append: %w", err)
	}
	written, writeErr := s.write(file, data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = s.syncFile(file)
	}
	if writeErr == nil {
		return nil
	}
	if rollbackErr := s.rollback(file, offset); rollbackErr != nil {
		s.poisoned = errors.Join(writeErr, rollbackErr)
		return fmt.Errorf("runs: append failed and rollback failed: %w", s.poisoned)
	}
	return fmt.Errorf("runs: append: %w", writeErr)
}

func rollbackAppend(file *os.File, offset int64) error {
	if err := file.Truncate(offset); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Store) compactIfNeededLocked() error {
	live := len(s.state.model.AdmissionBatches) + len(s.state.model.Runs) + len(s.state.model.RateBuckets)
	if s.operationsSinceCheckpoint <= max(1, live) {
		return nil
	}
	return s.writeCheckpointLocked(time.Time{})
}

func (s *Store) writeCheckpointLocked(expireRatesBefore time.Time) error {
	next, err := retainedSnapshot(s.state.model, s.retention, expireRatesBefore)
	if err != nil {
		return err
	}
	model := next.Model()
	operation := diskOperation{
		Kind: operationCheckpoint, Version: JournalVersion, Sequence: model.JournalSequence,
		Schema: SchemaVersion, Checkpoint: &model,
	}
	replaced, writeErr := s.checkpoint(s.location, operation, false, func(directory *os.File) error { return directory.Sync() })
	if replaced {
		s.state = next
		s.operationsSinceCheckpoint = 0
	}
	if writeErr != nil {
		if replaced {
			s.poisoned = writeErr
		}
		return writeErr
	}
	if !replaced {
		return errors.New("runs: checkpoint completed without replacing journal")
	}
	return nil
}

func retainedSnapshot(current Model, retention int, expireRatesBefore time.Time) (Snapshot, error) {
	nonterminalBatch := make(map[string]bool)
	for _, run := range current.Runs {
		if run.State.Nonterminal() {
			nonterminalBatch[run.Causation.BatchID] = true
		}
	}
	keep := make(map[string]bool)
	remaining := make([]string, 0, len(current.AdmissionBatches))
	for _, batch := range current.AdmissionBatches {
		if nonterminalBatch[batch.ID] {
			keep[batch.ID] = true
		} else {
			remaining = append(remaining, batch.ID)
		}
	}
	start := max(0, len(remaining)-retention)
	for _, batchID := range remaining[start:] {
		keep[batchID] = true
	}

	next := cloneModel(current)
	next.AdmissionBatches = next.AdmissionBatches[:0]
	for _, batch := range current.AdmissionBatches {
		if keep[batch.ID] {
			next.AdmissionBatches = append(next.AdmissionBatches, batch)
		}
	}
	next.Runs = next.Runs[:0]
	for _, run := range current.Runs {
		if keep[run.Causation.BatchID] {
			next.Runs = append(next.Runs, run)
		}
	}
	if !expireRatesBefore.IsZero() {
		cutoff := expireRatesBefore.UTC().Truncate(time.Minute)
		next.RateBuckets = slices.DeleteFunc(next.RateBuckets, func(bucket RateBucket) bool {
			return bucket.Minute.Before(cutoff)
		})
	}
	return NewSnapshot(next)
}

func writeCheckpoint(location *storeLocation, operation diskOperation, createNoReplace bool, syncDirectory func(*os.File) error) (bool, error) {
	if err := validateOperationShape(operation); err != nil {
		return false, err
	}
	data, err := json.Marshal(operation)
	if err != nil {
		return false, fmt.Errorf("runs: encode checkpoint: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxJournalBytes {
		return false, errors.New("runs: checkpoint is too large")
	}
	if !createNoReplace {
		if _, err := inspectArtifact(location); err != nil {
			return false, err
		}
	}
	temporaryName, temporary, err := createTemporaryArtifact(location)
	if err != nil {
		return false, fmt.Errorf("runs: create checkpoint: %w", err)
	}
	defer location.directory.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return false, fmt.Errorf("runs: set checkpoint permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return false, fmt.Errorf("runs: write checkpoint: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, fmt.Errorf("runs: sync checkpoint: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("runs: close checkpoint: %w", err)
	}
	reserved := false
	if createNoReplace {
		reservation, err := location.directory.OpenFile(location.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return false, errors.New("runs: create: artifact already exists")
			}
			return false, fmt.Errorf("runs: reserve artifact: %w", err)
		}
		reserved = true
		if err := reservation.Close(); err != nil {
			location.directory.Remove(location.name)
			return false, fmt.Errorf("runs: close artifact reservation: %w", err)
		}
	}
	defer func() {
		if reserved {
			location.directory.Remove(location.name)
		}
	}()
	if err := location.directory.Rename(temporaryName, location.name); err != nil {
		return false, fmt.Errorf("runs: replace checkpoint: %w", err)
	}
	reserved = false
	directory, err := location.directory.Open(".")
	if err != nil {
		return true, fmt.Errorf("runs: open checkpoint directory: %w", err)
	}
	defer directory.Close()
	if err := syncDirectory(directory); err != nil {
		return true, fmt.Errorf("runs: sync checkpoint directory: %w", err)
	}
	return true, nil
}

func createTemporaryArtifact(location *storeLocation) (string, *os.File, error) {
	for attempt := 0; attempt < 100; attempt++ {
		var entropy [12]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return "", nil, err
		}
		name := ".runs-" + hex.EncodeToString(entropy[:])
		file, err := location.directory.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return name, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, errors.New("temporary checkpoint name collision")
}

func readJournal(location *storeLocation, recoverTail bool) ([]byte, error) {
	info, file, err := openArtifact(location, os.O_RDWR)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("runs: journal changed while opening")
	}
	if info.Size() > maxJournalBytes {
		return nil, errors.New("runs: journal is too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	if err != nil {
		return nil, fmt.Errorf("runs: read journal: %w", err)
	}
	if len(data) > maxJournalBytes {
		return nil, errors.New("runs: journal is too large")
	}
	complete := len(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if index := bytes.LastIndexByte(data, '\n'); index >= 0 {
			complete = index + 1
		} else {
			complete = 0
		}
		if recoverTail {
			if err := file.Truncate(int64(complete)); err != nil {
				return nil, fmt.Errorf("runs: truncate incomplete journal tail: %w", err)
			}
			if err := file.Sync(); err != nil {
				return nil, fmt.Errorf("runs: sync recovered journal tail: %w", err)
			}
		}
	}
	return data[:complete], nil
}

func openArtifact(location *storeLocation, flags int) (os.FileInfo, *os.File, error) {
	info, err := inspectArtifact(location)
	if err != nil {
		return nil, nil, err
	}
	file, err := location.directory.OpenFile(location.name, flags, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("runs: open journal: %w", err)
	}
	return info, file, nil
}

func inspectArtifact(location *storeLocation) (os.FileInfo, error) {
	info, err := location.directory.Lstat(location.name)
	if err != nil {
		return nil, fmt.Errorf("runs: inspect journal: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("runs: journal must be a regular nonsymlink file")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("runs: journal permissions are %04o, want 0600", info.Mode().Perm())
	}
	return info, nil
}

func validateStoreArguments(trustedRoot, path string, retention int) error {
	if trustedRoot == "" || !filepath.IsAbs(trustedRoot) || filepath.Clean(trustedRoot) != trustedRoot {
		return errors.New("runs: trusted root must be canonical and absolute")
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
		return errors.New("runs: journal path must be canonical and absolute")
	}
	if !pathWithin(trustedRoot, path) || trustedRoot == path {
		return errors.New("runs: journal path must remain within the trusted root")
	}
	if retention < 1 {
		return errors.New("runs: retention must be positive")
	}
	return nil
}

func openStoreLocation(trustedRoot, path string, createDirectories bool) (*storeLocation, error) {
	info, err := os.Lstat(trustedRoot)
	if err != nil {
		return nil, fmt.Errorf("runs: inspect trusted root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("runs: trusted root must be a private nonsymlink directory")
	}
	root, err := os.OpenRoot(trustedRoot)
	if err != nil {
		return nil, fmt.Errorf("runs: open trusted root: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("runs: trusted root changed while opening")
	}
	relative, err := filepath.Rel(trustedRoot, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		root.Close()
		return nil, errors.New("runs: journal path escaped trusted root")
	}
	directoryPath := filepath.Dir(relative)
	current := root
	if directoryPath != "." {
		for _, component := range strings.Split(directoryPath, string(filepath.Separator)) {
			next, openErr := current.OpenRoot(component)
			if errors.Is(openErr, os.ErrNotExist) && createDirectories {
				if err := current.Mkdir(component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
					current.Close()
					return nil, fmt.Errorf("runs: create journal directory: %w", err)
				}
				next, openErr = current.OpenRoot(component)
			}
			if openErr != nil {
				current.Close()
				return nil, fmt.Errorf("runs: open nonsymlink journal directory: %w", openErr)
			}
			directoryInfo, statErr := next.Stat(".")
			if statErr != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0o700 {
				next.Close()
				current.Close()
				return nil, errors.New("runs: journal directory must be private and nonsymlinked")
			}
			current.Close()
			current = next
		}
	}
	return &storeLocation{directory: current, name: filepath.Base(path)}, nil
}

func (l *storeLocation) Close() error {
	if l == nil || l.directory == nil {
		return nil
	}
	err := l.directory.Close()
	l.directory = nil
	return err
}

func nextSequence(current uint64) (uint64, error) {
	if current == math.MaxUint64 {
		return 0, errors.New("runs: journal sequence exhausted")
	}
	return current + 1, nil
}
