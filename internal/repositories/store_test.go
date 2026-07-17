package repositories

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSourceStateRequiresSchemaAndMonotonicGeneration(t *testing.T) {
	state := repositoryStoreState(t)
	if state.Schema != SchemaVersion || state.Generation != 1 {
		t.Fatalf("converted schema/generation = %d/%d", state.Schema, state.Generation)
	}

	for _, test := range []struct {
		name   string
		mutate func(*SourceState)
	}{
		{name: "missing schema", mutate: func(value *SourceState) { value.Schema = 0 }},
		{name: "future schema", mutate: func(value *SourceState) { value.Schema++ }},
		{name: "missing generation", mutate: func(value *SourceState) { value.Generation = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := state.Clone()
			test.mutate(&candidate)
			if _, err := NewCatalog(candidate); err == nil {
				t.Fatal("invalid source state was accepted")
			}
		})
	}

	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.replace(state); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("same-generation replacement error = %v", err)
	}
	next := state.Clone()
	next.Generation++
	if err := catalog.replace(next); err != nil {
		t.Fatalf("next generation: %v", err)
	}
	skipped := next.Clone()
	skipped.Generation += 2
	if err := catalog.replace(skipped); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("skipped-generation replacement error = %v", err)
	}

	exhausted := state.Clone()
	exhausted.Generation = math.MaxUint64
	catalog, err = NewCatalog(exhausted)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.replace(exhausted); err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("exhausted-generation replacement error = %v", err)
	}
}

func TestStoreCreateOpenAndSnapshotAreCanonicalAndIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repositories.json")
	state := repositoryStoreState(t)
	store, err := Create(path, state)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != 0o600 {
		t.Fatalf("artifact mode = %v, %v", info, err)
	}
	if record, found := store.catalog.Record("tomnagengast/factory"); !found || record.Project.ID != "project-factory" {
		t.Fatalf("owned catalog record = %#v, found=%t", record, found)
	}

	snapshot := store.Snapshot()
	snapshot.Records[0].Project.Name = "Mutated"
	changed := time.Now().UTC().Add(time.Hour)
	*snapshot.Records[0].Setup.ProvisionedAt = changed
	current := store.Snapshot()
	if current.Records[0].Project.Name == "Mutated" || current.Records[0].Setup.ProvisionedAt.Equal(changed) {
		t.Fatalf("store snapshot aliases memory: %#v", current)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.Snapshot(), state) {
		t.Fatalf("reopened state = %#v, want %#v", reopened.Snapshot(), state)
	}
}

func TestStoreStrictOpenRejectsUnknownTrailingOversizedAndUnsafeFiles(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "repositories.json")
	if _, err := Create(path, repositoryStoreState(t)); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "unknown field",
			data: bytes.Replace(valid, []byte("{\n"), []byte("{\n  \"unknown\": true,\n"), 1),
		},
		{name: "trailing JSON", data: append(append([]byte(nil), valid...), []byte("{}")...)},
		{
			name: "unsupported schema",
			data: bytes.Replace(valid, []byte("\"schema\": 1"), []byte("\"schema\": 2"), 1),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-")+".json")
			if err := os.WriteFile(candidate, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(candidate); err == nil {
				t.Fatal("strict open accepted invalid JSON")
			}
		})
	}

	oversized := filepath.Join(directory, "oversized.json")
	if err := os.WriteFile(oversized, make([]byte, maxSourceStateBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(oversized); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized error = %v", err)
	}

	public := filepath.Join(directory, "public.json")
	if err := os.WriteFile(public, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(public); err == nil {
		t.Fatal("non-0600 artifact was accepted")
	}

	symlink := filepath.Join(directory, "repositories-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(symlink); err == nil {
		t.Fatal("symlinked artifact was accepted")
	}
	if _, err := Open(directory); err == nil {
		t.Fatal("directory artifact was accepted")
	}
}

func TestStoreCreateConflictPreservesExistingArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repositories.json")
	state := repositoryStoreState(t)
	if _, err := Create(path, state); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate := state.Clone()
	candidate.Records[0].Project.Name = "Replacement"
	if _, err := Create(path, candidate); err == nil {
		t.Fatal("Create replaced an existing artifact")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("existing artifact changed: %v", err)
	}
}

func TestStoreConcurrentCreateInstallsExactlyOneArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repositories.json")
	state := repositoryStoreState(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := Create(path, state)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded := 0
	conflicted := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case strings.Contains(err.Error(), "already exists"):
			conflicted++
		default:
			t.Fatalf("concurrent Create error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent Create results = %d success, %d conflict", succeeded, conflicted)
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("open installed artifact: %v", err)
	}
}

func TestStoreWriteFailureBeforeRenamePreservesMemoryAndDisk(t *testing.T) {
	store := mustCreateRepositoryStore(t)
	before := store.Snapshot()
	beforeDisk, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected pre-rename failure")
	store.writer = func(string, SourceState) (bool, error) {
		return false, injected
	}
	candidate := before.Clone()
	candidate.Records[0].Project.Name = "Factory Platform"
	updated, err := store.persist(candidate)
	if !errors.Is(err, injected) {
		t.Fatalf("persist error = %v", err)
	}
	if !reflect.DeepEqual(updated, before) || !reflect.DeepEqual(store.Snapshot(), before) {
		t.Fatalf("memory changed after pre-rename failure: %#v", store.Snapshot())
	}
	afterDisk, err := os.ReadFile(store.path)
	if err != nil || !bytes.Equal(afterDisk, beforeDisk) {
		t.Fatalf("disk changed after pre-rename failure: %v", err)
	}
}

func TestStoreConvergesAfterPostRenameDirectorySyncFailure(t *testing.T) {
	store := mustCreateRepositoryStore(t)
	injected := errors.New("injected directory sync failure")
	failed := false
	store.writer = func(path string, state SourceState) (bool, error) {
		if !failed {
			failed = true
			return writeSourceStateWithDirectorySync(path, state, func(*os.File) error {
				return injected
			})
		}
		return writeSourceState(path, state)
	}

	before := store.Snapshot()
	candidate := before.Clone()
	candidate.Records[0].Project.Name = "Factory Platform"
	updated, err := store.persist(candidate)
	if !errors.Is(err, injected) {
		t.Fatalf("post-rename persist error = %v", err)
	}
	if updated.Generation != before.Generation+1 || updated.Records[0].Project.Name != "Factory Platform" {
		t.Fatalf("post-rename snapshot = %#v", updated)
	}
	if !reflect.DeepEqual(store.Snapshot(), updated) {
		t.Fatalf("memory diverged after rename: %#v", store.Snapshot())
	}
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.Snapshot(), updated) {
		t.Fatalf("disk diverged after rename: %#v", reopened.Snapshot())
	}

	nextCandidate := store.Snapshot()
	nextCandidate.Records[0].Project.Name = "Factory Repository Platform"
	next, err := store.persist(nextCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != updated.Generation+1 {
		t.Fatalf("converged generation = %d, want %d", next.Generation, updated.Generation+1)
	}
}

func mustCreateRepositoryStore(t *testing.T) *Store {
	t.Helper()
	store, err := Create(filepath.Join(t.TempDir(), "repositories.json"), repositoryStoreState(t))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func repositoryStoreState(t *testing.T) SourceState {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	state, err := ConvertSources(
		[]CompiledSource{compiledSource(root, "factory")},
		[]SetupSource{succeededSetup(root, "factory", now)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
