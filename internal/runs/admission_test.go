package runs

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

var admissionTestNow = time.Date(2026, time.July, 16, 21, 0, 30, 0, time.UTC)

func TestAdmitBatchMatchesSortsAndRoutesWholeSourceBatch(t *testing.T) {
	rules := []triggerregistry.Rule{
		admissionRule("rule-z", 1, eventwire.SourceLinear, 10, 100),
		admissionRule("rule-a", 1, eventwire.SourceLinear, 1, 100),
	}
	rules[0].Filter.Subject = pointerString("TST-47")
	rules[0].Filter.Attributes = map[string]string{"required": "yes"}
	snapshot := admissionPolicy(t, rules)
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store := createEmptyStore(t, path, 10)
	defer store.Close()
	admitter := mustAdmitter(t, store)
	syncs := 0
	syncFile := store.syncFile
	store.syncFile = func(file *os.File) error {
		syncs++
		return syncFile(file)
	}
	records := []eventwire.Record{
		admissionRecordFor(1, "linear:first", eventwire.SourceLinear, "Issue", "update", "TST-47", map[string][]string{"required": {"other", "yes"}}),
		admissionRecordFor(2, "linear:second", eventwire.SourceLinear, "Issue", "update", "TST-47", map[string][]string{"required": {"yes"}}),
		admissionRecordFor(3, "linear:unmatched", eventwire.SourceGitHub, "Issue", "update", "TST-47", nil),
	}
	batches, err := admitter.AdmitBatch(records, snapshot, admissionTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if syncs != 1 {
		t.Fatalf("journal syncs = %d, want one atomic append", syncs)
	}
	if got := []string{batches[0].Outcomes[0].RuleID, batches[0].Outcomes[1].RuleID}; !slices.Equal(got, []string{"rule-a", "rule-z"}) {
		t.Fatalf("sorted outcomes = %#v", got)
	}
	if batches[1].Outcomes[0].Kind != AdmissionOutcomeSuppressed || batches[1].Outcomes[0].Reason != "rule-outstanding-limit" || len(batches[2].Outcomes) != 0 {
		t.Fatalf("sequential decisions = %#v", batches)
	}
	model := mustRunModel(t, store)
	if model.TotalBatches != 3 || model.TotalRuns != 3 || len(model.AdmissionOperations) != 1 ||
		len(model.AdmissionOperations[0].AdmissionBatches) != 3 {
		t.Fatalf("whole-batch projection = totals %d/%d receipts %#v", model.TotalBatches, model.TotalRuns, model.AdmissionOperations)
	}
	for _, batch := range batches {
		if batch.DecidedAt != admissionTestNow || batch.EventRecordDigest == "" {
			t.Fatalf("batch evidence = %#v", batch)
		}
	}
}

func TestAdmitBatchLegacyBehavioralEquivalenceFromConvertedPolicy(t *testing.T) {
	rules := []triggerregistry.Rule{
		admissionRule("rule-z", 2, eventwire.SourceLinear, 10, 100),
		admissionRule("rule-a", 3, eventwire.SourceLinear, 10, 100),
	}
	rules[0].Filter.Attributes = map[string]string{"label": "Factory"}
	snapshot := admissionPolicy(t, rules)
	records := []eventwire.Record{
		admissionRecordFor(11, "linear:eq-1", eventwire.SourceLinear, "Issue", "update", "TST-47", map[string][]string{"label": {"Factory"}}),
		admissionRecordFor(12, "linear:eq-2", eventwire.SourceLinear, "Issue", "update", "NOT-AN-ISSUE", map[string][]string{"label": {"Factory"}}),
	}
	legacy, err := triggerrouter.Open(filepath.Join(t.TempDir(), "routing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	legacyDecisions, err := legacy.ApplyDecisionBatch(records, policy.RegistryView(snapshot), policy.SettingsView(snapshot), admissionTestNow)
	if err != nil {
		t.Fatal(err)
	}
	store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
	defer store.Close()
	canonical, err := mustAdmitter(t, store).AdmitBatch(records, snapshot, admissionTestNow)
	if err != nil {
		t.Fatal(err)
	}
	model := mustRunModel(t, store)
	runsByID := make(map[string]Run, len(model.Runs))
	for _, run := range model.Runs {
		runsByID[run.ID] = run
	}
	for index, decision := range legacyDecisions {
		batch := canonical[index]
		if batch.EventID != decision.EventID || batch.EventSequence != decision.EventSequence || batch.EventSource != decision.Source ||
			batch.RegistryRevision != decision.RegistryRevision || batch.SettingsRevision != decision.SettingsRevision || batch.DecidedAt != decision.DecidedAt ||
			len(batch.Outcomes) != len(decision.Outcomes) {
			t.Fatalf("decision %d mismatch: legacy=%#v canonical=%#v", index, decision, batch)
		}
		for outcomeIndex, legacyOutcome := range decision.Outcomes {
			outcome := batch.Outcomes[outcomeIndex]
			if outcome.RuleID != legacyOutcome.RuleID || outcome.RuleRevision != legacyOutcome.RuleRevision || outcome.Reason != legacyOutcome.Reason {
				t.Fatalf("outcome mismatch: legacy=%#v canonical=%#v", legacyOutcome, outcome)
			}
			switch legacyOutcome.Kind {
			case triggerrouter.OutcomeInvocation:
				if outcome.Kind != AdmissionOutcomeRun || outcome.AdmissionID != legacyOutcome.InvocationID {
					t.Fatalf("runnable outcome mismatch: legacy=%#v canonical=%#v", legacyOutcome, outcome)
				}
				invocation, found := legacy.Invocation(legacyOutcome.InvocationID)
				run := runsByID[outcome.RunID]
				if !found || run.State != StateAdmitted || run.ID != "run-"+admissionDigest("factory-trigger-run-v1", invocation.ID)[:16] ||
					!run.Causation.Task.Equal(invocation.Task) || run.Causation.WorkflowDigest != invocation.WorkflowDigest ||
					run.Causation.RootEventID != invocation.RootEventID || run.Causation.Hop != invocation.Hop ||
					!slices.Equal(run.Causation.AncestorRuleIDs, invocation.AncestorRuleIDs) {
					t.Fatalf("invocation/Run mismatch: legacy=%#v canonical=%#v", invocation, run)
				}
			case triggerrouter.OutcomeRejected:
				if outcome.Kind != AdmissionOutcomeRejected {
					t.Fatalf("rejection mismatch: %#v", outcome)
				}
			case triggerrouter.OutcomeSuppressed:
				if outcome.Kind != AdmissionOutcomeSuppressed {
					t.Fatalf("suppression mismatch: %#v", outcome)
				}
			}
		}
	}
	legacySnapshot := legacy.Snapshot()
	if model.TotalRuns != uint64(len(legacySnapshot.Invocations)) || len(model.RateBuckets) != len(legacySnapshot.RateBuckets) {
		t.Fatalf("rate/lifetime mismatch: legacy=%#v canonical=%#v", legacySnapshot, model)
	}
}

func TestAdmitBatchSuppressionAndTargetReasons(t *testing.T) {
	base := admissionRule("rule-one", 1, eventwire.SourceLinear, 10, 100)
	derived := admissionRecordFor(1, "linear:derived", eventwire.SourceLinear, "Issue", "update", "TST-47", nil)
	derived.Event.RootEventID = "linear:root"
	derived.Event.ParentInvocationID = "parent-admission"
	derived.Event.ParentRunID = "run-parent"
	derived.Event.Hop = 1
	derived.Event.AncestorRuleIDs = []string{"parent-rule"}
	tests := []struct {
		name   string
		rule   triggerregistry.Rule
		record eventwire.Record
		reason string
	}{
		{name: "protected", rule: func() triggerregistry.Rule {
			rule := base
			rule.Filter.Source = eventwire.SourceFactory
			rule.Filter.Type = "task-mutation"
			return rule
		}(), record: admissionRecordFor(1, "factory:protected", eventwire.SourceFactory, "task-mutation", "update", "TST-47", nil), reason: "protected-task-operation"},
		{name: "cycle", rule: func() triggerregistry.Rule { rule := base; rule.ID = "parent-rule"; return rule }(), record: derived, reason: "ancestor-cycle"},
		{name: "hop", rule: func() triggerregistry.Rule { rule := base; rule.MaxHop = 1; return rule }(), record: derived, reason: "hop-limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
			defer store.Close()
			batches, err := mustAdmitter(t, store).AdmitBatch([]eventwire.Record{test.record}, admissionPolicy(t, []triggerregistry.Rule{test.rule}), admissionTestNow)
			if err != nil {
				t.Fatal(err)
			}
			if len(batches[0].Outcomes) != 1 || batches[0].Outcomes[0].Kind != AdmissionOutcomeSuppressed || batches[0].Outcomes[0].Reason != test.reason {
				t.Fatalf("outcome = %#v", batches[0].Outcomes)
			}
		})
	}

	for _, test := range []struct {
		name   string
		target policy.TargetPolicy
		event  eventwire.Event
		want   string
	}{
		{name: "provider", target: policy.TargetPolicy{Provider: taskmodel.SourceFactory, Kind: policy.TargetEventSubject}, event: derived.Event, want: "target-provider-invalid"},
		{name: "kind", target: policy.TargetPolicy{Provider: taskmodel.SourceLinear, Kind: "unknown"}, event: derived.Event, want: "target-policy-invalid"},
		{name: "cardinality", target: policy.TargetPolicy{Provider: taskmodel.SourceLinear, Kind: policy.TargetEventAttribute, Value: "issue"}, event: derived.Event, want: "target-attribute-cardinality"},
		{name: "identifier", target: policy.TargetPolicy{Provider: taskmodel.SourceLinear, Kind: policy.TargetEventSubject}, event: eventwire.Event{Subject: "invalid"}, want: "target-issue-invalid"},
	} {
		t.Run("target-"+test.name, func(t *testing.T) {
			if _, err := resolveAdmissionTask(test.target, test.event); err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
		})
	}
}

func TestAdmitBatchLimitsAreSequentialAndInclusive(t *testing.T) {
	t.Run("hourly inclusive cutoff", func(t *testing.T) {
		rule := admissionRule("rule-one", 1, eventwire.SourceLinear, 100, 1)
		store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
		defer store.Close()
		batches, err := mustAdmitter(t, store).AdmitBatch([]eventwire.Record{
			admissionRecordFor(1, "linear:hour-1", eventwire.SourceLinear, "Issue", "update", "TST-47", nil),
			admissionRecordFor(2, "linear:hour-2", eventwire.SourceLinear, "Issue", "update", "TST-48", nil),
		}, admissionPolicy(t, []triggerregistry.Rule{rule}), admissionTestNow)
		if err != nil || batches[1].Outcomes[0].Reason != "hourly-rate-limit" {
			t.Fatalf("batches=%#v err=%v", batches, err)
		}
	})

	t.Run("global", func(t *testing.T) {
		rules := make([]triggerregistry.Rule, 32)
		for index := range rules {
			rules[index] = admissionRule(fmt.Sprintf("rule-%02d", index), 1, eventwire.SourceLinear, 100, 10000)
		}
		store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
		defer store.Close()
		records := make([]eventwire.Record, 4)
		for index := range records {
			records[index] = admissionRecordFor(uint64(index+1), fmt.Sprintf("linear:global-%d", index), eventwire.SourceLinear, "Issue", "update", "TST-47", nil)
		}
		batches, err := mustAdmitter(t, store).AdmitBatch(records, admissionPolicy(t, rules), admissionTestNow)
		if err != nil {
			t.Fatal(err)
		}
		admitted, suppressed := 0, 0
		for _, batch := range batches {
			for _, outcome := range batch.Outcomes {
				if outcome.Kind == AdmissionOutcomeRun {
					admitted++
				} else if outcome.Reason == "global-outstanding-limit" {
					suppressed++
				}
			}
		}
		if admitted != GlobalOutstandingMax || suppressed != 28 {
			t.Fatalf("admitted=%d global-suppressed=%d", admitted, suppressed)
		}
	})
}

func TestAdmitterDurablyClassifiesEmptySuppressedAndRejectedBatches(t *testing.T) {
	rule := admissionRule("rule-one", 1, eventwire.SourceLinear, 10, 100)

	t.Run("empty source batch", func(t *testing.T) {
		store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
		defer store.Close()
		batches, err := mustAdmitter(t, store).AdmitBatch(nil, admissionPolicy(t, []triggerregistry.Rule{rule}), admissionTestNow)
		if err != nil || len(batches) != 0 {
			t.Fatalf("empty admission = %#v, %v", batches, err)
		}
		model := mustRunModel(t, store)
		if model.JournalSequence != 0 || model.TotalBatches != 0 || len(model.AdmissionOperations) != 0 {
			t.Fatalf("empty admission changed Store = %#v", model)
		}
	})

	t.Run("all suppressed", func(t *testing.T) {
		protectedRule := rule
		protectedRule.Filter.Source = eventwire.SourceFactory
		protectedRule.Filter.Type = "task-mutation"
		store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
		defer store.Close()
		record := admissionRecordFor(1, "factory:suppressed", eventwire.SourceFactory, "task-mutation", "update", "TST-47", nil)
		batches, err := mustAdmitter(t, store).AdmitBatch([]eventwire.Record{record}, admissionPolicy(t, []triggerregistry.Rule{protectedRule}), admissionTestNow)
		if err != nil || len(batches) != 1 || len(batches[0].Outcomes) != 1 ||
			batches[0].Outcomes[0].Kind != AdmissionOutcomeSuppressed {
			t.Fatalf("suppressed admission = %#v, %v", batches, err)
		}
		assertNonRunnableAdmissionStored(t, store)
	})

	t.Run("all rejected", func(t *testing.T) {
		store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
		defer store.Close()
		record := admissionRecordFor(1, "linear:rejected", eventwire.SourceLinear, "Issue", "update", "invalid", nil)
		batches, err := mustAdmitter(t, store).AdmitBatch([]eventwire.Record{record}, admissionPolicy(t, []triggerregistry.Rule{rule}), admissionTestNow)
		if err != nil || len(batches) != 1 || len(batches[0].Outcomes) != 1 ||
			batches[0].Outcomes[0].Kind != AdmissionOutcomeRejected || batches[0].Outcomes[0].Reason != "target-issue-invalid" {
			t.Fatalf("rejected admission = %#v, %v", batches, err)
		}
		assertNonRunnableAdmissionStored(t, store)
	})
}

func TestAdmitterReturnsCompactedMigratedDuplicateAfterPolicyChange(t *testing.T) {
	rule := admissionRule("rule-one", 1, eventwire.SourceLinear, 10, 100)
	originalPolicy := admissionPolicy(t, []triggerregistry.Rule{rule})
	records := []eventwire.Record{
		admissionRecordFor(1, "linear:migrated-one", eventwire.SourceLinear, "Issue", "update", "TST-47", nil),
		admissionRecordFor(2, "linear:migrated-two", eventwire.SourceLinear, "Issue", "update", "TST-48", nil),
	}

	sourcePath := filepath.Join(t.TempDir(), "source-runs.jsonl")
	source := createEmptyStore(t, sourcePath, 10)
	_, err := mustAdmitter(t, source).AdmitBatch(records, originalPolicy, admissionTestNow)
	if err != nil {
		t.Fatal(err)
	}
	sourceModel := mustRunModel(t, source)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	for index := range sourceModel.Runs {
		rejected := nextLifecycleRun(sourceModel.Runs[index], StateRejected, admissionTestNow.Add(time.Duration(index+1)*time.Second))
		rejected.Detail = "migrated sandbox terminal"
		sourceModel.Runs[index] = rejected
	}
	receipt, err := NewMigrationSnapshotReceipt(
		"migration-admitter-sandbox", strings.Repeat("a", 64), uint64(len(sourceModel.Runs)),
		sourceModel.AdmissionBatches, sourceModel.Runs, sourceModel.RateBuckets,
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := NewSnapshot(Model{
		Schema: SchemaVersion, TotalBatches: uint64(len(sourceModel.AdmissionBatches)), TotalRuns: uint64(len(sourceModel.Runs)),
		Migration: receipt, AdmissionOperations: []AdmissionOperationReceipt{},
		AdmissionBatches: sourceModel.AdmissionBatches, Runs: sourceModel.Runs, RateBuckets: sourceModel.RateBuckets,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := initial.Model()
	targetBatch := canonical.AdmissionBatches[0]
	var targetRecord eventwire.Record
	for _, record := range records {
		if record.Event.ID == targetBatch.EventID {
			targetRecord = record
		}
	}
	if targetRecord.Sequence == 0 {
		t.Fatal("migration fixture target record is missing")
	}

	root := t.TempDir()
	path := filepath.Join(root, "runs.jsonl")
	store, err := Create(trustedTestRoot(t, root), path, initial, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(time.Time{}); err != nil {
		t.Fatal(err)
	}
	compacted := mustRunModel(t, store)
	if len(compacted.AdmissionBatches) != 1 || compacted.AdmissionBatches[0].ID == targetBatch.ID {
		t.Fatalf("target migration batch was not compacted: %#v", compacted.AdmissionBatches)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	changedModel := originalPolicy.Model()
	changedModel.Generation++
	changedModel.Settings.Revision++
	changedModel.Registry.Revision++
	changedModel.Registry.Rules[0].Enabled = false
	changedPolicy, err := policy.NewSnapshot(changedModel)
	if err != nil {
		t.Fatal(err)
	}
	beforeDisk, _ := os.ReadFile(path)
	before := mustRunModel(t, reopened)
	replayed, err := mustAdmitter(t, reopened).AdmitBatch([]eventwire.Record{targetRecord}, changedPolicy, admissionTestNow.Add(2*time.Hour))
	if err != nil || len(replayed) != 1 || !reflect.DeepEqual(replayed[0], targetBatch) {
		t.Fatalf("migrated replay = %#v, %v; want %#v", replayed, err, targetBatch)
	}
	afterDisk, _ := os.ReadFile(path)
	after := mustRunModel(t, reopened)
	if !slices.Equal(beforeDisk, afterDisk) || !reflect.DeepEqual(before, after) {
		t.Fatal("compacted migrated duplicate changed durable Store state")
	}
}

func TestAdmitBatchDurableDuplicatePolicyMutationRewriteAndMixedBatch(t *testing.T) {
	rule := admissionRule("rule-one", 1, eventwire.SourceLinear, 10, 100)
	originalPolicy := admissionPolicy(t, []triggerregistry.Rule{rule})
	root := t.TempDir()
	path := filepath.Join(root, "runs.jsonl")
	store := createEmptyStore(t, path, 1)
	admitter := mustAdmitter(t, store)
	record := admissionRecordFor(7, "linear:durable", eventwire.SourceLinear, "Issue", "update", "TST-47", map[string][]string{"ignored": {"original"}})
	original, err := admitter.AdmitBatch([]eventwire.Record{record}, originalPolicy, admissionTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedAdmitter := mustAdmitter(t, reopened)
	changedModel := originalPolicy.Model()
	changedModel.Generation++
	changedModel.Settings.Revision++
	changedModel.Registry.Revision++
	changedModel.Registry.Rules[0].Enabled = false
	changedPolicy, err := policy.NewSnapshot(changedModel)
	if err != nil {
		t.Fatal(err)
	}
	before := mustRunModel(t, reopened)
	infoBefore, _ := os.Stat(path)
	replayed, err := reopenedAdmitter.AdmitBatch([]eventwire.Record{record}, changedPolicy, admissionTestNow.Add(2*time.Hour))
	if err != nil || !reflect.DeepEqual(replayed, original) {
		t.Fatalf("durable replay = %#v, %v; want %#v", replayed, err, original)
	}
	afterReplay := mustRunModel(t, reopened)
	infoAfter, _ := os.Stat(path)
	if before.JournalSequence != afterReplay.JournalSequence || before.TotalBatches != afterReplay.TotalBatches || infoBefore.Size() != infoAfter.Size() {
		t.Fatalf("duplicate mutated store: before=%#v after=%#v sizes=%d/%d", before, afterReplay, infoBefore.Size(), infoAfter.Size())
	}

	mutations := []func(*eventwire.Record){
		func(value *eventwire.Record) { value.Sequence++ },
		func(value *eventwire.Record) { value.Event.Source = eventwire.SourceGitHub },
		func(value *eventwire.Record) { value.Event.Attributes["ignored"] = []string{"rewritten"} },
		func(value *eventwire.Record) {
			value.Event.RootEventID = "linear:root"
			value.Event.ParentInvocationID = "parent-admission"
			value.Event.Hop = 1
			value.Event.AncestorRuleIDs = []string{"parent-rule"}
		},
	}
	for index, mutate := range mutations {
		candidate := record
		candidate.Event.Attributes = map[string][]string{"ignored": {"original"}}
		mutate(&candidate)
		if _, err := reopenedAdmitter.AdmitBatch([]eventwire.Record{candidate}, changedPolicy, admissionTestNow.Add(3*time.Hour)); !errors.Is(err, ErrIdentityCollision) {
			t.Fatalf("rewrite %d error = %v", index, err)
		}
	}

	newRecord := admissionRecordFor(8, "linear:new-policy", eventwire.SourceLinear, "Issue", "update", "TST-48", nil)
	mixed, err := reopenedAdmitter.AdmitBatch([]eventwire.Record{record, newRecord}, changedPolicy, admissionTestNow.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mixed[0], original[0]) || len(mixed[1].Outcomes) != 0 || mixed[1].SettingsRevision != changedPolicy.Settings().Revision || mixed[1].PolicyGeneration != changedPolicy.Generation() {
		t.Fatalf("mixed result = %#v", mixed)
	}
	afterMixed := mustRunModel(t, reopened)
	last := afterMixed.AdmissionOperations[len(afterMixed.AdmissionOperations)-1]
	if afterMixed.TotalBatches != before.TotalBatches+1 || len(last.AdmissionBatches) != 1 || last.AdmissionBatches[0].EventID != newRecord.Event.ID {
		t.Fatalf("mixed append = totals %d receipt %#v", afterMixed.TotalBatches, last)
	}
	if _, err := reopenedAdmitter.AdmitBatch([]eventwire.Record{newRecord, newRecord}, changedPolicy, admissionTestNow.Add(5*time.Hour)); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("duplicate input error = %v", err)
	}
}

func TestAdmitBatchConcurrentAdmissionCannotOversubscribe(t *testing.T) {
	rule := admissionRule("rule-one", 1, eventwire.SourceLinear, 1, 100)
	snapshot := admissionPolicy(t, []triggerregistry.Rule{rule})
	store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 100)
	defer store.Close()
	admitter := mustAdmitter(t, store)
	const calls = 24
	var wait sync.WaitGroup
	errorsByCall := make(chan error, calls)
	for index := 0; index < calls; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			record := admissionRecordFor(uint64(index+1), fmt.Sprintf("linear:race-%02d", index), eventwire.SourceLinear, "Issue", "update", "TST-47", nil)
			_, err := admitter.AdmitBatch([]eventwire.Record{record}, snapshot, admissionTestNow)
			errorsByCall <- err
		}(index)
	}
	wait.Wait()
	close(errorsByCall)
	for err := range errorsByCall {
		if err != nil {
			t.Fatal(err)
		}
	}
	model := mustRunModel(t, store)
	if model.TotalRuns != 1 || model.TotalBatches != calls || len(model.RateBuckets) != 1 || model.RateBuckets[0].Count != 1 {
		t.Fatalf("concurrent projection = batches %d Runs %d rates %#v", model.TotalBatches, model.TotalRuns, model.RateBuckets)
	}
}

func TestAdmitBatchFailureBoundaries(t *testing.T) {
	rule := admissionRule("rule-one", 1, eventwire.SourceLinear, 10, 100)
	snapshot := admissionPolicy(t, []triggerregistry.Rule{rule})
	record := admissionRecordFor(1, "linear:failure", eventwire.SourceLinear, "Issue", "update", "TST-47", nil)
	injected := errors.New("planned admission failure")

	for _, test := range []struct {
		name   string
		inject func(*Store)
	}{
		{name: "append", inject: func(store *Store) { store.write = func(*os.File, []byte) (int, error) { return 0, injected } }},
		{name: "sync", inject: func(store *Store) { store.syncFile = func(*os.File) error { return injected } }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := createEmptyStore(t, filepath.Join(t.TempDir(), "runs.jsonl"), 10)
			defer store.Close()
			admitter := mustAdmitter(t, store)
			test.inject(store)
			if _, err := admitter.AdmitBatch([]eventwire.Record{record}, snapshot, admissionTestNow); !errors.Is(err, injected) {
				t.Fatalf("error = %v", err)
			}
			if model := mustRunModel(t, store); model.TotalBatches != 0 || model.TotalRuns != 0 {
				t.Fatalf("partial visibility = %#v", model)
			}
		})
	}

	t.Run("post-append apply poisons and replays atomically", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "runs.jsonl")
		store := createEmptyStore(t, path, 10)
		admitter := mustAdmitter(t, store)
		store.apply = func(Model, diskOperation) (Snapshot, error) { return Snapshot{}, injected }
		if _, err := admitter.AdmitBatch([]eventwire.Record{record}, snapshot, admissionTestNow); !errors.Is(err, injected) {
			t.Fatalf("apply error = %v", err)
		}
		if _, err := store.Snapshot(); err == nil || !strings.Contains(err.Error(), "poisoned") {
			t.Fatalf("poisoned snapshot error = %v", err)
		}
		_ = store.Close()
		reopened, err := Open(root, path, 10)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		model := mustRunModel(t, reopened)
		if model.TotalBatches != 1 || model.TotalRuns != 1 || len(model.AdmissionOperations) != 1 {
			t.Fatalf("replayed atomic projection = %#v", model)
		}
	})
}

func TestNewAdmitterRequiresStoreAndHasNoProductionCaller(t *testing.T) {
	if _, err := NewAdmitter(nil); err == nil {
		t.Fatal("nil admission Store was accepted")
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
			if name == "NewAdmitter" {
				position := files.Position(call.Pos())
				relative, _ := filepath.Rel(repositoryRoot, position.Filename)
				calls = append(calls, fmt.Sprintf("%s:%d", relative, position.Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("production constructs dormant Admitter: %v", calls)
	}
}

func admissionPolicy(t *testing.T, rules []triggerregistry.Rule) policy.Snapshot {
	t.Helper()
	configuration := settings.Defaults(3)
	configuration.Revision = 7
	configuration.UpdatedAt = admissionTestNow
	registry := triggerregistry.Snapshot{
		Schema: triggerregistry.SchemaVersion, Revision: 4, UpdatedAt: admissionTestNow,
		Rules: slices.Clone(rules), Schedules: []triggerregistry.Schedule{},
	}
	snapshot, err := policy.ConvertSources(policy.Sources{
		Settings: configuration, Registry: &registry,
		TaskControl:    taskcontrol.Snapshot{Version: 1, Revision: 1, UpdatedAt: admissionTestNow, EnabledProjectIDs: []string{}},
		TriggerActorID: "actor-sanitized",
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func admissionRule(id string, revision uint64, source eventwire.Source, maxOutstanding, admissionsHour int) triggerregistry.Rule {
	return triggerregistry.Rule{
		ID: id, Revision: revision, Name: id, Enabled: true,
		Filter:     triggerregistry.Filter{Source: source, Type: "Issue", Action: "update", Attributes: map[string]string{}},
		WorkflowID: workflow.ProviderNeutralID,
		Target:     triggerregistry.TargetPolicy{Provider: taskmodel.SourceLinear, Kind: triggerregistry.TargetEventSubject},
		MaxHop:     4, MaxOutstanding: maxOutstanding, AdmissionsHour: admissionsHour,
	}
}

func admissionRecordFor(sequence uint64, id string, source eventwire.Source, eventType, action, subject string, attributes map[string][]string) eventwire.Record {
	return eventwire.Record{Sequence: sequence, Event: eventwire.Event{
		ID: id, Source: source, Type: eventType, Action: action, Subject: subject,
		Attributes: attributes, ReceivedAt: admissionTestNow.Add(-time.Minute),
	}}
}

func mustRunModel(t *testing.T, store *Store) Model {
	t.Helper()
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Model()
}

func mustAdmitter(t *testing.T, store *Store) *Admitter {
	t.Helper()
	admitter, err := NewAdmitter(store)
	if err != nil {
		t.Fatal(err)
	}
	return admitter
}

func assertNonRunnableAdmissionStored(t *testing.T, store *Store) {
	t.Helper()
	model := mustRunModel(t, store)
	if model.JournalSequence != 1 || model.TotalBatches != 1 || model.TotalRuns != 0 ||
		len(model.AdmissionOperations) != 1 || len(model.Runs) != 0 || len(model.RateBuckets) != 0 {
		t.Fatalf("non-runnable admission projection = %#v", model)
	}
}

func pointerString(value string) *string { return &value }
