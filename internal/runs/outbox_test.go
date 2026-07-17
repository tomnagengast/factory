package runs

import (
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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

// scriptedWire is a controllable OutboxWire. By default it assigns monotonic
// sequences, deduplicates by event ID, and reports every published record as
// dispatched. Tests override onPublish or hold the dispatched cursor to
// exercise recovery, dispatch errors, and acknowledgement gating.
type scriptedWire struct {
	seq          uint64
	dispatched   uint64
	holdDispatch bool
	byID         map[string]eventwire.Record
	published    []eventwire.Event
	onPublish    func(*scriptedWire, eventwire.Event) (eventwire.Record, bool, error)
}

func newScriptedWire() *scriptedWire {
	return &scriptedWire{byID: make(map[string]eventwire.Record)}
}

func (w *scriptedWire) Publish(_ context.Context, event eventwire.Event) (eventwire.Record, bool, error) {
	if w.onPublish != nil {
		return w.onPublish(w, event)
	}
	return w.append(event)
}

func (w *scriptedWire) append(event eventwire.Event) (eventwire.Record, bool, error) {
	if record, ok := w.byID[event.ID]; ok {
		return record, false, nil
	}
	w.seq++
	record := eventwire.Record{Sequence: w.seq, Event: event}
	w.byID[event.ID] = record
	w.published = append(w.published, event)
	if !w.holdDispatch {
		w.dispatched = w.seq
	}
	return record, true, nil
}

func (w *scriptedWire) Status() eventwire.Status {
	return eventwire.Status{Total: w.seq, Dispatched: w.dispatched}
}

func admitRunnableRun(t *testing.T, store *Store) Run {
	t.Helper()
	rule := admissionRule("rule-one", 1, eventwire.SourceLinear, 10, 100)
	snapshot := admissionPolicy(t, []triggerregistry.Rule{rule})
	record := admissionRecordFor(1, "linear:evt-1", eventwire.SourceLinear, "Issue", "update", "TST-1", nil)
	if _, err := mustAdmitter(t, store).AdmitBatch([]eventwire.Record{record}, snapshot, admissionTestNow); err != nil {
		t.Fatal(err)
	}
	model := mustRunModel(t, store)
	if len(model.Runs) != 1 {
		t.Fatalf("expected exactly one admitted run, got %d", len(model.Runs))
	}
	return model.Runs[0]
}

func onlyDelivery(t *testing.T, store *Store, runID string) TransitionDelivery {
	t.Helper()
	run := runByID(t, store, runID)
	if len(run.TransitionDeliveries) != 1 {
		t.Fatalf("run %s has %d deliveries, want 1", runID, len(run.TransitionDeliveries))
	}
	return run.TransitionDeliveries[0]
}

func runByID(t *testing.T, store *Store, runID string) Run {
	t.Helper()
	for _, run := range mustRunModel(t, store).Runs {
		if run.ID == runID {
			return run
		}
	}
	t.Fatalf("run %s not found", runID)
	return Run{}
}

func TestAdmissionDerivesExactlyOnePendingDeliveryPerInitialTransition(t *testing.T) {
	store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
	run := admitRunnableRun(t, store)
	if run.State != StateAdmitted || len(run.Transitions) != 1 || run.DeliveredThrough != 0 {
		t.Fatalf("initial admitted run = %#v", run)
	}
	if len(run.TransitionDeliveries) != 1 {
		t.Fatalf("admission derived %d deliveries, want exactly one", len(run.TransitionDeliveries))
	}
	delivery := run.TransitionDeliveries[0]
	transition := run.Transitions[0]
	if delivery.TransitionID != transition.ID || delivery.State != DeliveryPending || delivery.Sequence != 0 ||
		delivery.EventID != RunTransitionEventID(transition.ID) {
		t.Fatalf("initial pending delivery = %#v", delivery)
	}
}

func TestApplyAdmissionBatchDerivesDeliveryStateInsteadOfTrustingCaller(t *testing.T) {
	for _, test := range []struct {
		name  string
		forge func(*Run)
	}{
		{
			name: "pre-acknowledged",
			forge: func(run *Run) {
				run.DeliveredThrough = len(run.Transitions)
				run.TransitionDeliveries = nil
			},
		},
		{
			name: "published with forged sequence",
			forge: func(run *Run) {
				run.DeliveredThrough = 0
				transition := run.Transitions[0]
				run.TransitionDeliveries = []TransitionDelivery{{
					TransitionID: transition.ID,
					EventID:      RunTransitionEventID(transition.ID),
					State:        DeliveryPublished,
					Sequence:     99,
				}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := trustedTestRoot(t, t.TempDir())
			store := createEmptyStore(t, filepath.Join(root, "runs.jsonl"), 10)
			batch, run, rate := testAdmissionProjection(t, root, 1, StatePending)
			test.forge(&run)
			if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
				t.Fatal(err)
			}
			stored := runByID(t, store, run.ID)
			if stored.DeliveredThrough != 0 || len(stored.TransitionDeliveries) != 1 {
				t.Fatalf("store trusted caller delivery state: %#v", stored)
			}
			delivery := stored.TransitionDeliveries[0]
			if delivery.TransitionID != stored.Transitions[0].ID ||
				delivery.EventID != RunTransitionEventID(stored.Transitions[0].ID) ||
				delivery.State != DeliveryPending || delivery.Sequence != 0 {
				t.Fatalf("store did not derive the pending delivery: %#v", delivery)
			}
		})
	}
}

func TestAdmissionDerivesNoDeliveryForNonRunnableOrMigratedBaseline(t *testing.T) {
	t.Run("non-runnable batch derives no delivery", func(t *testing.T) {
		store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
		rule := admissionRule("rule-one", 1, eventwire.SourceLinear, 10, 100)
		snapshot := admissionPolicy(t, []triggerregistry.Rule{rule})
		// A GitHub event does not match a Linear rule, so it is suppressed with
		// no runnable outcome and therefore no delivery.
		record := admissionRecordFor(1, "github:evt-1", eventwire.SourceGitHub, "Issue", "update", "TST-1", nil)
		if _, err := mustAdmitter(t, store).AdmitBatch([]eventwire.Record{record}, snapshot, admissionTestNow); err != nil {
			t.Fatal(err)
		}
		model := mustRunModel(t, store)
		if len(model.Runs) != 0 {
			t.Fatalf("non-runnable admission created runs: %#v", model.Runs)
		}
	})

	t.Run("migrated baseline exemption derives no delivery", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		batch, run, rate := runningProjection(t, root)
		run.Transitions, run.DeliveredThrough = nil, 0
		run.MigratedBaseline = &MigratedBaseline{State: run.State, ObservedAt: run.UpdatedAt, PriorTransitionsAcknowledged: true}
		snapshot, err := NewSnapshot(testSingleAdmissionModel(batch, run, rate))
		if err != nil {
			t.Fatal(err)
		}
		migrated := snapshot.Model().Runs[0]
		if migrated.DeliveredThrough != 0 || len(migrated.TransitionDeliveries) != 0 {
			t.Fatalf("migrated baseline carried a delivery obligation: %#v", migrated)
		}
	})
}

