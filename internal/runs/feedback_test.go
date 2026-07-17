package runs

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

func TestLinearFeedbackAdmissionOwnsSourceEvidenceAndRetry(t *testing.T) {
	store := createEmptyStore(t, t.TempDir()+"/runs.jsonl", 10)
	t.Cleanup(func() { _ = store.Close() })
	admitter := mustAdmitter(t, store)
	snapshot := admissionPolicy(t, nil)
	record := linearFeedbackRecord(41, "linear:comment-41", "ENG-47")

	created, admitted, err := admitter.ContinueLinear(record, snapshot)
	if err != nil || !admitted {
		t.Fatalf("protected feedback = %#v, %t, %v", created, admitted, err)
	}
	digest, err := eventwire.CanonicalRecordDigest(record)
	if err != nil {
		t.Fatal(err)
	}
	model := snapshotModel(t, store)
	if len(model.AdmissionBatches) != 1 || len(model.AdmissionOperations) != 1 || len(model.Runs) != 1 {
		t.Fatalf("feedback projection = %#v", model)
	}
	batch := model.AdmissionBatches[0]
	if batch.Origin != AdmissionOriginEvent || batch.EventID != record.Event.ID || batch.EventSequence != record.Sequence ||
		batch.EventRecordDigest != digest || len(batch.Outcomes) != 1 || batch.Outcomes[0].RuleID != protectedLinearFeedbackRuleID ||
		created.Causation.Workflow == nil || created.Causation.Workflow.ID != snapshot.ProtectedWorkflows().LinearFeedback.WorkflowID {
		t.Fatalf("feedback evidence = batch %#v Run %#v", batch, created)
	}

	before := snapshotModel(t, store)
	retried, admitted, err := admitter.ContinueLinear(record, snapshot)
	if err != nil || admitted || !reflect.DeepEqual(retried, created) || !reflect.DeepEqual(snapshotModel(t, store), before) {
		t.Fatalf("protected retry = %#v, %t, %v", retried, admitted, err)
	}
}

func TestLinearFeedbackKeepsCustomizedGenericCommentRuleAdditive(t *testing.T) {
	rule := admissionRule("custom-comment", 3, eventwire.SourceLinear, 10, 100)
	rule.Filter.Type = "Comment"
	rule.Filter.Action = "create"
	rule.Filter.Attributes = map[string]string{eventwire.AttributeProvenance: "human"}
	store := createEmptyStore(t, t.TempDir()+"/runs.jsonl", 10)
	t.Cleanup(func() { _ = store.Close() })
	record := linearFeedbackRecord(42, "linear:comment-42", "ENG-47")

	protected, admitted, err := mustAdmitter(t, store).ContinueLinear(record, admissionPolicy(t, []triggerregistry.Rule{rule}))
	if err != nil || !admitted {
		t.Fatalf("additive feedback = %#v, %t, %v", protected, admitted, err)
	}
	model := snapshotModel(t, store)
	if len(model.AdmissionBatches) != 1 || len(model.AdmissionBatches[0].Outcomes) != 2 || len(model.Runs) != 2 {
		t.Fatalf("additive projection = %#v", model)
	}
	ruleIDs := []string{model.AdmissionBatches[0].Outcomes[0].RuleID, model.AdmissionBatches[0].Outcomes[1].RuleID}
	if !slices.Equal(ruleIDs, []string{"custom-comment", protectedLinearFeedbackRuleID}) {
		t.Fatalf("additive outcomes = %#v", ruleIDs)
	}
	for _, run := range model.Runs {
		if run.Causation.EventSequence != record.Sequence || run.Causation.BatchID != model.AdmissionBatches[0].ID {
			t.Fatalf("additive Run source evidence = %#v", run)
		}
	}
}

func TestLinearFeedbackCoalescesAndResumesAwaitingOwner(t *testing.T) {
	root := t.TempDir()
	store := createEmptyStore(t, root+"/runs.jsonl", 10)
	t.Cleanup(func() { _ = store.Close() })
	admitter := mustAdmitter(t, store)
	snapshot := admissionPolicy(t, nil)
	first := linearFeedbackRecord(43, "linear:comment-43", "ENG-47")
	owner, admitted, err := admitter.ContinueLinear(first, snapshot)
	if err != nil || !admitted {
		t.Fatalf("owner feedback = %#v, %t, %v", owner, admitted, err)
	}
	owner = moveLinearFeedbackToAwaiting(t, store, owner, root)
	ready := *owner.Ready

	second := linearFeedbackRecord(44, "linear:comment-44", "ENG-47")
	second.Event.ReceivedAt = owner.UpdatedAt.Add(time.Second)
	bookkeeping, admitted, err := admitter.ContinueLinear(second, snapshot)
	if err != nil || !admitted {
		t.Fatalf("coalesced feedback = %#v, %t, %v", bookkeeping, admitted, err)
	}
	updated := modelRun(t, snapshotModel(t, store), owner.ID)
	if bookkeeping.State != StateRejected || bookkeeping.Detail != "feedback-coalesced" || bookkeeping.Causation.ParentRunID != owner.ID ||
		updated.State != StatePending || updated.TriggerKind != triggerKindComment || updated.ResumeCount != owner.ResumeCount+1 ||
		updated.Detail != "task feedback received; resuming lifecycle" || !reflect.DeepEqual(updated.Ready, &ready) ||
		!slices.Contains(updated.DeliveryIDs, second.Event.ID) {
		t.Fatalf("coalesced bookkeeping %#v owner %#v", bookkeeping, updated)
	}
}

