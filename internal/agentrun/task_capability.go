package agentrun

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

const (
	TaskCapabilityFileName      = "task-capability.json"
	TaskCapabilityTokenFileName = "task-capability.token"
)

type TaskCapability struct {
	RunID      string            `json:"runId"`
	Task       taskmodel.TaskRef `json:"task"`
	Repository string            `json:"repository"`
	TokenHash  string            `json:"tokenHash"`
	CreatedAt  time.Time         `json:"createdAt"`
}

func WriteTaskCapability(runDirectory string, run Run, random io.Reader, now time.Time) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	if runDirectory == "" || run.ID == "" || run.Repository == "" {
		return "", errors.New("task capability: Run context is incomplete")
	}
	task, err := run.Task.Normalize()
	if err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	capability := TaskCapability{RunID: run.ID, Task: task, Repository: run.Repository, TokenHash: hashCapability(token), CreatedAt: now.UTC()}
	if err := writeJSONFile(filepath.Join(runDirectory, TaskCapabilityFileName), capability); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(runDirectory, TaskCapabilityTokenFileName), []byte(token), 0o600); err != nil {
		_ = os.Remove(filepath.Join(runDirectory, TaskCapabilityFileName))
		return "", err
	}
	return token, nil
}

func ReadTaskCapability(runDirectory string) (TaskCapability, error) {
	data, err := os.ReadFile(filepath.Join(runDirectory, TaskCapabilityFileName))
	if err != nil {
		return TaskCapability{}, err
	}
	var capability TaskCapability
	if err := json.Unmarshal(data, &capability); err != nil {
		return TaskCapability{}, err
	}
	if capability.RunID == "" || capability.Repository == "" || capability.TokenHash == "" || capability.CreatedAt.IsZero() {
		return TaskCapability{}, errors.New("task capability: persisted context is invalid")
	}
	if _, err := capability.Task.Normalize(); err != nil {
		return TaskCapability{}, err
	}
	return capability, nil
}

func (c TaskCapability) Authorizes(run Run, token string) bool {
	if token == "" || c.RunID != run.ID || !c.Task.Equal(run.Task) || c.Repository != run.Repository {
		return false
	}
	expected, err := hex.DecodeString(c.TokenHash)
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	actual, _ := hex.DecodeString(hashCapability(token))
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func hashCapability(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