func TestAdmissionDuplicateAndReplayPreserveDeliveryAndReceipt(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	path := filepath.Join(root, "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	rule := admissionRule("rule-one", 1, eventwire.SourceLinear, 10, 100)
	snapshot := admissionPolicy(t, []triggerregistry.Rule{rule})
	records := []eventwire.Record{admissionRecordFor(1, "linear:evt-1", eventwire.SourceLinear, "Issue", "update", "TST-1", nil)}
	admitter := mustAdmitter(t, store)
	if _, err := admitter.AdmitBatch(records, snapshot, admissionTestNow); err != nil {
		t.Fatal(err)
	}
	before := mustRunModel(t, store)

	// An exact duplicate admission is a no-op and cannot duplicate the pending
	// delivery, mint a second run, or change the receipt totals.
	if _, err := admitter.AdmitBatch(records, snapshot, admissionTestNow); err != nil {
		t.Fatalf("exact duplicate admission: %v", err)
	}
	after := mustRunModel(t, store)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("duplicate admission changed the projection")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root, path, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed.Model(), before) {
		t.Fatal("replay changed the admitted delivery or receipt")
	}
}

func TestTransitionAppendsOnePendingDeliveryAndStoreOwnsState(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	store := createEmptyStore(t, filepath.Join(root, "runs.jsonl"), 10)
	run := admitRunnableRun(t, store)

	routing := run.Clone()
	routing.State = StateRouting
	routing.UpdatedAt = run.UpdatedAt.Add(time.Second)
	routing.Transitions = append(routing.Transitions, LifecycleTransition{
		ID: run.ID + ":routing", State: StateRouting, At: routing.UpdatedAt,
	})
	// A caller cannot rewrite existing delivery state: tamper the carried
	// delivery and forge a fresh one; the store must ignore both and derive.
	routing.TransitionDeliveries = []TransitionDelivery{{
		TransitionID: "forged", EventID: "factory:run-transition:forged", State: DeliveryPublished, Sequence: 999,
	}}
	routing.DeliveredThrough = 5
	if err := store.Transition(routing); err != nil {
		t.Fatalf("transition to routing: %v", err)
	}

	stored := runByID(t, store, run.ID)
	if stored.DeliveredThrough != 0 || len(stored.TransitionDeliveries) != 2 {
		t.Fatalf("stored delivery suffix = %#v (watermark %d)", stored.TransitionDeliveries, stored.DeliveredThrough)
	}
	first, second := stored.TransitionDeliveries[0], stored.TransitionDeliveries[1]
	if first.TransitionID != run.ID+":admitted" || first.State != DeliveryPending {
		t.Fatalf("existing delivery was rewritten: %#v", first)
	}
	if second.TransitionID != run.ID+":routing" || second.State != DeliveryPending || second.Sequence != 0 ||
		second.EventID != RunTransitionEventID(run.ID+":routing") {
		t.Fatalf("appended delivery = %#v", second)
	}
}

