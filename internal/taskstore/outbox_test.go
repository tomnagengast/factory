package taskstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

func TestTaskOutboxExecutesEveryCommandAndKeepsBodiesPrivate(t *testing.T) {
	store, outbox, journal := newTaskOutboxHarness(t)
	ctx := context.Background()
	now := testNow

	created := executeOutbox(t, outbox, CreateEnvelope(CreateCommand{
		Actor: humanActor, Title: "Private native task", Description: "private-description-sentinel",
		ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "outbox-create",
	}), now)
	task := created.Task
	updated := executeOutbox(t, outbox, UpdateEnvelope(UpdateCommand{
		Actor: humanActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Title: "Updated native task",
		Description: "private-update-sentinel", ApprovalMode: ApprovalGated, IdempotencyKey: "outbox-update",
	}), now.Add(time.Second))
	task = updated.Task
	messageResult := executeOutbox(t, outbox, MessageEnvelope(MessageCommand{
		Actor: humanActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision,
		Body: "private-message-sentinel", IdempotencyKey: "outbox-message",
	}), now.Add(2*time.Second))
	task = messageResult.Task
	if messageResult.Message == nil {
		t.Fatal("message result is missing")
	}
	linkResult := executeOutbox(t, outbox, LinkEnvelope(LinkCommand{
		Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Label: "Pull request",
		URL: "https://github.com/tomnagengast/factory/pull/18", IdempotencyKey: "outbox-link",
	}), now.Add(3*time.Second))
	task = linkResult.Task
	gateResult := executeOutbox(t, outbox, GateEnvelope(GateCommand{
		Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Kind: GateKindResearch,
		Mode: ApprovalGated, ArtifactURL: "https://example.com/private-artifact", IdempotencyKey: "outbox-gate",
	}), now.Add(4*time.Second))
	task = gateResult.Task
	if gateResult.Gate == nil {
		t.Fatal("gate result is missing")
	}
	decisionResult := executeOutbox(t, outbox, DecisionEnvelope(DecisionCommand{
		Actor: humanActor, TaskID: task.Ref.ProviderID, GateID: gateResult.Gate.ID, ExpectedRevision: task.Revision,
		Action: DecisionApprove, Reason: "private-decision-sentinel", IdempotencyKey: "outbox-decision",
	}), now.Add(5*time.Second))
	task = decisionResult.Task
	routing := RoutingSnapshot{
		ProjectID: task.ProjectID, Repository: "tomnagengast/factory", RepositoryURL: "git@github.com:tomnagengast/factory.git",
		RepositoryPath: "/tmp/factory", ManagedRoot: "/tmp", BaseBranch: "main", WorkflowID: "full-sdlc-provider-neutral",
		WorkflowDigest: strings.Repeat("a", 64), AdmittedAt: now.Add(6 * time.Second),
	}
	routed := executeOutbox(t, outbox, RoutingEnvelope(RoutingCommand{
		Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision,
		Routing: routing, IdempotencyKey: "outbox-routing",
	}), routing.AdmittedAt)
	task = routed.Task
	completed := executeOutbox(t, outbox, CompletionEnvelope(CompletionCommand{
		Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision,
		Completion:     Completion{RunID: "run-0123456789abcdef", EvidenceRef: "private-evidence-sentinel"},
		IdempotencyKey: "outbox-completion",
	}), now.Add(7*time.Second))
	if completed.Task.State != StateCompleted {
		t.Fatalf("completed task = %#v", completed.Task)
	}

	cancelTask := executeOutbox(t, outbox, CreateEnvelope(CreateCommand{
		Actor: humanActor, Title: "Cancel me", ProjectID: "project-1", ApprovalMode: ApprovalAutomatic, IdempotencyKey: "outbox-create-cancel",
	}), now.Add(8*time.Second)).Task
	canceled := executeOutbox(t, outbox, StateEnvelope(StateCommand{
		Actor: humanActor, TaskID: cancelTask.Ref.ProviderID, ExpectedRevision: cancelTask.Revision,
		State: StateCanceled, IdempotencyKey: "outbox-state",
	}), now.Add(9*time.Second))
	if canceled.Task.State != StateCanceled {
		t.Fatalf("canceled task = %#v", canceled.Task)
	}

	operations := store.TaskOperations()
	if len(operations) != 10 || store.Status().PendingOperations != 0 {
		t.Fatalf("task operations = %d status = %#v", len(operations), store.Status())
	}
	for _, operation := range operations {
		if operation.State != TaskOperationAcknowledged || operation.EventSequence == 0 || operation.Result == nil || operation.Failure != nil {
			t.Fatalf("task operation = %#v", operation)
		}
	}
	_, dispatched, _, records := journal.Snapshot()
	if dispatched != uint64(len(records)) || len(records) != len(operations) {
		t.Fatalf("wire dispatched=%d records=%d operations=%d", dispatched, len(records), len(operations))
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		"private-description-sentinel", "private-update-sentinel", "private-message-sentinel",
		"private-decision-sentinel", "private-artifact", "private-evidence-sentinel",
	} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("wire exposed private task body %q: %s", private, encoded)
		}
	}

	replayed, err := outbox.Execute(ctx, CreateEnvelope(CreateCommand{
		Actor: humanActor, Title: "Private native task", Description: "private-description-sentinel",
		ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "outbox-create",
	}), now.Add(time.Hour))
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.Task, created.Task) {
		t.Fatalf("outbox replay = %#v, %v", replayed, err)
	}
	if len(store.TaskOperations()) != 10 {
		t.Fatal("exact task retry appended another operation")
	}
}

