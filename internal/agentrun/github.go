package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

type PullRequestSnapshot struct {
	State          string
	IsDraft        bool
	BaseBranch     string
	HeadBranch     string
	HeadOID        string
	MergeCommitOID string
	UpdatedAt      time.Time
}

type PullRequestReader interface {
	Snapshot(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error)
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
		"--json", "state,isDraft,baseRefName,headRefName,headRefOid,mergeCommit,updatedAt",
	)
	cmd.Dir = c.directory
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return PullRequestSnapshot{}, fmt.Errorf("GitHub CLI: read PR %d: %w: %s", checkpoint.PullRequest, err, stderr.String())
	}
	var value struct {
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
	if err := json.Unmarshal(output, &value); err != nil {
		return PullRequestSnapshot{}, fmt.Errorf("GitHub CLI: decode PR %d: %w", checkpoint.PullRequest, err)
	}
	snapshot := PullRequestSnapshot{
		State:      value.State,
		IsDraft:    value.IsDraft,
		BaseBranch: value.BaseRefName,
		HeadBranch: value.HeadRefName,
		HeadOID:    value.HeadRefOID,
		UpdatedAt:  value.UpdatedAt,
	}
	if value.MergeCommit != nil {
		snapshot.MergeCommitOID = value.MergeCommit.OID
	}
	return snapshot, nil
}