func TestTransitionCannotRewriteAPublishedDeliveryPrefix(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	store := createEmptyStore(t, filepath.Join(root, "runs.jsonl"), 10)
	run := admitRunnableRun(t, store)
	admitted := run.Transitions[0]
	if err := store.RecordPublication(run.ID, admitted.ID, 7); err != nil {
		t.Fatal(err)
	}

	routing := run.Clone()
	routing.State = StateRouting
	routing.UpdatedAt = run.UpdatedAt.Add(time.Second)
	routing.Transitions = append(routing.Transitions, LifecycleTransition{ID: run.ID + ":routing", State: StateRouting, At: routing.UpdatedAt})
	if err := store.Transition(routing); err != nil {
		t.Fatal(err)
	}
	stored := runByID(t, store, run.ID)
	if len(stored.TransitionDeliveries) != 2 || stored.TransitionDeliveries[0].State != DeliveryPublished ||
		stored.TransitionDeliveries[0].Sequence != 7 {
		t.Fatalf("published prefix was disturbed by a later transition: %#v", stored.TransitionDeliveries)
	}
}

func TestTransitionEventIsBodyFreeAndLegacyCompatible(t *testing.T) {
	store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
	run := admitRunnableRun(t, store)
	run.SessionName = "factory-secret-session"
	run.RunDirectory = "/srv/factory/private-run"
	transition := run.Transitions[0]

	event := TransitionEvent(run, transition)
	if event.ID != "factory:run-transition:"+transition.ID || event.ID != RunTransitionEventID(transition.ID) {
		t.Fatalf("event id = %q", event.ID)
	}
	if event.Source != eventwire.SourceFactory || event.Type != "agent-run" || event.Action != string(transition.State) ||
		event.Subject != run.Causation.Task.Identifier || !event.ReceivedAt.Equal(transition.At) {
		t.Fatalf("event envelope = %#v", event)
	}
	want := map[string][]string{
		"runId":                       {run.ID},
		"attempts":                    {strconv.Itoa(transition.Attempts)},
		"taskSource":                  {string(run.Causation.Task.Source)},
		"taskProviderId":              {run.Causation.Task.ProviderID},
		"taskIdentifier":              {run.Causation.Task.Identifier},
		eventwire.AttributeProducer:   {"agent-collector"},
		eventwire.AttributeProvenance: {"factory"},
	}
	if !reflect.DeepEqual(event.Attributes, want) {
		t.Fatalf("event attributes = %#v", event.Attributes)
	}
	// The admitted event is derived (hop >= 1) and carries this Run's admission
	// as its parent identity.
	if event.Hop != run.Causation.Hop || event.RootEventID != run.Causation.RootEventID ||
		event.ParentInvocationID != run.Causation.AdmissionID || event.ParentRunID != run.ID {
		t.Fatalf("event causation = %#v", event)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("body-free event is invalid: %v", err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"factory-secret-session", "/srv/factory/private-run", "Full SDLC", "Markdown"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("lifecycle event leaked a private body %q: %s", secret, encoded)
		}
	}
}

