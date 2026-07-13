package agentrun

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type routingEvidence struct {
	repository string
}

func (r routingEvidence) ReadCompletionEvidence(_ context.Context, run Run, _ PullRequestSnapshot) (CompletionEvidence, error) {
	return CompletionEvidence{Deployment: DeploymentReceipt{SourceRepository: r.repository + ":" + run.Repository}}, nil
}

func TestRunPersistsResolvedRepositoryIdentity(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "runs.json"), 10)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	run, created, err := store.Claim(Trigger{
		DeliveryID:      "delivery-1",
		IssueIdentifier: "ENG-31",
		Kind:            TriggerKindLabel,
		Repository:      "tomnagengast/notebook",
		RepositoryURL:   "git@github.com:tomnagengast/notebook.git",
		RepositoryPath:  "/Users/tom/repos/tomnagengast/notebook",
		BaseBranch:      "main",
	}, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !created {
		t.Fatal("Claim did not create a run")
	}
	if run.Repository != "tomnagengast/notebook" || run.RepositoryPath != "/Users/tom/repos/tomnagengast/notebook" {
		t.Fatalf("run routing = %#v", run)
	}
}

func TestTmuxLauncherRoutesResolvedRunsWithoutMutatingDefault(t *testing.T) {
	t.Parallel()
	launcher := &TmuxLauncher{config: LauncherConfig{
		RepoURL:  "git@github.com:tomnagengast/network.git",
		RepoPath: "/workspace/network",
	}}
	routed := launcher.forRun(Run{
		RepositoryURL:  "git@github.com:tomnagengast/notebook.git",
		RepositoryPath: "/workspace/notebook",
	})
	if routed == launcher {
		t.Fatal("forRun returned the default launcher")
	}
	if routed.config.RepoPath != "/workspace/notebook" || routed.config.RepoURL != "git@github.com:tomnagengast/notebook.git" {
		t.Fatalf("routed config = %#v", routed.config)
	}
	if launcher.config.RepoPath != "/workspace/network" {
		t.Fatalf("default launcher mutated: %#v", launcher.config)
	}
}

func TestRepositoryCompletionEvidenceSelectsRunRepository(t *testing.T) {
	t.Parallel()
	router, err := NewRepositoryCompletionEvidence(map[string]CompletionEvidenceReader{
		"tomnagengast/network":  routingEvidence{repository: "network"},
		"tomnagengast/notebook": routingEvidence{repository: "notebook"},
	})
	if err != nil {
		t.Fatalf("NewRepositoryCompletionEvidence: %v", err)
	}
	evidence, err := router.ReadCompletionEvidence(
		context.Background(),
		Run{Repository: "tomnagengast/notebook"},
		PullRequestSnapshot{},
	)
	if err != nil {
		t.Fatalf("ReadCompletionEvidence: %v", err)
	}
	if evidence.Deployment.SourceRepository != "notebook:tomnagengast/notebook" {
		t.Fatalf("selected evidence = %#v", evidence)
	}
}
