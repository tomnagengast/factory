package triggerrouter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

func TestApplyDecisionBatchPersistsAllMatchingRulesOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	configuration, registry := testPolicy()
	store, err := Open(filepath.Join(t.TempDir(), "routing.jsonl"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	syncs := 0
	store.sync = func(file *os.File) error {
		syncs++
		return file.Sync()
	}
	record := testRecord("factory:batch", 1, eventwire.SourceFactory, now)
	decisions, err := store.ApplyDecisionBatch([]eventwire.Record{record}, registry, configuration, now)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if syncs != 1 {
		t.Fatalf("syncs = %d, want 1", syncs)
	}
	if len(decisions) != 1 || len(decisions[0].Outcomes) != 2 {
		t.Fatalf("decisions = %#v", decisions)
	}
	for _, outcome := range decisions[0].Outcomes {
		if outcome.Kind != OutcomeInvocation || outcome.InvocationID == "" {
			t.Fatalf("outcome = %#v", outcome)
		}
	}
	if snapshot := store.Snapshot(); len(snapshot.Invocations) != 2 || totalRates(snapshot.RateBuckets) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	decisions, err = store.ApplyDecisionBatch([]eventwire.Record{record}, registry, configuration, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if syncs != 1 || len(decisions) != 1 {
		t.Fatalf("reapply syncs=%d decisions=%#v", syncs, decisions)
	}
}

func TestApplyDecisionBatchSuppressesCyclesHopRateAndOutstanding(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	configuration, registry := testPolicy()
	registry.Rules = registry.Rules[:1]
	rule := &registry.Rules[0]
	rule.MaxHop = 2
	rule.MaxOutstanding = 1
	rule.AdmissionsHour = 2
	store, err := Open(filepath.Join(t.TempDir(), "routing.jsonl"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	cycle := testRecord("factory:cycle", 1, eventwire.SourceFactory, now)
	cycle.Event.RootEventID = "factory:root"
	cycle.Event.ParentInvocationID = "parent"
	cycle.Event.Hop = 1
	cycle.Event.AncestorRuleIDs = []string{rule.ID}
	hop := testRecord("factory:hop", 2, eventwire.SourceFactory, now)
	hop.Event.RootEventID = "factory:root"
	hop.Event.ParentInvocationID = "parent"
	hop.Event.Hop = 2
	hop.Event.AncestorRuleIDs = []string{"first", "second"}
	admit := testRecord("factory:admit", 3, eventwire.SourceFactory, now)
	outstanding := testRecord("factory:outstanding", 4, eventwire.SourceFactory, now)
	decisions, err := store.ApplyDecisionBatch([]eventwire.Record{cycle, hop, admit, outstanding}, registry, configuration, now)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []string{"ancestor-cycle", "hop-limit", "", "rule-outstanding-limit"}
	for index, decision := range decisions {
		outcome := decision.Outcomes[0]
		if outcome.Reason != want[index] {
			t.Fatalf("decision %d outcome=%#v, want reason %q", index, outcome, want[index])
		}
	}
	if decisions[2].Outcomes[0].Kind != OutcomeInvocation {
		t.Fatalf("admission = %#v", decisions[2])
	}
}

func TestResolveIssueRejectsAmbiguousAttribute(t *testing.T) {
	t.Parallel()
	event := eventwire.Event{Attributes: map[string][]string{"issue": {"ENG-40", "ENG-41"}}}
	_, err := resolveIssue(triggerregistry.TargetPolicy{Kind: triggerregistry.TargetEventAttribute, Value: "issue"}, event)
	if err == nil || err.Error() != "target-attribute-cardinality" {
		t.Fatalf("error = %v", err)
	}
}

func testPolicy() (settings.Snapshot, triggerregistry.Snapshot) {
	configuration := settings.Defaults(3)
	registry := triggerregistry.Defaults(configuration, "human")
	for index := range registry.Rules {
		registry.Rules[index].Enabled = true
		registry.Rules[index].Filter = triggerregistry.Filter{Source: eventwire.SourceFactory, Type: "service", Action: "complete"}
		registry.Rules[index].Target = triggerregistry.TargetPolicy{Kind: triggerregistry.TargetEventSubject}
	}
	return configuration, registry
}

func testRecord(id string, sequence uint64, source eventwire.Source, now time.Time) eventwire.Record {
	return eventwire.Record{Sequence: sequence, Event: eventwire.Event{
		ID: id, Source: source, Type: "service", Action: "complete", Subject: "ENG-40", RootEventID: id, ReceivedAt: now,
	}}
}

func totalRates(buckets []RateBucket) int {
	total := 0
	for _, bucket := range buckets {
		total += bucket.Count
	}
	return total
}

type registryStub struct {
	snapshot triggerregistry.Snapshot
	marks    int
}

func (s *registryStub) Snapshot() triggerregistry.Snapshot { return s.snapshot.Clone() }

func (s *registryStub) MarkLegacyRollbackIncompatible(now time.Time) (triggerregistry.Snapshot, error) {
	s.marks++
	s.snapshot.LegacyRollbackIncompatible = true
	s.snapshot.Revision++
	s.snapshot.UpdatedAt = now.UTC()
	return s.snapshot.Clone(), nil
}

type settingsStub struct{ snapshot settings.Snapshot }

func (s settingsStub) Snapshot() settings.Snapshot { return s.snapshot.Clone() }

func TestCoordinatedWireRoutesWholeFutureSourceBatchBeforeRecordHandlers(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	configuration, registry := testPolicy()
	for index := range registry.Rules {
		registry.Rules[index].Filter.Source = ""
	}
	registryStore := &registryStub{snapshot: registry}
	routing, err := Open(filepath.Join(directory, "routing.jsonl"))
	if err != nil {
		t.Fatalf("open routing: %v", err)
	}
	syncs := 0
	routing.sync = func(file *os.File) error {
		syncs++
		return file.Sync()
	}
	journal, err := eventwire.Open(filepath.Join(directory, "events.jsonl"), 20, nil)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	raw, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	wire, err := NewCoordinatedWire(raw, registryStore, settingsStub{snapshot: configuration}, routing, func() time.Time { return now })
	if err != nil {
		t.Fatalf("coordinate: %v", err)
	}
	handled := 0
	if err := wire.Handle(eventwire.Filter{}, func(_ context.Context, _ eventwire.Record) error {
		handled++
		if got := routing.Snapshot(); len(got.Decisions) != 2 || len(got.Invocations) != 4 {
			return errors.New("routing projection was not complete before per-record dispatch")
		}
		return nil
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	events := []eventwire.Event{
		{ID: "slack:one", Source: "slack", Type: "service", Action: "complete", Subject: "ENG-40", ReceivedAt: now},
		{ID: "slack:two", Source: "slack", Type: "service", Action: "complete", Subject: "ENG-41", ReceivedAt: now},
	}
	if _, err := wire.PublishBatch(context.Background(), events); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if handled != 2 || syncs != 1 || registryStore.marks != 1 || !registryStore.snapshot.LegacyRollbackIncompatible {
		t.Fatalf("handled=%d syncs=%d marks=%d registry=%#v", handled, syncs, registryStore.marks, registryStore.snapshot)
	}
}

func TestCoordinatedWireRejectsPolicyMutationUntilPendingAdmissionIsDurable(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	configuration, defaults := testPolicy()
	configurationStore, err := settings.Open(filepath.Join(directory, "settings.json"), configuration)
	if err != nil {
		t.Fatal(err)
	}
	registryStore, err := triggerregistry.Open(filepath.Join(directory, "triggers.json"), defaults, configuration)
	if err != nil {
		t.Fatal(err)
	}
	routing, err := Open(filepath.Join(directory, "routing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := eventwire.Open(filepath.Join(directory, "events.jsonl"), 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := eventwire.New(journal)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := NewCoordinatedWire(raw, registryStore, configurationStore, routing, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	routing.sync = func(*os.File) error { return errors.New("injected routing sync failure") }
	if _, _, err := wire.Publish(context.Background(), testRecord("factory:pending", 1, eventwire.SourceFactory, now).Event); err == nil {
		t.Fatal("routing failure was ignored")
	}
	candidate := registryStore.Snapshot()
	candidate.Rules[0].Name = "Edited while pending"
	if _, err := wire.UpdateRegistry(candidate.Revision, configuration.Revision, candidate, now); !errors.Is(err, ErrPolicyPending) {
		t.Fatalf("registry update error = %v, want pending admission", err)
	}
	settingsCandidate := configurationStore.Snapshot()
	settingsCandidate.Runtime.MaxConcurrentRuns++
	if _, err := wire.UpdateSettings(settingsCandidate.Revision, settingsCandidate, now); !errors.Is(err, ErrPolicyPending) {
		t.Fatalf("settings update error = %v, want pending admission", err)
	}
	if registryStore.Snapshot().Revision != 0 || configurationStore.Snapshot().Revision != 0 {
		t.Fatal("policy changed while an event lacked a durable decision")
	}
	routing.sync = func(file *os.File) error { return file.Sync() }
	if err := wire.CatchUp(context.Background()); err != nil {
		t.Fatalf("catch up: %v", err)
	}
	if _, err := wire.UpdateRegistry(candidate.Revision, configuration.Revision, candidate, now); err != nil {
		t.Fatalf("registry update after admission: %v", err)
	}
}