func TestTransitionEventPreservesRootlessDirectValidity(t *testing.T) {
	store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
	run := admitRunnableRun(t, store)
	// Simulate a rootless (hop-zero) migrated-direct Run: the lifecycle event
	// must remain a valid direct event with no parent identity.
	run.Causation.Hop = 0
	run.Causation.AncestorRuleIDs = nil
	event := TransitionEvent(run, run.Transitions[0])
	if event.Hop != 0 || event.ParentInvocationID != "" || event.ParentRunID != "" || len(event.AncestorRuleIDs) != 0 {
		t.Fatalf("rootless event carried parent identity: %#v", event)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("rootless direct event is invalid: %v", err)
	}
}

func TestOutboxCollectorPublishesThenAcknowledgesThroughRealWire(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	store := createEmptyStore(t, filepath.Join(root, "runs.jsonl"), 10)
	run := admitRunnableRun(t, store)

	journal, err := eventwire.Open(filepath.Join(root, "events.jsonl"), 128, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatal(err)
	}
	collector, err := NewOutboxCollector(store, wire)
	if err != nil {
		t.Fatal(err)
	}

	// The real wire dispatches synchronously with no handlers, so one Deliver
	// pass publishes the pending delivery and acknowledges it once the wire
	// reports it dispatched.
	if err := collector.Deliver(context.Background()); err != nil {
		t.Fatalf("first deliver: %v", err)
	}
	acknowledged := runByID(t, store, run.ID)
	if acknowledged.DeliveredThrough != 1 || len(acknowledged.TransitionDeliveries) != 0 {
		t.Fatalf("delivery after acknowledgement = %#v (watermark %d)", acknowledged.TransitionDeliveries, acknowledged.DeliveredThrough)
	}
	if _, found := wire.Record(1); !found {
		t.Fatal("wire did not durably retain the published lifecycle record")
	}

	// Delivery is idempotent: a further pass changes nothing.
	if err := collector.Deliver(context.Background()); err != nil {
		t.Fatalf("idempotent deliver: %v", err)
	}
	if got := runByID(t, store, run.ID); got.DeliveredThrough != 1 || len(got.TransitionDeliveries) != 0 {
		t.Fatalf("idempotent deliver changed state: %#v", got)
	}
}

