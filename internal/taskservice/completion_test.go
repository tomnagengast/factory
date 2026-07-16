package taskservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskstore"
)

type completionFixture struct {
	task     taskstore.Task
	messages []taskstore.Message
	gates    []taskstore.Gate
	command  *taskstore.CompletionCommand
}

func (f *completionFixture) Find(string) (taskstore.Task, bool) { return f.task, true }

func (f *completionFixture) Messages(_ string, after uint64, limit int) (taskstore.MessagePage, error) {
	var page taskstore.MessagePage
	for _, message := range f.messages {
		if message.Ordinal > after && len(page.Messages) < limit {
			page.Messages = append(page.Messages, message)
		}
	}
	return page, nil
}

func (f *completionFixture) Gates(string) ([]taskstore.Gate, error) { return f.gates, nil }

func (f *completionFixture) Execute(_ context.Context, envelope taskstore.CommandEnvelope, now time.Time) (taskstore.Result, error) {
	if envelope.Completion == nil {
		return taskstore.Result{}, errors.New("missing completion")
	}
	f.command = envelope.Completion
	next := f.task
	next.State = taskstore.StateCompleted
	next.Completion = &taskstore.Completion{RunID: envelope.Completion.Completion.RunID, EvidenceRef: envelope.Completion.Completion.EvidenceRef, CompletedAt: now}
	next.CompletedAt = &now
	return taskstore.Result{Task: next}, nil
}

func TestCompleterRequiresApprovedGatesAndAnsweredHumanFeedback(t *testing.T) {
	now := time.Date(2026, time.July, 15, 22, 0, 0, 0, time.UTC)
	ref := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"}
	humanAt := now.Add(-2 * time.Minute)
	fixture := &completionFixture{task: taskstore.Task{
		Ref: ref, State: taskstore.StateInProgress, Revision: 8, LatestHumanAt: &humanAt,
		Routing: &taskstore.RoutingSnapshot{Repository: "tomnagengast/factory"},
	}, gates: []taskstore.Gate{
		approvedHumanGate("gate-0123456789abcdef", taskstore.GateKindResearch, now),
		approvedHumanGate("gate-fedcba9876543210", taskstore.GateKindPlan, now),
	}}
	completer, err := NewCompleter(fixture, fixture, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if complete, err := completer.Complete(t.Context(), ref, "run-0123456789abcdef", "tomnagengast/factory", "github:factory:merge"); err == nil || complete {
		t.Fatalf("unanswered completion = %t, %v", complete, err)
	}
	fixture.messages = []taskstore.Message{{Ordinal: 1, Author: taskstore.Actor{ID: "run:1", Kind: taskstore.AuthorAgent}, CreatedAt: now.Add(-time.Minute)}}
	complete, err := completer.Complete(t.Context(), ref, "run-0123456789abcdef", "tomnagengast/factory", "github:factory:merge")
	if err != nil || !complete || fixture.command == nil || fixture.command.Actor.Kind != taskstore.AuthorSystem {
		t.Fatalf("completion = %t command=%#v err=%v", complete, fixture.command, err)
	}
	fixture.command = nil
	fixture.gates[0].Status = taskstore.GateRevisionRequested
	if complete, err := completer.Complete(t.Context(), ref, "run-0123456789abcdef", "tomnagengast/factory", "github:factory:merge"); err == nil || complete || fixture.command != nil {
		t.Fatalf("revision gate completion = %t command=%#v err=%v", complete, fixture.command, err)
	}
}

func TestCompleterRequiresCurrentResearchAndPlanEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 15, 22, 0, 0, 0, time.UTC)
	ref := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"}
	base := taskstore.Task{
		Ref: ref, State: taskstore.StateInProgress, Revision: 8, ApprovalMode: taskstore.ApprovalGated,
		Routing: &taskstore.RoutingSnapshot{Repository: "tomnagengast/factory"},
	}
	tests := []struct {
		name  string
		gates []taskstore.Gate
		ok    bool
	}{
		{name: "none"},
		{name: "research only", gates: []taskstore.Gate{approvedHumanGate("gate-0123456789abcdef", taskstore.GateKindResearch, now)}},
		{name: "unrelated", gates: []taskstore.Gate{approvedHumanGate("gate-0123456789abcdef", "security", now)}},
		{name: "both", gates: []taskstore.Gate{
			approvedHumanGate("gate-0123456789abcdef", taskstore.GateKindResearch, now),
			approvedHumanGate("gate-fedcba9876543210", taskstore.GateKindPlan, now),
		}, ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := &completionFixture{task: base, gates: test.gates}
			completer, _ := NewCompleter(fixture, fixture, func() time.Time { return now })
			complete, err := completer.Complete(t.Context(), ref, "run-0123456789abcdef", "tomnagengast/factory", "github:factory:merge")
			if complete != test.ok || (err == nil) != test.ok {
				t.Fatalf("completion = %t err=%v, want ok=%t", complete, err, test.ok)
			}
		})
	}

	revised := approvedHumanGate("gate-aaaaaaaaaaaaaaaa", taskstore.GateKindPlan, now.Add(-time.Minute))
	revised.Status = taskstore.GateRevisionRequested
	revised.Decision.Action = taskstore.DecisionRevise
	fixture := &completionFixture{task: base, gates: []taskstore.Gate{
		approvedHumanGate("gate-bbbbbbbbbbbbbbbb", taskstore.GateKindResearch, now), revised,
		approvedHumanGate("gate-cccccccccccccccc", taskstore.GateKindPlan, now),
	}}
	completer, _ := NewCompleter(fixture, fixture, func() time.Time { return now })
	if complete, err := completer.Complete(t.Context(), ref, "run-0123456789abcdef", "tomnagengast/factory", "github:factory:merge"); err != nil || !complete {
		t.Fatalf("superseded revision gate completion = %t err=%v", complete, err)
	}
}

func approvedHumanGate(id, kind string, now time.Time) taskstore.Gate {
	return taskstore.Gate{
		ID: id, TaskID: "task-0123456789abcdef", Kind: kind, Mode: taskstore.ApprovalGated,
		Status: taskstore.GateApproved, ArtifactURL: "https://example.com/" + kind,
		OpenedBy: taskstore.Actor{ID: "run:1", Kind: taskstore.AuthorAgent}, OpenedAt: now.Add(-time.Minute),
		Decision: &taskstore.GateDecision{Action: taskstore.DecisionApprove, Actor: taskstore.Actor{ID: "human", Kind: taskstore.AuthorHuman}, DecidedAt: now},
	}
}

func TestCompleterAcceptsOnlyExactExistingEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 15, 22, 0, 0, 0, time.UTC)
	ref := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"}
	fixture := &completionFixture{task: taskstore.Task{
		Ref: ref, State: taskstore.StateCompleted,
		Completion: &taskstore.Completion{RunID: "run-0123456789abcdef", EvidenceRef: "github:factory:merge", CompletedAt: now},
	}}
	completer, err := NewCompleter(fixture, fixture, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if complete, err := completer.Complete(t.Context(), ref, "run-0123456789abcdef", "tomnagengast/factory", "github:factory:merge"); err != nil || !complete {
		t.Fatalf("exact completion = %t, %v", complete, err)
	}
	if complete, err := completer.Complete(t.Context(), ref, "run-other", "tomnagengast/factory", "github:factory:merge"); err != nil || complete {
		t.Fatalf("other completion = %t, %v", complete, err)
	}
}
