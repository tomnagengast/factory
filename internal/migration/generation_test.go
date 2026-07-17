package migration

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildGenerationMaterializesAndIdempotentlyReopensCompleteState(t *testing.T) {
	t.Parallel()
	source := copyGolden(t)
	beforeFiles, err := hashTree(source)
	if err != nil {
		t.Fatal(err)
	}
	beforeDirectories, err := directoryModes(source)
	if err != nil {
		t.Fatal(err)
	}
	generations := filepath.Join(t.TempDir(), "generations")
	created, err := BuildGeneration(source, generations, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest.StateGeneration != 1 || created.Manifest.MigrationID != created.Report.Manifest.MigrationID {
		t.Fatalf("created generation = %#v", created)
	}
	for _, name := range []string{
		"policy.json", "repositories.json", "runs.jsonl", "tasks.jsonl", "system-events.jsonl",
		"task-source-neutral.json", "activity", "backup", "generation.json", "audit.json", "migration.json", "backup-receipt.json",
	} {
		if _, err := os.Lstat(filepath.Join(created.Path, name)); err != nil {
			t.Fatalf("missing generation artifact %s: %v", name, err)
		}
	}
	reopened, err := BuildGeneration(source, generations, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Path != created.Path || !reflect.DeepEqual(reopened.Manifest, created.Manifest) {
		t.Fatalf("idempotent reopen = %#v, want %#v", reopened, created)
	}
	afterFiles, err := hashTree(source)
	if err != nil {
		t.Fatal(err)
	}
	afterDirectories, err := directoryModes(source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterFiles, beforeFiles) || !reflect.DeepEqual(afterDirectories, beforeDirectories) {
		t.Fatal("generation construction mutated legacy source state")
	}
}

func TestValidateStagedGenerationRejectsArtifactAndBackupTampering(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		path func(Generation) string
	}{
		{name: "canonical artifact", path: func(g Generation) string { return filepath.Join(g.Path, "tasks.jsonl") }},
		{name: "backup", path: func(g Generation) string { return filepath.Join(g.Path, "backup", "native-tasks.jsonl") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := copyGolden(t)
			generation, err := BuildGeneration(source, filepath.Join(t.TempDir(), "generations"), testOptions())
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(test.path(generation), os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.WriteString("tampered\n")
			closeErr := file.Close()
			if err := errors.Join(writeErr, closeErr); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateStagedGeneration(generation.Path, generation.Report); err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("tampered generation error = %v", err)
			}
		})
	}
}

func TestBuildGenerationCleansInterruptedTemporaryState(t *testing.T) {
	t.Parallel()
	source := copyGolden(t)
	generations := filepath.Join(t.TempDir(), "generations")
	options := testOptions()
	options.Inject = func(point string) error {
		if point == "before-generation-install" {
			return errors.New("stop")
		}
		return nil
	}
	if _, err := BuildGeneration(source, generations, options); err == nil || !strings.Contains(err.Error(), "before-generation-install") {
		t.Fatalf("interrupted build error = %v", err)
	}
	entries, err := os.ReadDir(generations)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("interrupted generation residue = %#v", entries)
	}
}