func TestOutboxCollectorPublicationScenarios(t *testing.T) {
	t.Run("duplicate publish reuses the same sequence", func(t *testing.T) {
		store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
		run := admitRunnableRun(t, store)
		wire := newScriptedWire()
		// Pre-publish the exact event so the collector observes a duplicate on
		// recovery and adopts the existing authoritative sequence.
		record, added, err := wire.Publish(context.Background(), TransitionEvent(run, run.Transitions[0]))
		if err != nil || !added {
			t.Fatalf("seed publish = %#v added=%t err=%v", record, added, err)
		}
		collector, _ := NewOutboxCollector(store, wire)
		if err := collector.Deliver(context.Background()); err != nil {
			t.Fatalf("deliver: %v", err)
		}
		delivery := runByID(t, store, run.ID).TransitionDeliveries
		if len(delivery) != 0 || runByID(t, store, run.ID).DeliveredThrough != 1 {
			// With auto-dispatch the record is also acknowledged in the same pass.
			t.Fatalf("recovery did not adopt the duplicate sequence: %#v", runByID(t, store, run.ID))
		}
	})

	t.Run("returned record with dispatch error is persisted before the error", func(t *testing.T) {
		store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
		run := admitRunnableRun(t, store)
		wire := newScriptedWire()
		wire.onPublish = func(w *scriptedWire, event eventwire.Event) (eventwire.Record, bool, error) {
			record, _, _ := w.append(event)
			return record, true, errors.New("dispatch failed after append")
		}
		collector, _ := NewOutboxCollector(store, wire)
		err := collector.Deliver(context.Background())
		if err == nil || !strings.Contains(err.Error(), "dispatch failed") {
			t.Fatalf("deliver error = %v", err)
		}
		delivery := onlyDelivery(t, store, run.ID)
		if delivery.State != DeliveryPublished || delivery.Sequence == 0 {
			t.Fatalf("publication evidence was not persisted before the error: %#v", delivery)
		}
	})

	t.Run("failure before wire append persists nothing", func(t *testing.T) {
		store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
		run := admitRunnableRun(t, store)
		wire := newScriptedWire()
		wire.onPublish = func(_ *scriptedWire, _ eventwire.Event) (eventwire.Record, bool, error) {
			return eventwire.Record{}, false, errors.New("wire append refused")
		}
		collector, _ := NewOutboxCollector(store, wire)
		if err := collector.Deliver(context.Background()); err == nil || !strings.Contains(err.Error(), "wire append refused") {
			t.Fatalf("deliver error = %v", err)
		}
		if got := onlyDelivery(t, store, run.ID); got.State != DeliveryPending || got.Sequence != 0 {
			t.Fatalf("delivery advanced despite append failure: %#v", got)
		}
	})

	t.Run("mark-publication failure surfaces and preserves the journal", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		path := filepath.Join(root, "runs.jsonl")
		store := createEmptyStore(t, path, 10)
		admitRunnableRun(t, store)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		store.write = func(file *os.File, data []byte) (int, error) {
			written, _ := file.Write(data[:len(data)/2])
			return written, errors.New("injected publication append failure")
		}
		wire := newScriptedWire()
		collector, _ := NewOutboxCollector(store, wire)
		if err := collector.Deliver(context.Background()); err == nil {
			t.Fatal("mark-publication failure was ignored")
		}
		after, err := os.ReadFile(path)
		if err != nil || !reflectBytesEqual(before, after) {
			t.Fatalf("failed publication mutated the journal: %v", err)
		}
	})
}

func reflectBytesEqual(a, b []byte) bool { return reflect.DeepEqual(a, b) }

