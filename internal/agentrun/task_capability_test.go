package agentrun

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

func TestTaskCapabilityBindsExactRunTaskAndRepositoryWithoutPersistingToken(t *testing.T) {
	run := Run{
		ID: "run-0123456789abcdef", Task: taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"},
		Repository: "tomnagengast/factory",
	}
	directory := filepath.Join(t.TempDir(), "runs", run.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	token, err := WriteTaskCapability(directory, run, bytes.NewReader(bytes.Repeat([]byte{7}, 32)), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, TaskCapabilityFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) {
		t.Fatal("persisted capability contains the bearer token")
	}
	tokenInfo, err := os.Stat(filepath.Join(directory, TaskCapabilityTokenFileName))
	if err != nil || tokenInfo.Mode().Perm() != 0o600 {
		t.Fatalf("token file info=%v err=%v", tokenInfo, err)
	}
	capability, err := ReadTaskCapability(directory)
	if err != nil || !capability.Authorizes(run, token) || capability.Authorizes(run, token+"x") {
		t.Fatalf("capability=%#v err=%v", capability, err)
	}
	other := run
	other.Task.ProviderID = "task-fedcba9876543210"
	if capability.Authorizes(other, token) {
		t.Fatal("capability authorized another task")
	}
}
