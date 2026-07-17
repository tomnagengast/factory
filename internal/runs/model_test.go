package runs

import (
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
	firstBatch, firstRun, firstRate := testAdmissionProjection(t, root, 1, StatePending)
	secondBatch, secondRun, secondRate := testAdmissionProjection(t, root, 2, StatePending)
	model.JournalSequence = 7
	model.TotalBatches = 2
	model.TotalRuns = 2
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
	model.Runs[0].DeliveryIDs[0] = "mutated-input"
	model.Runs[0].Causation.AncestorRuleIDs[0] = "mutated-input"
	model.Runs[0].Causation.Workflow.Steps = append(model.Runs[0].Causation.Workflow.Steps, "mutated")
	model.Runs[0].Repository.Repository = "mutated/repository"
	model.Runs[0].StartedAt = pointerTime(modelTestNow.Add(24 * time.Hour))
	read := snapshot.Model()
	if read.AdmissionBatches[1].Outcomes[0].Reason == "mutated input" || strings.Contains(strings.Join(read.Runs[1].DeliveryIDs, ","), "mutated") ||
		read.Runs[1].Repository.Repository == "mutated/repository" || read.Runs[1].StartedAt != nil {
		t.Fatalf("snapshot aliases caller input: %#v", read)
	}

	read.AdmissionBatches[0].Outcomes[0].AdmissionID = "mutated-output"
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
	base := Model{
		Schema: SchemaVersion, TotalBatches: 1, TotalRuns: 1,
		AdmissionBatches: []AdmissionBatch{batch}, Runs: []Run{run}, RateBuckets: []RateBucket{rate},
	}
	tests := []struct {
		name   string
		mutate func(*Model)
		want   string
	}{
		{name: "missing schema", mutate: func(value *Model) { value.Schema = 0 }, want: "schema"},
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
			} else if compactWorkflow(*test.pin) {
				run.Causation.WorkflowDigest = strings.Repeat("a", 64)
			} else {
				digest, err := test.pin.Digest()
				if err != nil {
					t.Fatal(err)
				}
				run.Causation.WorkflowDigest = digest
			}
			model := Model{Schema: SchemaVersion, TotalBatches: 1, TotalRuns: 1, AdmissionBatches: []AdmissionBatch{batch}, Runs: []Run{run}, RateBuckets: []RateBucket{rate}}
			if _, err := NewSnapshot(model); err != nil {
				t.Fatalf("legacy-compatible terminal Run: %v", err)
			}
		})
	}
}

func TestSnapshotPreservesCompleteLifecycleCompatibilityPayload(t *testing.T) {
	root := t.TempDir()
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
	model := Model{
		Schema: SchemaVersion, JournalSequence: 42, TotalBatches: 1, TotalRuns: 1,
		AdmissionBatches: []AdmissionBatch{batch}, Runs: []Run{run}, RateBuckets: []RateBucket{rate},
	}
	snapshot, err := NewSnapshot(model)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runs.jsonl")
	if _, err := Create(path, snapshot, 10); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, 10)
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
	task := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-1", Identifier: "FAC-1"}
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
