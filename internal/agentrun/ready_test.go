package agentrun

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadyCheckpointRoundTrip(t *testing.T) {
	t.Parallel()

	runDirectory := filepath.Join(t.TempDir(), "runs", "run-test")
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatalf("create run directory: %v", err)
	}
	checkpoint := ReadyCheckpoint{
		ContractVersion: LifecycleContractVersion,
		RunID:           "run-test",
		Repository:      "tomnagengast/network",
		PullRequest:     8,
		BaseBranch:      "main",
		HeadBranch:      "eng-28-fix",
		VerifiedHeadOID: "08c1c678a0b23bbe8e2dc2da1e398583d7e4c416",
		CreatedAt:       time.Date(2026, time.July, 11, 20, 0, 0, 0, time.UTC),
	}
	if err := WriteReadyCheckpoint(runDirectory, checkpoint); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	got, err := ReadReadyCheckpoint(runDirectory)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if got != checkpoint {
		t.Fatalf("checkpoint = %#v, want %#v", got, checkpoint)
	}
	info, err := os.Stat(filepath.Join(runDirectory, readyCheckpointFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode = %v, err = %v", info.Mode().Perm(), err)
	}
}

func TestReadyCheckpointRejectsInvalidTrustFields(t *testing.T) {
	t.Parallel()

	base := ReadyCheckpoint{
		ContractVersion: LifecycleContractVersion,
		RunID:           "run-test",
		Repository:      "tomnagengast/network",
		PullRequest:     8,
		BaseBranch:      "main",
		HeadBranch:      "eng-28-fix",
		VerifiedHeadOID: "08c1c678a0b23bbe8e2dc2da1e398583d7e4c416",
		CreatedAt:       time.Now(),
	}
	tests := []struct {
		name   string
		mutate func(*ReadyCheckpoint)
	}{
		{name: "contract", mutate: func(value *ReadyCheckpoint) { value.ContractVersion++ }},
		{name: "repository", mutate: func(value *ReadyCheckpoint) { value.Repository = "network" }},
		{name: "pull request", mutate: func(value *ReadyCheckpoint) { value.PullRequest = 0 }},
		{name: "branch", mutate: func(value *ReadyCheckpoint) { value.HeadBranch = "../bad" }},
		{name: "head", mutate: func(value *ReadyCheckpoint) { value.VerifiedHeadOID = "not-an-oid" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := base
			tt.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
