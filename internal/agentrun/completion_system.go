package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type SystemCompletionConfig struct {
	App            string
	Repository     string
	RemoteURLs     []string
	RepoPath       string
	BaseBranch     string
	ReceiptPath    string
	PendingReceipt string
	HealthURL      string
	SourcePath     string
	LinearURL      string
	GitPath        string
	WorktrunkPath  string
	LinearAPIKey   string
	HTTPClient     *http.Client
}

type SystemCompletionEvidence struct {
	config             SystemCompletionConfig
	deploymentRequired bool
}

func NewSystemCompletionEvidence(config SystemCompletionConfig) (*SystemCompletionEvidence, error) {
	if config.App == "" {
		config.App = "factory"
	}
	if config.ReceiptPath == "" || config.PendingReceipt == "" || config.HealthURL == "" {
		return nil, errors.New("completion evidence: repository, paths, and health URL are required")
	}
	reader, err := newCompletionEvidence(config)
	if err != nil {
		return nil, err
	}
	reader.deploymentRequired = true
	return reader, nil
}

func NewRepositoryOnlyCompletionEvidence(config SystemCompletionConfig) (*SystemCompletionEvidence, error) {
	if config.ReceiptPath != "" || config.PendingReceipt != "" || config.HealthURL != "" {
		return nil, errors.New("repository-only completion evidence must not configure deployment locations")
	}
	return newCompletionEvidence(config)
}

func newCompletionEvidence(config SystemCompletionConfig) (*SystemCompletionEvidence, error) {
	if !repositoryPattern.MatchString(config.Repository) || config.RepoPath == "" || config.LinearURL == "" {
		return nil, errors.New("completion evidence: repository and paths are required")
	}
	if len(config.RemoteURLs) == 0 || config.GitPath == "" || config.WorktrunkPath == "" || config.LinearAPIKey == "" || config.HTTPClient == nil {
		return nil, errors.New("completion evidence: git, Worktrunk, Linear, and HTTP clients are required")
	}
	if config.BaseBranch == "" {
		config.BaseBranch = "main"
	}
	if !validBranch(config.BaseBranch) {
		return nil, errors.New("completion evidence: valid base branch is required")
	}
	return &SystemCompletionEvidence{config: config}, nil
}

func (r *SystemCompletionEvidence) ReadCompletionEvidence(ctx context.Context, run Run, snapshot PullRequestSnapshot) (CompletionEvidence, error) {
	evidence := CompletionEvidence{DeploymentRequired: r.deploymentRequired, SafeguardRegression: snapshot.SafeguardRegression}
	childrenComplete, err := completedChildResults(run.RunDirectory)
	if err != nil {
		return evidence, err
	}
	evidence.ChildrenComplete = childrenComplete
	if r.deploymentRequired {
		return r.readDeployableCompletion(ctx, run, snapshot, evidence)
	}
	repository, err := r.readRepository(ctx, run, snapshot, DeploymentReceipt{})
	if err != nil {
		return evidence, err
	}
	evidence.SourceValid = repository.sourceValid
	evidence.MergeContained = repository.mergeContained
	evidence.VerifiedHeadContained = repository.verifiedHeadContained
	evidence.RemoteBranchAbsent = repository.remoteBranchAbsent
	evidence.WorktreeAbsent = repository.worktreeAbsent
	evidence.TaskComplete, err = r.taskComplete(ctx, run)
	return evidence, err
}

func (r *SystemCompletionEvidence) readDeployableCompletion(ctx context.Context, run Run, snapshot PullRequestSnapshot, evidence CompletionEvidence) (CompletionEvidence, error) {
	if pending, err := readDeploymentReceipt(r.config.PendingReceipt); err == nil && pending.Status == "failed" {
		checkpointTime := run.Ready.CreatedAt
		if !run.Ready.ValidatedAt.IsZero() {
			checkpointTime = run.Ready.ValidatedAt
		}
		evidence.DeploymentFailed = pending.App == r.config.App && pending.ContractVersion == LifecycleContractVersion && !pending.StartedAt.Before(checkpointTime)
	}
	receipt, err := readDeploymentReceipt(r.config.ReceiptPath)
	if err != nil {
		if evidence.DeploymentFailed {
			return evidence, nil
		}
		return evidence, err
	}
	evidence.Deployment = receipt
	health, err := r.readHealth(ctx)
	if err != nil {
		return evidence, err
	}
	evidence.Health = health
	evidence.HealthMatches = health.Status == "ok" && health.App == receipt.App &&
		health.Commit == receipt.SourceCommit && health.Tree == receipt.SourceTree &&
		health.BuildID == receipt.BuildID && health.DeploymentID == receipt.DeploymentID &&
		health.ContractVersion == fmt.Sprint(LifecycleContractVersion) && !health.StartedAt.Before(receipt.StartedAt)

	repository, err := r.readRepository(ctx, run, snapshot, receipt)
	if err != nil {
		return evidence, err
	}
	evidence.SourceValid = repository.sourceValid
	evidence.MergeContained = repository.mergeContained
	evidence.VerifiedHeadContained = repository.verifiedHeadContained
	evidence.RemoteBranchAbsent = repository.remoteBranchAbsent
	evidence.WorktreeAbsent = repository.worktreeAbsent
	evidence.TaskComplete, err = r.taskComplete(ctx, run)
	if err != nil {
		return evidence, err
	}
	return evidence, nil
}

