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
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type SystemCompletionConfig struct {
	Repository     string
	RemoteURLs     []string
	RepoPath       string
	ReceiptPath    string
	PendingReceipt string
	HealthURL      string
	LinearURL      string
	GitPath        string
	WorktrunkPath  string
	LinearAPIKey   string
	HTTPClient     *http.Client
}

type SystemCompletionEvidence struct {
	config SystemCompletionConfig
}

func NewSystemCompletionEvidence(config SystemCompletionConfig) (*SystemCompletionEvidence, error) {
	if !repositoryPattern.MatchString(config.Repository) || config.RepoPath == "" || config.ReceiptPath == "" || config.PendingReceipt == "" || config.HealthURL == "" || config.LinearURL == "" {
		return nil, errors.New("completion evidence: repository, paths, and health URL are required")
	}
	if len(config.RemoteURLs) == 0 || config.GitPath == "" || config.WorktrunkPath == "" || config.LinearAPIKey == "" || config.HTTPClient == nil {
		return nil, errors.New("completion evidence: git, Worktrunk, Linear, and HTTP clients are required")
	}
	return &SystemCompletionEvidence{config: config}, nil
}

func (r *SystemCompletionEvidence) ReadCompletionEvidence(ctx context.Context, run Run, snapshot PullRequestSnapshot) (CompletionEvidence, error) {
	var evidence CompletionEvidence
	childrenComplete, err := completedChildResults(run.RunDirectory)
	if err != nil {
		return evidence, err
	}
	evidence.ChildrenComplete = childrenComplete
	if pending, err := readDeploymentReceipt(r.config.PendingReceipt); err == nil && pending.Status == "failed" {
		checkpointTime := run.Ready.CreatedAt
		if !run.Ready.ValidatedAt.IsZero() {
			checkpointTime = run.Ready.ValidatedAt
		}
		evidence.DeploymentFailed = pending.App == "factory" && pending.ContractVersion == LifecycleContractVersion && !pending.StartedAt.Before(checkpointTime)
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
	evidence.RemoteBranchAbsent = repository.remoteBranchAbsent
	evidence.WorktreeAbsent = repository.worktreeAbsent
	evidence.LinearComplete, err = r.linearComplete(ctx, run.IssueIdentifier)
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
		if result.Status != string(StateSucceeded) || result.ExitCode != 0 || result.FinishedAt.IsZero() {
			return false, nil
		}
	}
	return true, nil
}

type repositoryCompletion struct {
	sourceValid        bool
	mergeContained     bool
	remoteBranchAbsent bool
	worktreeAbsent     bool
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
	tree, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "rev-parse", "HEAD^{tree}")
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
	originMain, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "rev-parse", "origin/main")
	if err != nil {
		return result, err
	}
	remoteURL, err := completionCommand(ctx, r.config.RepoPath, r.config.GitPath, "remote", "get-url", "origin")
	if err != nil {
		return result, err
	}
	checkpointTime := run.Ready.CreatedAt
	if !run.Ready.ValidatedAt.IsZero() {
		checkpointTime = run.Ready.ValidatedAt
	}
	result.sourceValid = strings.TrimSpace(string(status)) == "" && strings.TrimSpace(string(branch)) == "main" &&
		strings.TrimSpace(string(upstream)) == "origin/main" && strings.TrimSpace(string(head)) == strings.TrimSpace(string(originMain)) &&
		receipt.Status == "success" && receipt.App == "factory" && receipt.SourceBranch == "main" &&
		receipt.SourceCommit == strings.TrimSpace(string(head)) && receipt.SourceTree == strings.TrimSpace(string(tree)) &&
		receipt.ContractVersion == LifecycleContractVersion && gitOIDPattern.MatchString(receipt.SourceCommit) && gitOIDPattern.MatchString(receipt.SourceTree) &&
		sha256Pattern.MatchString(receipt.BinarySHA256) && receipt.DeploymentID != "" && receipt.BuildID != "" &&
		slices.Contains(r.config.RemoteURLs, strings.TrimSpace(string(remoteURL))) && receipt.SourceRepository == r.config.Repository && !receipt.StartedAt.Before(checkpointTime) &&
		!receipt.FinishedAt.Before(receipt.StartedAt)
	mergeCommand := exec.CommandContext(ctx, r.config.GitPath, "merge-base", "--is-ancestor", snapshot.MergeCommitOID, receipt.SourceCommit)
	mergeCommand.Dir = r.config.RepoPath
	result.mergeContained = mergeCommand.Run() == nil

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
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value); err != nil {
		return false, fmt.Errorf("decode Linear issue state: %w", err)
	}
	if len(value.Errors) > 0 || value.Data.Issue == nil {
		return false, errors.New("Linear issue state response is incomplete")
	}
	return strings.EqualFold(value.Data.Issue.State.Type, "completed"), nil
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
