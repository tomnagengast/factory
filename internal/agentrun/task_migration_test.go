package agentrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

func TestStoreMigratesLegacyLinearTaskIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	legacy := diskState{
		Version: 1,
		Total:   1,
		Runs: []Run{{
			ID: "run-0123456789abcdef", IssueIdentifier: "ENG-46", State: StatePending,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	run := store.Snapshot().Runs[0]
	want := taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-46", Identifier: "ENG-46"}
	if run.Task != want || run.IssueIdentifier != want.Identifier {
		t.Fatalf("migrated Run identity = %#v / %q", run.Task, run.IssueIdentifier)
	}
}

func TestStoreRejectsConflictingTaskIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	state := diskState{
		Version: stateVersion,
		Runs: []Run{{
			ID:              "run-0123456789abcdef",
			Task:            taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-1", Identifier: "FAC-1"},
			IssueIdentifier: "ENG-46",
		}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, 10); err == nil {
		t.Fatal("conflicting legacy and current task identities were accepted")
	}
}

func TestStoreSeparatesSameDisplayIdentifierAcrossProviders(t *testing.T) {
	store := openTestStore(t, 10)
	now := time.Now().UTC()
	linear, err := taskmodel.LegacyLinear("FAC-1")
	if err != nil {
		t.Fatal(err)
	}
	factory := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-1", Identifier: "FAC-1"}
	if _, created, err := store.Claim(testInitialClaim(Trigger{DeliveryID: "linear", Task: linear, Kind: TriggerKindComment}), now); err != nil || !created {
		t.Fatalf("claim Linear task: created=%t err=%v", created, err)
	}
	if _, created, err := store.Claim(testInitialClaim(Trigger{DeliveryID: "factory", Task: factory, Kind: TriggerKindComment}), now); err != nil || !created {
		t.Fatalf("claim Factory task: created=%t err=%v", created, err)
	}
	if got := store.Snapshot(); got.Active != 2 || got.Total != 2 {
		t.Fatalf("cross-provider ownership = %#v", got)
	}
}
