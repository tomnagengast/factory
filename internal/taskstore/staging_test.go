package taskstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomnagengast/factory/internal/eventwire"
)

func TestStagingDispatchKeepsBodiesOffGlobalEvent(t *testing.T) {
	directory := t.TempDir()
	storePath := filepath.Join(directory, "tasks.jsonl")
	store, err := Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	stager, err := NewStager(filepath.Join(directory, "staged"), storePath)
	if err != nil {
		t.Fatal(err)
	}
	stager.random = bytes.NewReader([]byte("12345678abcdefgh"))
	dispatcher, err := NewDispatcher(store, stager)
	if err != nil {
		t.Fatal(err)
	}

	privateTitle := "private task title 6dfb6d"
	create := CommandEnvelope{Kind: operationCreate, Create: &CreateCommand{
		Actor: humanActor, Title: privateTitle, Description: "private description", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "create-stage",
	}}
	staged, err := stager.Stage(create, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !BodyFreeEvent(staged.Event, privateTitle, create.Create.Description, create.Create.Actor.ID) {
		t.Fatalf("global event exposed private command: %#v", staged.Event)
	}
	info, err := os.Stat(stager.path(staged.OperationID))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("staged permissions = %v err=%v", info, err)
	}
	privateFile, err := os.ReadFile(stager.path(staged.OperationID))
	if err != nil || !strings.Contains(string(privateFile), privateTitle) {
		t.Fatalf("private command missing body: %q err=%v", privateFile, err)
	}
	created, err := dispatcher.Apply(context.Background(), eventwire.Record{Sequence: 1, Event: staged.Event})
	if err != nil || created.Task.Ref.Identifier != "FAC-1" || created.Replayed {
		t.Fatalf("dispatch create: result=%#v err=%v", created, err)
	}
	if _, err := os.Stat(stager.path(staged.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("applied create stage was retained: %v", err)
	}

	privateBody := "private reply body bdf301"
	messageCommand := CommandEnvelope{Kind: operationMessage, Message: &MessageCommand{
		Actor: humanActor, TaskID: created.Task.Ref.ProviderID, ExpectedRevision: created.Task.Revision, Body: privateBody, IdempotencyKey: "message-stage",
	}}
	stagedMessage, err := stager.Stage(messageCommand, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !BodyFreeEvent(stagedMessage.Event, privateBody, humanActor.ID) {
		t.Fatalf("global message event exposed private content: %#v", stagedMessage.Event)
	}
	result, err := dispatcher.Apply(context.Background(), eventwire.Record{Sequence: 2, Event: stagedMessage.Event})
	if err != nil || result.Message == nil || result.Message.Body != privateBody {
		t.Fatalf("dispatch message: result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(stager.path(stagedMessage.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("applied message stage was retained: %v", err)
	}
}

func TestDispatcherReplaysAppliedCommandBeforeRemovingStage(t *testing.T) {
	directory := t.TempDir()
	storePath := filepath.Join(directory, "tasks.jsonl")
	store, err := Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	stager, err := NewStager(filepath.Join(directory, "staged"), storePath)
	if err != nil {
		t.Fatal(err)
	}
	stager.random = bytes.NewReader([]byte("12345678"))
	dispatcher, _ := NewDispatcher(store, stager)
	command := CommandEnvelope{Kind: operationCreate, Create: &CreateCommand{Actor: humanActor, Title: "Replay", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "replay"}}
	staged, err := stager.Stage(command, testNow)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Execute(command, testNow)
	if err != nil || first.Replayed {
		t.Fatalf("direct apply: result=%#v err=%v", first, err)
	}
	replayed, err := dispatcher.Apply(context.Background(), eventwire.Record{Sequence: 1, Event: staged.Event})
	if err != nil || !replayed.Replayed || replayed.Task.Ref != first.Task.Ref {
		t.Fatalf("replayed dispatch: result=%#v err=%v", replayed, err)
	}
	if _, err := os.Stat(stager.path(staged.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replayed stage was retained: %v", err)
	}
}

func TestDispatcherRejectsMetadataConflictWithoutApplying(t *testing.T) {
	directory := t.TempDir()
	storePath := filepath.Join(directory, "tasks.jsonl")
	store, _ := Open(storePath)
	stager, _ := NewStager(filepath.Join(directory, "staged"), storePath)
	stager.random = bytes.NewReader([]byte("12345678"))
	dispatcher, _ := NewDispatcher(store, stager)
	staged, err := stager.Stage(CommandEnvelope{Kind: operationCreate, Create: &CreateCommand{Actor: humanActor, Title: "Conflict", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "conflict"}}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	staged.Event.Action = operationUpdate
	if _, err := dispatcher.Apply(context.Background(), eventwire.Record{Sequence: 1, Event: staged.Event}); err == nil || !permanent(err) {
		t.Fatal("metadata conflict was accepted")
	}
	if len(store.Snapshot().Tasks) != 0 {
		t.Fatal("metadata conflict mutated the task store")
	}
}

func permanent(err error) bool {
	var classified interface{ Permanent() bool }
	return errors.As(err, &classified) && classified.Permanent()
}

type dispatcherPublisher struct {
	dispatcher *Dispatcher
	failBefore bool
	failAfter  bool
	sequence   uint64
}

func (p *dispatcherPublisher) Publish(ctx context.Context, event eventwire.Event) (eventwire.Record, bool, error) {
	if p.failBefore {
		return eventwire.Record{}, false, errors.New("catch-up acknowledgment failed")
	}
	p.sequence++
	record := eventwire.Record{Sequence: p.sequence, Event: event}
	if _, err := p.dispatcher.Apply(ctx, record); err != nil {
		return record, true, err
	}
	if p.failAfter {
		return record, true, errors.New("ack failed")
	}
	return record, true, nil
}

func TestCoordinatorCleansUnpublishedStageWhenCatchUpFails(t *testing.T) {
	directory := t.TempDir()
	storePath := filepath.Join(directory, "tasks.jsonl")
	store, _ := Open(storePath)
	stager, _ := NewStager(filepath.Join(directory, "staged"), storePath)
	stager.random = bytes.NewReader([]byte("12345678"))
	dispatcher, _ := NewDispatcher(store, stager)
	coordinator, _ := NewCoordinator(store, stager, &dispatcherPublisher{dispatcher: dispatcher, failBefore: true})
	command := CommandEnvelope{Kind: operationCreate, Create: &CreateCommand{Actor: humanActor, Title: "Unpublished", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "unpublished"}}
	if _, err := coordinator.Execute(context.Background(), command, testNow); err == nil {
		t.Fatal("pre-publication catch-up failure was accepted")
	}
	entries, err := os.ReadDir(stager.directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unpublished stage was retained: entries=%#v err=%v", entries, err)
	}
	if len(store.Snapshot().Tasks) != 0 {
		t.Fatal("unpublished command mutated the task store")
	}
}

func TestCoordinatorCleansStageAfterApplyEvenWhenAcknowledgmentFails(t *testing.T) {
	directory := t.TempDir()
	storePath := filepath.Join(directory, "tasks.jsonl")
	store, _ := Open(storePath)
	stager, _ := NewStager(filepath.Join(directory, "staged"), storePath)
	stager.random = bytes.NewReader([]byte("12345678abcdefgh"))
	dispatcher, _ := NewDispatcher(store, stager)
	publisher := &dispatcherPublisher{dispatcher: dispatcher, failAfter: true}
	coordinator, _ := NewCoordinator(store, stager, publisher)
	command := CommandEnvelope{Kind: operationCreate, Create: &CreateCommand{Actor: humanActor, Title: "Coordinated", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "coordinated"}}
	if _, err := coordinator.Execute(context.Background(), command, testNow); err == nil {
		t.Fatal("publish acknowledgment failure was accepted")
	}
	entries, err := os.ReadDir(stager.directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("stage after acknowledgment failure = %#v err=%v", entries, err)
	}
	if got := store.Snapshot(); len(got.Tasks) != 1 {
		t.Fatalf("durable apply before acknowledgment = %#v", got)
	}

	publisher.failAfter = false
	result, err := coordinator.Execute(context.Background(), command, testNow)
	if err != nil || !result.Replayed {
		t.Fatalf("coordinator retry: result=%#v err=%v", result, err)
	}
	entries, err = os.ReadDir(stager.directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("retry retained a stage: entries=%#v err=%v", entries, err)
	}
}

func TestStagerRecoveryKeepsOnlyPendingWireStages(t *testing.T) {
	directory := t.TempDir()
	storePath := filepath.Join(directory, "tasks.jsonl")
	stager, _ := NewStager(filepath.Join(directory, "staged"), storePath)
	stager.random = bytes.NewReader([]byte("12345678abcdefghABCDEFGH"))
	command := CommandEnvelope{Kind: operationCreate, Create: &CreateCommand{Actor: humanActor, Title: "Recover", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "recover"}}
	orphan, err := stager.Stage(command, testNow)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := stager.Stage(command, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stager.directory, ".task-operation-crash"), []byte("private temporary body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stager.Recover(1, []eventwire.Record{{Sequence: 2, Event: pending.Event}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stager.path(orphan.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned stage was retained: %v", err)
	}
	if _, err := os.Stat(stager.path(pending.OperationID)); err != nil {
		t.Fatalf("pending stage was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stager.directory, ".task-operation-crash")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary stage was retained: %v", err)
	}
}