func TestOutboxCollectorAcknowledgesOnlyContiguousDispatchedPrefix(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	store := createEmptyStore(t, filepath.Join(root, "runs.jsonl"), 10)
	run := admitRunnableRun(t, store)
	// Grow a suffix of three more transitions, each with its own delivery, by
	// bouncing between admitted and routing so no repository route is required.
	steps := []struct {
		state LifecycleState
		id    string
	}{
		{StateRouting, run.ID + ":routing-1"},
		{StateAdmitted, run.ID + ":admitted-2"},
		{StateRouting, run.ID + ":routing-2"},
	}
	current := run
	at := run.UpdatedAt
	for _, step := range steps {
		at = at.Add(time.Second)
		next := current.Clone()
		next.State = step.state
		next.UpdatedAt = at
		next.Transitions = append(next.Transitions, LifecycleTransition{ID: step.id, State: step.state, At: at})
		if err := store.Transition(next); err != nil {
			t.Fatalf("transition to %s: %v", step.state, err)
		}
		current = runByID(t, store, run.ID)
	}
	// Publish deliveries 0,1,2 with sequences 5,6,7; leave delivery 3 pending.
	deliveries := runByID(t, store, run.ID).TransitionDeliveries
	if len(deliveries) != 4 {
		t.Fatalf("want 4 deliveries, got %d", len(deliveries))
	}
	for index, sequence := range []uint64{5, 6, 7} {
		if err := store.RecordPublication(run.ID, deliveries[index].TransitionID, sequence); err != nil {
			t.Fatal(err)
		}
	}

	wire := newScriptedWire()
	wire.holdDispatch = true
	collector, _ := NewOutboxCollector(store, wire)

	// Cursor below the first published sequence: nothing is acknowledged.
	wire.dispatched = 4
	if err := collector.acknowledgeDispatched(); err != nil {
		t.Fatal(err)
	}
	if got := runByID(t, store, run.ID); got.DeliveredThrough != 0 {
		t.Fatalf("acknowledged below the dispatched cursor: watermark %d", got.DeliveredThrough)
	}

	// Cursor at sequence 6 acknowledges only the contiguous prefix 5,6.
	wire.dispatched = 6
	if err := collector.acknowledgeDispatched(); err != nil {
		t.Fatal(err)
	}
	got := runByID(t, store, run.ID)
	if got.DeliveredThrough != 2 || len(got.TransitionDeliveries) != 2 {
		t.Fatalf("prefix acknowledgement watermark = %d, suffix = %#v", got.DeliveredThrough, got.TransitionDeliveries)
	}

	// The next delivery (sequence 7) is at/below the cursor, but the one after
	// it is still pending, so only 7 advances the watermark to 3.
	wire.dispatched = 100
	if err := collector.acknowledgeDispatched(); err != nil {
		t.Fatal(err)
	}
	got = runByID(t, store, run.ID)
	if got.DeliveredThrough != 3 || len(got.TransitionDeliveries) != 1 || got.TransitionDeliveries[0].State != DeliveryPending {
		t.Fatalf("pending delivery did not block the watermark: %#v", got.TransitionDeliveries)
	}
}

func TestOutboxCollectorAcknowledgesRejectedRecordOnceCursorAdvances(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	store := createEmptyStore(t, filepath.Join(root, "runs.jsonl"), 10)
	run := admitRunnableRun(t, store)
	admitted := run.Transitions[0]
	// A permanently rejected record keeps its authoritative sequence and the
	// global dispatched cursor still advances past it, so it is acknowledged
	// rather than cancelled.
	if err := store.RecordPublication(run.ID, admitted.ID, 3); err != nil {
		t.Fatal(err)
	}
	wire := newScriptedWire()
	wire.holdDispatch = true
	wire.dispatched = 3
	collector, _ := NewOutboxCollector(store, wire)
	if err := collector.acknowledgeDispatched(); err != nil {
		t.Fatal(err)
	}
	got := runByID(t, store, run.ID)
	if got.DeliveredThrough != 1 || len(got.TransitionDeliveries) != 0 {
		t.Fatalf("rejected-but-dispatched record was not acknowledged: %#v", got)
	}
}

