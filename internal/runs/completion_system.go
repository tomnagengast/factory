package runs

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

	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/taskmodel"
)

const resultFileName = "result.json"

var completionSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SystemCompletionOptions contains process-local authorities. Repository
// identity and deployment locations come only from the canonical repository
// catalog's CompletionIdentity.
type SystemCompletionOptions struct {
	LinearURL      string
	GitPath        string
	WorktrunkPath  string
	LinearAPIKey   string
	HTTPClient     *http.Client
	TaskCompletion TaskCompletionProvider
}

type TaskCompletionProvider interface {
	Complete(context.Context, taskmodel.TaskRef, string, string, string) (bool, error)
}

// SystemCompletionEvidence verifies post-merge evidence against one immutable
// allowlisted repository identity from the selected canonical catalog.
type SystemCompletionEvidence struct {
	identity repositories.CompletionIdentity
	options  SystemCompletionOptions
}

func NewSystemCompletionEvidence(identity repositories.CompletionIdentity, options SystemCompletionOptions) (*SystemCompletionEvidence, error) {
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("completion evidence: %w", err)
	}
	if options.LinearURL == "" || options.GitPath == "" || options.WorktrunkPath == "" || options.LinearAPIKey == "" || options.HTTPClient == nil {
		return nil, errors.New("completion evidence: git, Worktrunk, Linear, and HTTP clients are required")
	}
	return &SystemCompletionEvidence{identity: identity.Clone(), options: options}, nil
}

