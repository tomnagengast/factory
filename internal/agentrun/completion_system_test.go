package agentrun

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemCompletionEvidenceVerifiesDeploymentAfterMainAdvances(t *testing.T) {
	t.Parallel()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "tomnagengast", "network.git")
	repository := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Dir(remote), 0o700); err != nil {
		t.Fatalf("create remote parent: %v", err)
	}
	runGit(t, gitPath, "", "init", "--bare", "--initial-branch=main", remote)
	runGit(t, gitPath, "", "init", "--initial-branch=main", repository)
	runGit(t, gitPath, repository, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(repository, "version.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	runGit(t, gitPath, repository, "add", "version.txt")
	runGit(t, gitPath, repository, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "initial")
	runGit(t, gitPath, repository, "push", "-u", "origin", "main")
	commit := gitOutput(t, gitPath, repository, "rev-parse", "HEAD")
	tree := gitOutput(t, gitPath, repository, "rev-parse", "HEAD^{tree}")
	runGit(t, gitPath, repository, "switch", "-c", "verified-head")
	if err := os.WriteFile(filepath.Join(repository, "feature.txt"), []byte("verified\n"), 0o600); err != nil {
		t.Fatalf("write verified feature: %v", err)
	}
	runGit(t, gitPath, repository, "add", "feature.txt")
	runGit(t, gitPath, repository, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "verified feature")
	verifiedHead := gitOutput(t, gitPath, repository, "rev-parse", "HEAD")
	runGit(t, gitPath, repository, "switch", "main")
	if err := os.WriteFile(filepath.Join(repository, "version.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatalf("advance source: %v", err)
	}
	runGit(t, gitPath, repository, "add", "version.txt")
	runGit(t, gitPath, repository, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "advance main")
	runGit(t, gitPath, repository, "push", "origin", "main")

	worktrunk := filepath.Join(root, "wt")
	if err := os.WriteFile(worktrunk, []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o700); err != nil {
		t.Fatalf("write Worktrunk fixture: %v", err)
	}
	now := time.Date(2026, time.July, 11, 22, 0, 0, 0, time.UTC)
	receipt := DeploymentReceipt{
		ContractVersion:  LifecycleContractVersion,
		DeploymentID:     "deploy-1",
		BuildID:          "build-1",
		Status:           "success",
		App:              "factory",
		SourceRepository: "tomnagengast/network",
		SourceBranch:     "main",
		SourceCommit:     commit,
		SourceTree:       tree,
		BinarySHA256:     strings.Repeat("a", 64),
		StartedAt:        now.Add(time.Minute),
		FinishedAt:       now.Add(2 * time.Minute),
	}
	receiptPath := filepath.Join(root, "deployments", "current.json")
	writeTestJSON(t, receiptPath, receipt)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/healthz":
			_ = json.NewEncoder(w).Encode(HealthIdentity{
				Status: "ok", App: "factory", Commit: commit, Tree: tree,
				BuildID: "build-1", DeploymentID: "deploy-1", ContractVersion: "1", StartedAt: now.Add(90 * time.Second),
			})
		case "/graphql":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": map[string]any{"state": map[string]string{"type": "completed"}}}})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	reader, err := NewSystemCompletionEvidence(SystemCompletionConfig{
		Repository:     "tomnagengast/network",
		RemoteURLs:     []string{remote},
		RepoPath:       repository,
		ReceiptPath:    receiptPath,
		PendingReceipt: filepath.Join(root, "deployments", "pending.json"),
		HealthURL:      server.URL + "/api/healthz",
		LinearURL:      server.URL + "/graphql",
		GitPath:        gitPath,
		WorktrunkPath:  worktrunk,
		LinearAPIKey:   "linear-test",
		HTTPClient:     server.Client(),
	})
	if err != nil {
		t.Fatalf("new evidence reader: %v", err)
	}
	checkpoint := testReadyCheckpoint("run-1", now)
	checkpoint.VerifiedHeadOID = commit
	checkpoint.ValidatedAt = now
	evidence, err := reader.ReadCompletionEvidence(t.Context(), Run{ID: "run-1", IssueIdentifier: "ENG-123", RunDirectory: filepath.Join(root, "run-1"), Ready: &checkpoint}, PullRequestSnapshot{
		State: "MERGED", BaseBranch: "main", HeadBranch: checkpoint.HeadBranch, HeadOID: checkpoint.VerifiedHeadOID, MergeCommitOID: commit,
	})
	if err != nil {
		t.Fatalf("read completion evidence: %v", err)
	}
	if !evidence.SourceValid || !evidence.MergeContained || !evidence.VerifiedHeadContained || !evidence.HealthMatches || !evidence.RemoteBranchAbsent || !evidence.WorktreeAbsent || !evidence.LinearComplete {
		t.Fatalf("evidence = %#v", evidence)
	}
	if !evidence.DeploymentRequired {
		t.Fatal("deployable evidence did not require deployment")
	}

	repositoryOnly, err := NewRepositoryOnlyCompletionEvidence(SystemCompletionConfig{
		Repository: "tomnagengast/network", RemoteURLs: []string{remote}, RepoPath: repository,
		BaseBranch: "main", LinearURL: server.URL + "/graphql", GitPath: gitPath,
		WorktrunkPath: worktrunk, LinearAPIKey: "linear-test", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("new repository-only reader: %v", err)
	}
	repositoryEvidence, err := repositoryOnly.ReadCompletionEvidence(t.Context(), Run{ID: "run-1", IssueIdentifier: "ENG-123", RunDirectory: filepath.Join(root, "run-1"), Ready: &checkpoint}, PullRequestSnapshot{
		State: "MERGED", BaseBranch: "main", HeadBranch: checkpoint.HeadBranch, HeadOID: checkpoint.VerifiedHeadOID, MergeCommitOID: commit,
	})
	if err != nil {
		t.Fatalf("read repository-only evidence: %v", err)
	}
	if repositoryEvidence.DeploymentRequired || !repositoryEvidence.SourceValid || !repositoryEvidence.MergeContained || !repositoryEvidence.VerifiedHeadContained || !repositoryEvidence.LinearComplete {
		t.Fatalf("repository-only evidence = %#v", repositoryEvidence)
	}

	rebasedCheckpoint := checkpoint
	rebasedCheckpoint.VerifiedHeadOID = verifiedHead
	rebasedEvidence, err := repositoryOnly.ReadCompletionEvidence(t.Context(), Run{ID: "run-1", IssueIdentifier: "ENG-123", RunDirectory: filepath.Join(root, "run-1"), Ready: &rebasedCheckpoint}, PullRequestSnapshot{
		State: "MERGED", BaseBranch: "main", HeadBranch: rebasedCheckpoint.HeadBranch, HeadOID: rebasedCheckpoint.VerifiedHeadOID, MergeCommitOID: commit,
	})
	if err != nil {
		t.Fatalf("read rebased repository-only evidence: %v", err)
	}
	if rebasedEvidence.VerifiedHeadContained {
		t.Fatalf("rebased repository-only evidence = %#v", rebasedEvidence)
	}

	advancer := filepath.Join(root, "advancer")
	runGit(t, gitPath, "", "clone", remote, advancer)
	if err := os.WriteFile(filepath.Join(advancer, "version.txt"), []byte("three\n"), 0o600); err != nil {
		t.Fatalf("advance remote source: %v", err)
	}
	runGit(t, gitPath, advancer, "add", "version.txt")
	runGit(t, gitPath, advancer, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "remote only")
	runGit(t, gitPath, advancer, "push", "origin", "main")
	remoteOnlyMerge := gitOutput(t, gitPath, advancer, "rev-parse", "HEAD")
	behindEvidence, err := repositoryOnly.ReadCompletionEvidence(t.Context(), Run{ID: "run-1", IssueIdentifier: "ENG-123", RunDirectory: filepath.Join(root, "run-1"), Ready: &checkpoint}, PullRequestSnapshot{
		State: "MERGED", BaseBranch: "main", HeadBranch: checkpoint.HeadBranch, HeadOID: checkpoint.VerifiedHeadOID, MergeCommitOID: remoteOnlyMerge,
	})
	if err != nil {
		t.Fatalf("read behind repository-only evidence: %v", err)
	}
	if !behindEvidence.SourceValid || behindEvidence.MergeContained {
		t.Fatalf("behind repository-only evidence = %#v", behindEvidence)
	}
}

func TestCompletedChildResultsRequiresEveryChildToFinish(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	if complete, err := completedChildResults(runDirectory); err != nil || !complete {
		t.Fatalf("no children: complete=%t err=%v", complete, err)
	}
	child := filepath.Join(runDirectory, "children", "review")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if complete, err := completedChildResults(runDirectory); err != nil || complete {
		t.Fatalf("missing result: complete=%t err=%v", complete, err)
	}
	if err := os.WriteFile(filepath.Join(child, resultFileName), []byte("{\n"), 0o600); err != nil {
		t.Fatalf("write malformed result: %v", err)
	}
	if complete, err := completedChildResults(runDirectory); err == nil || complete {
		t.Fatalf("malformed result: complete=%t err=%v", complete, err)
	}
	writeTestJSON(t, filepath.Join(child, resultFileName), ProcessResult{
		Status: string(StateSucceeded),
	})
	if complete, err := completedChildResults(runDirectory); err != nil || complete {
		t.Fatalf("unfinished result: complete=%t err=%v", complete, err)
	}
	writeTestJSON(t, filepath.Join(child, resultFileName), ProcessResult{
		Status: string(StateFailed), ExitCode: 1, FinishedAt: time.Now().UTC(),
	})
	if complete, err := completedChildResults(runDirectory); err != nil || !complete {
		t.Fatalf("finished failed result: complete=%t err=%v", complete, err)
	}
	writeTestJSON(t, filepath.Join(child, resultFileName), ProcessResult{
		Status: string(StateSucceeded), FinishedAt: time.Now().UTC(),
	})
	if complete, err := completedChildResults(runDirectory); err != nil || !complete {
		t.Fatalf("successful result: complete=%t err=%v", complete, err)
	}
}

func TestLinearCompleteClassifiesGraphQLAuthenticationFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": "Authentication required"}}})
	}))
	defer server.Close()
	reader := &SystemCompletionEvidence{config: SystemCompletionConfig{
		LinearURL:    server.URL,
		LinearAPIKey: "invalid",
		HTTPClient:   server.Client(),
	}}
	if _, err := reader.linearComplete(t.Context(), "ENG-123"); !isExternalAuthenticationError(err) {
		t.Fatalf("linearComplete error = %v, want external authentication error", err)
	}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create JSON parent: %v", err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write JSON: %v", err)
	}
}
