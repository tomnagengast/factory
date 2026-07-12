package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

type PullRequestSnapshot struct {
	Number              int
	State               string
	IsDraft             bool
	BaseBranch          string
	HeadBranch          string
	HeadOID             string
	MergeCommitOID      string
	SafeguardRegression bool
	UpdatedAt           time.Time
}

type pullRequestCheck struct {
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
	Status     string `json:"status"`
}

type PullRequestReader interface {
	Snapshot(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error)
}

type PullRequestDiscoverer interface {
	MatchingIssuePullRequests(context.Context, string, string) ([]PullRequestSnapshot, error)
}

type GitHubCLI struct {
	path      string
	directory string
}

func NewGitHubCLI(path, directory string) (*GitHubCLI, error) {
	if path == "" || directory == "" {
		return nil, errors.New("GitHub CLI: path and directory are required")
	}
	return &GitHubCLI{path: path, directory: directory}, nil
}

func (c *GitHubCLI) Snapshot(ctx context.Context, checkpoint ReadyCheckpoint) (PullRequestSnapshot, error) {
	cmd := exec.CommandContext(ctx, c.path,
		"pr", "view", strconv.Itoa(checkpoint.PullRequest),
		"--repo", checkpoint.Repository,
		"--json", "state,isDraft,baseRefName,headRefName,headRefOid,mergeCommit,reviewDecision,statusCheckRollup,updatedAt",
	)
	cmd.Dir = c.directory
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if looksLikeAuthenticationFailure(detail) {
			return PullRequestSnapshot{}, externalAuthenticationError{operation: "GitHub CLI", detail: detail}
		}
		return PullRequestSnapshot{}, fmt.Errorf("GitHub CLI: read PR %d: %w: %s", checkpoint.PullRequest, err, detail)
	}
	var value struct {
		State       string             `json:"state"`
		IsDraft     bool               `json:"isDraft"`
		BaseRefName string             `json:"baseRefName"`
		HeadRefName string             `json:"headRefName"`
		HeadRefOID  string             `json:"headRefOid"`
		Review      string             `json:"reviewDecision"`
		Checks      []pullRequestCheck `json:"statusCheckRollup"`
		UpdatedAt   time.Time          `json:"updatedAt"`
		MergeCommit *struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
	}
	if err := json.Unmarshal(output, &value); err != nil {
		return PullRequestSnapshot{}, fmt.Errorf("GitHub CLI: decode PR %d: %w", checkpoint.PullRequest, err)
	}
	snapshot := PullRequestSnapshot{
		Number:     checkpoint.PullRequest,
		State:      value.State,
		IsDraft:    value.IsDraft,
		BaseBranch: value.BaseRefName,
		HeadBranch: value.HeadRefName,
		HeadOID:    value.HeadRefOID,
		SafeguardRegression: pullRequestSafeguardRegression(
			value.Review,
			value.Checks,
		),
		UpdatedAt: value.UpdatedAt,
	}
	if value.MergeCommit != nil {
		snapshot.MergeCommitOID = value.MergeCommit.OID
	}
	return snapshot, nil
}

func pullRequestSafeguardRegression(reviewDecision string, checks []pullRequestCheck) bool {
	if strings.EqualFold(reviewDecision, "CHANGES_REQUESTED") {
		return true
	}
	for _, check := range checks {
		if check.Status != "" && !strings.EqualFold(check.Status, "COMPLETED") {
			return true
		}
		outcome := check.Conclusion
		if outcome == "" {
			outcome = check.State
		}
		if outcome != "" && !slices.Contains([]string{"NEUTRAL", "SKIPPED", "SUCCESS"}, strings.ToUpper(outcome)) {
			return true
		}
	}
	return false
}

func (c *GitHubCLI) MatchingIssuePullRequests(ctx context.Context, repository, issueIdentifier string) ([]PullRequestSnapshot, error) {
	cmd := exec.CommandContext(ctx, c.path,
		"pr", "list", "--repo", repository, "--state", "all", "--limit", "100",
		"--json", "number,state,isDraft,baseRefName,headRefName,headRefOid,mergeCommit,updatedAt",
	)
	cmd.Dir = c.directory
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if looksLikeAuthenticationFailure(detail) {
			return nil, externalAuthenticationError{operation: "GitHub CLI", detail: detail}
		}
		return nil, fmt.Errorf("GitHub CLI: discover issue PRs: %w: %s", err, detail)
	}
	var values []struct {
		Number      int       `json:"number"`
		State       string    `json:"state"`
		IsDraft     bool      `json:"isDraft"`
		BaseRefName string    `json:"baseRefName"`
		HeadRefName string    `json:"headRefName"`
		HeadRefOID  string    `json:"headRefOid"`
		UpdatedAt   time.Time `json:"updatedAt"`
		MergeCommit *struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
	}
	if err := json.Unmarshal(output, &values); err != nil {
		return nil, fmt.Errorf("GitHub CLI: decode issue PRs: %w", err)
	}
	prefix := strings.ToLower(issueIdentifier) + "-"
	var snapshots []PullRequestSnapshot
	for _, value := range values {
		if !strings.HasPrefix(strings.ToLower(value.HeadRefName), prefix) {
			continue
		}
		snapshot := PullRequestSnapshot{
			Number: value.Number, State: value.State, IsDraft: value.IsDraft, BaseBranch: value.BaseRefName,
			HeadBranch: value.HeadRefName, HeadOID: value.HeadRefOID, UpdatedAt: value.UpdatedAt,
		}
		if value.MergeCommit != nil {
			snapshot.MergeCommitOID = value.MergeCommit.OID
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}
