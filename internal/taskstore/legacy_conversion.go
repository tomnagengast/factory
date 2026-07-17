package taskstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

// ReadLegacyStagedCommand strictly reads one private legacy command file for
// conversion. Runtime staging remains authoritative until the activation
// boundary; this reader never mutates or removes the source file.
func ReadLegacyStagedCommand(directory, operationID string) (CommandEnvelope, error) {
	if !operationIDPattern.MatchString(operationID) {
		return CommandEnvelope{}, errors.New("task store: invalid legacy operation ID")
	}
	path := filepath.Join(directory, operationID+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return CommandEnvelope{}, errors.New("task store: legacy staged command is missing or unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CommandEnvelope{}, fmt.Errorf("task store: read legacy staged command: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var command CommandEnvelope
	if err := decoder.Decode(&command); err != nil {
		return CommandEnvelope{}, fmt.Errorf("task store: decode legacy staged command: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CommandEnvelope{}, errors.New("task store: legacy staged command has trailing content")
	}
	if err := command.ValidateShape(); err != nil {
		return CommandEnvelope{}, err
	}
	return command, nil
}

// ConvertLegacyPendingOperation folds the only admissible legacy staging
// boundary into a canonical task transaction and rewrites its still-pending
// body-free wire record to the deterministic canonical identity. An exact
// legacy outcome advances the transaction to applied-result; otherwise it
// remains published for canonical recovery to apply once.
func ConvertLegacyPendingOperation(snapshot Snapshot, legacyOperationID string, command CommandEnvelope, record eventwire.Record) (Snapshot, eventwire.Record, error) {
	if len(snapshot.Operations) != 0 {
		return Snapshot{}, eventwire.Record{}, errors.New("task store: source snapshot already contains canonical task operations")
	}
	if !legacyTaskRecordMatches(legacyOperationID, command, record) {
		return Snapshot{}, eventwire.Record{}, errors.New("task store: pending stage conflicts with its wire record")
	}
	scope, hash, err := command.identity()
	if err != nil {
		return Snapshot{}, eventwire.Record{}, err
	}
	at := record.Event.ReceivedAt.UTC()
	operation := TaskOperation{
		ID: taskOperationID(scope, hash), Scope: scope, CommandHash: hash, Command: command.Clone(),
		State: TaskOperationPublished, EventID: "factory:task:" + taskOperationID(scope, hash), EventSequence: record.Sequence,
		CreatedAt: at, UpdatedAt: at,
	}
	for _, outcome := range snapshot.Outcomes {
		if outcome.Scope != scope {
			continue
		}
		if outcome.CommandHash != hash {
			return Snapshot{}, eventwire.Record{}, ErrIdempotencyConflict
		}
		result := resultFromOutcome(outcome)
		operation.State = TaskOperationAppliedResult
		operation.Result = &result
		break
	}
	if err := operation.Validate(); err != nil {
		return Snapshot{}, eventwire.Record{}, fmt.Errorf("task store: invalid converted task operation: %w", err)
	}
	converted := snapshot.Clone()
	converted.Operations = append(converted.Operations, operation)
	sort.Slice(converted.Operations, func(i, j int) bool { return converted.Operations[i].ID < converted.Operations[j].ID })
	if err := ValidateSnapshot(converted); err != nil {
		return Snapshot{}, eventwire.Record{}, fmt.Errorf("task store: validate converted task operation: %w", err)
	}
	canonicalEvent := operation.Event()
	canonicalEvent.RootEventID = canonicalEvent.ID
	canonicalRecord := eventwire.Record{
		Sequence: record.Sequence, ChannelSequences: cloneChannelSequences(record.ChannelSequences), Event: canonicalEvent,
	}
	if _, err := eventwire.CanonicalRecordDigest(canonicalRecord); err != nil {
		return Snapshot{}, eventwire.Record{}, fmt.Errorf("task store: validate converted wire record: %w", err)
	}
	return converted, canonicalRecord, nil
}

func legacyTaskRecordMatches(operationID string, command CommandEnvelope, record eventwire.Record) bool {
	if !operationIDPattern.MatchString(operationID) || record.Sequence == 0 || record.Event.ReceivedAt.IsZero() || record.Event.ReceivedAt.Location() != time.UTC || command.ValidateShape() != nil {
		return false
	}
	expected := eventwire.Event{
		ID: "factory:task:" + operationID, Source: eventwire.SourceFactory, Type: StagedEventType, Action: command.Kind,
		Subject: command.TaskID(), Attributes: map[string][]string{
			attributeOperation:            {operationID},
			"taskSource":                  {"factory"},
			eventwire.AttributeProducer:   {"task-service"},
			eventwire.AttributeProvenance: {"factory"},
		}, ReceivedAt: record.Event.ReceivedAt,
	}
	return record.Event.ID == expected.ID && record.Event.Source == expected.Source && record.Event.Type == expected.Type &&
		record.Event.Action == expected.Action && record.Event.Subject == expected.Subject && reflect.DeepEqual(record.Event.Attributes, expected.Attributes) &&
		(record.Event.RootEventID == "" || record.Event.RootEventID == expected.ID) && len(record.Event.Channels) == 0 && len(record.ChannelSequences) == 0 &&
		record.Event.ParentInvocationID == "" && record.Event.ParentRunID == "" && record.Event.Hop == 0 && len(record.Event.AncestorRuleIDs) == 0
}

func cloneChannelSequences(values map[string]uint64) map[string]uint64 {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]uint64, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
