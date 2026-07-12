package agentrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
	ContractVersion      int       `json:"contractVersion"`
	RunID                string    `json:"runId"`
	Repository           string    `json:"repository"`
	PullRequest          int       `json:"pullRequest"`
	BaseBranch           string    `json:"baseBranch"`
	HeadBranch           string    `json:"headBranch"`
	VerifiedHeadOID      string    `json:"verifiedHeadOid"`
	PullRequestUpdatedAt time.Time `json:"pullRequestUpdatedAt,omitempty"`
	CreatedAt            time.Time `json:"createdAt"`
	ValidatedAt          time.Time `json:"validatedAt,omitempty"`
}

func (c ReadyCheckpoint) Validate() error {
	if c.ContractVersion != LifecycleContractVersion {
		return fmt.Errorf("ready checkpoint: unsupported contract version %d", c.ContractVersion)
	}
	if c.RunID == "" {
		return errors.New("ready checkpoint: run ID is required")
	}
	if !repositoryPattern.MatchString(c.Repository) {
		return errors.New("ready checkpoint: repository must be owner/name")
	}
	if c.PullRequest < 1 {
		return errors.New("ready checkpoint: pull request must be positive")
	}
	if !validBranch(c.BaseBranch) || !validBranch(c.HeadBranch) {
		return errors.New("ready checkpoint: base and head branches are invalid")
	}
	if !gitOIDPattern.MatchString(c.VerifiedHeadOID) {
		return errors.New("ready checkpoint: verified head must be a lowercase 40-character Git OID")
	}
	if c.CreatedAt.IsZero() {
		return errors.New("ready checkpoint: creation time is required")
	}
	return nil
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
	if filepath.Base(filepath.Clean(runDirectory)) != checkpoint.RunID {
		return errors.New("ready checkpoint: run directory does not match run ID")
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
	if filepath.Base(filepath.Clean(runDirectory)) != checkpoint.RunID {
		return ReadyCheckpoint{}, errors.New("ready checkpoint: run directory does not match run ID")
	}
	return checkpoint, nil
}
