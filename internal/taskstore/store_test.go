package taskstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

var (
	testNow    = time.Date(2026, time.July, 15, 20, 0, 0, 0, time.UTC)
	humanActor = Actor{ID: "human@example.com", Kind: AuthorHuman}
	agentActor = Actor{ID: "run-0123456789abcdef", Kind: AuthorAgent}
)

func TestStoreNativeTaskLifecycleAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	create := CreateCommand{Actor: humanActor, Title: "Native task", Description: "Private body", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "create-1"}
	task, replayed, err := store.Create(create, testNow)
	if err != nil || replayed {
		t.Fatalf("create: task=%#v replayed=%t err=%v", task, replayed, err)
	}
	if task.Ref.Identifier != "FAC-1" || task.Ref.Source != taskmodel.SourceFactory || task.Revision != 1 {
		t.Fatalf("created task = %#v", task)
	}
	retry, replayed, err := store.Create(create, testNow.Add(time.Hour))
	if err != nil || !replayed || !reflect.DeepEqual(retry, task) {
		t.Fatalf("create retry: task=%#v replayed=%t err=%v", retry, replayed, err)
	}
	conflicting := create
	conflicting.Title = "Different"
	if _, _, err := store.Create(conflicting, testNow); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}

	task, _, err = store.Update(UpdateCommand{Actor: humanActor, TaskID: task.Ref.ProviderID, ExpectedRevision: 1, Title: "Updated task", Description: "Private body", ApprovalMode: ApprovalGated, IdempotencyKey: "update-1"}, testNow.Add(time.Minute))
	if err != nil || task.Revision != 2 || task.Title != "Updated task" {
		t.Fatalf("update: task=%#v err=%v", task, err)
	}
	if _, _, err := store.Update(UpdateCommand{Actor: humanActor, TaskID: task.Ref.ProviderID, ExpectedRevision: 1, Title: task.Title, Description: task.Description, ApprovalMode: task.ApprovalMode, IdempotencyKey: "stale"}, testNow); !isRevisionConflict(err) {
		t.Fatalf("stale revision = %v", err)
	}

	task, message, _, err := store.AddMessage(MessageCommand{Actor: humanActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Body: "Human comment", IdempotencyKey: "message-1"}, testNow.Add(2*time.Minute))
	if err != nil || message.Ordinal != 1 || task.MessageCount != 1 || task.LatestHumanAt == nil {
		t.Fatalf("message: task=%#v message=%#v err=%v", task, message, err)
	}
	task, reply, _, err := store.AddMessage(MessageCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, ParentID: message.ID, Body: "Agent reply", IdempotencyKey: "message-2"}, testNow.Add(3*time.Minute))
	if err != nil || reply.ParentID != message.ID || task.MessageCount != 2 {
		t.Fatalf("reply: task=%#v message=%#v err=%v", task, reply, err)
	}

	task, link, _, err := store.AddLink(LinkCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Label: "Pull request", URL: "https://github.com/tomnagengast/factory/pull/15", IdempotencyKey: "link-1"}, testNow.Add(4*time.Minute))
	if err != nil || link.TaskID != task.Ref.ProviderID || task.LinkCount != 1 {
		t.Fatalf("link: task=%#v link=%#v err=%v", task, link, err)
	}

	task, gate, _, err := store.OpenGate(GateCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Kind: "research", Mode: ApprovalGated, ArtifactURL: "https://github.com/tomnagengast/factory/blob/main/research.md", IdempotencyKey: "gate-1"}, testNow.Add(5*time.Minute))
	if err != nil || gate.Status != GateOpen || task.GateCount != 1 {
		t.Fatalf("gate: task=%#v gate=%#v err=%v", task, gate, err)
	}
	task, gate, _, err = store.DecideGate(DecisionCommand{Actor: humanActor, TaskID: task.Ref.ProviderID, GateID: gate.ID, ExpectedRevision: task.Revision, Action: DecisionApprove, IdempotencyKey: "decision-1"}, testNow.Add(6*time.Minute))
	if err != nil || gate.Status != GateApproved || gate.Decision == nil || task.LatestHumanAt == nil {
		t.Fatalf("decision: task=%#v gate=%#v err=%v", task, gate, err)
	}

	routing := RoutingSnapshot{ProjectID: task.ProjectID, Repository: "tomnagengast/factory", RepositoryURL: "https://github.com/tomnagengast/factory", RepositoryPath: "/tmp/factory", ManagedRoot: "/tmp", BaseBranch: "main", WorkflowID: "full-sdlc-provider-neutral", WorkflowDigest: "0123456789abcdef", AdmittedAt: testNow.Add(7 * time.Minute)}
	task, _, err = store.SetRouting(RoutingCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Routing: routing, IdempotencyKey: "routing-1"}, routing.AdmittedAt)
	if err != nil || task.State != StateInProgress || task.Routing == nil {
		t.Fatalf("routing: task=%#v err=%v", task, err)
	}
	task, _, err = store.Complete(CompletionCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Completion: Completion{RunID: "run-0123456789abcdef", EvidenceRef: "checkpoint:run-0123456789abcdef"}, IdempotencyKey: "complete-1"}, testNow.Add(8*time.Minute))
	if err != nil || task.State != StateCompleted || task.Completion == nil || task.CompletedAt == nil {
		t.Fatalf("completion: task=%#v err=%v", task, err)
	}

	if err := store.Compact(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, found := reopened.Find(task.Ref.ProviderID)
	if !found || !reflect.DeepEqual(got, task) {
		t.Fatalf("reopened task = %#v found=%t, want %#v", got, found, task)
	}
	messages, err := reopened.Messages(task.Ref.ProviderID, 0, 10)
	if err != nil || !reflect.DeepEqual(messages.Messages, []Message{message, reply}) {
		t.Fatalf("reopened messages = %#v err=%v", messages, err)
	}
	if gates, err := reopened.Gates(task.Ref.ProviderID); err != nil || len(gates) != 1 || gates[0].Status != GateApproved {
		t.Fatalf("reopened gates = %#v err=%v", gates, err)
	}
}

