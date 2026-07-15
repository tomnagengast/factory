package agentrun

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
	workflowpkg "github.com/tomnagengast/factory/internal/workflow"
)

func TestEnsureInvocationRunIsIdempotentAndRejectsIssueOwner(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store, err := Open(filepath.Join(directory, "runs.json"), 100)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	claim := testInvocationClaim(directory, "run-0123456789abcdef", "invocation-one", "factory:one")
	run, created, err := store.EnsureInvocationRun(claim, time.Now())
	if err != nil || !created {
		t.Fatalf("ensure: run=%#v created=%t err=%v", run, created, err)
	}
	if same, created, err := store.EnsureInvocationRun(claim, time.Now()); err != nil || created || same.ID != run.ID {
		t.Fatalf("repeat: run=%#v created=%t err=%v", same, created, err)
	}
	second := testInvocationClaim(directory, "run-fedcba9876543210", "invocation-two", "factory:two")
	if _, _, err := store.EnsureInvocationRun(second, time.Now()); err != ErrInvocationIssueOwned {
		t.Fatalf("issue owner error = %v", err)
	}
}

func TestPinnedWorkflowStrictReadAndRunCausation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	runDirectory := filepath.Join(directory, "runs", "run-0123456789abcdef")
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	pinned := workflowpkg.Pin(settings.Defaults(3).Workflows[0])
	digest, err := pinned.Digest()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runDirectory, WorkflowSnapshotFileName)
	if err := writeJSONFile(path, workflowpkg.EncodePinnedSnapshot(pinned, digest)); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if got, gotDigest, err := ReadWorkflowSnapshot(runDirectory, path); err != nil || !workflowEqual(got, pinned) || gotDigest != digest {
		t.Fatalf("read workflow=%#v err=%v", got, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadWorkflowSnapshot(runDirectory, path); err == nil {
		t.Fatal("insecure workflow permissions were accepted")
	}

	run := Run{
		ID: "run-0123456789abcdef", IssueIdentifier: "ENG-40", InvocationID: "invocation-one",
		InvocationRootEventID: "linear:root", InvocationHop: 2, InvocationAncestorRuleIDs: []string{"first", "second"},
	}
	event := agentRecordEvent(run, "attempt-1-events.jsonl", 0, 10, []byte(`{"type":"assistant"}`))
	if event.RootEventID != "linear:root" || event.ParentInvocationID != run.InvocationID || event.ParentRunID != run.ID || event.Hop != 2 {
		t.Fatalf("causation = %#v", event)
	}
	if !event.Has(eventwire.AttributeProducer, "agent-collector") || !event.Has(eventwire.AttributeProvenance, "factory") {
		t.Fatalf("normalized metadata = %#v", event.Attributes)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("validate caused event: %v", err)
	}
}

func testInvocationClaim(directory, runID, invocationID, eventID string) InvocationClaim {
	root := filepath.Join(directory, "repos")
	pinned := workflowpkg.Pin(settings.Defaults(3).Workflows[0])
	digest, err := pinned.Digest()
	if err != nil {
		panic(err)
	}
	return InvocationClaim{
		RunID: runID, InvocationID: invocationID, EventID: eventID, IssueIdentifier: "ENG-40",
		RootEventID: "linear:root", Hop: 1, AncestorRuleIDs: []string{"linear-label"},
		Workflow: pinned, WorkflowDigest: digest, PolicyRevision: 5,
		Repository: RepositoryConfig{
			App: "factory", Repository: "tomnagengast/factory", RepoURL: "https://github.com/tomnagengast/factory",
			RepoPath: filepath.Join(root, "factory"), ManagedRoot: root, BaseBranch: "main",
		},
	}
}
