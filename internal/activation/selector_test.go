package activation

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/migration"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerrouter"
)

var activationNow = time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)

func TestPublishSelectionEstablishesExactProviderManifestAndMonotonicBoundary(t *testing.T) {
	t.Parallel()
	dataRoot, generation := buildActivationFixture(t)
	lease, err := AcquireLease(filepath.Join(dataRoot, "state-transition.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	options := PublishOptions{DeploymentID: "deploy-1", SourceCommit: strings.Repeat("a", 40), Now: activationNow}
	boundary, err := publishSelection(dataRoot, generation.Path, lease, options)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := ReadSelection(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantSelection := StateSelection{ContractVersion: 1, StateGeneration: 1, DeploymentContract: 1}
	if selection != wantSelection {
		t.Fatalf("selection = %#v, want %#v", selection, wantSelection)
	}
	data, err := os.ReadFile(filepath.Join(dataRoot, selectionFileName))
	if err != nil || string(data) != "{\"contractVersion\":1,\"stateGeneration\":1,\"deploymentContract\":1}\n" {
		t.Fatalf("provider manifest = %q, %v", data, err)
	}
	persisted, err := ReadWriteBoundary(generation.Path)
	if err != nil || persisted != boundary || boundary.MigrationID != generation.Manifest.MigrationID {
		t.Fatalf("write boundary = %#v persisted %#v err %v", boundary, persisted, err)
	}
	replayed, err := publishSelection(dataRoot, generation.Path, lease, options)
	if err != nil || replayed != boundary {
		t.Fatalf("idempotent publication = %#v, %v", replayed, err)
	}
	if err := DeactivatePreWrite(dataRoot, generation.Path, lease); err == nil || !strings.Contains(err.Error(), "writes started") {
		t.Fatalf("post-write deactivation error = %v", err)
	}
	selected, err := migration.OpenSelectedGeneration(generation.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := selected.Tasks.Create(taskstore.CreateCommand{
		Actor: taskstore.Actor{ID: "activation-test", Kind: taskstore.AuthorSystem},
		Title: "Post-activation task", ProjectID: "project-1", ApprovalMode: taskstore.ApprovalGated,
		IdempotencyKey: "post-activation-task",
	}, activationNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedSelected, err := migration.OpenSelectedGeneration(generation.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopenedSelected.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.OpenStagedGeneration(generation.Path); err == nil || !strings.Contains(err.Error(), "hashes changed") {
		t.Fatalf("mutable selected generation still passed initial hash validation: %v", err)
	}
}

func TestPublishSelectionFailureBeforeWriteBoundaryCanDeactivate(t *testing.T) {
	t.Parallel()
	dataRoot, generation := buildActivationFixture(t)
	lease, err := AcquireLease(filepath.Join(dataRoot, "state-transition.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	options := PublishOptions{
		DeploymentID: "deploy-2", SourceCommit: strings.Repeat("b", 40), Now: activationNow,
		Inject: func(point string) error {
			if point == "after-selection" {
				return errors.New("stop")
			}
			return nil
		},
	}
	if _, err := publishSelection(dataRoot, generation.Path, lease, options); err == nil || !strings.Contains(err.Error(), "after-selection") {
		t.Fatalf("publication error = %v", err)
	}
	if _, err := ReadSelection(dataRoot); err != nil {
		t.Fatalf("selector was not durably published before failure: %v", err)
	}
	if _, err := ReadWriteBoundary(generation.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write boundary exists after injected failure: %v", err)
	}
	if err := DeactivatePreWrite(dataRoot, generation.Path, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSelection(dataRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selector remained after safe deactivation: %v", err)
	}
}

func TestPublishSelectionRejectsChangedLegacySource(t *testing.T) {
	t.Parallel()
	dataRoot, generation := buildActivationFixture(t)
	lease, err := AcquireLease(filepath.Join(dataRoot, "state-transition.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	path := filepath.Join(dataRoot, "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := publishSelection(dataRoot, generation.Path, lease, PublishOptions{
		DeploymentID: "deploy-3", SourceCommit: strings.Repeat("c", 40), Now: activationNow,
	}); err == nil || !strings.Contains(err.Error(), "source artifact changed") {
		t.Fatalf("changed source publication error = %v", err)
	}
	if _, err := ReadSelection(dataRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed source published selector: %v", err)
	}
}

func buildActivationFixture(t *testing.T) (string, migration.Generation) {
	return buildActivationFixtureWithTerminalRun(t, false)
}

func buildTerminalActivationFixture(t *testing.T) (string, migration.Generation) {
	return buildActivationFixtureWithTerminalRun(t, true)
}

func buildActivationFixtureWithTerminalRun(t *testing.T, terminal bool) (string, migration.Generation) {
	t.Helper()
	source := filepath.Join("..", "migration", "testdata", "current-shape")
	home := privateTemp(t)
	stateRoot := filepath.Join(home, ".local", "share", "factory")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(stateRoot, "data")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dataRoot, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		destination := filepath.Join(dataRoot, relative)
		if entry.IsDir() {
			if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
				return err
			}
		}
		return os.Chmod(destination, info.Mode().Perm())
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dataRoot, "task-operations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if terminal {
		runStore, err := agentrun.Open(filepath.Join(dataRoot, "agent-runs.json"), 100)
		if err != nil {
			t.Fatal(err)
		}
		run := runStore.Snapshot().Runs[0]
		finishedAt := activationNow.Add(time.Minute)
		if err := runStore.Finish(run.ID, agentrun.StateSucceeded, run.Attempts, "complete", finishedAt); err != nil {
			t.Fatal(err)
		}
		router, err := triggerrouter.Open(filepath.Join(dataRoot, "trigger-routing.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		invocation := router.Snapshot().Invocations[0]
		if _, err := router.TransitionInvocation(invocation.ID, triggerrouter.StateSucceeded, run.ID, "complete", &finishedAt, finishedAt); err != nil {
			t.Fatal(err)
		}
		if err := runStore.MarkInvocationReflected(run.ID, finishedAt); err != nil {
			t.Fatal(err)
		}
		finished := runStore.Snapshot().Runs[0]
		transition := finished.Transitions[len(finished.Transitions)-1]
		wire, err := eventwire.OpenExisting(filepath.Join(dataRoot, "system-events.jsonl"), 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		record, _, err := wire.Add(eventwire.Event{
			ID:      "factory:run-transition:" + transition.ID,
			Source:  eventwire.SourceFactory,
			Type:    "agent-run",
			Action:  string(transition.State),
			Subject: finished.IssueIdentifier,
			Attributes: map[string][]string{
				"runId": {finished.ID}, "attempts": {strconv.Itoa(transition.Attempts)},
				"taskSource": {string(finished.Task.Source)}, "taskProviderId": {finished.Task.ProviderID}, "taskIdentifier": {finished.Task.Identifier},
				eventwire.AttributeProducer: {"agent-collector"}, eventwire.AttributeProvenance: {"factory"},
			},
			RootEventID: finished.InvocationRootEventID, ParentInvocationID: finished.InvocationID,
			ParentRunID: finished.ID, Hop: finished.InvocationHop,
			AncestorRuleIDs: slices.Clone(finished.InvocationAncestorRuleIDs), ReceivedAt: transition.At,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, channelAcks, _ := wire.Snapshot()
		if err := wire.Acknowledge(record.Sequence, channelAcks); err != nil {
			t.Fatal(err)
		}
	}
	generationRoot := filepath.Join(stateRoot, "generations")
	generation, err := migration.BuildGeneration(dataRoot, generationRoot, migration.Options{
		TriggerActorID: "actor-sanitized",
		CompiledRepositories: []repositories.CompiledSource{{
			App: "factory", Repository: "tomnagengast/factory",
			RepoURL: "git@github.com:tomnagengast/factory.git", RepoPath: "/srv/factory/repos/factory",
			ManagedRoot: "/srv/factory/repos", ProjectPath: "/srv/factory/repos/factory", BaseBranch: "main",
		}},
		Now: activationNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dataRoot, generation
}