func TestTaskOutboxDurablyReplaysTypedFailure(t *testing.T) {
	store, outbox, journal := newTaskOutboxHarness(t)
	created := executeOutbox(t, outbox, CreateEnvelope(CreateCommand{
		Actor: humanActor, Title: "Conflict task", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "failure-create",
	}), testNow).Task
	stale := UpdateEnvelope(UpdateCommand{
		Actor: humanActor, TaskID: created.Ref.ProviderID, ExpectedRevision: created.Revision + 1,
		Title: created.Title, ApprovalMode: created.ApprovalMode, IdempotencyKey: "failure-stale",
	})
	_, err := outbox.Execute(context.Background(), stale, testNow.Add(time.Second))
	var conflict RevisionConflict
	if !errors.As(err, &conflict) || conflict.Current.Ref.ProviderID != created.Ref.ProviderID {
		t.Fatalf("typed failure = %T %v", err, err)
	}
	operations := store.TaskOperations()
	var failure TaskOperation
	for _, operation := range operations {
		if operation.Failure != nil {
			failure = operation
			break
		}
	}
	if failure.State != TaskOperationAcknowledged || failure.Result != nil || failure.Failure == nil || failure.Failure.Code != taskFailureRevisionConflict {
		t.Fatalf("durable failure = %#v", failure)
	}
	beforeTotal, beforeDispatched, _, _ := journal.Snapshot()
	if err := store.Compact(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	reopenedWire, err := eventwire.New(journal)
	if err != nil {
		t.Fatal(err)
	}
	reopenedOutbox, err := NewTaskOutbox(reopened, reopenedWire)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reopenedOutbox.Execute(context.Background(), stale, testNow.Add(time.Hour))
	if !errors.As(err, &conflict) {
		t.Fatalf("replayed typed failure = %T %v", err, err)
	}
	afterTotal, afterDispatched, _, _ := journal.Snapshot()
	if beforeTotal != afterTotal || beforeDispatched != afterDispatched {
		t.Fatal("typed failure retry republished its event")
	}
}

func TestTaskOutboxRecoversEveryDurabilityBoundary(t *testing.T) {
	for _, boundary := range []string{"pending", "published", "applied"} {
		t.Run(boundary, func(t *testing.T) {
			store, _, journal := newTaskOutboxStoreAndJournal(t)
			command := CreateEnvelope(CreateCommand{
				Actor: humanActor, Title: "Recovered task", ProjectID: "project-1", ApprovalMode: ApprovalGated,
				IdempotencyKey: "recover-" + boundary,
			})
			operation, _, err := store.SubmitTaskOperation(command, testNow)
			if err != nil {
				t.Fatal(err)
			}
			if boundary != "pending" {
				record, _, err := journal.Add(operation.Event())
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.RecordTaskOperationPublished(operation.ID, record.Event.ID, record.Sequence, record.Event.ReceivedAt); err != nil {
					t.Fatal(err)
				}
				if boundary == "applied" {
					if _, err := store.ApplyTaskOperation(operation.ID, record.Event.ReceivedAt); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := store.Compact(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(store.path)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := eventwire.New(journal)
			if err != nil {
				t.Fatal(err)
			}
			outbox, err := NewTaskOutbox(reopened, wire)
			if err != nil {
				t.Fatal(err)
			}
			if err := outbox.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			recovered, found := reopened.TaskOperation(operation.ID)
			if !found || recovered.State != TaskOperationAcknowledged || recovered.Result == nil || recovered.Result.Task.Title != "Recovered task" {
				t.Fatalf("recovered operation = %#v, found=%t", recovered, found)
			}
		})
	}
}

func TestTaskOperationApplyConvergesAfterResultAppendFailure(t *testing.T) {
	store, _, journal := newTaskOutboxStoreAndJournal(t)
	operation, _, err := store.SubmitTaskOperation(CreateEnvelope(CreateCommand{
		Actor: humanActor, Title: "Crash gap", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "crash-gap",
	}), testNow)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := journal.Add(operation.Event())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordTaskOperationPublished(operation.ID, record.Event.ID, record.Sequence, record.Event.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	writes := 0
	store.write = func(file *os.File, data []byte) (int, error) {
		writes++
		if writes == 2 {
			return 0, errors.New("injected applied-result append failure")
		}
		return file.Write(data)
	}
	if _, err := store.ApplyTaskOperation(operation.ID, testNow); err == nil {
		t.Fatal("applied-result append failure was ignored")
	}
	if got, found := store.FindIdentifier("FAC-1"); !found || got.Title != "Crash gap" {
		t.Fatalf("task mutation did not precede result append: %#v, %t", got, found)
	}
	if current, _ := store.TaskOperation(operation.ID); current.State != TaskOperationPublished {
		t.Fatalf("failed result append advanced operation: %#v", current)
	}
	store.write = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	result, err := store.ApplyTaskOperation(operation.ID, testNow)
	if err != nil || result.Task.Title != "Crash gap" {
		t.Fatalf("recovered result = %#v, %v", result, err)
	}
	if snapshot := store.Snapshot(); len(snapshot.Tasks) != 1 || len(snapshot.Outcomes) != 1 {
		t.Fatalf("crash recovery duplicated task state: %#v", snapshot)
	}
}

func TestTaskOutboxPublicationFailureLeavesRecoverableAppliedOperation(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := eventwire.Open(filepath.Join(root, "events.jsonl"), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := eventwire.New(journal)
	outbox, _ := NewTaskOutbox(store, wire)
	injected := errors.New("injected later handler failure")
	if err := wire.Handle(eventwire.Filter{Source: eventwire.SourceFactory, Type: StagedEventType}, func(context.Context, eventwire.Record) error {
		return injected
	}); err != nil {
		t.Fatal(err)
	}
	command := CreateEnvelope(CreateCommand{
		Actor: humanActor, Title: "Publish retry", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "publish-retry",
	})
	if _, err := outbox.Execute(context.Background(), command, testNow); !errors.Is(err, injected) {
		t.Fatalf("publish error = %v", err)
	}
	operations := store.TaskOperations()
	if len(operations) != 1 || operations[0].State != TaskOperationAppliedResult || journal.Status().Pending != 1 {
		t.Fatalf("failed publish boundary = operations %#v status %#v", operations, journal.Status())
	}

	reopenedJournal, err := eventwire.Open(journalPath(journal, root), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	recoveryWire, _ := eventwire.New(reopenedJournal)
	recoveryOutbox, _ := NewTaskOutbox(store, recoveryWire)
	if err := recoveryOutbox.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	operation, _ := store.TaskOperation(operations[0].ID)
	if operation.State != TaskOperationAcknowledged || reopenedJournal.Status().Pending != 0 {
		t.Fatalf("publication recovery = operation %#v status %#v", operation, reopenedJournal.Status())
	}
}

func TestTaskOperationCheckpointRejectsTamperedEvidence(t *testing.T) {
	store, outbox, _ := newTaskOutboxHarness(t)
	executeOutbox(t, outbox, CreateEnvelope(CreateCommand{
		Actor: humanActor, Title: "Checkpoint task", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "checkpoint-operation",
	}), testNow)
	base := store.Snapshot()
	if len(base.Operations) != 1 {
		t.Fatalf("checkpoint operations = %#v", base.Operations)
	}
	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{name: "command hash", mutate: func(value *Snapshot) { value.Operations[0].CommandHash = strings.Repeat("f", 64) }},
		{name: "result", mutate: func(value *Snapshot) { value.Operations[0].Result.Task.Title = "Tampered" }},
		{name: "state regression", mutate: func(value *Snapshot) { value.Operations[0].State = TaskOperationPublished }},
		{name: "sequence", mutate: func(value *Snapshot) { value.Operations[0].EventSequence = 0 }},
		{name: "duplicate", mutate: func(value *Snapshot) { value.Operations = append(value.Operations, value.Operations[0].Clone()) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base.Clone()
			test.mutate(&candidate)
			data, err := json.Marshal(diskOperation{Kind: operationCheckpoint, Schema: SchemaVersion, Checkpoint: &candidate})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "tasks.jsonl")
			if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Fatal("tampered task operation checkpoint was accepted")
			}
		})
	}
}

func TestNewTaskOutboxRequiresDependenciesAndIsComposedOnlyByApp(t *testing.T) {
	store, wire, _ := newTaskOutboxStoreAndWire(t)
	if _, err := NewTaskOutbox(nil, wire); err == nil {
		t.Fatal("nil task store was accepted")
	}
	if _, err := NewTaskOutbox(store, nil); err == nil {
		t.Fatal("nil event wire was accepted")
	}

	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	var calls []string
	err = filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".worktrees", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(files, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch function := call.Fun.(type) {
			case *ast.Ident:
				name = function.Name
			case *ast.SelectorExpr:
				name = function.Sel.Name
			}
			if name == "NewTaskOutbox" {
				position := files.Position(call.Pos())
				relative, _ := filepath.Rel(repositoryRoot, position.Filename)
				if !strings.HasPrefix(filepath.ToSlash(relative), "internal/app/") {
					calls = append(calls, fmt.Sprintf("%s:%d", relative, position.Line))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("production constructs canonical task outbox outside internal/app: %v", calls)
	}
}

func newTaskOutboxHarness(t *testing.T) (*Store, *TaskOutbox, *eventwire.Journal) {
	t.Helper()
	store, wire, journal := newTaskOutboxStoreAndWire(t)
	outbox, err := NewTaskOutbox(store, wire)
	if err != nil {
		t.Fatal(err)
	}
	return store, outbox, journal
}

func newTaskOutboxStoreAndJournal(t *testing.T) (*Store, *eventwire.Wire, *eventwire.Journal) {
	t.Helper()
	return newTaskOutboxStoreAndWire(t)
}

func newTaskOutboxStoreAndWire(t *testing.T) (*Store, *eventwire.Wire, *eventwire.Journal) {
	t.Helper()
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "tasks.jsonl"))
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
	return store, wire, journal
}

func executeOutbox(t *testing.T, outbox *TaskOutbox, command CommandEnvelope, at time.Time) Result {
	t.Helper()
	result, err := outbox.Execute(context.Background(), command, at.UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Replayed {
		t.Fatal("task outbox response changed the legacy coordinator replay flag")
	}
	return result
}

// Journal paths are deliberately kept by the test harness; this helper makes
// the recovery intent explicit without reaching into unexported wire state.
func journalPath(_ *eventwire.Journal, root string) string {
	return filepath.Join(root, "events.jsonl")
}
