package taskcompat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureWritesMonotonicMarker(t *testing.T) {
	dataFile := filepath.Join(t.TempDir(), "agent-runs.json")
	if err := Ensure(dataFile); err != nil {
		t.Fatal(err)
	}
	first, err := Read(PathFor(dataFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(dataFile); err != nil {
		t.Fatal(err)
	}
	second, err := Read(PathFor(dataFile))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("marker changed: first=%#v second=%#v", first, second)
	}
}

func TestEnsureRejectsCorruptExistingMarker(t *testing.T) {
	dataFile := filepath.Join(t.TempDir(), "trigger-routing.jsonl")
	if err := os.WriteFile(PathFor(dataFile), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(dataFile); err == nil {
		t.Fatal("corrupt compatibility marker was replaced")
	}
}
