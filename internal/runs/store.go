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
	operationPublish        = "delivery-publish"
	operationAcknowledge    = "delivery-acknowledge"
	maxJournalBytes         = 64 << 20
)

var (
	ErrDuplicateAdmissionBatch = errors.New("runs: duplicate admission batch")
	ErrIdentityCollision       = errors.New("runs: durable identity collision")
)

type diskOperation struct {
	Kind             string                   `json:"kind"`
	Version          int                      `json:"version"`
	Sequence         uint64                   `json:"sequence"`
	Schema           int                      `json:"schema,omitempty"`
	Checkpoint       *Model                   `json:"checkpoint,omitempty"`
	AdmissionBatches []AdmissionBatch         `json:"admissionBatches,omitempty"`
	Runs             []Run                    `json:"runs,omitempty"`
	RateIncrements   []RateBucket             `json:"rateIncrements,omitempty"`
	Transition       *Run                     `json:"transition,omitempty"`
	Publication      *DeliveryPublication     `json:"publication,omitempty"`
	Acknowledgement  *DeliveryAcknowledgement `json:"acknowledgement,omitempty"`
}

// DeliveryPublication records that one pending Run transition delivery was
// published to the wire with the returned authoritative positive sequence.
type DeliveryPublication struct {
	RunID        string `json:"runId"`
	TransitionID string `json:"transitionId"`
	Sequence     uint64 `json:"sequence"`
}

// DeliveryAcknowledgement advances a Run's DeliveredThrough watermark over a
// contiguous published prefix of its unacknowledged suffix.
type DeliveryAcknowledgement struct {
	RunID string `json:"runId"`
	Count int    `json:"count"`
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
	if len(runs) == 0 {
		runs = nil
	}
	increments = slices.Clone(increments)
	for index := range increments {
		increments[index].Minute = increments[index].Minute.UTC().Truncate(time.Minute)
	}
	slices.SortFunc(increments, compareRateBuckets)
	if len(increments) == 0 {
		increments = nil
	}

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
	// Delivery state is store-derived, so a transition delta never carries it.
	// Stripping here keeps the on-disk operation uniform and canonical while
	// applyTransition re-derives the durable delivery from the current Run.
	next.DeliveredThrough = 0
	next.TransitionDeliveries = nil
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

// RecordPublication marks one pending Run transition delivery published with
// the authoritative wire sequence returned by the event wire. It never
// rewrites an already-published delivery; the store derives delivery state so
// no caller can forge a sequence into an existing entry.
func (s *Store) RecordPublication(runID, transitionID string, sequence uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.healthyLocked(); err != nil {
		return err
	}
	next, err := nextSequence(s.state.model.JournalSequence)
	if err != nil {
		return err
	}
	operation := diskOperation{
		Kind: operationPublish, Version: JournalVersion, Sequence: next,
		Publication: &DeliveryPublication{RunID: runID, TransitionID: transitionID, Sequence: sequence},
	}
	if _, err := applyOperation(s.state.model, operation); err != nil {
		return err
	}
	if err := s.appendOperationLocked(operation); err != nil {
		return err
	}
	state, err := s.apply(s.state.model, operation)
	if err != nil {
		s.poisoned = err
		return fmt.Errorf("runs: apply persisted delivery publication: %w", err)
	}
	s.state = state
	s.operationsSinceCheckpoint++
	return s.compactIfNeededLocked()
}

// AcknowledgeDeliveries advances a Run's DeliveredThrough watermark over the
// leading count of its unacknowledged suffix. Every acknowledged delivery must
// already be published; the store derives the watermark so no caller can
// acknowledge a pending delivery.
func (s *Store) AcknowledgeDeliveries(runID string, count int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.healthyLocked(); err != nil {
		return err
	}
	next, err := nextSequence(s.state.model.JournalSequence)
	if err != nil {
		return err
	}
	operation := diskOperation{
		Kind: operationAcknowledge, Version: JournalVersion, Sequence: next,
		Acknowledgement: &DeliveryAcknowledgement{RunID: runID, Count: count},
	}
	if _, err := applyOperation(s.state.model, operation); err != nil {
		return err
	}
	if err := s.appendOperationLocked(operation); err != nil {
		return err
	}
	state, err := s.apply(s.state.model, operation)
	if err != nil {
		s.poisoned = err
		return fmt.Errorf("runs: apply persisted delivery acknowledgement: %w", err)
	}
	s.state = state
	s.operationsSinceCheckpoint++
	return s.compactIfNeededLocked()
}

// Compact replaces the journal with one checkpoint, retaining every batch
// that owns a nonterminal Run or a Run with any unacknowledged transition
// delivery, plus the newest configured number of remaining admission batches.
// Runs are retained and evicted with their owning batch. The surviving Run's
// DeliveredThrough watermark remains the proof for evicted acknowledged
// deliveries. A nonzero rate cutoff also expires older rate buckets.
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
	case operationPublish:
		return applyPublish(current, operation)
	case operationAcknowledge:
		return applyAcknowledge(current, operation)
	default:
		return Snapshot{}, fmt.Errorf("runs: cannot apply operation %q", operation.Kind)
	}
}

