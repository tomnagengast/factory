package triggerrouter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/taskmodel"
)

func TestStoreMigratesLegacyInvocationTaskIdentity(t *testing.T) {
	directory := t.TempDir()
	configuration, registry := testPolicy()
	store, err := Open(filepath.Join(directory, "current.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.ApplyDecisionBatch([]eventwire.Record{testRecord("linear:legacy", 1, eventwire.SourceLinear, now)}, registry, configuration, now); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	for i := range snapshot.Invocations {
		snapshot.Invocations[i].Task = taskmodel.TaskRef{}
	}
	snapshot.Schema = legacySchemaVersion
	operation := diskOperation{Kind: operationCheckpoint, Schema: legacySchemaVersion, Checkpoint: &snapshot}
	data, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "legacy.jsonl")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, invocation := range migrated.Snapshot().Invocations {
		if invocation.Task.Source != taskmodel.SourceLinear || invocation.Task.ProviderID != invocation.IssueIdentifier {
			t.Fatalf("migrated invocation = %#v", invocation)
		}
	}
}
