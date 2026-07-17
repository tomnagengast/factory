package taskstore

import (
	"path/filepath"
	"testing"

	"github.com/tomnagengast/factory/internal/eventwire"
)

func TestConvertLegacyPendingOperationPreservesPublishedAndAppliedBoundaries(t *testing.T) {
	command := CreateEnvelope(CreateCommand{
		Actor: humanActor, Title: "Pending migration", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "pending-migration",
	})
	legacyID := "op-0123456789abcdef"
	record := eventwire.Record{Sequence: 7, Event: eventwire.Event{
		ID: "factory:task:" + legacyID, Source: eventwire.SourceFactory, Type: StagedEventType, Action: operationCreate,
		Attributes: map[string][]string{
			attributeOperation: {legacyID}, "taskSource": {"factory"},
			eventwire.AttributeProducer: {"task-service"}, eventwire.AttributeProvenance: {"factory"},
		}, RootEventID: "factory:task:" + legacyID, ReceivedAt: testNow,
	}}

	empty := Snapshot{Schema: SchemaVersion, NextSequence: 1}
	published, canonicalRecord, err := ConvertLegacyPendingOperation(empty, legacyID, command, record)
	if err != nil {
		t.Fatal(err)
	}
	if len(published.Operations) != 1 || published.Operations[0].State != TaskOperationPublished || canonicalRecord.Sequence != record.Sequence || canonicalRecord.Event.ID == record.Event.ID {
		t.Fatalf("published conversion = snapshot %#v record %#v", published, canonicalRecord)
	}
	if canonicalRecord.Event.ID != published.Operations[0].EventID || canonicalRecord.Event.RootEventID != canonicalRecord.Event.ID {
		t.Fatalf("canonical record identity = %#v", canonicalRecord)
	}

	store, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := store.Execute(command, testNow); err != nil || result.Replayed {
		t.Fatalf("legacy apply = %#v, %v", result, err)
	}
	applied, _, err := ConvertLegacyPendingOperation(store.Snapshot(), legacyID, command, record)
	if err != nil {
		t.Fatal(err)
	}
	if operation := applied.Operations[0]; operation.State != TaskOperationAppliedResult || operation.Result == nil || operation.Result.Replayed {
		t.Fatalf("applied conversion = %#v", operation)
	}
}

func TestConvertLegacyPendingOperationRejectsMismatchedEvidence(t *testing.T) {
	command := CreateEnvelope(CreateCommand{Actor: humanActor, Title: "Pending", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "pending"})
	record := eventwire.Record{Sequence: 1, Event: eventwire.Event{
		ID: "factory:task:op-0123456789abcdef", Source: eventwire.SourceFactory, Type: StagedEventType, Action: operationCreate,
		Attributes: map[string][]string{
			attributeOperation: {"op-0123456789abcdef"}, "taskSource": {"factory"},
			eventwire.AttributeProducer: {"task-service"}, eventwire.AttributeProvenance: {"factory"},
		}, RootEventID: "factory:task:op-0123456789abcdef", ReceivedAt: testNow,
	}}
	for name, mutate := range map[string]func(*eventwire.Record){
		"operation ID": func(record *eventwire.Record) { record.Event.ID = "factory:task:op-fedcba9876543210" },
		"action":       func(record *eventwire.Record) { record.Event.Action = operationUpdate },
		"channel": func(record *eventwire.Record) {
			record.Event.Channels = []string{"factory"}
			record.ChannelSequences = map[string]uint64{"factory": 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := record
			mutate(&changed)
			if _, _, err := ConvertLegacyPendingOperation(Snapshot{Schema: SchemaVersion, NextSequence: 1}, "op-0123456789abcdef", command, changed); err == nil {
				t.Fatal("mismatched pending evidence was accepted")
			}
		})
	}
}