func completedChildResults(runDirectory string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(runDirectory, "children"))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read child results: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(runDirectory, "children", entry.Name(), resultFileName))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read child result %s: %w", entry.Name(), err)
		}
		var result ProcessResult
		if err := json.Unmarshal(data, &result); err != nil {
			return false, fmt.Errorf("decode child result %s: %w", entry.Name(), err)
		}
		if result.FinishedAt.IsZero() {
			return false, nil
		}
	}
	return true, nil
}

type repositoryCompletion struct {
	sourceValid           bool
	mergeContained        bool
	verifiedHeadContained bool
	remoteBranchAbsent    bool
	worktreeAbsent        bool
}

func (r *SystemCompletionEvidence) readRepository(ctx context.Context, run Run, snapshot PullRequestSnapshot, receipt DeploymentReceipt) (repositoryCompletion, error) {
	var result repositoryCompletion
	if _, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "fetch", "--prune", "origin"); err != nil {
		return result, err
	}
	status, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "status", "--porcelain", "--untracked-files=normal", "--", ".", ":(exclude,literal).worktrees")
	if err != nil {
		return result, err
	}
	head, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	branch, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return result, err
	}
	upstream, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		return result, err
	}
	originMain, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "rev-parse", "origin/"+r.config.BaseBranch)
	if err != nil {
		return result, err
	}
	remoteURL, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "config", "--get", "remote.origin.url")
	if err != nil {
		return result, err
	}
	headOnMain := completionAncestor(ctx, r.config.RepoPath, r.config.GitPath, strings.TrimSpace(string(head)), strings.TrimSpace(string(originMain)))
	result.verifiedHeadContained = completionAncestor(ctx, r.config.RepoPath, r.config.GitPath, run.Ready.VerifiedHeadOID, snapshot.MergeCommitOID)
	receiptOnMain := false
	receiptTree := ""
	if gitOIDPattern.MatchString(receipt.SourceCommit) {
		receiptOnMain = completionAncestor(ctx, r.config.RepoPath, r.config.GitPath, receipt.SourceCommit, strings.TrimSpace(string(originMain)))
		treeRevision := receipt.SourceCommit + "^{tree}"
		if r.config.SourcePath != "" {
			treeRevision = receipt.SourceCommit + ":" + r.config.SourcePath
		}
		if output, treeErr := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "rev-parse", treeRevision); treeErr == nil {
			receiptTree = strings.TrimSpace(string(output))
		}
	}
	checkpointTime := run.Ready.CreatedAt
	if !run.Ready.ValidatedAt.IsZero() {
		checkpointTime = run.Ready.ValidatedAt
	}
	baseValid := strings.TrimSpace(string(status)) == "" && strings.TrimSpace(string(branch)) == r.config.BaseBranch &&
		strings.TrimSpace(string(upstream)) == "origin/"+r.config.BaseBranch && headOnMain &&
		slices.Contains(r.config.RemoteURLs, strings.TrimSpace(string(remoteURL)))
	if r.deploymentRequired {
		result.sourceValid = baseValid && receiptOnMain && receipt.Status == "success" && receipt.App == r.config.App &&
			receipt.SourceBranch == r.config.BaseBranch && receipt.SourceTree == receiptTree &&
			receipt.ContractVersion == LifecycleContractVersion && gitOIDPattern.MatchString(receipt.SourceCommit) && gitOIDPattern.MatchString(receipt.SourceTree) &&
			sha256Pattern.MatchString(receipt.BinarySHA256) && receipt.DeploymentID != "" && receipt.BuildID != "" &&
			receipt.SourceRepository == r.config.Repository && !receipt.StartedAt.Before(checkpointTime) && !receipt.FinishedAt.Before(receipt.StartedAt)
		result.mergeContained = completionAncestor(ctx, r.config.RepoPath, r.config.GitPath, snapshot.MergeCommitOID, receipt.SourceCommit)
	} else {
		result.sourceValid = baseValid
		result.mergeContained = completionAncestor(ctx, r.config.RepoPath, r.config.GitPath, snapshot.MergeCommitOID, strings.TrimSpace(string(head)))
	}

	remote, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "ls-remote", "--heads", "origin", "refs/heads/"+run.Ready.HeadBranch)
	if err != nil {
		return result, err
	}
	result.remoteBranchAbsent = strings.TrimSpace(string(remote)) == ""
	worktrees, err := completionCommand(ctx, r.config.RepoPath, r.config.WorktrunkPath, "list", "--format=json", "--branches")
	if err != nil {
		return result, err
	}
	var rows []struct {
		Branch string `json:"branch"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(worktrees, &rows); err != nil {
		return result, fmt.Errorf("decode Worktrunk list: %w", err)
	}
	result.worktreeAbsent = true
	for _, row := range rows {
		if row.Branch == run.Ready.HeadBranch {
			result.worktreeAbsent = false
			break
		}
	}
	return result, nil
}

func (r *SystemCompletionEvidence) readHealth(ctx context.Context) (HealthIdentity, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.config.HealthURL, nil)
	if err != nil {
		return HealthIdentity{}, err
	}
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return HealthIdentity{}, fmt.Errorf("read Factory health: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return HealthIdentity{}, fmt.Errorf("read Factory health: HTTP %d", response.StatusCode)
	}
	var identity HealthIdentity
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&identity); err != nil {
		return HealthIdentity{}, fmt.Errorf("decode Factory health: %w", err)
	}
	return identity, nil
}

func (r *SystemCompletionEvidence) linearComplete(ctx context.Context, issueIdentifier string) (bool, error) {
	payload, err := json.Marshal(map[string]any{
		"query":     `query FactoryCompletion($identifier: String!) { issue(id: $identifier) { state { type } } }`,
		"variables": map[string]string{"identifier": issueIdentifier},
	})
	if err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.config.LinearURL, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	request.Header.Set("Authorization", r.config.LinearAPIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return false, fmt.Errorf("read Linear issue state: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return false, externalAuthenticationError{operation: "Linear issue state", detail: fmt.Sprintf("HTTP %d", response.StatusCode)}
		}
		return false, fmt.Errorf("read Linear issue state: HTTP %d", response.StatusCode)
	}
	var value struct {
		Data struct {
			Issue *struct {
				State struct {
					Type string `json:"type"`
				} `json:"state"`
			} `json:"issue"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value); err != nil {
		return false, fmt.Errorf("decode Linear issue state: %w", err)
	}
	for _, responseError := range value.Errors {
		if looksLikeAuthenticationFailure(responseError.Message) {
			return false, externalAuthenticationError{operation: "Linear issue state", detail: responseError.Message}
		}
	}
	if len(value.Errors) > 0 || value.Data.Issue == nil {
		return false, errors.New("Linear issue state response is incomplete")
	}
	return strings.EqualFold(value.Data.Issue.State.Type, "completed"), nil
}

func (r *SystemCompletionEvidence) taskComplete(ctx context.Context, run Run) (bool, error) {
	task, err := taskmodel.ResolveCompatibilityIdentity(run.Task, run.IssueIdentifier)
	if err != nil {
		return false, fmt.Errorf("read task state: %w", err)
	}
	switch task.Source {
	case taskmodel.SourceLinear:
		return r.linearComplete(ctx, task.Identifier)
	default:
		return false, fmt.Errorf("read task state: unsupported provider %q", task.Source)
	}
}

func readDeploymentReceipt(path string) (DeploymentReceipt, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return DeploymentReceipt{}, fmt.Errorf("read deployment receipt: %w", err)
	}
	var receipt DeploymentReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return DeploymentReceipt{}, fmt.Errorf("decode deployment receipt: %w", err)
	}
	return receipt, nil
}

func completionCommand(ctx context.Context, directory, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("completion evidence command %s: %w: %s", filepath.Base(name), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func completionAncestor(ctx context.Context, directory, gitPath, ancestor, descendant string) bool {
	cmd := exec.CommandContext(ctx, gitPath, "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = directory
	return cmd.Run() == nil
}
