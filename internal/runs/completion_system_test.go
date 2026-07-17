package runs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/taskmodel"
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
		t.Fatal(err)
	}
	runCompletionGit(t, gitPath, "", "init", "--bare", "--initial-branch=main", remote)
	runCompletionGit(t, gitPath, "", "init", "--initial-branch=main", repository)
	runCompletionGit(t, gitPath, repository, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(repository, "version.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCompletionGit(t, gitPath, repository, "add", "version.txt")
	runCompletionGit(t, gitPath, repository, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "initial")
	runCompletionGit(t, gitPath, repository, "push", "-u", "origin", "main")
	commit := completionGitOutput(t, gitPath, repository, "rev-parse", "HEAD")
	tree := completionGitOutput(t, gitPath, repository, "rev-parse", "HEAD^{tree}")
	runCompletionGit(t, gitPath, repository, "switch", "-c", "verified-head")
	if err := os.WriteFile(filepath.Join(repository, "feature.txt"), []byte("verified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCompletionGit(t, gitPath, repository, "add", "feature.txt")
	runCompletionGit(t, gitPath, repository, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "verified feature")
	verifiedHead := completionGitOutput(t, gitPath, repository, "rev-parse", "HEAD")
	runCompletionGit(t, gitPath, repository, "switch", "main")
	if err := os.WriteFile(filepath.Join(repository, "version.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCompletionGit(t, gitPath, repository, "add", "version.txt")
	runCompletionGit(t, gitPath, repository, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "advance main")
	runCompletionGit(t, gitPath, repository, "push", "origin", "main")
	canonicalRemote := "git@github.com:tomnagengast/network.git"
	runCompletionGit(t, gitPath, repository, "remote", "set-url", "origin", canonicalRemote)
	runCompletionGit(t, gitPath, repository, "config", "url."+remote+".insteadOf", canonicalRemote)

	worktrunk := filepath.Join(root, "wt")
	if err := os.WriteFile(worktrunk, []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 16, 22, 0, 0, 0, time.UTC)
	receipt := DeploymentReceipt{
		ContractVersion: LifecycleContractVersion, DeploymentID: "deploy-1", BuildID: "build-1", Status: "success", App: "factory",
		SourceRepository: "tomnagengast/network", SourceBranch: "main", SourceCommit: commit, SourceTree: tree,
		BinarySHA256: strings.Repeat("a", 64), StartedAt: now.Add(time.Minute), FinishedAt: now.Add(2 * time.Minute),
	}
	receiptPath := filepath.Join(root, "deployments", "current.json")
	writeCompletionJSON(t, receiptPath, receipt)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/healthz":
			_ = json.NewEncoder(writer).Encode(HealthIdentity{
				Status: "ok", App: "factory", Commit: commit, Tree: tree, BuildID: "build-1", DeploymentID: "deploy-1",
				ContractVersion: "1", StartedAt: now.Add(90 * time.Second),
			})
		case "/graphql":
			_ = json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{"issue": map[string]any{"state": map[string]string{"type": "completed"}}}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	options := SystemCompletionOptions{
		LinearURL: server.URL + "/graphql", GitPath: gitPath, WorktrunkPath: worktrunk,
		LinearAPIKey: "linear-test", HTTPClient: server.Client(),
	}
	identity := repositories.CompletionIdentity{
		App: "factory", Repository: "tomnagengast/network", RemoteURLs: completionRemoteURLs("tomnagengast/network"), Path: repository, DefaultBranch: "main",
		Deployment: repositories.Deployment{
			ReceiptPath: receiptPath, PendingReceiptPath: filepath.Join(root, "deployments", "pending.json"), HealthURL: server.URL + "/api/healthz",
		},
	}
	reader, err := NewSystemCompletionEvidence(identity, options)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := systemCompletionCheckpoint(now, identity.Repository, commit)
	run := systemCompletionRun(root, identity.Repository, checkpoint)
	snapshot := PullRequestSnapshot{
		Number: 18, State: "MERGED", BaseBranch: "main", HeadBranch: checkpoint.HeadBranch,
		HeadOID: checkpoint.VerifiedHeadOID, MergeCommitOID: commit,
	}
	evidence, err := reader.ReadCompletionEvidence(t.Context(), run, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.DeploymentRequired || !evidence.SourceValid || !evidence.MergeContained || !evidence.VerifiedHeadContained ||
		!evidence.HealthMatches || !evidence.RemoteBranchAbsent || !evidence.WorktreeAbsent || !evidence.TaskComplete || !evidence.ChildrenComplete {
		t.Fatalf("evidence = %#v", evidence)
	}

	repositoryIdentity := identity
	repositoryIdentity.Deployment = repositories.Deployment{}
	repositoryOnly, err := NewSystemCompletionEvidence(repositoryIdentity, options)
	if err != nil {
		t.Fatal(err)
	}
	repositoryEvidence, err := repositoryOnly.ReadCompletionEvidence(t.Context(), run, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if repositoryEvidence.DeploymentRequired || !repositoryEvidence.SourceValid || !repositoryEvidence.MergeContained ||
		!repositoryEvidence.VerifiedHeadContained || !repositoryEvidence.TaskComplete {
		t.Fatalf("repository evidence = %#v", repositoryEvidence)
	}

	rebasedCheckpoint := checkpoint
	rebasedCheckpoint.VerifiedHeadOID = verifiedHead
	rebasedRun := systemCompletionRun(root, identity.Repository, rebasedCheckpoint)
	rebasedSnapshot := snapshot
	rebasedSnapshot.HeadOID = verifiedHead
	rebasedEvidence, err := repositoryOnly.ReadCompletionEvidence(t.Context(), rebasedRun, rebasedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if rebasedEvidence.VerifiedHeadContained {
		t.Fatalf("rebased evidence = %#v", rebasedEvidence)
	}

	advancer := filepath.Join(root, "advancer")
	runCompletionGit(t, gitPath, "", "clone", remote, advancer)
	if err := os.WriteFile(filepath.Join(advancer, "version.txt"), []byte("three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCompletionGit(t, gitPath, advancer, "add", "version.txt")
	runCompletionGit(t, gitPath, advancer, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "remote only")
	runCompletionGit(t, gitPath, advancer, "push", "origin", "main")
	behindSnapshot := snapshot
	behindSnapshot.MergeCommitOID = completionGitOutput(t, gitPath, advancer, "rev-parse", "HEAD")
	behindEvidence, err := repositoryOnly.ReadCompletionEvidence(t.Context(), run, behindSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !behindEvidence.SourceValid || behindEvidence.MergeContained {
		t.Fatalf("behind evidence = %#v", behindEvidence)
	}
}

func TestNewSystemCompletionEvidenceRequiresCanonicalCatalogIdentity(t *testing.T) {
	t.Parallel()
	valid := repositories.CompletionIdentity{
		App: "factory", Repository: "tomnagengast/factory", RemoteURLs: completionRemoteURLs("tomnagengast/factory"),
		Path: t.TempDir(), DefaultBranch: "main",
	}
	options := SystemCompletionOptions{LinearURL: "https://api.linear.app/graphql", GitPath: "git", WorktrunkPath: "wt", LinearAPIKey: "key", HTTPClient: http.DefaultClient}
	if _, err := NewSystemCompletionEvidence(valid, options); err != nil {
		t.Fatalf("valid identity: %v", err)
	}
	for name, mutate := range map[string]func(*repositories.CompletionIdentity){
		"repository": func(value *repositories.CompletionIdentity) { value.Repository = "" },
		"remotes":    func(value *repositories.CompletionIdentity) { value.RemoteURLs = nil },
		"path":       func(value *repositories.CompletionIdentity) { value.Path = "relative" },
		"branch":     func(value *repositories.CompletionIdentity) { value.DefaultBranch = "" },
		"deployment": func(value *repositories.CompletionIdentity) {
			value.Deployment.ReceiptPath = filepath.Join(t.TempDir(), "receipt.json")
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid.Clone()
			mutate(&candidate)
			if _, err := NewSystemCompletionEvidence(candidate, options); err == nil {
				t.Fatalf("invalid identity accepted: %#v", candidate)
			}
		})
	}
	if _, err := NewSystemCompletionEvidence(valid, SystemCompletionOptions{}); err == nil {
		t.Fatal("missing process authorities accepted")
	}
}

func TestSystemCompletionEvidenceRejectsRunRouteMismatchBeforeExternalReads(t *testing.T) {
	t.Parallel()
	identity := repositories.CompletionIdentity{
		App: "factory", Repository: "tomnagengast/factory", RemoteURLs: completionRemoteURLs("tomnagengast/factory"),
		Path: t.TempDir(), DefaultBranch: "main",
	}
	reader, err := NewSystemCompletionEvidence(identity, SystemCompletionOptions{
		LinearURL: "https://api.linear.app/graphql", GitPath: "missing-git", WorktrunkPath: "missing-wt", LinearAPIKey: "key", HTTPClient: http.DefaultClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := systemCompletionRun(t.TempDir(), "tomnagengast/network", systemCompletionCheckpoint(completionNow(), "tomnagengast/network", strings.Repeat("a", 40)))
	if _, err := reader.ReadCompletionEvidence(t.Context(), run, PullRequestSnapshot{}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("route mismatch error = %v", err)
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
		t.Fatal(err)
	}
	if complete, err := completedChildResults(runDirectory); err != nil || complete {
		t.Fatalf("missing result: complete=%t err=%v", complete, err)
	}
	if err := os.WriteFile(filepath.Join(child, resultFileName), []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if complete, err := completedChildResults(runDirectory); err == nil || complete {
		t.Fatalf("malformed result: complete=%t err=%v", complete, err)
	}
	writeCompletionJSON(t, filepath.Join(child, resultFileName), ProcessResult{Status: string(StateSucceeded)})
	if complete, err := completedChildResults(runDirectory); err != nil || complete {
		t.Fatalf("unfinished result: complete=%t err=%v", complete, err)
	}
	writeCompletionJSON(t, filepath.Join(child, resultFileName), ProcessResult{Status: string(StateFailed), ExitCode: 1, FinishedAt: time.Now().UTC()})
	if complete, err := completedChildResults(runDirectory); err != nil || !complete {
		t.Fatalf("finished result: complete=%t err=%v", complete, err)
	}
}

func TestTaskCompleteClassifiesGraphQLAuthenticationFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"errors": []map[string]string{{"message": "Authentication required"}}})
	}))
	defer server.Close()
	reader := &SystemCompletionEvidence{options: SystemCompletionOptions{LinearURL: server.URL, LinearAPIKey: "invalid", HTTPClient: server.Client()}}
	if _, err := reader.linearComplete(t.Context(), "ENG-123"); !isExternalAuthenticationError(err) {
		t.Fatalf("linearComplete error = %v", err)
	}
}

type recordingTaskCompleter struct {
	task       taskmodel.TaskRef
	runID      string
	repository string
	evidence   string
}

func (c *recordingTaskCompleter) Complete(_ context.Context, task taskmodel.TaskRef, runID, repository, evidence string) (bool, error) {
	c.task, c.runID, c.repository, c.evidence = task, runID, repository, evidence
	return true, nil
}

func TestTaskCompleteUsesCanonicalRunTaskAndRepository(t *testing.T) {
	t.Parallel()
	completer := &recordingTaskCompleter{}
	reader := &SystemCompletionEvidence{options: SystemCompletionOptions{TaskCompletion: completer}}
	route := managerRoute(t.TempDir())
	run := Run{ID: "run-native", Causation: Causation{Task: taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"}}, Repository: &route}
	evidence := CompletionEvidence{SourceValid: true, MergeContained: true, VerifiedHeadContained: true, RemoteBranchAbsent: true, WorktreeAbsent: true, ChildrenComplete: true}
	snapshot := PullRequestSnapshot{Number: 18, MergeCommitOID: strings.Repeat("b", 40)}
	complete, err := reader.taskComplete(t.Context(), run, snapshot, evidence)
	if err != nil || !complete || completer.task != run.Causation.Task || completer.runID != run.ID || completer.repository != route.Repository || !strings.Contains(completer.evidence, "github:"+route.Repository) {
		t.Fatalf("complete=%t completer=%#v err=%v", complete, completer, err)
	}
}

func systemCompletionCheckpoint(now time.Time, repository, verifiedHead string) ReadyCheckpoint {
	return ReadyCheckpoint{
		ContractVersion: LifecycleContractVersion, RunID: "run-system-completion", Task: linearTask("ENG-123"),
		Repository: repository, PullRequest: 18, BaseBranch: "main", HeadBranch: "eng-123-fix",
		VerifiedHeadOID: verifiedHead, CreatedAt: now, ValidatedAt: now,
	}
}

func systemCompletionRun(root, repository string, checkpoint ReadyCheckpoint) Run {
	route := repositories.Route{ProjectID: "project-test", Repository: repository, DefaultBranch: "main"}
	return Run{
		ID: checkpoint.RunID, Causation: Causation{Task: checkpoint.Task}, Repository: &route,
		RunDirectory: filepath.Join(root, checkpoint.RunID), Ready: &checkpoint,
	}
}

func runCompletionGit(t *testing.T, gitPath, directory string, args ...string) {
	t.Helper()
	command := exec.Command(gitPath, args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func completionGitOutput(t *testing.T, gitPath, directory string, args ...string) string {
	t.Helper()
	command := exec.Command(gitPath, args...)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func writeCompletionJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func completionRemoteURLs(repository string) []string {
	return []string{
		"git@github.com:" + repository + ".git",
		"https://github.com/" + repository,
		"https://github.com/" + repository + ".git",
	}
}