func TestStoreAutomaticGateRecordsExplicitDecision(t *testing.T) {
	store := openTestStore(t)
	task := createTestTask(t, store, ApprovalAutomatic, "create-auto")
	next, gate, _, err := store.OpenGate(GateCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Kind: "plan", IdempotencyKey: "gate-auto"}, testNow.Add(time.Minute))
	if err != nil || next.Revision != 2 || gate.Mode != ApprovalAutomatic || gate.Status != GateApproved || gate.Decision == nil || gate.Decision.Actor.Kind != AuthorSystem {
		t.Fatalf("automatic gate: task=%#v gate=%#v err=%v", next, gate, err)
	}
}

func TestStoreRejectsCrossTaskReplyAndAgentGateDecision(t *testing.T) {
	store := openTestStore(t)
	first := createTestTask(t, store, ApprovalGated, "first")
	second, _, err := store.Create(CreateCommand{Actor: humanActor, Title: "Second", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "second"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	first, message, _, err := store.AddMessage(MessageCommand{Actor: humanActor, TaskID: first.Ref.ProviderID, ExpectedRevision: first.Revision, Body: "First", IdempotencyKey: "first-message"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.AddMessage(MessageCommand{Actor: humanActor, TaskID: second.Ref.ProviderID, ExpectedRevision: second.Revision, ParentID: message.ID, Body: "Cross task", IdempotencyKey: "cross"}, testNow); err == nil {
		t.Fatal("cross-task reply was accepted")
	}
	first, gate, _, err := store.OpenGate(GateCommand{Actor: agentActor, TaskID: first.Ref.ProviderID, ExpectedRevision: first.Revision, Kind: "plan", Mode: ApprovalGated, IdempotencyKey: "first-gate"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.DecideGate(DecisionCommand{Actor: agentActor, TaskID: first.Ref.ProviderID, GateID: gate.ID, ExpectedRevision: first.Revision, Action: DecisionApprove, IdempotencyKey: "agent-decision"}, testNow); err == nil {
		t.Fatal("agent gate decision was accepted")
	}
}

func TestStoreRecoversTornTailAndRejectsCompleteCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	createTestTask(t, store, ApprovalGated, "create")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendFile(path, []byte(`{"kind":"update"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("open after torn tail: %v", err)
	}
	if after, err := os.Stat(path); err != nil || after.Size() != info.Size() {
		t.Fatalf("torn tail size = %v err=%v, want %d", after, err, info.Size())
	}
	if err := appendFile(path, []byte("not-json\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("complete corrupt record was accepted")
	}
}

func TestStoreAppendFailureDoesNotMutateProjection(t *testing.T) {
	store := openTestStore(t)
	store.write = func(_ *os.File, _ []byte) (int, error) { return 0, errors.New("write failed") }
	if _, _, err := store.Create(CreateCommand{Actor: humanActor, Title: "Failed", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "failed"}, testNow); err == nil {
		t.Fatal("failed append succeeded")
	}
	if got := store.Snapshot(); len(got.Tasks) != 0 || got.NextSequence != 1 {
		t.Fatalf("projection mutated after failed append: %#v", got)
	}
}

func TestStoreRejectsNewMutationsAfterCompletionButReplaysHistory(t *testing.T) {
	store := openTestStore(t)
	task := createTestTask(t, store, ApprovalGated, "create-terminal")
	routing := RoutingSnapshot{ProjectID: task.ProjectID, Repository: "tomnagengast/factory", RepositoryURL: "https://github.com/tomnagengast/factory", RepositoryPath: "/tmp/factory", ManagedRoot: "/tmp", BaseBranch: "main", WorkflowID: "full-sdlc-provider-neutral", WorkflowDigest: "digest", AdmittedAt: testNow}
	route := RoutingCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Routing: routing, IdempotencyKey: "route-terminal"}
	task, replayed, err := store.SetRouting(route, testNow)
	if err != nil || replayed {
		t.Fatalf("route: task=%#v replayed=%t err=%v", task, replayed, err)
	}
	complete := CompletionCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Completion: Completion{RunID: "run-0123456789abcdef", EvidenceRef: "checkpoint:ready"}, IdempotencyKey: "complete-terminal"}
	task, _, err = store.Complete(complete, testNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := store.SetRouting(route, testNow.Add(2*time.Minute)); err != nil || !replayed {
		t.Fatalf("historical retry replayed=%t err=%v", replayed, err)
	}

	checks := []struct {
		name string
		run  func() error
	}{
		{name: "update", run: func() error {
			_, _, err := store.Update(UpdateCommand{Actor: humanActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Title: task.Title, ApprovalMode: task.ApprovalMode, IdempotencyKey: "terminal-update"}, testNow)
			return err
		}},
		{name: "message", run: func() error {
			_, _, _, err := store.AddMessage(MessageCommand{Actor: humanActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Body: "late feedback", IdempotencyKey: "terminal-message"}, testNow)
			return err
		}},
		{name: "link", run: func() error {
			_, _, _, err := store.AddLink(LinkCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Label: "late", URL: "https://example.com/late", IdempotencyKey: "terminal-link"}, testNow)
			return err
		}},
		{name: "gate", run: func() error {
			_, _, _, err := store.OpenGate(GateCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Kind: GateKindPlan, Mode: ApprovalGated, IdempotencyKey: "terminal-gate"}, testNow)
			return err
		}},
		{name: "state", run: func() error {
			_, _, err := store.ChangeState(StateCommand{Actor: humanActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, State: StateCanceled, IdempotencyKey: "terminal-state"}, testNow)
			return err
		}},
		{name: "completion", run: func() error {
			_, _, err := store.Complete(CompletionCommand{Actor: agentActor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Completion: Completion{RunID: "run-fedcba9876543210", EvidenceRef: "other"}, IdempotencyKey: "terminal-completion"}, testNow)
			return err
		}},
	}
	before := store.Snapshot()
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, ErrTerminalTask) {
				t.Fatalf("error = %v, want ErrTerminalTask", err)
			}
		})
	}
	if after := store.Snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatal("terminal mutation attempts changed the task projection")
	}
}

func TestStoreReopensAndPagesThousandTasksAndTenThousandMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	snapshot := workloadSnapshot(1000, 10)
	operation := diskOperation{Kind: operationCheckpoint, Schema: SchemaVersion, Checkpoint: &snapshot}
	data, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	started := time.Now()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.List("", 100)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages(page.Tasks[0].Ref.ProviderID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("reopen and first pages took %s, want <= 1s", elapsed)
	}
	if len(page.Tasks) != 100 || page.NextCursor == "" || len(messages.Messages) != 10 || store.Snapshot().NextSequence != 1001 {
		t.Fatalf("workload pages: tasks=%d cursor=%q messages=%d", len(page.Tasks), page.NextCursor, len(messages.Messages))
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func createTestTask(t *testing.T, store *Store, approval, key string) Task {
	t.Helper()
	task, _, err := store.Create(CreateCommand{Actor: humanActor, Title: "Test task", ProjectID: "project-1", ApprovalMode: approval, IdempotencyKey: key}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func isRevisionConflict(err error) bool {
	var conflict RevisionConflict
	return errors.As(err, &conflict)
}

func appendFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}

func workloadSnapshot(taskCount, messagesPerTask int) Snapshot {
	snapshot := Snapshot{Schema: SchemaVersion, NextSequence: uint64(taskCount + 1)}
	for index := 1; index <= taskCount; index++ {
		providerID := fmt.Sprintf("task-%016x", index)
		activity := testNow.Add(time.Duration(index) * time.Second)
		task := Task{
			Ref:      taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: providerID, Identifier: fmt.Sprintf("FAC-%d", index)},
			Sequence: uint64(index), Title: fmt.Sprintf("Task %d", index), ProjectID: "project-1", ApprovalMode: ApprovalGated,
			State: StateOpen, Revision: uint64(messagesPerTask + 1), CreatedBy: humanActor, CreatedAt: testNow,
			UpdatedAt: activity, LatestActivityAt: activity, MessageCount: uint64(messagesPerTask),
		}
		snapshot.Tasks = append(snapshot.Tasks, task)
		for ordinal := 1; ordinal <= messagesPerTask; ordinal++ {
			snapshot.Messages = append(snapshot.Messages, Message{
				ID: fmt.Sprintf("msg-%016x", index*messagesPerTask+ordinal), TaskID: providerID, Ordinal: uint64(ordinal),
				Body: fmt.Sprintf("Message %d", ordinal), Author: agentActor, CreatedAt: activity,
			})
		}
	}
	return snapshot
}

func TestCommandDigestIsStable(t *testing.T) {
	command := CreateCommand{Actor: humanActor, Title: "Stable", ProjectID: "project-1", ApprovalMode: ApprovalGated, IdempotencyKey: "stable"}
	if digest := commandDigest(operationCreate, command); digest == "" || digest != commandDigest(operationCreate, command) {
		t.Fatal("command digest is unstable")
	}
}