func TestDeliveryValidationRejectsMalformedProjections(t *testing.T) {
	base := func(t *testing.T) Run {
		t.Helper()
		_, run, _ := testAdmissionProjection(t, t.TempDir(), 1, StatePending)
		// Reset to a single pending delivery for the one pending transition.
		run.DeliveredThrough = 0
		run.TransitionDeliveries = []TransitionDelivery{{
			TransitionID: run.Transitions[0].ID, EventID: RunTransitionEventID(run.Transitions[0].ID), State: DeliveryPending,
		}}
		return run
	}
	for _, test := range []struct {
		name   string
		mutate func(*Run)
	}{
		{"missing delivery for suffix", func(run *Run) { run.TransitionDeliveries = nil }},
		{"duplicate suffix delivery", func(run *Run) {
			run.TransitionDeliveries = append(run.TransitionDeliveries, run.TransitionDeliveries[0])
		}},
		{"misaligned transition id", func(run *Run) { run.TransitionDeliveries[0].TransitionID = "run-1:elsewhere" }},
		{"bad deterministic event id", func(run *Run) { run.TransitionDeliveries[0].EventID = "factory:other:x" }},
		{"invalid state", func(run *Run) { run.TransitionDeliveries[0].State = "acknowledged" }},
		{"pending with sequence", func(run *Run) { run.TransitionDeliveries[0].Sequence = 4 }},
		{"published without sequence", func(run *Run) { run.TransitionDeliveries[0].State = DeliveryPublished }},
		{"watermark overflow", func(run *Run) { run.DeliveredThrough = len(run.Transitions) + 1; run.TransitionDeliveries = nil }},
		{"watermark regression", func(run *Run) { run.DeliveredThrough = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := base(t)
			test.mutate(&run)
			if err := run.Validate(); err == nil {
				t.Fatalf("invalid delivery projection was accepted: %#v", run.TransitionDeliveries)
			}
		})
	}

	t.Run("migrated baseline with a delivery", func(t *testing.T) {
		root := trustedTestRoot(t, t.TempDir())
		batch, run, rate := runningProjection(t, root)
		run.Transitions, run.DeliveredThrough = nil, 0
		run.MigratedBaseline = &MigratedBaseline{State: run.State, ObservedAt: run.UpdatedAt, PriorTransitionsAcknowledged: true}
		run.TransitionDeliveries = []TransitionDelivery{{
			TransitionID: "run-1:ghost", EventID: RunTransitionEventID("run-1:ghost"), State: DeliveryPending,
		}}
		if _, err := NewSnapshot(testSingleAdmissionModel(batch, run, rate)); err == nil {
			t.Fatal("migrated baseline with a delivery obligation was accepted")
		}
	})
}

func TestCompactionRetainsUnacknowledgedTerminalRunsAndPrunesAcknowledged(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	path := filepath.Join(root, "runs.jsonl")
	store := createEmptyStore(t, path, 1)

	// run-1 becomes terminal with an unacknowledged delivery. run-2 and run-3
	// are fully acknowledged terminal history. With retention 1, retention
	// alone keeps only the newest acknowledged batch (run-3); run-1 is kept
	// solely because it still owes a delivery.
	pendingBatch, pendingRun, pendingRate := testAdmissionProjection(t, root, 1, StatePending)
	if err := store.ApplyAdmissionBatch([]AdmissionBatch{pendingBatch}, []Run{pendingRun}, []RateBucket{pendingRate}); err != nil {
		t.Fatal(err)
	}
	failed := nextLifecycleRun(pendingRun, StateFailed, pendingRun.UpdatedAt.Add(time.Second))
	if err := store.Transition(failed); err != nil {
		t.Fatal(err)
	}
	for _, number := range []int{2, 3} {
		batch, run, rate := testAdmissionProjection(t, root, number, StateSucceeded)
		if err := store.ApplyAdmissionBatch([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}); err != nil {
			t.Fatal(err)
		}
		acknowledgeRunDeliveries(t, store, run.ID)
	}

	if err := store.Compact(time.Time{}); err != nil {
		t.Fatal(err)
	}
	model := mustRunModel(t, store)
	if _, found := runInModel(model, pendingRun.ID); !found {
		t.Fatalf("compaction dropped the unacknowledged terminal run: %#v", model.Runs)
	}
	if _, found := runInModel(model, "run-2"); found {
		t.Fatal("compaction retained an acknowledged terminal batch beyond retention")
	}
	if _, found := runInModel(model, "run-3"); !found {
		t.Fatal("compaction dropped the newest retained acknowledged batch")
	}

	// Once run-1's terminal delivery is acknowledged, only retention governs it
	// and the newest acknowledged batch (run-3) survives.
	acknowledgeAllDeliveries(t, store)
	if err := store.Compact(time.Time{}); err != nil {
		t.Fatal(err)
	}
	model = mustRunModel(t, store)
	if _, found := runInModel(model, pendingRun.ID); found {
		t.Fatalf("acknowledged terminal run survived retention: %#v", model.Runs)
	}
	if len(model.Runs) != 1 || model.Runs[0].ID != "run-3" {
		t.Fatalf("unexpected retained runs after acknowledgement: %#v", model.Runs)
	}
	// Lifetime totals preserve the pruned history; the watermark survives on the
	// retained run's receipt evidence.
	if model.TotalRuns != 3 || model.TotalBatches != 3 {
		t.Fatalf("compaction lost lifetime totals: %#v", model)
	}
}

