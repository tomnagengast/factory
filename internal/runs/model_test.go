package runs

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

var modelTestNow = time.Date(2026, time.July, 16, 19, 0, 0, 0, time.UTC)

func TestSnapshotCanonicalDigestAndNonAliasing(t *testing.T) {
	model := EmptyModel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	firstBatch, firstRun, firstRate := testAdmissionProjection(t, root, 1, StatePending)
	secondBatch, secondRun, secondRate := testAdmissionProjection(t, root, 2, StatePending)
	model.JournalSequence = 7
	model.TotalBatches = 2
	model.TotalRuns = 2
	model.AdmissionOperations = []AdmissionOperationReceipt{
		testAdmissionReceipt([]AdmissionBatch{secondBatch}, []Run{secondRun}, []RateBucket{secondRate}),
		testAdmissionReceipt([]AdmissionBatch{firstBatch}, []Run{firstRun}, []RateBucket{firstRate}),
	}
	model.AdmissionBatches = []AdmissionBatch{secondBatch, firstBatch}
	model.Runs = []Run{secondRun, firstRun}
	model.RateBuckets = []RateBucket{secondRate, firstRate}

	snapshot, err := NewSnapshot(model)
	if err != nil {
		t.Fatal(err)
	}
	canonical := snapshot.Model()
	if canonical.AdmissionBatches[0].ID != firstBatch.ID || canonical.Runs[0].ID != firstRun.ID || canonical.RateBuckets[0].Minute.After(canonical.RateBuckets[1].Minute) {
		t.Fatalf("projection is not canonical: %#v", canonical)
	}
	if !slices.IsSorted(canonical.Runs[0].DeliveryIDs) {
		t.Fatalf("delivery IDs are not canonical: %#v", canonical.Runs[0].DeliveryIDs)
	}

	digest, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	canonical.JournalSequence = 99
	secondSnapshot, err := NewSnapshot(canonical)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := secondSnapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest != secondDigest || len(digest) != 64 {
		t.Fatalf("semantic digests = %q and %q", digest, secondDigest)
	}

	model.AdmissionBatches[0].Outcomes[0].Reason = "mutated input"
	model.AdmissionOperations[0].Runs[0].DeliveryIDs[0] = "mutated-operation-input"
	model.Runs[0].DeliveryIDs[0] = "mutated-input"
	model.Runs[0].Causation.AncestorRuleIDs[0] = "mutated-input"
	model.Runs[0].Causation.Workflow.Steps = append(model.Runs[0].Causation.Workflow.Steps, "mutated")
	model.Runs[0].Repository.Repository = "mutated/repository"
	model.Runs[0].StartedAt = pointerTime(modelTestNow.Add(24 * time.Hour))
	read := snapshot.Model()
	if read.AdmissionBatches[1].Outcomes[0].Reason == "mutated input" ||
		strings.Contains(strings.Join(read.AdmissionOperations[0].Runs[0].DeliveryIDs, ","), "mutated-operation") ||
		strings.Contains(strings.Join(read.Runs[1].DeliveryIDs, ","), "mutated") ||
		read.Runs[1].Repository.Repository == "mutated/repository" || read.Runs[1].StartedAt != nil {
		t.Fatalf("snapshot aliases caller input: %#v", read)
	}

	read.AdmissionBatches[0].Outcomes[0].AdmissionID = "mutated-output"
	read.AdmissionOperations[0].AdmissionBatches[0].EventID = "mutated-operation-output"
	read.Runs[0].Causation.Workflow.Markdown = "mutated-output"
	read.Runs[0].Repository.ProjectID = "mutated-output"
	read.Runs[0].DeliveryIDs[0] = "mutated-output"
	readAgain := snapshot.Model()
	if reflect.DeepEqual(read, readAgain) || strings.Contains(readAgain.Runs[0].Causation.Workflow.Markdown, "mutated-output") {
		t.Fatalf("snapshot Model aliases returned data: %#v", readAgain)
	}
}