func TestLinearFeedbackRejectsNonhumanAndMalformedRecords(t *testing.T) {
	store := createEmptyStore(t, t.TempDir()+"/runs.jsonl", 10)
	t.Cleanup(func() { _ = store.Close() })
	admitter := mustAdmitter(t, store)
	snapshot := admissionPolicy(t, nil)
	valid := linearFeedbackRecord(45, "linear:comment-45", "ENG-47")
	tests := []struct {
		name   string
		mutate func(*eventwire.Record)
	}{
		{name: "factory provenance", mutate: func(record *eventwire.Record) {
			record.Event.Attributes[eventwire.AttributeProvenance] = []string{"factory"}
		}},
		{name: "missing provenance", mutate: func(record *eventwire.Record) { delete(record.Event.Attributes, eventwire.AttributeProvenance) }},
		{name: "wrong source", mutate: func(record *eventwire.Record) { record.Event.Source = eventwire.SourceGitHub }},
		{name: "wrong action", mutate: func(record *eventwire.Record) { record.Event.Action = "update" }},
		{name: "invalid task", mutate: func(record *eventwire.Record) { record.Event.Subject = "not-an-issue" }},
		{name: "missing sequence", mutate: func(record *eventwire.Record) { record.Sequence = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Event.Attributes = map[string][]string{eventwire.AttributeProvenance: {"human"}}
			test.mutate(&candidate)
			if _, _, err := admitter.ContinueLinear(candidate, snapshot); err == nil {
				t.Fatal("invalid feedback was admitted")
			}
		})
	}
	if len(snapshotModel(t, store).Runs) != 0 {
		t.Fatal("invalid feedback changed the Store")
	}
}

func linearFeedbackRecord(sequence uint64, id, subject string) eventwire.Record {
	return eventwire.Record{Sequence: sequence, Event: eventwire.Event{
		ID: id, Source: eventwire.SourceLinear, Type: "Comment", Action: "create", Subject: subject,
		Attributes: map[string][]string{eventwire.AttributeProvenance: {"human"}}, ReceivedAt: admissionTestNow.Add(time.Duration(sequence) * time.Second),
	}}
}

func moveLinearFeedbackToAwaiting(t *testing.T, store *Store, admitted Run, root string) Run {
	t.Helper()
	routing := nextLifecycleRun(admitted, StateRouting, admitted.UpdatedAt.Add(time.Second))
	if err := store.Transition(routing); err != nil {
		t.Fatal(err)
	}
	pending := nextLifecycleRun(routing, StatePending, routing.UpdatedAt.Add(time.Second))
	route := managerRoute(root)
	pending.Repository = &route
	if err := store.Transition(pending); err != nil {
		t.Fatal(err)
	}
	starting := nextLifecycleRun(pending, StateStarting, pending.UpdatedAt.Add(time.Second))
	starting.SessionName = taskSessionName(starting)
	starting.RunDirectory = runPath(root, starting.ID)
	starting.SegmentStartedAt = pointerTime(starting.UpdatedAt)
	if err := store.Transition(starting); err != nil {
		t.Fatal(err)
	}
	running := nextLifecycleRun(starting, StateRunning, starting.UpdatedAt.Add(time.Second))
	running.Attempts = 1
	running.Transitions[len(running.Transitions)-1].Attempts = 1
	running.StartedAt = pointerTime(running.UpdatedAt)
	if err := store.Transition(running); err != nil {
		t.Fatal(err)
	}
	awaiting := nextLifecycleRun(running, StateAwaitingHumanMerge, running.UpdatedAt.Add(time.Second))
	awaiting.Ready = &ReadyCheckpoint{
		ContractVersion: readyContractVersion, RunID: awaiting.ID, Task: awaiting.Causation.Task,
		Repository: awaiting.Repository.Repository, PullRequest: 18, BaseBranch: awaiting.Repository.DefaultBranch,
		HeadBranch: "eng-47-review", VerifiedHeadOID: strings.Repeat("a", 40),
		CreatedAt: running.UpdatedAt.Add(time.Nanosecond), ValidatedAt: awaiting.UpdatedAt,
	}
	if err := store.Transition(awaiting); err != nil {
		t.Fatal(err)
	}
	return modelRun(t, snapshotModel(t, store), awaiting.ID)
}
