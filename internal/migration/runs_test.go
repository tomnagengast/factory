package migration

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

var converterNow = time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC)

func TestConvertRunSourcesCoversLegacyOriginsAndEvidence(t *testing.T) {
	state, canonical := syntheticRunSources(t)
	migrationID := preAuditMigrationID(strings.Repeat("1", 64))
	snapshot, metrics, err := convertRunSources(state, canonical, migrationID, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	model := snapshot.Model()
	if model.TotalBatches != 12 || len(model.AdmissionBatches) != 12 {
		t.Fatalf("batch totals = %d retained %d", model.TotalBatches, len(model.AdmissionBatches))
	}
	if model.TotalRuns != 14 || len(model.Runs) != 10 {
		t.Fatalf("Run totals = %d retained %d", model.TotalRuns, len(model.Runs))
	}
	if model.Migration == nil || model.Migration.MigrationID != migrationID || model.Migration.SourceRootDigest != strings.Repeat("1", 64) {
		t.Fatalf("migration receipt = %#v", model.Migration)
	}
	origins := map[runs.AdmissionOrigin]int{}
	emptyBatches := 0
	nonRunnable := map[runs.AdmissionOutcomeKind]int{}
	for _, batch := range model.AdmissionBatches {
		origins[batch.Origin]++
		if len(batch.Outcomes) == 0 {
			emptyBatches++
		}
		for _, outcome := range batch.Outcomes {
			if outcome.Kind != runs.AdmissionOutcomeRun {
				nonRunnable[outcome.Kind]++
			}
		}
		if batch.Origin != runs.AdmissionOriginEvent && batch.EventSequence != 0 {
			t.Fatalf("synthetic batch %s retained sequence %d", batch.ID, batch.EventSequence)
		}
	}
	if origins[runs.AdmissionOriginEvent] != 7 || origins[runs.AdmissionOriginNative] != 1 ||
		origins[runs.AdmissionOriginContinuation] != 1 || origins[runs.AdmissionOriginMigratedDirect] != 3 ||
		emptyBatches != 1 || nonRunnable[runs.AdmissionOutcomeRejected] != 1 || nonRunnable[runs.AdmissionOutcomeSuppressed] != 1 {
		t.Fatalf("origins=%#v empty=%d non-runnable=%#v", origins, emptyBatches, nonRunnable)
	}

	byID := make(map[string]runs.Run, len(model.Runs))
	for _, run := range model.Runs {
		byID[run.ID] = run
		if run.MigratedBaseline == nil || !run.MigratedBaseline.PriorTransitionsAcknowledged || len(run.Transitions) != 0 {
			t.Fatalf("Run %s lacks a migration baseline: %#v", run.ID, run)
		}
	}
	linked := byID["run-sandbox-linked"]
	if linked.State != runs.StateRunning || linked.Repository == nil || linked.Repository.ProjectID != "project-sandbox" ||
		linked.SessionName != "sandbox-session" || linked.RunDirectory != "/srv/sandbox/runs/run-sandbox-linked" ||
		linked.Attempts != 2 || linked.SegmentAttempt != 2 || linked.GitHub.LastCursor != 17 || !linked.GitHub.RemediationRequested ||
		len(linked.DeliveryIDs) != 2 || linked.DuplicateDeliveries != 1 {
		t.Fatalf("linked active Run = %#v", linked)
	}
	if byID[syntheticRunID("event:queued", "sandbox-rule", 1)].State != runs.StateAdmitted ||
		byID[syntheticRunID("event:claiming", "sandbox-rule", 1)].State != runs.StateRouting ||
		byID[syntheticRunID("event:rejected", "sandbox-rule", 1)].State != runs.StateRejected {
		t.Fatalf("synthesized event states = %#v", byID)
	}
	coalesced := byID["run-"+digestRunParts("factory-trigger-run-v1", syntheticContinuationID())[:16]]
	if coalesced.State != runs.StateRejected || coalesced.Causation.ParentAdmissionID != syntheticNativeStartID(t) ||
		coalesced.Causation.ParentRunID != "run-native-owner" || coalesced.Detail != "native-feedback-coalesced" {
		t.Fatalf("coalesced bookkeeping Run = %#v", coalesced)
	}
	directSuccess := byID["run-direct-success"]
	if directSuccess.Ready == nil || directSuccess.Completion == nil || !directSuccess.Completion.Accepted ||
		directSuccess.MergeCommitOID != strings.Repeat("b", 40) || directSuccess.Repository == nil {
		t.Fatalf("direct success evidence = %#v", directSuccess)
	}
	if historical := byID["run-direct-historical"].MigratedBaseline.HistoricalRepository; historical == nil || historical.Repository != "example/retired-sandbox" {
		t.Fatalf("historical repository = %#v", historical)
	}
	unavailable := byID["run-direct-unavailable"].MigratedBaseline
	if !unavailable.WorkflowPinUnavailable || !unavailable.RepositoryRouteUnavailable {
		t.Fatalf("unavailable direct evidence = %#v", unavailable)
	}
	linkedTerminal := byID["run-linked-terminal"]
	if linkedTerminal.State != runs.StateFailed || !linkedTerminal.MigratedBaseline.RepositoryRouteUnavailable ||
		linkedTerminal.Causation.Workflow == nil || !linkedTerminal.Causation.Workflow.Complete() {
		t.Fatalf("migration-backed linked terminal gap = %#v", linkedTerminal)
	}

	audit, err := canonicalRunsEvidence(snapshot, metrics)
	if err != nil {
		t.Fatal(err)
	}
	if audit.SourceDecisions != 9 || audit.SourceInvocations != 7 || audit.SourceRateBuckets != 2 ||
		audit.SourceRunsRetained != 6 || audit.SourceRunsLifetime != 10 || audit.LinkedPairs != 3 ||
		audit.SynthesizedRuns != 4 || audit.DirectRuns != 3 || audit.TransitionReceipts != 1 ||
		audit.ReflectionReceipts != 2 || audit.CanonicalBatchesRetained != 12 || audit.CanonicalRunsRetained != 10 ||
		audit.CanonicalRunsLifetime != 14 || audit.CanonicalRateBuckets != 2 {
		t.Fatalf("Runs audit = %#v", audit)
	}
	auditJSON, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"# Sandbox converter workflow", "/srv/sandbox", "git@github.com"} {
		if strings.Contains(string(auditJSON), body) {
			t.Fatalf("body-free audit exposed %q: %s", body, auditJSON)
		}
	}

	reordered := state
	reordered.routing = state.routing.Clone()
	slices.Reverse(reordered.routing.Decisions)
	slices.Reverse(reordered.routing.Invocations)
	slices.Reverse(reordered.routing.RateBuckets)
	reordered.runs.Runs = slices.Clone(state.runs.Runs)
	slices.Reverse(reordered.runs.Runs)
	reordered.wireRecords = slices.Clone(state.wireRecords)
	slices.Reverse(reordered.wireRecords)
	reorderedSnapshot, reorderedMetrics, err := convertRunSources(reordered, canonical, migrationID, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	reorderedDigest, _ := reorderedSnapshot.Digest()
	digest, _ := snapshot.Digest()
	if reorderedDigest != digest || reorderedMetrics.audit != metrics.audit {
		t.Fatalf("source reorder changed evidence: %s/%#v != %s/%#v", reorderedDigest, reorderedMetrics.audit, digest, metrics.audit)
	}
}

func TestConvertLinkedTerminalRouteGapsPreserveAdmissionOrigin(t *testing.T) {
	for _, origin := range []runs.AdmissionOrigin{
		runs.AdmissionOriginEvent, runs.AdmissionOriginNative, runs.AdmissionOriginContinuation,
	} {
		t.Run(string(origin), func(t *testing.T) {
			state, canonical := syntheticRunSources(t)
			invocation := &state.routing.Invocations[4]
			decision := &state.routing.Decisions[6]
			legacy := &state.runs.Runs[5]
			if origin != runs.AdmissionOriginEvent {
				state.wireRecords = slices.DeleteFunc(state.wireRecords, func(record eventwire.Record) bool {
					return record.Event.ID == invocation.EventID
				})
				state.wireTotal, state.wireDispatched = 7, 7
				task := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-sandbox-2", Identifier: "FAC-2"}
				invocation.Task, invocation.IssueIdentifier = task, task.Identifier
				invocation.Rule = state.routing.Invocations[5].Rule.Clone()
				invocation.AncestorRuleIDs = []string{invocation.Rule.ID}
				invocation.EventSequence = 102
				if origin == runs.AdmissionOriginNative {
					invocation.EventID = "factory:native-start:" + task.ProviderID
					invocation.ID = digestRunParts("factory-native-invocation-v1", task.OwnershipKey(), invocation.WorkflowDigest, "start")
				} else {
					eventKey := "message:msg-fedcba9876543210"
					invocation.EventID = "factory:native-continue:" + task.ProviderID + ":" + digestRunParts(eventKey)[:16]
					invocation.ID = digestRunParts("factory-native-invocation-v1", task.OwnershipKey(), invocation.WorkflowDigest, eventKey)
					legacy.TriggerKind = agentrun.TriggerKindComment
				}
				invocation.RootEventID = invocation.EventID
				decision.EventID, decision.EventSequence, decision.Source = invocation.EventID, invocation.EventSequence, eventwire.SourceFactory
				decision.Outcomes[0].RuleID, decision.Outcomes[0].InvocationID = invocation.Rule.ID, invocation.ID
				legacy.Task, legacy.IssueIdentifier = task, task.Identifier
				legacy.InvocationID, legacy.InvocationRootEventID = invocation.ID, invocation.RootEventID
				legacy.InvocationAncestorRuleIDs = slices.Clone(invocation.AncestorRuleIDs)
				legacy.DeliveryIDs = []string{invocation.EventID}
			}
			snapshot, _, err := convertRunSources(state, canonical, preAuditMigrationID(strings.Repeat("3", 64)), strings.Repeat("3", 64))
			if err != nil {
				t.Fatal(err)
			}
			model := snapshot.Model()
			var converted runs.Run
			var batch runs.AdmissionBatch
			for _, candidate := range model.Runs {
				if candidate.ID == legacy.ID {
					converted = candidate
				}
			}
			for _, candidate := range model.AdmissionBatches {
				if candidate.ID == converted.Causation.BatchID {
					batch = candidate
				}
			}
			if batch.Origin != origin || converted.MigratedBaseline == nil || !converted.MigratedBaseline.RepositoryRouteUnavailable {
				t.Fatalf("origin %q batch=%#v Run=%#v", origin, batch, converted)
			}
		})
	}
}

func TestConvertSynthesizesReflectedTerminalInvocation(t *testing.T) {
	state, canonical := syntheticRunSources(t)
	invocation := &state.routing.Invocations[2]
	invocation.State = triggerrouter.StateFailed
	invocation.Reason = "pruned terminal Run"
	invocation.ReflectedAt = ptrTime(invocation.UpdatedAt)
	snapshot, _, err := convertRunSources(state, canonical, preAuditMigrationID(strings.Repeat("4", 64)), strings.Repeat("4", 64))
	if err != nil {
		t.Fatal(err)
	}
	model := snapshot.Model()
	if model.TotalRuns != 13 {
		t.Fatalf("canonical lifetime total = %d, want 13", model.TotalRuns)
	}
	for _, run := range model.Runs {
		if run.Causation.AdmissionID == invocation.ID {
			if run.ID != invocation.RunID || run.State != runs.StateFailed || run.Detail != invocation.Reason || !run.MigratedBaseline.RepositoryRouteUnavailable {
				t.Fatalf("terminal synthesized Run = %#v", run)
			}
			return
		}
	}
	t.Fatal("terminal synthesized Run was not retained")
}

func TestConvertDirectTransitionUsesExactLegacyWireCausation(t *testing.T) {
	state, canonical := syntheticRunSources(t)
	direct := &state.runs.Runs[2]
	transition := agentrun.Transition{
		ID: direct.ID + ":succeeded:1784110920000000000", State: agentrun.StateSucceeded,
		Attempts: direct.Attempts, At: direct.UpdatedAt,
	}
	direct.Transitions = []agentrun.Transition{transition}
	state.wireRecords = append(state.wireRecords, transitionWireRecord(9, *direct, transition))
	state.wireTotal, state.wireDispatched = 9, 9
	slices.SortFunc(state.wireRecords, func(left, right eventwire.Record) int {
		if left.Sequence < right.Sequence {
			return -1
		}
		if left.Sequence > right.Sequence {
			return 1
		}
		return 0
	})

	_, metrics, err := convertRunSources(state, canonical, preAuditMigrationID(strings.Repeat("5", 64)), strings.Repeat("5", 64))
	if err != nil {
		t.Fatalf("exact direct transition: %v", err)
	}
	if metrics.audit.TransitionReceipts != 2 {
		t.Fatalf("transition receipts = %d, want 2", metrics.audit.TransitionReceipts)
	}

	mismatched := state
	mismatched.wireRecords = slices.Clone(state.wireRecords)
	for index := range mismatched.wireRecords {
		if mismatched.wireRecords[index].Event.ID == "factory:run-transition:"+transition.ID {
			mismatched.wireRecords[index].Event.RootEventID = mismatched.wireRecords[index].Event.ID
		}
	}
	if _, _, err := convertRunSources(mismatched, canonical, preAuditMigrationID(strings.Repeat("5", 64)), strings.Repeat("5", 64)); err == nil || !strings.Contains(err.Error(), "not exactly globally dispatched") {
		t.Fatalf("mismatched direct transition error = %v", err)
	}
}

func TestConvertTerminalHistoricalRoutePreservesReadyCheckpoint(t *testing.T) {
	state, canonical := syntheticRunSources(t)
	legacy := &state.runs.Runs[2]
	legacy.Repository = "example/retired-ready"
	legacy.RepositoryURL = "git@github.com:example/retired-ready.git"
	legacy.RepositoryPath = "/srv/sandbox/retired-ready"
	legacy.ManagedRoot = "/srv/sandbox"
	legacy.BaseBranch = "trunk"
	legacy.Ready.Repository = legacy.Repository
	legacy.Ready.BaseBranch = legacy.BaseBranch

	snapshot, _, err := convertRunSources(state, canonical, preAuditMigrationID(strings.Repeat("6", 64)), strings.Repeat("6", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range snapshot.Model().Runs {
		if run.ID != legacy.ID {
			continue
		}
		if run.Repository != nil || run.Ready == nil || run.Ready.Repository != legacy.Repository || run.Ready.BaseBranch != legacy.BaseBranch ||
			run.MigratedBaseline == nil || run.MigratedBaseline.HistoricalRepository == nil ||
			run.MigratedBaseline.HistoricalRepository.Repository != legacy.Repository {
			t.Fatalf("historical ready Run = %#v", run)
		}
		return
	}
	t.Fatal("historical ready Run was not retained")
}

func TestConvertRunSourcesFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*sourceState)
		want   string
	}{
		{name: "active missing pin", mutate: func(state *sourceState) {
			state.runs.Runs[0].PinnedWorkflow = nil
			state.runs.Runs[0].PinnedWorkflowDigest = ""
		}, want: "pin or digest"},
		{name: "active missing route", mutate: func(state *sourceState) {
			run := &state.runs.Runs[0]
			run.Repository, run.RepositoryURL, run.RepositoryPath, run.ManagedRoot, run.BaseBranch, run.CloudURL = "", "", "", "", "", ""
		}, want: "missing its repository route"},
		{name: "exact task mismatch", mutate: func(state *sourceState) {
			state.runs.Runs[1].Task.Identifier = "FAC-2"
			state.runs.Runs[1].IssueIdentifier = "FAC-2"
		}, want: "task identities disagree"},
		{name: "claimed missing Run", mutate: func(state *sourceState) {
			state.runs.Runs = slices.Delete(state.runs.Runs, 0, 1)
		}, want: "missing its Run"},
		{name: "ambiguous coalescing", mutate: func(state *sourceState) {
			state.runs.Runs[1].DeliveryIDs = state.runs.Runs[1].DeliveryIDs[:1]
			state.runs.Runs[1].DuplicateTriggers = 0
		}, want: "does not exactly match its owner"},
		{name: "unsupported rejection", mutate: func(state *sourceState) {
			state.routing.Invocations[3].Reason = "unknown-rejection"
		}, want: "unsupported evidence"},
		{name: "terminal mismatch", mutate: func(state *sourceState) {
			reflected := state.routing.Invocations[5].UpdatedAt
			state.routing.Invocations[5].State = triggerrouter.StateSucceeded
			state.routing.Invocations[5].ReflectedAt = &reflected
		}, want: "terminal states disagree"},
		{name: "missing terminal reflection", mutate: func(state *sourceState) {
			state.routing.Invocations[2].State = triggerrouter.StateFailed
			state.routing.Invocations[2].Reason = "historical terminal"
			state.routing.Invocations[2].RunID = ""
		}, want: "invalid reflection evidence"},
		{name: "terminal missing deterministic Run ID", mutate: func(state *sourceState) {
			invocation := &state.routing.Invocations[2]
			invocation.State = triggerrouter.StateFailed
			invocation.Reason = "historical terminal"
			invocation.RunID = ""
			invocation.ReflectedAt = ptrTime(invocation.UpdatedAt)
		}, want: "invalid reflection evidence"},
		{name: "terminal mismatched deterministic Run ID", mutate: func(state *sourceState) {
			invocation := &state.routing.Invocations[2]
			invocation.State = triggerrouter.StateFailed
			invocation.Reason = "historical terminal"
			invocation.RunID = "run-fedcba9876543210"
			invocation.ReflectedAt = ptrTime(invocation.UpdatedAt)
		}, want: "invalid reflection evidence"},
		{name: "terminal reflection timestamp mismatch", mutate: func(state *sourceState) {
			invocation := &state.routing.Invocations[2]
			invocation.State = triggerrouter.StateFailed
			invocation.Reason = "historical terminal"
			invocation.ReflectedAt = ptrTime(invocation.UpdatedAt.Add(-time.Nanosecond))
		}, want: "invalid reflection evidence"},
		{name: "terminal lifetime coverage gap", mutate: func(state *sourceState) {
			invocation := &state.routing.Invocations[2]
			invocation.State = triggerrouter.StateFailed
			invocation.Reason = "historical terminal"
			invocation.ReflectedAt = ptrTime(invocation.UpdatedAt)
			state.runs.Total = uint64(len(state.runs.Runs))
		}, want: "does not cover synthesized terminal Runs"},
		{name: "unacknowledged transition", mutate: func(state *sourceState) {
			state.wireDispatched = 6
		}, want: "not exactly globally dispatched"},
		{name: "duplicate wire sequence", mutate: func(state *sourceState) {
			state.wireRecords[1].Sequence = state.wireRecords[0].Sequence
		}, want: "duplicate wire identity or sequence"},
		{name: "deterministic identity", mutate: func(state *sourceState) {
			wrong := strings.Repeat("a", 64)
			state.routing.Decisions[1].Outcomes[0].InvocationID = wrong
			state.routing.Invocations[1].ID = wrong
		}, want: "deterministic identity conflicts"},
		{name: "mixed origins", mutate: func(state *sourceState) {
			state.routing.Decisions[0].Outcomes = append(state.routing.Decisions[0].Outcomes, state.routing.Decisions[7].Outcomes[0])
		}, want: "mixes admission origins"},
		{name: "invalid rate bucket", mutate: func(state *sourceState) {
			state.routing.RateBuckets[0].Minute = state.routing.RateBuckets[0].Minute.Add(time.Second)
		}, want: "rate bucket is invalid"},
		{name: "retained total underflow", mutate: func(state *sourceState) {
			state.runs.Total = 4
		}, want: "exceed their lifetime total"},
		{name: "Run identity collision", mutate: func(state *sourceState) {
			state.runs.Runs[4].ID = syntheticRunID("event:queued", "sandbox-rule", 1)
		}, want: "sorted and unique"},
		{name: "completion evidence mismatch", mutate: func(state *sourceState) {
			state.runs.Runs[2].Completion.PullRequestHead = strings.Repeat("c", 40)
		}, want: "verified head"},
		{name: "worker identity missing", mutate: func(state *sourceState) {
			state.runs.Runs[0].SessionName = ""
		}, want: "worker lifecycle"},
		{name: "direct reflection mismatch", mutate: func(state *sourceState) {
			state.runs.Runs[3].InvocationReflectedAt = ptrTime(converterNow)
		}, want: "invalid reflection evidence"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, canonical := syntheticRunSources(t)
			test.mutate(&state)
			_, _, err := convertRunSources(state, canonical, preAuditMigrationID(strings.Repeat("2", 64)), strings.Repeat("2", 64))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func syntheticRunSources(t *testing.T) (sourceState, canonicalEvidence) {
	t.Helper()
	pin := workflow.Pin(workflow.Definition{
		ID: "sandbox-review", Revision: 1, Name: "Sandbox review", Enabled: true,
		Markdown: "# Sandbox converter workflow\n", UpdatedAt: converterNow,
	})
	digest, err := pin.Digest()
	if err != nil {
		t.Fatal(err)
	}
	rule := triggerregistry.Rule{ID: "sandbox-rule", Revision: 1, Name: "Sandbox rule", Enabled: true, WorkflowID: pin.ID}
	nativeRule := triggerregistry.Rule{ID: "native-task-start", Revision: 1, Name: "Native task start", Enabled: true, WorkflowID: pin.ID}

	linearTasks := []taskmodel.TaskRef{
		{Source: taskmodel.SourceLinear, ProviderID: "OPS-142", Identifier: "OPS-142"},
		{Source: taskmodel.SourceLinear, ProviderID: "OPS-143", Identifier: "OPS-143"},
		{Source: taskmodel.SourceLinear, ProviderID: "OPS-144", Identifier: "OPS-144"},
		{Source: taskmodel.SourceLinear, ProviderID: "OPS-145", Identifier: "OPS-145"},
	}
	nativeTask := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-sandbox-1", Identifier: "FAC-1"}

	decisions := make([]triggerrouter.Decision, 0, 8)
	invocations := make([]triggerrouter.Invocation, 0, 6)
	wire := make([]eventwire.Record, 0, 7)
	for index, eventID := range []string{"event:linked", "event:queued", "event:claiming", "event:rejected"} {
		sequence := uint64(index + 1)
		admitted := converterNow.Add(time.Duration(index+1) * time.Minute)
		invocation := triggerrouter.Invocation{
			ID: syntheticInvocationID(eventID, rule.ID, rule.Revision), EventID: eventID, EventSequence: sequence,
			Rule: rule, Workflow: pin.Clone(), WorkflowDigest: digest, PolicyRevision: 7,
			Task: linearTasks[index], IssueIdentifier: linearTasks[index].Identifier,
			RootEventID: eventID, Hop: 1, AncestorRuleIDs: []string{rule.ID},
			State: triggerrouter.StateQueued, AdmittedAt: admitted, UpdatedAt: admitted,
		}
		invocations = append(invocations, invocation)
		decisions = append(decisions, triggerrouter.Decision{
			EventID: eventID, EventSequence: sequence, Source: eventwire.SourceLinear,
			RegistryRevision: 3, SettingsRevision: 7, DecidedAt: admitted.Add(-time.Second),
			Outcomes: []triggerrouter.Outcome{{Kind: triggerrouter.OutcomeInvocation, RuleID: rule.ID, RuleRevision: rule.Revision, InvocationID: invocation.ID}},
		})
		wire = append(wire, directWireRecord(sequence, eventID, eventwire.SourceLinear, admitted.Add(-time.Second)))
	}
	invocations[0].State = triggerrouter.StateClaimed
	invocations[0].RunID = "run-sandbox-linked"
	invocations[0].UpdatedAt = converterNow.Add(3 * time.Minute)
	invocations[2].State = triggerrouter.StateClaiming
	invocations[2].RunID = syntheticRunID("event:claiming", rule.ID, rule.Revision)
	invocations[2].UpdatedAt = converterNow.Add(4*time.Minute + time.Second)
	invocations[3].State = triggerrouter.StateRejected
	invocations[3].RunID = syntheticRunID("event:rejected", rule.ID, rule.Revision)
	invocations[3].Reason = "repository-routing-rejected"
	invocations[3].UpdatedAt = converterNow.Add(5 * time.Minute)

	decisions = append(decisions,
		triggerrouter.Decision{
			EventID: "event:non-runnable", EventSequence: 5, Source: eventwire.SourceLinear,
			RegistryRevision: 3, SettingsRevision: 7, DecidedAt: converterNow.Add(5 * time.Minute),
			Outcomes: []triggerrouter.Outcome{
				{Kind: triggerrouter.OutcomeRejected, RuleID: rule.ID, RuleRevision: 1, Reason: "quota"},
				{Kind: triggerrouter.OutcomeSuppressed, RuleID: "other-rule", RuleRevision: 2, Reason: "filter"},
			},
		},
		triggerrouter.Decision{
			EventID: "event:empty", EventSequence: 6, Source: eventwire.SourceGitHub,
			RegistryRevision: 3, SettingsRevision: 7, DecidedAt: converterNow.Add(6 * time.Minute), Outcomes: []triggerrouter.Outcome{},
		},
	)
	wire = append(wire,
		directWireRecord(5, "event:non-runnable", eventwire.SourceLinear, converterNow.Add(5*time.Minute)),
		directWireRecord(6, "event:empty", eventwire.SourceGitHub, converterNow.Add(6*time.Minute)),
	)
	terminalTask := linearTask("OPS-149")
	terminalAdmitted := converterNow.Add(9 * time.Minute)
	terminalReflected := terminalAdmitted.Add(3 * time.Second)
	terminalInvocation := triggerrouter.Invocation{
		ID:      syntheticInvocationID("event:linked-terminal", rule.ID, rule.Revision),
		EventID: "event:linked-terminal", EventSequence: 8, Rule: rule,
		Workflow: pin.Clone(), WorkflowDigest: digest, PolicyRevision: 7,
		Task: terminalTask, IssueIdentifier: terminalTask.Identifier,
		RootEventID: "event:linked-terminal", Hop: 1, AncestorRuleIDs: []string{rule.ID},
		State: triggerrouter.StateFailed, RunID: "run-linked-terminal", Reason: "terminal sandbox failure",
		AdmittedAt: terminalAdmitted, UpdatedAt: terminalReflected, ReflectedAt: &terminalReflected,
	}
	invocations = append(invocations, terminalInvocation)
	decisions = append(decisions, triggerrouter.Decision{
		EventID: terminalInvocation.EventID, EventSequence: terminalInvocation.EventSequence, Source: eventwire.SourceLinear,
		RegistryRevision: 3, SettingsRevision: 7, DecidedAt: terminalAdmitted.Add(-time.Second),
		Outcomes: []triggerrouter.Outcome{{Kind: triggerrouter.OutcomeInvocation, RuleID: rule.ID, RuleRevision: rule.Revision, InvocationID: terminalInvocation.ID}},
	})
	wire = append(wire, directWireRecord(8, terminalInvocation.EventID, eventwire.SourceLinear, terminalAdmitted.Add(-time.Second)))

	nativeStartID := syntheticNativeStartID(t)
	nativeAdmitted := converterNow.Add(7 * time.Minute)
	nativeInvocation := triggerrouter.Invocation{
		ID: nativeStartID, EventID: "factory:native-start:" + nativeTask.ProviderID, EventSequence: 100,
		Rule: nativeRule, Workflow: pin.Clone(), WorkflowDigest: digest, PolicyRevision: 7,
		Task: nativeTask, IssueIdentifier: nativeTask.Identifier, RootEventID: "factory:native-start:" + nativeTask.ProviderID,
		Hop: 1, AncestorRuleIDs: []string{nativeRule.ID}, State: triggerrouter.StateClaimed, RunID: "run-native-owner",
		AdmittedAt: nativeAdmitted, UpdatedAt: nativeAdmitted.Add(time.Second),
	}
	continuationAdmitted := nativeAdmitted.Add(time.Minute)
	continuationEventID := syntheticContinuationEventID()
	reflected := continuationAdmitted.Add(2 * time.Second)
	continuation := triggerrouter.Invocation{
		ID: syntheticContinuationID(), EventID: continuationEventID, EventSequence: 101,
		Rule: nativeRule, Workflow: pin.Clone(), WorkflowDigest: digest, PolicyRevision: 7,
		Task: nativeTask, IssueIdentifier: nativeTask.Identifier, RootEventID: continuationEventID,
		Hop: 1, AncestorRuleIDs: []string{nativeRule.ID}, State: triggerrouter.StateRejected,
		RunID: "run-native-owner", Reason: "native-feedback-coalesced", AdmittedAt: continuationAdmitted,
		UpdatedAt: reflected, ReflectedAt: &reflected,
	}
	invocations = append(invocations, nativeInvocation, continuation)
	decisions = append(decisions,
		triggerrouter.Decision{
			EventID: nativeInvocation.EventID, EventSequence: 100, Source: eventwire.SourceFactory,
			RegistryRevision: 3, SettingsRevision: 7, DecidedAt: nativeAdmitted,
			Outcomes: []triggerrouter.Outcome{{Kind: triggerrouter.OutcomeInvocation, RuleID: nativeRule.ID, RuleRevision: 1, InvocationID: nativeInvocation.ID}},
		},
		triggerrouter.Decision{
			EventID: continuation.EventID, EventSequence: 101, Source: eventwire.SourceFactory,
			RegistryRevision: 3, SettingsRevision: 7, DecidedAt: continuationAdmitted,
			Outcomes: []triggerrouter.Outcome{{Kind: triggerrouter.OutcomeInvocation, RuleID: nativeRule.ID, RuleRevision: 1, InvocationID: continuation.ID}},
		},
	)

	segmentStart := converterNow.Add(2*time.Minute + 10*time.Second)
	started := converterNow.Add(2*time.Minute + 5*time.Second)
	refresh := converterNow.Add(2*time.Minute + 30*time.Second)
	nextReconcile := converterNow.Add(10 * time.Minute)
	linkedTransition := agentrun.Transition{
		ID: "run-sandbox-linked:pending:1784106120000000000", State: agentrun.StatePending,
		Attempts: 0, At: converterNow.Add(2 * time.Minute),
	}
	linkedRun := agentrun.Run{
		ID: "run-sandbox-linked", Task: linearTasks[0], IssueIdentifier: linearTasks[0].Identifier,
		Repository: "example/factory-sandbox", RepositoryURL: "git@github.com:example/factory-sandbox.git",
		RepositoryPath: "/srv/sandbox/repos/factory-sandbox", ManagedRoot: "/srv/sandbox/repos", BaseBranch: "main",
		TriggerKind: agentrun.TriggerKindRule, DeliveryIDs: []string{"event:linked", "delivery:linked-duplicate"},
		State: agentrun.StateRunning, SessionName: "sandbox-session", RunDirectory: "/srv/sandbox/runs/run-sandbox-linked",
		Attempts: 2, DuplicateTriggers: 1, Detail: "active sandbox work",
		CreatedAt: converterNow.Add(2 * time.Minute), UpdatedAt: converterNow.Add(3 * time.Minute),
		StartedAt: &started, SegmentStartedAt: &segmentStart, SegmentAttempt: 2,
		Transitions: []agentrun.Transition{linkedTransition}, LastGitHubCursor: 17,
		LastAuthoritativeRefreshAt: &refresh, NextReconcileAt: &nextReconcile, ReconcileFailures: 2,
		RemediationRequested: true, ResumeCount: 1,
		InvocationID: invocations[0].ID, InvocationRootEventID: invocations[0].RootEventID,
		InvocationHop: invocations[0].Hop, InvocationAncestorRuleIDs: slices.Clone(invocations[0].AncestorRuleIDs),
		PinnedWorkflow: ptrPinned(pin), PinnedWorkflowDigest: digest, PinnedPolicyRevision: 7,
	}
	wire = append(wire, transitionWireRecord(7, linkedRun, linkedTransition))
	nativeOwner := agentrun.Run{
		ID: "run-native-owner", Task: nativeTask, IssueIdentifier: nativeTask.Identifier,
		Repository: "example/factory-sandbox", RepositoryURL: "git@github.com:example/factory-sandbox.git",
		RepositoryPath: "/srv/sandbox/repos/factory-sandbox", ManagedRoot: "/srv/sandbox/repos", BaseBranch: "main",
		TriggerKind: agentrun.TriggerKindRule, DeliveryIDs: []string{nativeInvocation.EventID, continuation.EventID},
		State: agentrun.StatePending, DuplicateTriggers: 1,
		CreatedAt: nativeAdmitted.Add(time.Second), UpdatedAt: reflected, Transitions: []agentrun.Transition{},
		InvocationID: nativeInvocation.ID, InvocationRootEventID: nativeInvocation.RootEventID,
		InvocationHop: nativeInvocation.Hop, InvocationAncestorRuleIDs: slices.Clone(nativeInvocation.AncestorRuleIDs),
		PinnedWorkflow: ptrPinned(pin), PinnedWorkflowDigest: digest, PinnedPolicyRevision: 7,
	}
	terminalFinished := terminalReflected
	linkedTerminalRun := agentrun.Run{
		ID: "run-linked-terminal", Task: terminalTask, IssueIdentifier: terminalTask.Identifier,
		TriggerKind: agentrun.TriggerKindRule, DeliveryIDs: []string{terminalInvocation.EventID},
		State: agentrun.StateFailed, Detail: terminalInvocation.Reason,
		CreatedAt: terminalAdmitted.Add(time.Second), UpdatedAt: terminalReflected, FinishedAt: &terminalFinished,
		InvocationID: terminalInvocation.ID, InvocationRootEventID: terminalInvocation.RootEventID,
		InvocationHop: terminalInvocation.Hop, InvocationAncestorRuleIDs: slices.Clone(terminalInvocation.AncestorRuleIDs),
		PinnedWorkflow: ptrPinned(pin.Compact()), PinnedWorkflowDigest: digest, PinnedPolicyRevision: 7,
		InvocationReflectedAt: &terminalReflected,
	}

	directSuccess := syntheticDirectSuccess(pin, digest, linearTask("OPS-146"))
	directHistorical := syntheticDirectHistorical(pin.Compact(), digest, linearTask("OPS-147"))
	directUnavailable := syntheticDirectUnavailable(linearTask("OPS-148"))
	slices.SortFunc(wire, func(left, right eventwire.Record) int {
		if left.Sequence < right.Sequence {
			return -1
		}
		if left.Sequence > right.Sequence {
			return 1
		}
		return 0
	})

	state := sourceState{
		routing: triggerrouter.Snapshot{
			Schema: triggerrouter.SchemaVersion, Decisions: decisions, Invocations: invocations,
			RateBuckets: []triggerrouter.RateBucket{
				{RuleID: rule.ID, Minute: converterNow.UTC().Truncate(time.Minute), Count: 8},
				{RuleID: nativeRule.ID, Minute: converterNow.Add(time.Minute).UTC().Truncate(time.Minute), Count: 2},
			},
		},
		runs:           runState{Version: 2, Total: 10, Runs: []agentrun.Run{linkedRun, nativeOwner, directSuccess, directHistorical, directUnavailable, linkedTerminalRun}},
		wireRecords:    wire,
		wireTotal:      8,
		wireDispatched: 8,
	}
	canonical := canonicalEvidence{
		policySnapshot:  syntheticPolicySnapshot(t),
		repositoryState: syntheticRepositoryState(),
	}
	return state, canonical
}

func syntheticDirectSuccess(pin workflow.Pinned, digest string, task taskmodel.TaskRef) agentrun.Run {
	created := converterNow.Add(20 * time.Minute)
	started := created.Add(time.Minute)
	finished := created.Add(5 * time.Minute)
	readyCreated := created.Add(3 * time.Minute)
	readyValidated := readyCreated.Add(time.Second)
	head := strings.Repeat("a", 40)
	merge := strings.Repeat("b", 40)
	return agentrun.Run{
		ID: "run-direct-success", Task: task, IssueIdentifier: task.Identifier,
		Repository: "example/factory-sandbox", RepositoryURL: "git@github.com:example/factory-sandbox.git",
		RepositoryPath: "/srv/sandbox/repos/factory-sandbox", ManagedRoot: "/srv/sandbox/repos", BaseBranch: "main",
		TriggerKind: agentrun.TriggerKindRule, DeliveryIDs: []string{"delivery:direct-success"}, State: agentrun.StateSucceeded,
		Attempts: 1, Detail: "completed", CreatedAt: created, UpdatedAt: finished, StartedAt: &started, FinishedAt: &finished,
		Ready: &agentrun.ReadyCheckpoint{
			ContractVersion: 1, RunID: "run-direct-success", Task: task, Repository: "example/factory-sandbox",
			PullRequest: 42, BaseBranch: "main", HeadBranch: "ops-146-sandbox", VerifiedHeadOID: head,
			CreatedAt: readyCreated, ValidatedAt: readyValidated,
		},
		MergeCommitOID: merge, LastGitHubCursor: 29, ResumeCount: 2,
		Completion: &agentrun.CompletionValidation{
			Accepted: true, Intent: string(agentrun.StateSucceeded), State: agentrun.StateSucceeded,
			Reason: "verified", ValidatedAt: finished, PullRequestState: "merged",
			PullRequestHead: head, MergeCommitOID: merge,
		},
		PinnedWorkflow: ptrPinned(pin), PinnedWorkflowDigest: digest, PinnedPolicyRevision: 7,
	}
}

func syntheticDirectHistorical(pin workflow.Pinned, digest string, task taskmodel.TaskRef) agentrun.Run {
	created := converterNow.Add(30 * time.Minute)
	finished := created.Add(2 * time.Minute)
	return agentrun.Run{
		ID: "run-direct-historical", InvocationID: "invocation-pruned", Task: task, IssueIdentifier: task.Identifier,
		Repository: "example/retired-sandbox", RepositoryURL: "git@github.com:example/retired-sandbox.git",
		RepositoryPath: "/srv/sandbox/retired", ManagedRoot: "/srv/sandbox", BaseBranch: "trunk",
		TriggerKind: agentrun.TriggerKindRule, DeliveryIDs: []string{"delivery:direct-historical"},
		State: agentrun.StateFailed, Attempts: 1, Detail: "historical failure",
		CreatedAt: created, UpdatedAt: finished, FinishedAt: &finished,
		PinnedWorkflow: ptrPinned(pin), PinnedWorkflowDigest: digest, PinnedPolicyRevision: 7,
	}
}

func syntheticDirectUnavailable(task taskmodel.TaskRef) agentrun.Run {
	created := converterNow.Add(40 * time.Minute)
	finished := created.Add(time.Minute)
	return agentrun.Run{
		ID: "run-direct-unavailable", Task: task, IssueIdentifier: task.Identifier,
		TriggerKind: agentrun.TriggerKindRule, DeliveryIDs: []string{"delivery:direct-unavailable"},
		State: agentrun.StateBlocked, Detail: "historical metadata unavailable",
		CreatedAt: created, UpdatedAt: finished, FinishedAt: &finished, PinnedPolicyRevision: 7,
	}
}

func syntheticPolicySnapshot(t *testing.T) policy.Snapshot {
	t.Helper()
	snapshot, err := policy.NewSnapshot(policy.Model{
		Schema: policy.SchemaVersion, Generation: 1,
		Settings: policy.Settings{
			Revision: 7, UpdatedAt: converterNow,
			Agents: policy.AgentSettings{
				Principal:   policy.PrincipalSettings{ProviderSettings: policy.ProviderSettings{Model: "gpt-5", Effort: "high"}, MaxAttempts: 3},
				CodexChild:  policy.ProviderSettings{Model: "gpt-5", Effort: "high"},
				ClaudeChild: policy.ProviderSettings{Model: "claude-sonnet", Effort: "high"},
			},
			Runtime: policy.RuntimeSettings{MaxConcurrentRuns: 2},
		},
		ProtectedWorkflows: policy.ProtectedWorkflowBindings{LinearFeedback: policy.WorkflowBinding{WorkflowID: "sandbox-review"}},
		Workflows:          []policy.Workflow{{ID: "sandbox-review", Revision: 1, Name: "Sandbox review", Enabled: true, Markdown: "# Sandbox converter workflow\n", UpdatedAt: converterNow}},
		Registry:           policy.Registry{Revision: 3, UpdatedAt: converterNow},
		TaskControl:        policy.TaskControl{Revision: 1, UpdatedAt: converterNow, EnabledProjectIDs: []string{"project-sandbox"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func syntheticRepositoryState() repositories.SourceState {
	provisioned := converterNow
	return repositories.SourceState{
		Schema: repositories.SchemaVersion, Generation: 1,
		Records: []repositories.Record{{
			App: "sandbox", Provenance: repositories.ProvenanceProject,
			Project:    repositories.ProjectIdentity{ID: "project-sandbox", Name: "Sandbox"},
			Repository: "example/factory-sandbox", Origin: "git@github.com:example/factory-sandbox.git",
			LocalPath: "/srv/sandbox/repos/factory-sandbox", ManagedPath: "/srv/sandbox/repos/factory-sandbox",
			ManagedRoot: "/srv/sandbox/repos", DefaultBranch: "main",
			Setup: repositories.Setup{
				State: repositories.SetupStateSucceeded, Attempts: 1, CreatedAt: converterNow, UpdatedAt: converterNow,
				ProvisionedAt: &provisioned, ProviderCoordinated: true,
			},
		}},
	}
}

func syntheticInvocationID(eventID, ruleID string, revision uint64) string {
	return digestRunParts("factory-trigger-invocation-v1", eventID, ruleID, strconv.FormatUint(revision, 10))
}

func syntheticRunID(eventID, ruleID string, revision uint64) string {
	return "run-" + digestRunParts("factory-trigger-run-v1", syntheticInvocationID(eventID, ruleID, revision))[:16]
}

func syntheticNativeStartID(t *testing.T) string {
	t.Helper()
	pin := workflow.Pin(workflow.Definition{ID: "sandbox-review", Revision: 1, Name: "Sandbox review", Enabled: true, Markdown: "# Sandbox converter workflow\n", UpdatedAt: converterNow})
	digest, err := pin.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digestRunParts("factory-native-invocation-v1", "factory:task-sandbox-1", digest, "start")
}

func syntheticContinuationEventID() string {
	eventKey := "message:msg-0123456789abcdef"
	return "factory:native-continue:task-sandbox-1:" + digestRunParts(eventKey)[:16]
}

func syntheticContinuationID() string {
	pin := workflow.Pin(workflow.Definition{ID: "sandbox-review", Revision: 1, Name: "Sandbox review", Enabled: true, Markdown: "# Sandbox converter workflow\n", UpdatedAt: converterNow})
	digest, _ := pin.Digest()
	return digestRunParts("factory-native-invocation-v1", "factory:task-sandbox-1", digest, "message:msg-0123456789abcdef")
}

func directWireRecord(sequence uint64, id string, source eventwire.Source, at time.Time) eventwire.Record {
	return eventwire.Record{Sequence: sequence, Event: eventwire.Event{
		ID: id, Source: source, Type: "sandbox", Action: "admit", RootEventID: id, ReceivedAt: at,
	}}
}

func transitionWireRecord(sequence uint64, run agentrun.Run, transition agentrun.Transition) eventwire.Record {
	record := eventwire.Record{Sequence: sequence, Event: eventwire.Event{
		ID: "factory:run-transition:" + transition.ID, Source: eventwire.SourceFactory,
		Type: "agent-run", Action: string(transition.State), Subject: run.IssueIdentifier,
		Attributes: map[string][]string{
			"runId": {run.ID}, "attempts": {strconv.Itoa(transition.Attempts)}, "taskSource": {string(run.Task.Source)},
			"taskProviderId": {run.Task.ProviderID}, "taskIdentifier": {run.Task.Identifier},
			eventwire.AttributeProducer: {"agent-collector"}, eventwire.AttributeProvenance: {"factory"},
		},
		ReceivedAt: transition.At,
	}}
	if run.InvocationID != "" {
		record.Event.RootEventID = run.InvocationRootEventID
		record.Event.ParentInvocationID = run.InvocationID
		record.Event.ParentRunID = run.ID
		record.Event.Hop = run.InvocationHop
		record.Event.AncestorRuleIDs = slices.Clone(run.InvocationAncestorRuleIDs)
	}
	return record
}

func linearTask(identifier string) taskmodel.TaskRef {
	return taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: identifier, Identifier: identifier}
}

func ptrPinned(pin workflow.Pinned) *workflow.Pinned {
	clone := pin.Clone()
	return &clone
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