func (r *SystemCompletionEvidence) ReadCompletionEvidence(ctx context.Context, run Run, snapshot PullRequestSnapshot) (CompletionEvidence, error) {
	evidence := CompletionEvidence{DeploymentRequired: r.identity.Deployment.Required(), SafeguardRegression: snapshot.SafeguardRegression}
	if run.Repository == nil || run.Repository.Repository != r.identity.Repository || run.Ready == nil || run.Ready.Repository != r.identity.Repository {
		return evidence, errors.New("completion evidence: Run repository identity conflicts with reader authority")
	}
	childrenComplete, err := completedChildResults(run.RunDirectory)
	if err != nil {
		return evidence, err
	}
	evidence.ChildrenComplete = childrenComplete
	if evidence.DeploymentRequired {
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
	evidence.TaskComplete, err = r.taskComplete(ctx, run, snapshot, evidence)
	return evidence, err
}

func (r *SystemCompletionEvidence) readDeployableCompletion(ctx context.Context, run Run, snapshot PullRequestSnapshot, evidence CompletionEvidence) (CompletionEvidence, error) {
	deployment := r.identity.Deployment
	if pending, err := readDeploymentReceipt(deployment.PendingReceiptPath); err == nil && pending.Status == "failed" {
		checkpointTime := run.Ready.CreatedAt
		if !run.Ready.ValidatedAt.IsZero() {
			checkpointTime = run.Ready.ValidatedAt
		}
		evidence.DeploymentFailed = pending.App == r.identity.App && pending.ContractVersion == LifecycleContractVersion && !pending.StartedAt.Before(checkpointTime)
	}
	receipt, err := readDeploymentReceipt(deployment.ReceiptPath)
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
	evidence.TaskComplete, err = r.taskComplete(ctx, run, snapshot, evidence)
	return evidence, err
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
	directory := r.identity.Path
	if _, err := completionCommand(ctx, directory, r.options.GitPath, "fetch", "--prune", "origin"); err != nil {
		return result, err
	}
	status, err := completionCommand(ctx, directory, r.options.GitPath, "status", "--porcelain", "--untracked-files=normal", "--", ".", ":(exclude,literal).worktrees")
	if err != nil {
		return result, err
	}
	head, err := completionCommand(ctx, directory, r.options.GitPath, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	branch, err := completionCommand(ctx, directory, r.options.GitPath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return result, err
	}
	upstream, err := completionCommand(ctx, directory, r.options.GitPath, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		return result, err
	}
	originMain, err := completionCommand(ctx, directory, r.options.GitPath, "rev-parse", "origin/"+r.identity.DefaultBranch)
	if err != nil {
		return result, err
	}
	remoteURL, err := completionCommand(ctx, directory, r.options.GitPath, "config", "--get", "remote.origin.url")
	if err != nil {
		return result, err
	}
	headOnMain := completionAncestor(ctx, directory, r.options.GitPath, strings.TrimSpace(string(head)), strings.TrimSpace(string(originMain)))
	result.verifiedHeadContained = completionAncestor(ctx, directory, r.options.GitPath, run.Ready.VerifiedHeadOID, snapshot.MergeCommitOID)
	receiptOnMain := false
	receiptTree := ""
	if gitOIDPattern.MatchString(receipt.SourceCommit) {
		receiptOnMain = completionAncestor(ctx, directory, r.options.GitPath, receipt.SourceCommit, strings.TrimSpace(string(originMain)))
		treeRevision := receipt.SourceCommit + "^{tree}"
		if r.identity.Deployment.SourcePath != "" {
			treeRevision = receipt.SourceCommit + ":" + r.identity.Deployment.SourcePath
		}
		if output, treeErr := completionCommand(ctx, directory, r.options.GitPath, "rev-parse", treeRevision); treeErr == nil {
			receiptTree = strings.TrimSpace(string(output))
		}
	}
	checkpointTime := run.Ready.CreatedAt
	if !run.Ready.ValidatedAt.IsZero() {
		checkpointTime = run.Ready.ValidatedAt
	}
	baseValid := strings.TrimSpace(string(status)) == "" && strings.TrimSpace(string(branch)) == r.identity.DefaultBranch &&
		strings.TrimSpace(string(upstream)) == "origin/"+r.identity.DefaultBranch && headOnMain &&
		slices.Contains(r.identity.RemoteURLs, strings.TrimSpace(string(remoteURL)))
	if r.identity.Deployment.Required() {
		result.sourceValid = baseValid && receiptOnMain && receipt.Status == "success" && receipt.App == r.identity.App &&
			receipt.SourceBranch == r.identity.DefaultBranch && receipt.SourceTree == receiptTree &&
			receipt.ContractVersion == LifecycleContractVersion && gitOIDPattern.MatchString(receipt.SourceCommit) && gitOIDPattern.MatchString(receipt.SourceTree) &&
			completionSHA256Pattern.MatchString(receipt.BinarySHA256) && receipt.DeploymentID != "" && receipt.BuildID != "" &&
			receipt.SourceRepository == r.identity.Repository && !receipt.StartedAt.Before(checkpointTime) && !receipt.FinishedAt.Before(receipt.StartedAt)
		result.mergeContained = completionAncestor(ctx, directory, r.options.GitPath, snapshot.MergeCommitOID, receipt.SourceCommit)
	} else {
		result.sourceValid = baseValid
		result.mergeContained = completionAncestor(ctx, directory, r.options.GitPath, snapshot.MergeCommitOID, strings.TrimSpace(string(head)))
	}

	remote, err := completionCommand(ctx, directory, r.options.GitPath, "ls-remote", "--heads", "origin", "refs/heads/"+run.Ready.HeadBranch)
	if err != nil {
		return result, err
	}
	result.remoteBranchAbsent = strings.TrimSpace(string(remote)) == ""
	worktrees, err := completionCommand(ctx, directory, r.options.WorktrunkPath, "list", "--format=json", "--branches")
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.identity.Deployment.HealthURL, nil)
	if err != nil {
		return HealthIdentity{}, err
	}
	response, err := r.options.HTTPClient.Do(request)
	if err != nil {
		return HealthIdentity{}, fmt.Errorf("read deployment health: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return HealthIdentity{}, fmt.Errorf("read deployment health: HTTP %d", response.StatusCode)
	}
	var identity HealthIdentity
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&identity); err != nil {
		return HealthIdentity{}, fmt.Errorf("decode deployment health: %w", err)
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.options.LinearURL, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	request.Header.Set("Authorization", r.options.LinearAPIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.options.HTTPClient.Do(request)
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

func (r *SystemCompletionEvidence) taskComplete(ctx context.Context, run Run, snapshot PullRequestSnapshot, evidence CompletionEvidence) (bool, error) {
	task, err := run.Causation.Task.Normalize()
	if err != nil {
		return false, fmt.Errorf("read task state: %w", err)
	}
	switch task.Source {
	case taskmodel.SourceLinear:
		return r.linearComplete(ctx, task.Identifier)
	case taskmodel.SourceFactory:
		if r.options.TaskCompletion == nil {
			return false, errors.New("read task state: native completion provider is unavailable")
		}
		if !taskCompletionEvidenceReady(evidence) {
			return false, nil
		}
		return r.options.TaskCompletion.Complete(ctx, task, run.ID, run.Repository.Repository, completionEvidenceRef(run.Repository.Repository, snapshot, evidence))
	default:
		return false, fmt.Errorf("read task state: unsupported provider %q", task.Source)
	}
}

func taskCompletionEvidenceReady(evidence CompletionEvidence) bool {
	if evidence.DeploymentRequired && (evidence.Deployment.Status != "success" || !evidence.HealthMatches) {
		return false
	}
	return evidence.SourceValid && evidence.MergeContained && evidence.VerifiedHeadContained && !evidence.SafeguardRegression &&
		evidence.RemoteBranchAbsent && evidence.WorktreeAbsent && evidence.ChildrenComplete
}

func completionEvidenceRef(repository string, snapshot PullRequestSnapshot, evidence CompletionEvidence) string {
	value := fmt.Sprintf("github:%s:pr:%d:merge:%s", repository, snapshot.Number, snapshot.MergeCommitOID)
	if evidence.DeploymentRequired {
		value += ":deployment:" + evidence.Deployment.DeploymentID
	}
	return value
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
	command := exec.CommandContext(ctx, gitPath, "merge-base", "--is-ancestor", ancestor, descendant)
	command.Dir = directory
	return command.Run() == nil
}
