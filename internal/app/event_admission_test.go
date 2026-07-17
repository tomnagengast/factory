package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

func TestEventAdmissionDecidesBeforeRoutesAndProjectsContinuation(t *testing.T) {
	policyAdapter := newPolicyAdapterFixture(t, func() bool { return false })
	store := newAppRunStore(t)
	admitter, _ := runs.NewAdmitter(store)
	notifications := 0
	now := time.Date(2026, time.July, 17, 13, 0, 0, 0, time.UTC)
	admission, err := NewEventAdmission(policyAdapter.coordinator, admitter, func() { notifications++ }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	journal, err := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := admission.Register(wire); err != nil {
		t.Fatal(err)
	}
	routeObserved := false
	if err := wire.Handle(eventwire.Filter{Source: eventwire.SourceLinear}, func(_ context.Context, record eventwire.Record) error {
		model := appRunModel(t, store)
		routeObserved = len(model.Runs) == 1 && model.Runs[0].Causation.EventSequence == record.Sequence
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	event := eventwire.Event{
		ID: "linear:delivery-47", Source: eventwire.SourceLinear, Type: "Comment", Action: "create", Subject: "ENG-47",
		Attributes: map[string][]string{eventwire.AttributeProvenance: {"human"}}, ReceivedAt: now.Add(-time.Second),
	}
	record, added, err := wire.Publish(context.Background(), event)
	if err != nil || !added || !routeObserved || notifications != 1 {
		t.Fatalf("event admission = record %#v added=%t route=%t notifications=%d err=%v", record, added, routeObserved, notifications, err)
	}

	model := appRunModel(t, store)
	run := model.Runs[0]
	runAdapter, err := NewRunAdapter(store, policyAdapter.RegistrySnapshot, func() {})
	if err != nil {
		t.Fatal(err)
	}
	projected, created, err := runAdapter.ClaimContinuation(agentrun.ContinuationClaim{
		Trigger:  agentrun.Trigger{DeliveryID: "delivery-47", IssueIdentifier: "ENG-47"},
		Workflow: *run.Causation.Workflow, WorkflowDigest: run.Causation.WorkflowDigest,
		PolicyRevision: run.Causation.PolicyRevision,
	}, now)
	if err != nil || created || projected.ID != run.ID {
		t.Fatalf("continuation projection = %#v, %t, %v", projected, created, err)
	}

	if _, added, err := wire.Publish(context.Background(), event); err != nil || added || notifications != 1 || len(appRunModel(t, store).Runs) != 1 {
		t.Fatalf("wire duplicate = added=%t notifications=%d err=%v", added, notifications, err)
	}

	second := event
	second.ID = "linear:delivery-48"
	second.ReceivedAt = now.Add(time.Second)
	if _, added, err := wire.Publish(context.Background(), second); err != nil || !added || notifications != 2 {
		t.Fatalf("second feedback = added=%t notifications=%d err=%v", added, notifications, err)
	}
	coalesced, created, err := runAdapter.ClaimContinuation(agentrun.ContinuationClaim{
		Trigger: agentrun.Trigger{DeliveryID: "delivery-48", IssueIdentifier: "ENG-47"},
	}, now)
	if err != nil || created || coalesced.ID != run.ID || coalesced.DuplicateTriggers != 1 {
		t.Fatalf("coalesced continuation projection = %#v, %t, %v", coalesced, created, err)
	}
}

func TestEventAdmissionRoutesGenericPolicyThroughCanonicalRuns(t *testing.T) {
	policyAdapter := newPolicyAdapterFixture(t, func() bool { return false })
	store := newAppRunStore(t)
	admitter, _ := runs.NewAdmitter(store)
	now := time.Date(2026, time.July, 17, 13, 0, 0, 0, time.UTC)
	admission, _ := NewEventAdmission(policyAdapter.coordinator, admitter, func() {}, func() time.Time { return now })
	journal, _ := eventwire.Open(filepath.Join(t.TempDir(), "events.jsonl"), 100, nil)
	wire, _ := eventwire.New(journal)
	if err := admission.Register(wire); err != nil {
		t.Fatal(err)
	}
	event := eventwire.Event{
		ID: "linear:label-47", Source: eventwire.SourceLinear, Type: "Issue", Action: "update", Subject: "ENG-47",
		Attributes: map[string][]string{
			eventwire.AttributeActorID: {"actor-tom"}, triggerregistry.AttributeAddedLabel: {"FACTORY"},
		},
		ReceivedAt: now.Add(-time.Second),
	}
	if _, added, err := wire.Publish(context.Background(), event); err != nil || !added {
		t.Fatalf("generic event admission: added=%t err=%v", added, err)
	}
	model := appRunModel(t, store)
	if len(model.Runs) != 1 || model.Runs[0].Causation.EventSource != eventwire.SourceLinear || model.Runs[0].Causation.RuleID == "" {
		t.Fatalf("generic canonical Runs = %#v; registry=%#v batches=%#v", model.Runs, policyAdapter.RegistrySnapshot(), model.AdmissionBatches)
	}
}