func TestSnapshotRejectsBrokenIdentityAndLifecycleInvariants(t *testing.T) {
	batch, run, rate := testAdmissionProjection(t, t.TempDir(), 1, StatePending)
	base := testSingleAdmissionModel(batch, run, rate)
	tests := []struct {
		name   string
		mutate func(*Model)
		want   string
	}{
		{name: "missing schema", mutate: func(value *Model) { value.Schema = 0 }, want: "schema"},
		{name: "missing operation receipts", mutate: func(value *Model) { value.AdmissionOperations = nil }, want: "receipts"},
		{name: "invalid operation receipt", mutate: func(value *Model) { value.AdmissionOperations[0].RateIncrements[0].Count++ }, want: "operation receipt"},
		{name: "total below retained", mutate: func(value *Model) { value.TotalRuns = 0 }, want: "retained"},
		{name: "outcome link", mutate: func(value *Model) { value.AdmissionBatches[0].Outcomes[1].RunID = "run-other" }, want: "linkage"},
		{name: "admission identity", mutate: func(value *Model) { value.Runs[0].Causation.AdmissionID = "admission-other" }, want: "linkage"},
		{name: "workflow digest", mutate: func(value *Model) { value.Runs[0].Causation.WorkflowDigest = strings.Repeat("0", 64) }, want: "workflow pin"},
		{name: "ancestor path", mutate: func(value *Model) { value.Runs[0].Causation.AncestorRuleIDs[0] = "rule-other" }, want: "ancestor"},
		{name: "repository containment", mutate: func(value *Model) { value.Runs[0].Repository.ManagedPath = "/tmp/outside" }, want: "repository route"},
		{name: "delivery collision", mutate: func(value *Model) { value.Runs[0].DeliveryIDs = []string{"delivery", "delivery"} }, want: "delivery"},
		{name: "transition state", mutate: func(value *Model) { value.Runs[0].Transitions[0].State = StateRunning }, want: "transition history"},
		{name: "terminal finish", mutate: func(value *Model) { value.Runs[0].State = StateSucceeded }, want: "finish"},
		{name: "rate bucket", mutate: func(value *Model) { value.RateBuckets[0].Count = 0 }, want: "rate bucket"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneModel(base)
			test.mutate(&candidate)
			if _, err := NewSnapshot(candidate); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("NewSnapshot error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSnapshotDigestIncludesEvictedAdmissionOperationReceipt(t *testing.T) {
	root := privateModelTestRoot(t)
	firstBatch, firstRun, firstRate := testAdmissionProjection(t, root, 1, StateSucceeded)
	secondBatch, secondRun, secondRate := testAdmissionProjection(t, root, 2, StateSucceeded)
	model := Model{
		Schema: SchemaVersion, TotalBatches: 2, TotalRuns: 2,
		AdmissionOperations: []AdmissionOperationReceipt{
			testAdmissionReceipt([]AdmissionBatch{firstBatch}, []Run{firstRun}, []RateBucket{firstRate}),
			testAdmissionReceipt([]AdmissionBatch{secondBatch}, []Run{secondRun}, []RateBucket{secondRate}),
		},
		AdmissionBatches: []AdmissionBatch{secondBatch}, Runs: []Run{secondRun},
		RateBuckets: []RateBucket{firstRate, secondRate},
	}
	before, err := NewSnapshot(model)
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest, err := before.Digest()
	if err != nil {
		t.Fatal(err)
	}
	changed := before.Model()
	for index := range changed.AdmissionOperations[0].AdmissionBatches[0].Outcomes {
		outcome := &changed.AdmissionOperations[0].AdmissionBatches[0].Outcomes[index]
		if outcome.Kind == AdmissionOutcomeSuppressed {
			outcome.Reason = "different durable suppression"
		}
	}
	after, err := NewSnapshot(changed)
	if err != nil {
		t.Fatal(err)
	}
	afterDigest, err := after.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if beforeDigest == afterDigest {
		t.Fatal("semantic digest omitted an evicted durable admission operation receipt")
	}
}

func TestTerminalRunPreservesLegacyAndCompactedWorkflowPins(t *testing.T) {
	for _, test := range []struct {
		name string
		pin  *workflow.Pinned
	}{
		{name: "legacy", pin: pointerPin(workflow.Pinned{ID: "full-sdlc", Name: "Full SDLC", Enabled: true, Runner: "do", Steps: []string{"plan", "implement"}})},
		{name: "compacted", pin: pointerPin(workflow.Pinned{ID: "full-sdlc", Revision: 4})},
		{name: "historical direct without pin", pin: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			batch, run, rate := testAdmissionProjection(t, t.TempDir(), 1, StateSucceeded)
			run.Causation.Workflow = test.pin
			if test.pin == nil {
				run.Causation.WorkflowDigest = ""
				run.MigratedBaseline = &MigratedBaseline{
					State: run.State, ObservedAt: run.UpdatedAt, PriorTransitionsAcknowledged: true, WorkflowPinUnavailable: true,
				}
				run.Transitions = nil
				makeMigratedDirect(&batch, &run)
			} else if compactWorkflow(*test.pin) {
				run.Causation.WorkflowDigest = strings.Repeat("a", 64)
			} else {
				digest, err := test.pin.Digest()
				if err != nil {
					t.Fatal(err)
				}
				run.Causation.WorkflowDigest = digest
			}
			model := testSingleAdmissionModel(batch, run, rate)
			if _, err := NewSnapshot(model); err != nil {
				t.Fatalf("legacy-compatible terminal Run: %v", err)
			}
		})
	}
}

func TestSnapshotPreservesCompleteLifecycleCompatibilityPayload(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	batch, run, rate := testAdmissionProjection(t, root, 1, StateSucceeded)
	created := run.CreatedAt
	startingAt := created.Add(time.Second)
	runningAt := created.Add(2 * time.Second)
	readyAt := created.Add(2500 * time.Millisecond)
	awaitingAt := created.Add(3 * time.Second)
	finishedAt := created.Add(5 * time.Second)
	nextReconcile := created.Add(6 * time.Second)
	verifiedHead := strings.Repeat("a", 40)
	mergeCommit := strings.Repeat("b", 40)
	deploymentCommit := strings.Repeat("c", 40)

	run.Causation.ParentAdmissionID = "admission-parent"
	run.Causation.ParentRunID = "run-parent"
	run.Attempts = 2
	run.State = StateSucceeded
	run.SessionName = "factory-run-1"
	run.RunDirectory = filepath.Join(root, "run-1")
	run.StartedAt = pointerTime(runningAt)
	run.SegmentStartedAt = pointerTime(startingAt)
	run.SegmentAttempt = 1
	run.UpdatedAt = finishedAt
	run.FinishedAt = pointerTime(finishedAt)
	run.Transitions = []LifecycleTransition{
		{ID: "run-1:pending", State: StatePending, At: created},
		{ID: "run-1:starting", State: StateStarting, At: startingAt},
		{ID: "run-1:running", State: StateRunning, Attempts: 2, At: runningAt},
		{ID: "run-1:awaiting", State: StateAwaitingHumanMerge, Attempts: 2, At: awaitingAt},
		{ID: "run-1:succeeded", State: StateSucceeded, Attempts: 2, At: finishedAt},
	}
	run.Ready = &ReadyCheckpoint{
		ContractVersion: readyContractVersion, RunID: run.ID, Task: run.Causation.Task,
		Repository: run.Repository.Repository, PullRequest: 18, BaseBranch: run.Repository.DefaultBranch,
		HeadBranch: "factory-task-1-eng-47", VerifiedHeadOID: verifiedHead,
		PullRequestUpdatedAt: runningAt, CreatedAt: readyAt, ValidatedAt: awaitingAt,
	}
	run.MergeCommitOID = mergeCommit
	run.GitHub = GitHubState{
		LastCursor: 41, LastAuthoritativeRefreshAt: pointerTime(created.Add(4 * time.Second)),
		NextReconcileAt: pointerTime(nextReconcile), ReconcileFailures: 2, RemediationRequested: true,
	}
	run.ResumeCount = 3
	run.TerminalIntent = string(StateSucceeded)
	run.Completion = &CompletionValidation{
		Accepted: true, Intent: string(StateSucceeded), State: StateSucceeded,
		Reason: "all mechanical post-merge conditions verified", ValidatedAt: finishedAt,
		PullRequestState: "MERGED", PullRequestHead: verifiedHead, MergeCommitOID: mergeCommit,
		DeploymentID: "deployment-1", DeploymentCommit: deploymentCommit,
	}
	model := testSingleAdmissionModel(batch, run, rate)
	model.JournalSequence = 42
	snapshot, err := NewSnapshot(model)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runs.jsonl")
	if _, err := Create(root, path, snapshot, 10); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, path, 10)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := replayed.Model().Runs[0]
	want := snapshot.Model().Runs[0]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("complete lifecycle payload changed:\n got %#v\nwant %#v", got, want)
	}
	got.Ready.HeadBranch = "mutated"
	got.Completion.Reason = "mutated"
	*got.GitHub.NextReconcileAt = got.GitHub.NextReconcileAt.Add(time.Hour)
	again, _ := reopened.Snapshot()
	if reflect.DeepEqual(got, again.Model().Runs[0]) {
		t.Fatal("complete lifecycle projection aliases a snapshot reader")
	}
}

