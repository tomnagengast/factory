package agentrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/predicate"
	"github.com/tomnagengast/factory/internal/taskmodel"
)

const (
	LifecycleContractVersion = 1
	readyCheckpointFileName  = "ready-for-merge.json"
)

var (
	gitOIDPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	branchPattern     = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

type ReadyCheckpoint struct {
	ContractVersion      int               `json:"contractVersion"`
	RunID                string            `json:"runId"`
	Task                 taskmodel.TaskRef `json:"task,omitzero"`
	Repository           string            `json:"repository"`
	PullRequest          int               `json:"pullRequest"`
	BaseBranch           string            `json:"baseBranch"`
	HeadBranch           string            `json:"headBranch"`
	VerifiedHeadOID      string            `json:"verifiedHeadOid"`
	PullRequestUpdatedAt time.Time         `json:"pullRequestUpdatedAt,omitempty"`
	CreatedAt            time.Time         `json:"createdAt"`
	ValidatedAt          time.Time         `json:"validatedAt,omitempty"`
}

func (c ReadyCheckpoint) Validate() error {
	taskValid := true
	taskFailure := "ready checkpoint: task is invalid"
	if !c.Task.IsZero() {
		if err := c.Task.Validate(); err != nil {
			taskValid = false
			taskFailure = fmt.Sprintf("ready checkpoint: invalid task: %v", err)
		}
	}
	taskPrefixValid := true
	taskPrefixFailure := "ready checkpoint: task prefix is invalid"
	if !c.Task.IsZero() {
		prefix, err := c.Task.BranchPrefix()
		if err != nil {
			taskPrefixValid = false
			taskPrefixFailure = fmt.Sprintf("ready checkpoint: derive task branch prefix: %v", err)
		} else {
			taskPrefixValid = strings.HasPrefix(c.HeadBranch, prefix)
			taskPrefixFailure = fmt.Sprintf("ready checkpoint: head branch must begin with task prefix %q", prefix)
		}
	}
	profile := predicate.Profile{Name: "ready-checkpoint", Mode: predicate.All, Requirements: []predicate.Requirement{
		{Atom: predicate.ReadyContractVersion, Failure: fmt.Sprintf("ready checkpoint: unsupported contract version %d", c.ContractVersion)},
		{Atom: predicate.ReadyRunID, Failure: "ready checkpoint: run ID is required"},
		{Atom: predicate.ReadyTaskIdentity, Failure: taskFailure},
		{Atom: predicate.ReadyRepository, Failure: "ready checkpoint: repository must be owner/name"},
		{Atom: predicate.ReadyPullRequest, Failure: "ready checkpoint: pull request must be positive"},
		{Atom: predicate.ReadyBaseBranch, Failure: "ready checkpoint: base and head branches are invalid"},
		{Atom: predicate.ReadyHeadBranch, Failure: "ready checkpoint: base and head branches are invalid"},
		{Atom: predicate.ReadyTaskPrefix, Failure: taskPrefixFailure},
		{Atom: predicate.ReadyVerifiedHead, Failure: "ready checkpoint: verified head must be a lowercase 40-character Git OID"},
		{Atom: predicate.ReadyCreatedAt, Failure: "ready checkpoint: creation time is required"},
	}}
	return predicateProfileFailure(profile, map[predicate.Atom]bool{
		predicate.ReadyContractVersion: c.ContractVersion == LifecycleContractVersion,
		predicate.ReadyRunID:           c.RunID != "",
		predicate.ReadyTaskIdentity:    taskValid,
		predicate.ReadyRepository:      repositoryPattern.MatchString(c.Repository),
		predicate.ReadyPullRequest:     c.PullRequest > 0,
		predicate.ReadyBaseBranch:      validBranch(c.BaseBranch),
		predicate.ReadyHeadBranch:      validBranch(c.HeadBranch),
		predicate.ReadyTaskPrefix:      taskPrefixValid,
		predicate.ReadyVerifiedHead:    gitOIDPattern.MatchString(c.VerifiedHeadOID),
		predicate.ReadyCreatedAt:       !c.CreatedAt.IsZero(),
	})
}

func validBranch(value string) bool {
	return value != "" && len(value) <= 255 && branchPattern.MatchString(value) &&
		!strings.HasPrefix(value, "/") && !strings.HasSuffix(value, "/") &&
		!strings.Contains(value, "..") && !strings.Contains(value, "//")
}

func WriteReadyCheckpoint(runDirectory string, checkpoint ReadyCheckpoint) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	if err := validateReadyRunDirectory(runDirectory, checkpoint); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(runDirectory, readyCheckpointFileName), checkpoint)
}

func ReadReadyCheckpoint(runDirectory string) (ReadyCheckpoint, error) {
	data, err := os.ReadFile(filepath.Join(runDirectory, readyCheckpointFileName))
	if err != nil {
		return ReadyCheckpoint{}, fmt.Errorf("read ready checkpoint: %w", err)
	}
	var checkpoint ReadyCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return ReadyCheckpoint{}, fmt.Errorf("decode ready checkpoint: %w", err)
	}
	if err := checkpoint.Validate(); err != nil {
		return ReadyCheckpoint{}, err
	}
	if err := validateReadyRunDirectory(runDirectory, checkpoint); err != nil {
		return ReadyCheckpoint{}, err
	}
	return checkpoint, nil
}

func validateReadyRunDirectory(runDirectory string, checkpoint ReadyCheckpoint) error {
	profile := predicate.Profile{Name: "ready-run-directory", Mode: predicate.All, Requirements: []predicate.Requirement{{
		Atom: predicate.ReadyRunDirectory, Failure: "ready checkpoint: run directory does not match run ID",
	}}}
	return predicateProfileFailure(profile, map[predicate.Atom]bool{
		predicate.ReadyRunDirectory: filepath.Base(filepath.Clean(runDirectory)) == checkpoint.RunID,
	})
}