func runInModel(model Model, id string) (Run, bool) {
	for _, run := range model.Runs {
		if run.ID == id {
			return run, true
		}
	}
	return Run{}, false
}

func TestSemanticDigestDistinguishesDeliveryObligations(t *testing.T) {
	root := trustedTestRoot(t, t.TempDir())
	batch, run, rate := testAdmissionProjection(t, root, 1, StatePending)
	transitionID := run.Transitions[0].ID

	digestFor := func(t *testing.T, apply func(*Run)) string {
		t.Helper()
		variant := run.Clone()
		apply(&variant)
		snapshot, err := NewSnapshot(testSingleAdmissionModel(batch, variant, rate))
		if err != nil {
			t.Fatal(err)
		}
		digest, err := snapshot.Digest()
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}

	acknowledged := digestFor(t, func(r *Run) { r.DeliveredThrough = len(r.Transitions) })
	pending := digestFor(t, func(r *Run) {
		r.DeliveredThrough = 0
		r.TransitionDeliveries = []TransitionDelivery{{TransitionID: transitionID, EventID: RunTransitionEventID(transitionID), State: DeliveryPending}}
	})
	published := digestFor(t, func(r *Run) {
		r.DeliveredThrough = 0
		r.TransitionDeliveries = []TransitionDelivery{{TransitionID: transitionID, EventID: RunTransitionEventID(transitionID), State: DeliveryPublished, Sequence: 9}}
	})
	if acknowledged == pending || acknowledged == published || pending == published {
		t.Fatalf("digest did not distinguish delivery obligations: acked=%s pending=%s published=%s", acknowledged, pending, published)
	}

	// The digest is stable across a store reopen and checkpoint.
	path := filepath.Join(root, "runs.jsonl")
	pendingRun := run.Clone()
	pendingRun.DeliveredThrough = 0
	pendingRun.TransitionDeliveries = []TransitionDelivery{{TransitionID: transitionID, EventID: RunTransitionEventID(transitionID), State: DeliveryPending}}
	snapshot, err := NewSnapshot(testSingleAdmissionModel(batch, pendingRun, rate))
	if err != nil {
		t.Fatal(err)
	}
	store, err := Create(root, path, snapshot, 10)
	if err != nil {
		t.Fatal(err)
	}
	live, _ := store.Snapshot()
	liveDigest, _ := live.Digest()
	if err := store.Compact(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, path, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, _ := reopened.Snapshot()
	replayedDigest, _ := replayed.Digest()
	if liveDigest != replayedDigest || liveDigest != pending {
		t.Fatalf("delivery digest drifted across reopen/checkpoint: live=%s replayed=%s pending=%s", liveDigest, replayedDigest, pending)
	}
}

func TestNewOutboxCollectorRequiresDependenciesAndHasNoProductionCaller(t *testing.T) {
	store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
	if _, err := NewOutboxCollector(nil, newScriptedWire()); err == nil {
		t.Fatal("nil store was accepted")
	}
	if _, err := NewOutboxCollector(store, nil); err == nil {
		t.Fatal("nil wire was accepted")
	}

	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	var calls []string
	walkErr := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
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
			if name == "NewOutboxCollector" {
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
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if len(calls) != 0 {
		t.Fatalf("production constructs canonical OutboxCollector outside internal/app: %v", calls)
	}
}