func TestMigratedBaselinePreservesAcknowledgedLegacyShapes(t *testing.T) {
	t.Run("terminal current-shape fixture", func(t *testing.T) {
		root := privateModelTestRoot(t)
		batch, run, rate := testAdmissionProjection(t, root, 1, StateSucceeded)
		historical := &HistoricalRepository{
			Repository: run.Repository.Repository, Origin: run.Repository.Origin,
			ManagedPath: run.Repository.ManagedPath, ManagedRoot: run.Repository.ManagedRoot,
			DefaultBranch: run.Repository.DefaultBranch, CloudURL: run.Repository.CloudURL,
		}
		run.Repository = nil
		run.Causation.Workflow = pointerPin(workflow.Pinned{ID: "full-sdlc", Revision: 3})
		run.Causation.WorkflowDigest = ""
		run.Transitions = nil
		run.MigratedBaseline = &MigratedBaseline{
			State: run.State, ObservedAt: run.UpdatedAt, PriorTransitionsAcknowledged: true,
			WorkflowDigestUnavailable: true, HistoricalRepository: historical,
		}
		makeMigratedDirect(&batch, &run)
		snapshot, err := NewSnapshot(testSingleAdmissionModel(batch, run, rate))
		if err != nil {
			t.Fatal(err)
		}
		absentRepository := snapshot.Model()
		absentRepository.Runs[0].MigratedBaseline.HistoricalRepository = nil
		absentRepository.Runs[0].MigratedBaseline.RepositoryRouteUnavailable = true
		absentRepository.AdmissionOperations[0].Runs[0].MigratedBaseline.HistoricalRepository = nil
		absentRepository.AdmissionOperations[0].Runs[0].MigratedBaseline.RepositoryRouteUnavailable = true
		if _, err := NewSnapshot(absentRepository); err != nil {
			t.Fatalf("absent historical repository: %v", err)
		}
		path := filepath.Join(root, "generation", "runs.jsonl")
		store, err := Create(root, path, snapshot, 10)
		if err != nil {
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
		got, err := reopened.Snapshot()
		if err != nil || !reflect.DeepEqual(got.Model(), snapshot.Model()) {
			t.Fatalf("migrated fixture changed: %#v, %v", got.Model(), err)
		}
	})

	t.Run("active baseline accepts first canonical transition", func(t *testing.T) {
		root := privateModelTestRoot(t)
		batch, run, rate := testAdmissionProjection(t, root, 1, StatePending)
		run.State = StateRunning
		run.Attempts = 1
		run.SessionName = "factory-sanitized"
		run.RunDirectory = filepath.Join(root, "run-sanitized")
		run.StartedAt = pointerTime(run.UpdatedAt)
		run.SegmentStartedAt = pointerTime(run.UpdatedAt)
		run.Transitions = nil
		run.MigratedBaseline = &MigratedBaseline{State: StateRunning, ObservedAt: run.UpdatedAt, PriorTransitionsAcknowledged: true}
		makeMigratedDirect(&batch, &run)
		snapshot, err := NewSnapshot(testSingleAdmissionModel(batch, run, rate))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "runs.jsonl")
		store, err := Create(root, path, snapshot, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		failed := nextLifecycleRun(run, StateFailed, run.UpdatedAt.Add(time.Second))
		if err := store.Transition(failed); err != nil {
			t.Fatalf("first canonical transition: %v", err)
		}
		got, _ := store.Snapshot()
		if len(got.Model().Runs[0].Transitions) != 1 || got.Model().Runs[0].State != StateFailed {
			t.Fatalf("transitioned migrated Run = %#v", got.Model().Runs[0])
		}
	})

	for _, state := range []LifecycleState{StatePending, StateRunning} {
		t.Run("active direct source without pin or route "+string(state), func(t *testing.T) {
			root := privateModelTestRoot(t)
			batch, run, rate := testAdmissionProjection(t, root, 1, StatePending)
			if state == StateRunning {
				batch, run, rate = runningProjection(t, root)
			}
			run.Causation.Workflow = nil
			run.Causation.WorkflowDigest = ""
			run.Repository = nil
			run.Transitions = nil
			run.MigratedBaseline = &MigratedBaseline{
				State: run.State, ObservedAt: run.UpdatedAt, PriorTransitionsAcknowledged: true,
				WorkflowPinUnavailable: true, RepositoryRouteUnavailable: true,
			}
			makeMigratedDirect(&batch, &run)
			snapshot, err := NewSnapshot(testSingleAdmissionModel(batch, run, rate))
			if err != nil {
				t.Fatalf("sanitized legacy %s source shape: %v", state, err)
			}
			path := filepath.Join(root, "legacy-"+string(state), "runs.jsonl")
			store, err := Create(root, path, snapshot, 1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			failed := nextLifecycleRun(run, StateFailed, run.UpdatedAt.Add(time.Second))
			if err := store.Transition(failed); err != nil {
				t.Fatalf("first canonical transition from %s migrated baseline: %v", state, err)
			}
		})
	}

	for _, state := range []LifecycleState{StatePending, StateRunning} {
		t.Run("ordinary active Run rejects absent pin and route "+string(state), func(t *testing.T) {
			root := privateModelTestRoot(t)
			batch, run, rate := testAdmissionProjection(t, root, 1, StatePending)
			if state == StateRunning {
				batch, run, rate = runningProjection(t, root)
			}
			run.Causation.Workflow = nil
			run.Causation.WorkflowDigest = ""
			run.Repository = nil
			if _, err := NewSnapshot(testSingleAdmissionModel(batch, run, rate)); err == nil {
				t.Fatal("ordinary canonical active Run accepted absent pin and route")
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*Run)
	}{
		{name: "acknowledged transition history", mutate: func(run *Run) { run.Transitions = nil }},
		{name: "absent terminal route", mutate: func(run *Run) { run.Repository = nil }},
		{name: "digestless compact pin", mutate: func(run *Run) {
			run.Causation.Workflow = pointerPin(workflow.Pinned{ID: "full-sdlc", Revision: 3})
			run.Causation.WorkflowDigest = ""
		}},
		{name: "unavailable workflow pin", mutate: func(run *Run) {
			run.Causation.Workflow = nil
			run.Causation.WorkflowDigest = ""
		}},
	} {
		t.Run("ordinary Run rejects "+test.name, func(t *testing.T) {
			batch, run, rate := testAdmissionProjection(t, privateModelTestRoot(t), 1, StateSucceeded)
			test.mutate(&run)
			_, err := NewSnapshot(testSingleAdmissionModel(batch, run, rate))
			if err == nil {
				t.Fatal("ordinary canonical Run accepted migration-only evidence gap")
			}
		})
	}
}

func TestModelLinksPolicyOwnershipAndCompletionEvidence(t *testing.T) {
	t.Run("policy revision matches admission settings", func(t *testing.T) {
		batch, run, rate := testAdmissionProjection(t, privateModelTestRoot(t), 1, StatePending)
		run.Causation.PolicyRevision++
		if _, err := NewSnapshot(testSingleAdmissionModel(batch, run, rate)); err == nil || !strings.Contains(err.Error(), "linkage") {
			t.Fatalf("policy linkage error = %v", err)
		}
	})

	t.Run("same-task ownership belongs to oldest nonterminal Run", func(t *testing.T) {
		root := privateModelTestRoot(t)
		firstBatch, firstRun, firstRate := testAdmissionProjection(t, root, 1, StatePending)
		secondBatch, secondRun, secondRate := testAdmissionProjection(t, root, 2, StatePending)
		secondRun.Causation.Task = firstRun.Causation.Task
		admit := func(run *Run) {
			run.State = StateAdmitted
			run.Transitions[0].State = StateAdmitted
		}
		admit(&firstRun)
		admit(&secondRun)
		model := Model{
			Schema: SchemaVersion, TotalBatches: 2, TotalRuns: 2,
			AdmissionOperations: []AdmissionOperationReceipt{
				testAdmissionReceipt([]AdmissionBatch{firstBatch}, []Run{firstRun}, []RateBucket{firstRate}),
				testAdmissionReceipt([]AdmissionBatch{secondBatch}, []Run{secondRun}, []RateBucket{secondRate}),
			},
			AdmissionBatches: []AdmissionBatch{firstBatch, secondBatch}, Runs: []Run{firstRun, secondRun}, RateBuckets: []RateBucket{firstRate, secondRate},
		}
		if _, err := NewSnapshot(model); err != nil {
			t.Fatalf("multiple admitted Runs: %v", err)
		}
		oldestOwns := cloneModel(model)
		oldestOwns.Runs[0].State = StatePending
		oldestOwns.Runs[0].Transitions[0].State = StatePending
		if _, err := NewSnapshot(oldestOwns); err != nil {
			t.Fatalf("oldest owner: %v", err)
		}
		youngerOwns := cloneModel(model)
		youngerOwns.Runs[1].State = StatePending
		youngerOwns.Runs[1].Transitions[0].State = StatePending
		if _, err := NewSnapshot(youngerOwns); err == nil || !strings.Contains(err.Error(), "oldest") {
			t.Fatalf("younger owner error = %v", err)
		}
	})

	t.Run("completion is Run-aware", func(t *testing.T) {
		root := privateModelTestRoot(t)
		batch, run, rate := testAdmissionProjection(t, root, 1, StateSucceeded)
		head := strings.Repeat("a", 40)
		merge := strings.Repeat("b", 40)
		run.Ready = &ReadyCheckpoint{
			ContractVersion: readyContractVersion, RunID: run.ID, Task: run.Causation.Task,
			Repository: run.Repository.Repository, PullRequest: 18, BaseBranch: run.Repository.DefaultBranch,
			HeadBranch: "factory-task-1-sanitized", VerifiedHeadOID: head, CreatedAt: run.CreatedAt,
		}
		run.MergeCommitOID = merge
		run.Completion = &CompletionValidation{
			Accepted: true, Intent: string(StateSucceeded), State: StateSucceeded, Reason: "verified",
			ValidatedAt: *run.FinishedAt, PullRequestState: "MERGED", PullRequestHead: head, MergeCommitOID: merge,
		}
		base := testSingleAdmissionModel(batch, run, rate)
		if _, err := NewSnapshot(base); err != nil {
			t.Fatalf("repository-only completion: %v", err)
		}
		for _, mutation := range []struct {
			name string
			edit func(*Run)
		}{
			{name: "running", edit: func(run *Run) {
				run.State = StateRunning
				run.FinishedAt = nil
				run.Transitions[len(run.Transitions)-1].State = StateRunning
			}},
			{name: "terminal state", edit: func(run *Run) { run.Completion.State = StateBlocked; run.Completion.Intent = string(StateBlocked) }},
			{name: "verified head", edit: func(run *Run) { run.Completion.PullRequestHead = strings.Repeat("c", 40) }},
			{name: "merge", edit: func(run *Run) { run.Completion.MergeCommitOID = strings.Repeat("c", 40) }},
			{name: "head without ready", edit: func(run *Run) {
				run.Ready = nil
				run.MergeCommitOID = ""
				run.Completion.PullRequestState = ""
				run.Completion.MergeCommitOID = ""
			}},
			{name: "merge without Run merge", edit: func(run *Run) { run.MergeCommitOID = "" }},
			{name: "success without ready", edit: func(run *Run) {
				run.Ready = nil
				run.MergeCommitOID = ""
				run.Completion.PullRequestState = ""
				run.Completion.PullRequestHead = ""
				run.Completion.MergeCommitOID = ""
			}},
		} {
			t.Run(mutation.name, func(t *testing.T) {
				candidate := cloneModel(base)
				mutation.edit(&candidate.Runs[0])
				if _, err := NewSnapshot(candidate); err == nil {
					t.Fatal("conflicting accepted completion was accepted")
				}
			})
		}

		resumed := run.Clone()
		resumed.State = StatePending
		resumed.FinishedAt = nil
		resumed.Ready = nil
		resumed.MergeCommitOID = ""
		resumed.Transitions = resumed.Transitions[:1]
		resumed.Completion = &CompletionValidation{Accepted: false, Intent: string(StateSucceeded), State: StateFailed, Reason: "retry", ValidatedAt: resumed.UpdatedAt}
		resumedModel := cloneModel(base)
		resumedModel.Runs[0] = resumed
		if _, err := NewSnapshot(resumedModel); err != nil {
			t.Fatalf("rejected completion on resumed Run: %v", err)
		}

		blocked := run.Clone()
		blocked.State = StateBlocked
		blocked.Repository = nil
		blocked.Ready = nil
		blocked.MergeCommitOID = ""
		blocked.Transitions[len(blocked.Transitions)-1].State = StateBlocked
		blocked.Completion = &CompletionValidation{
			Accepted: true, Intent: string(StateBlocked), Blocker: "missing_routing_metadata",
			State: StateBlocked, Reason: "typed pre-checkpoint blocker accepted", ValidatedAt: blocked.UpdatedAt,
		}
		blockedModel := cloneModel(base)
		blockedModel.Runs[0] = blocked
		if _, err := NewSnapshot(blockedModel); err != nil {
			t.Fatalf("typed pre-PR blocker: %v", err)
		}

		failed := run.Clone()
		failed.State = StateFailed
		failed.Ready = nil
		failed.MergeCommitOID = ""
		failed.Transitions[len(failed.Transitions)-1].State = StateFailed
		failed.Completion = &CompletionValidation{
			Accepted: true, Intent: string(StateFailed), State: StateFailed,
			Reason: "process failure preserved", ValidatedAt: failed.UpdatedAt,
		}
		failedModel := cloneModel(base)
		failedModel.Runs[0] = failed
		if _, err := NewSnapshot(failedModel); err != nil {
			t.Fatalf("accepted pre-checkpoint process failure: %v", err)
		}

		for _, blocker := range []string{
			"closed_unmerged", "verified_head_mismatch", "safeguard_regression",
			"deployment_source_invalid", "deployment_failed", "cleanup_failed", "external_authentication",
		} {
			t.Run("accepted post-ready blocker "+blocker, func(t *testing.T) {
				candidate := run.Clone()
				candidate.State = StateBlocked
				candidate.Transitions[len(candidate.Transitions)-1].State = StateBlocked
				candidate.Completion = &CompletionValidation{
					Accepted: true, Intent: string(StateBlocked), Blocker: blocker, State: StateBlocked,
					Reason: "typed post-ready blocker verified", ValidatedAt: candidate.UpdatedAt,
					PullRequestState: "MERGED", PullRequestHead: head, MergeCommitOID: merge,
				}
				switch blocker {
				case "closed_unmerged":
					candidate.MergeCommitOID = ""
					candidate.Completion.PullRequestState = "CLOSED"
					candidate.Completion.MergeCommitOID = ""
				case "verified_head_mismatch":
					candidate.Completion.PullRequestHead = strings.Repeat("c", 40)
				case "external_authentication":
					candidate.Completion.PullRequestState = ""
					candidate.Completion.PullRequestHead = ""
					candidate.Completion.MergeCommitOID = ""
				}
				candidateModel := cloneModel(base)
				candidateModel.Runs[0] = candidate
				if _, err := NewSnapshot(candidateModel); err != nil {
					t.Fatalf("accepted blocker shape: %v", err)
				}
			})
		}
	})
}

func privateModelTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func testAdmissionProjection(t *testing.T, root string, number int, state LifecycleState) (AdmissionBatch, Run, RateBucket) {
	t.Helper()
	at := modelTestNow.Add(time.Duration(number-1) * time.Minute)
	ruleID := "rule-one"
	batchID := "batch-" + string(rune('0'+number))
	eventID := "factory:event-" + string(rune('0'+number))
	admissionID := "admission-" + string(rune('0'+number))
	runID := "run-" + string(rune('0'+number))
	pin := workflow.Pinned{
		ID: "full-sdlc", Revision: 3, Name: "Full SDLC", Enabled: true,
		Markdown: "# Full SDLC\n", UpdatedAt: pointerTime(at),
	}
	digest, err := pin.Digest()
	if err != nil {
		t.Fatal(err)
	}
	task := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-" + string(rune('0'+number)), Identifier: "FAC-" + string(rune('0'+number))}
	batch := AdmissionBatch{
		ID: batchID, Origin: AdmissionOriginEvent, EventID: eventID, EventSequence: uint64(number),
		EventSource: eventwire.SourceFactory, RegistryRevision: 2, SettingsRevision: 3, PolicyGeneration: 4, DecidedAt: at,
		Outcomes: []AdmissionOutcome{
			{Kind: AdmissionOutcomeSuppressed, RuleID: "rule-two", RuleRevision: 1, Reason: "hop-limit"},
			{Kind: AdmissionOutcomeRun, RuleID: ruleID, RuleRevision: 2, AdmissionID: admissionID, RunID: runID},
		},
	}
	route := repositories.Route{
		ProjectID: "project-factory", Repository: "tomnagengast/factory", Origin: "git@github.com:tomnagengast/factory.git",
		ManagedPath: root + "/factory", ManagedRoot: root, DefaultBranch: "main", Bootstrap: false, CloudURL: "https://factory.nags.cloud",
	}
	run := Run{
		ID: runID,
		Causation: Causation{
			AdmissionID: admissionID, BatchID: batchID, EventID: eventID, EventSequence: uint64(number), EventSource: eventwire.SourceFactory,
			RuleID: ruleID, RuleRevision: 2, Workflow: &pin, WorkflowDigest: digest, PolicyRevision: 3, PolicyGeneration: 4,
			Task: task, RootEventID: eventID, Hop: 1, AncestorRuleIDs: []string{ruleID}, AdmittedAt: at,
		},
		Repository: &route, TriggerKind: "configured-rule", DeliveryIDs: []string{"delivery-b", "delivery-a"}, DuplicateDeliveries: 1,
		State: state, Attempts: 0, CreatedAt: at, UpdatedAt: at,
		Transitions: []LifecycleTransition{{ID: runID + ":pending", State: StatePending, Attempts: 0, At: at}},
	}
	if state.Terminal() {
		finished := at.Add(time.Second)
		run.UpdatedAt = finished
		run.FinishedAt = &finished
		run.State = state
		run.Transitions = append(run.Transitions, LifecycleTransition{ID: runID + ":" + string(state), State: state, Attempts: 0, At: finished})
	}
	return batch, run, RateBucket{RuleID: ruleID, Minute: at.Truncate(time.Minute), Count: 1}
}

func pointerTime(value time.Time) *time.Time { return &value }

func pointerPin(value workflow.Pinned) *workflow.Pinned { return &value }

func testSingleAdmissionModel(batch AdmissionBatch, run Run, rate RateBucket) Model {
	return Model{
		Schema: SchemaVersion, TotalBatches: 1, TotalRuns: 1,
		AdmissionOperations: []AdmissionOperationReceipt{
			testAdmissionReceipt([]AdmissionBatch{batch}, []Run{run}, []RateBucket{rate}),
		},
		AdmissionBatches: []AdmissionBatch{batch}, Runs: []Run{run}, RateBuckets: []RateBucket{rate},
	}
}

func testAdmissionReceipt(batches []AdmissionBatch, runs []Run, rates []RateBucket) AdmissionOperationReceipt {
	return AdmissionOperationReceipt{AdmissionBatches: batches, Runs: runs, RateIncrements: rates}
}

func makeMigratedDirect(batch *AdmissionBatch, run *Run) {
	batch.Origin = AdmissionOriginMigratedDirect
	batch.EventSequence = 0
	run.Causation.EventSequence = 0
}