func applyAdmissionBatch(current Model, operation diskOperation) (Snapshot, error) {
	receipt := receiptForAdmissionOperation(operation)
	if err := classifyAdmissionOperation(current.Migration, current.AdmissionOperations, receipt); err != nil {
		return Snapshot{}, err
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
	next.AdmissionOperations = append(next.AdmissionOperations, receipt)
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

func receiptForAdmissionOperation(operation diskOperation) AdmissionOperationReceipt {
	return AdmissionOperationReceipt{
		AdmissionBatches: cloneAdmissionBatches(operation.AdmissionBatches),
		Runs:             cloneRuns(operation.Runs),
		RateIncrements:   slices.Clone(operation.RateIncrements),
	}
}

func classifyAdmissionOperation(migration *MigrationSnapshotReceipt, existing []AdmissionOperationReceipt, candidate AdmissionOperationReceipt) error {
	if migrationSnapshotOverlapsAdmissionOperation(migration, candidate) {
		return fmt.Errorf("%w: admission operation overlaps migration snapshot evidence", ErrIdentityCollision)
	}
	overlaps := false
	for _, receipt := range existing {
		if !admissionOperationsOverlap(receipt, candidate) {
			continue
		}
		overlaps = true
		if reflect.DeepEqual(receipt, candidate) {
			return ErrDuplicateAdmissionBatch
		}
	}
	if overlaps {
		return fmt.Errorf("%w: admission operation overlaps a durable receipt", ErrIdentityCollision)
	}
	return nil
}

func migrationSnapshotOverlapsAdmissionOperation(migration *MigrationSnapshotReceipt, candidate AdmissionOperationReceipt) bool {
	if migration == nil {
		return false
	}
	for _, batch := range candidate.AdmissionBatches {
		_, sameBatch := slices.BinarySearch(migration.BatchIDs, batch.ID)
		_, sameEvent := slices.BinarySearch(migration.EventIDs, batch.EventID)
		_, sameSequence := slices.BinarySearch(migration.EventSequences, batch.EventSequence)
		if sameBatch || sameEvent || batch.EventSequence != 0 && sameSequence {
			return true
		}
	}
	for _, run := range candidate.Runs {
		_, sameRun := slices.BinarySearch(migration.RunIDs, run.ID)
		_, sameAdmission := slices.BinarySearch(migration.AdmissionIDs, run.Causation.AdmissionID)
		if sameRun || sameAdmission {
			return true
		}
	}
	return false
}

func admissionOperationsOverlap(left, right AdmissionOperationReceipt) bool {
	batchIDs := make(map[string]struct{}, len(left.AdmissionBatches))
	eventIDs := make(map[string]struct{}, len(left.AdmissionBatches))
	eventSequences := make(map[uint64]struct{}, len(left.AdmissionBatches))
	for _, batch := range left.AdmissionBatches {
		batchIDs[batch.ID] = struct{}{}
		eventIDs[batch.EventID] = struct{}{}
		if batch.EventSequence != 0 {
			eventSequences[batch.EventSequence] = struct{}{}
		}
	}
	for _, batch := range right.AdmissionBatches {
		_, sameBatch := batchIDs[batch.ID]
		_, sameEvent := eventIDs[batch.EventID]
		_, sameSequence := eventSequences[batch.EventSequence]
		if sameBatch || sameEvent || batch.EventSequence != 0 && sameSequence {
			return true
		}
	}
	runIDs := make(map[string]struct{}, len(left.Runs))
	admissionIDs := make(map[string]struct{}, len(left.Runs))
	for _, run := range left.Runs {
		runIDs[run.ID] = struct{}{}
		admissionIDs[run.Causation.AdmissionID] = struct{}{}
	}
	for _, run := range right.Runs {
		_, sameRun := runIDs[run.ID]
		_, sameAdmission := admissionIDs[run.Causation.AdmissionID]
		if sameRun || sameAdmission {
			return true
		}
	}
	return false
}

func validateAdmissionOperationProjection(operation diskOperation) error {
	if err := validateRateIncrements(operation.AdmissionBatches, operation.RateIncrements); err != nil {
		return fmt.Errorf("runs: invalid admission operation projection: %w", err)
	}
	candidate := Model{
		Schema: SchemaVersion, TotalBatches: uint64(len(operation.AdmissionBatches)), TotalRuns: uint64(len(operation.Runs)),
		AdmissionBatches: cloneAdmissionBatches(operation.AdmissionBatches), Runs: cloneRuns(operation.Runs),
		RateBuckets: slices.Clone(operation.RateIncrements),
	}
	if !canonicalBatchOrder(candidate.AdmissionBatches) || !canonicalRunOrder(candidate.Runs) || !canonicalRateOrder(candidate.RateBuckets) {
		return errors.New("runs: invalid admission operation projection: ordering is not canonical")
	}
	if err := validateRetainedProjection(candidate, migrationIdentityEvidence{}); err != nil {
		return fmt.Errorf("runs: invalid admission operation projection: %w", err)
	}
	return nil
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
	// The store owns delivery state: it derives the watermark and unacknowledged
	// suffix from the durable current Run and appends exactly one pending
	// delivery for the new transition. Whatever a caller supplied for these
	// fields is discarded, so no lifecycle transition can rewrite an existing
	// delivery prefix, forge a published sequence, or move the watermark.
	nextRun.DeliveredThrough = currentRun.DeliveredThrough
	nextRun.TransitionDeliveries = append(slices.Clone(currentRun.TransitionDeliveries), TransitionDelivery{
		TransitionID: transition.ID, EventID: RunTransitionEventID(transition.ID), State: DeliveryPending,
	})
	next := cloneModel(current)
	next.JournalSequence = operation.Sequence
	next.Runs[index] = nextRun
	return NewSnapshot(next)
}

func applyPublish(current Model, operation diskOperation) (Snapshot, error) {
	publication := *operation.Publication
	index := -1
	for candidate, run := range current.Runs {
		if run.ID == publication.RunID {
			index = candidate
			break
		}
	}
	if index < 0 {
		return Snapshot{}, fmt.Errorf("runs: Run %q not found", publication.RunID)
	}
	run := cloneRun(current.Runs[index])
	deliveryIndex := -1
	for candidate := range run.TransitionDeliveries {
		if run.TransitionDeliveries[candidate].TransitionID == publication.TransitionID {
			deliveryIndex = candidate
			break
		}
	}
	if deliveryIndex < 0 {
		return Snapshot{}, fmt.Errorf("runs: Run %q has no unacknowledged delivery for transition %q", publication.RunID, publication.TransitionID)
	}
	delivery := run.TransitionDeliveries[deliveryIndex]
	if delivery.State != DeliveryPending {
		return Snapshot{}, fmt.Errorf("runs: transition delivery %q is already published", publication.TransitionID)
	}
	delivery.State = DeliveryPublished
	delivery.Sequence = publication.Sequence
	run.TransitionDeliveries[deliveryIndex] = delivery
	next := cloneModel(current)
	next.JournalSequence = operation.Sequence
	next.Runs[index] = run
	return NewSnapshot(next)
}

func applyAcknowledge(current Model, operation diskOperation) (Snapshot, error) {
	acknowledgement := *operation.Acknowledgement
	index := -1
	for candidate, run := range current.Runs {
		if run.ID == acknowledgement.RunID {
			index = candidate
			break
		}
	}
	if index < 0 {
		return Snapshot{}, fmt.Errorf("runs: Run %q not found", acknowledgement.RunID)
	}
	run := cloneRun(current.Runs[index])
	if acknowledgement.Count < 1 || acknowledgement.Count > len(run.TransitionDeliveries) {
		return Snapshot{}, errors.New("runs: delivery acknowledgement count is out of range")
	}
	for offset := 0; offset < acknowledgement.Count; offset++ {
		if run.TransitionDeliveries[offset].State != DeliveryPublished {
			return Snapshot{}, errors.New("runs: delivery acknowledgement includes an unpublished delivery")
		}
	}
	if run.DeliveredThrough > math.MaxInt-acknowledgement.Count {
		return Snapshot{}, errors.New("runs: delivery watermark exhausted")
	}
	run.DeliveredThrough += acknowledgement.Count
	run.TransitionDeliveries = slices.Clone(run.TransitionDeliveries[acknowledgement.Count:])
	next := cloneModel(current)
	next.JournalSequence = operation.Sequence
	next.Runs[index] = run
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
		if !setting {
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
			operation.Publication != nil || operation.Acknowledgement != nil ||
			operation.Checkpoint.Schema != operation.Schema || operation.Checkpoint.JournalSequence != operation.Sequence {
			return errors.New("runs: invalid checkpoint operation")
		}
	case operationAdmissionBatch:
		if operation.Sequence == 0 || operation.Schema != 0 || operation.Checkpoint != nil || len(operation.AdmissionBatches) == 0 || operation.Transition != nil ||
			operation.Publication != nil || operation.Acknowledgement != nil {
			return errors.New("runs: invalid admission-batch operation")
		}
	case operationTransition:
		if operation.Sequence == 0 || operation.Schema != 0 || operation.Checkpoint != nil || len(operation.AdmissionBatches) != 0 || operation.Transition == nil || len(operation.Runs) != 0 || len(operation.RateIncrements) != 0 ||
			operation.Publication != nil || operation.Acknowledgement != nil {
			return errors.New("runs: invalid lifecycle-transition operation")
		}
	case operationPublish:
		if operation.Sequence == 0 || operation.Schema != 0 || operation.Checkpoint != nil || len(operation.AdmissionBatches) != 0 || operation.Transition != nil || len(operation.Runs) != 0 || len(operation.RateIncrements) != 0 ||
			operation.Acknowledgement != nil || operation.Publication == nil ||
			operation.Publication.RunID == "" || operation.Publication.TransitionID == "" || operation.Publication.Sequence == 0 {
			return errors.New("runs: invalid delivery-publish operation")
		}
	case operationAcknowledge:
		if operation.Sequence == 0 || operation.Schema != 0 || operation.Checkpoint != nil || len(operation.AdmissionBatches) != 0 || operation.Transition != nil || len(operation.Runs) != 0 || len(operation.RateIncrements) != 0 ||
			operation.Publication != nil || operation.Acknowledgement == nil ||
			operation.Acknowledgement.RunID == "" || operation.Acknowledgement.Count < 1 {
			return errors.New("runs: invalid delivery-acknowledge operation")
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
		if len(canonical.Runs) == 0 {
			canonical.Runs = nil
		}
	}
	if operation.RateIncrements != nil {
		canonical.RateIncrements = slices.Clone(operation.RateIncrements)
		for index := range canonical.RateIncrements {
			canonical.RateIncrements[index].Minute = canonical.RateIncrements[index].Minute.UTC().Truncate(time.Minute)
		}
		slices.SortFunc(canonical.RateIncrements, compareRateBuckets)
		if len(canonical.RateIncrements) == 0 {
			canonical.RateIncrements = nil
		}
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
	live := len(s.state.model.AdmissionOperations) + len(s.state.model.AdmissionBatches) + len(s.state.model.Runs) + len(s.state.model.RateBuckets)
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
	retainBatch := make(map[string]bool)
	for _, run := range current.Runs {
		if run.State.Nonterminal() || len(run.TransitionDeliveries) > 0 {
			retainBatch[run.Causation.BatchID] = true
		}
	}
	keep := make(map[string]bool)
	remaining := make([]string, 0, len(current.AdmissionBatches))
	for _, batch := range current.AdmissionBatches {
		if retainBatch[batch.ID] {
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
			next, openErr := openPrivateDirectory(current, component, createDirectories)
			if openErr != nil {
				current.Close()
				return nil, fmt.Errorf("runs: open nonsymlink journal directory: %w", openErr)
			}
			current.Close()
			current = next
		}
	}
	return &storeLocation{directory: current, name: filepath.Base(path)}, nil
}

func openPrivateDirectory(parent *os.Root, component string, create bool) (*os.Root, error) {
	for {
		observed, err := parent.Lstat(component)
		if errors.Is(err, os.ErrNotExist) && create {
			if mkdirErr := parent.Mkdir(component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return nil, fmt.Errorf("create journal directory: %w", mkdirErr)
			}
			// Whether this call created the name or raced with another creator,
			// prove the resulting component from a fresh no-follow observation.
			continue
		}
		if err != nil {
			return nil, err
		}
		if observed.Mode()&os.ModeSymlink != 0 || !observed.IsDir() || observed.Mode().Perm() != 0o700 {
			return nil, errors.New("journal directory must be a private nonsymlink directory")
		}
		next, err := parent.OpenRoot(component)
		if err != nil {
			return nil, err
		}
		opened, openStatErr := next.Stat(".")
		confirmed, confirmErr := parent.Lstat(component)
		if openStatErr != nil || confirmErr != nil || confirmed.Mode()&os.ModeSymlink != 0 || !confirmed.IsDir() ||
			confirmed.Mode().Perm() != 0o700 || !os.SameFile(observed, opened) || !os.SameFile(confirmed, opened) {
			next.Close()
			return nil, errors.New("journal directory changed while opening without symlinks")
		}
		return next, nil
	}
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
