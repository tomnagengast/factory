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
	if _, err := os.Stat(stager.path(staged.OperationID)); err != nil {
		t.Fatalf("staged create was removed before wire acknowledgment: %v", err)
	}
	if err := stager.Cancel(staged.OperationID); err != nil {
		t.Fatal(err)
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
	if err := stager.Cancel(stagedMessage.OperationID); err != nil {
		t.Fatal(err)
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
	if _, err := os.Stat(stager.path(staged.OperationID)); err != nil {
		t.Fatalf("replayed stage was removed before acknowledgment: %v", err)
	}
	if err := stager.Cancel(staged.OperationID); err != nil {
		t.Fatal(err)
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
	failAfter  bool
	sequence   uint64
}

func (p *dispatcherPublisher) Publish(ctx context.Context, event eventwire.Event) (eventwire.Record, bool, error) {
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

func TestCoordinatorCleansStageOnlyAfterPublishAcknowledges(t *testing.T) {
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
	if err != nil || len(entries) != 1 {
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
	if err != nil || len(entries) != 1 {
		t.Fatalf("retry should consume only its acknowledged stage: entries=%#v err=%v", entries, err)
	}
}
