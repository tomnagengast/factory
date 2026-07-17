package taskstore

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

func TestTaskOutboxMatchesLegacyCoordinatorResponsesAndTaskState(t *testing.T) {
	legacyStore, legacyCoordinator, legacyJournal := newLegacyCoordinatorHarness(t)
	canonicalStore, canonicalOutbox, canonicalJournal := newTaskOutboxHarness(t)
	random := make([]byte, 64)
	for index := range random {
		random[index] = byte(index + 1)
	}
	legacyStore.random = bytes.NewReader(random)
	canonicalStore.random = bytes.NewReader(random)

	create := CreateEnvelope(CreateCommand{
		Actor: humanActor, Title: "Equivalent private task", Description: "private-equivalence-sentinel",
		ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "equivalent-create",
	})
	legacyCreated, legacyErr := legacyCoordinator.Execute(context.Background(), create, testNow)
	canonicalCreated, canonicalErr := canonicalOutbox.Execute(context.Background(), create, testNow)
	assertEquivalentTaskResponse(t, legacyCreated, legacyErr, canonicalCreated, canonicalErr)
	if !legacyCreated.Replayed || !canonicalCreated.Replayed {
		t.Fatalf("create replay parity = legacy %t canonical %t", legacyCreated.Replayed, canonicalCreated.Replayed)
	}

	message := MessageEnvelope(MessageCommand{
		Actor: humanActor, TaskID: legacyCreated.Task.Ref.ProviderID, ExpectedRevision: legacyCreated.Task.Revision,
		Body: "private-equivalent-message", IdempotencyKey: "equivalent-message",
	})
	legacyMessage, legacyErr := legacyCoordinator.Execute(context.Background(), message, testNow.Add(time.Second))
	canonicalMessage, canonicalErr := canonicalOutbox.Execute(context.Background(), message, testNow.Add(time.Second))
	assertEquivalentTaskResponse(t, legacyMessage, legacyErr, canonicalMessage, canonicalErr)

	legacySnapshot := legacyStore.Snapshot()
	canonicalSnapshot := canonicalStore.Snapshot()
	canonicalSnapshot.Operations = nil
	if !reflect.DeepEqual(legacySnapshot, canonicalSnapshot) {
		t.Fatalf("task state differs:\nlegacy=%#v\ncanonical=%#v", legacySnapshot, canonicalSnapshot)
	}
	assertEquivalentTaskWire(t, legacyJournal, canonicalJournal)

	stale := UpdateEnvelope(UpdateCommand{
		Actor: humanActor, TaskID: legacyMessage.Task.Ref.ProviderID, ExpectedRevision: 1,
		Title: "Stale", ApprovalMode: ApprovalGated, IdempotencyKey: "equivalent-stale",
	})
	_, legacyErr = legacyCoordinator.Execute(context.Background(), stale, testNow.Add(2*time.Second))
	_, canonicalErr = canonicalOutbox.Execute(context.Background(), stale, testNow.Add(2*time.Second))
	var legacyConflict, canonicalConflict RevisionConflict
	if !errors.As(legacyErr, &legacyConflict) || !errors.As(canonicalErr, &canonicalConflict) || !reflect.DeepEqual(legacyConflict.Current, canonicalConflict.Current) {
		t.Fatalf("typed conflict differs: legacy=%T %v canonical=%T %v", legacyErr, legacyErr, canonicalErr, canonicalErr)
	}
}

func newLegacyCoordinatorHarness(t *testing.T) (*Store, *Coordinator, *eventwire.Journal) {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "tasks.jsonl")
	store, err := Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	stager, err := NewStager(filepath.Join(root, "task-operations"), storePath)
	if err != nil {
		t.Fatal(err)
	}
	stager.random = bytes.NewReader([]byte("12345678abcdefghABCDEFGH87654321"))
	dispatcher, err := NewDispatcher(store, stager)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := eventwire.Open(filepath.Join(root, "events.jsonl"), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.Handle(eventwire.Filter{Source: eventwire.SourceFactory, Type: StagedEventType}, func(ctx context.Context, record eventwire.Record) error {
		_, err := dispatcher.Apply(ctx, record)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(store, stager, wire)
	if err != nil {
		t.Fatal(err)
	}
	return store, coordinator, journal
}

func assertEquivalentTaskResponse(t *testing.T, legacy Result, legacyErr error, canonical Result, canonicalErr error) {
	t.Helper()
	if legacyErr != nil || canonicalErr != nil || !reflect.DeepEqual(legacy, canonical) {
		t.Fatalf("task response differs:\nlegacy=%#v err=%v\ncanonical=%#v err=%v", legacy, legacyErr, canonical, canonicalErr)
	}
}

func assertEquivalentTaskWire(t *testing.T, legacy, canonical *eventwire.Journal) {
	t.Helper()
	legacyTotal, legacyDispatched, _, legacyRecords := legacy.Snapshot()
	canonicalTotal, canonicalDispatched, _, canonicalRecords := canonical.Snapshot()
	if legacyTotal != canonicalTotal || legacyDispatched != canonicalDispatched || len(legacyRecords) != len(canonicalRecords) {
		t.Fatalf("wire counts differ: legacy=%d/%d/%d canonical=%d/%d/%d", legacyTotal, legacyDispatched, len(legacyRecords), canonicalTotal, canonicalDispatched, len(canonicalRecords))
	}
	for index := range legacyRecords {
		legacyEvent := legacyRecords[index].Event
		canonicalEvent := canonicalRecords[index].Event
		delete(legacyEvent.Attributes, attributeOperation)
		delete(canonicalEvent.Attributes, attributeOperation)
		legacyEvent.ID, legacyEvent.RootEventID = "", ""
		canonicalEvent.ID, canonicalEvent.RootEventID = "", ""
		if !reflect.DeepEqual(legacyEvent, canonicalEvent) {
			t.Fatalf("wire event %d differs: legacy=%#v canonical=%#v", index, legacyEvent, canonicalEvent)
		}
	}
}
