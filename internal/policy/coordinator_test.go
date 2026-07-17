package policy

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/workflow"
)

func TestCoordinatorAdmissionAndMutationExcludeEachOther(t *testing.T) {
	t.Run("admission excludes mutation", func(t *testing.T) {
		store := mustCreateCoordinatorStore(t)
		predicateEntered := make(chan struct{})
		coordinator := mustCoordinator(t, store, func() bool {
			close(predicateEntered)
			return false
		})
		admissionEntered := make(chan struct{})
		releaseAdmission := make(chan struct{})
		admissionDone := make(chan error, 1)
		go func() {
			admissionDone <- coordinator.Admit(func(Snapshot) error {
				close(admissionEntered)
				<-releaseAdmission
				return nil
			})
		}()
		<-admissionEntered

		mutationStarted := make(chan struct{})
		mutationDone := make(chan error, 1)
		go func() {
			current := coordinator.Snapshot()
			close(mutationStarted)
			_, err := coordinator.SetProject(current.TaskControl().Revision, "project-network", true, policyTestNow)
			mutationDone <- err
		}()
		<-mutationStarted
		assertBlocked(t, predicateEntered, "mutation entered while admission held the coordinator")
		close(releaseAdmission)
		if err := <-admissionDone; err != nil {
			t.Fatal(err)
		}
		<-predicateEntered
		if err := <-mutationDone; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mutation excludes admission", func(t *testing.T) {
		store := mustCreateCoordinatorStore(t)
		writerEntered := make(chan struct{})
		releaseWriter := make(chan struct{})
		store.writer = func(path string, snapshot Snapshot) (bool, error) {
			close(writerEntered)
			<-releaseWriter
			return writeSnapshot(path, snapshot)
		}
		coordinator := mustCoordinator(t, store, func() bool { return false })
		mutationDone := make(chan error, 1)
		go func() {
			current := coordinator.Snapshot()
			_, err := coordinator.SetProject(current.TaskControl().Revision, "project-network", true, policyTestNow)
			mutationDone <- err
		}()
		<-writerEntered

		admissionStarted := make(chan struct{})
		admissionEntered := make(chan struct{})
		admissionDone := make(chan error, 1)
		go func() {
			close(admissionStarted)
			admissionDone <- coordinator.Admit(func(Snapshot) error {
				close(admissionEntered)
				return nil
			})
		}()
		<-admissionStarted
		assertBlocked(t, admissionEntered, "admission entered while mutation held the coordinator")
		close(releaseWriter)
		if err := <-mutationDone; err != nil {
			t.Fatal(err)
		}
		<-admissionEntered
		if err := <-admissionDone; err != nil {
			t.Fatal(err)
		}
	})
}

func TestCoordinatorAdmissionReceivesOneImmutableSnapshot(t *testing.T) {
	store := mustCreateCoordinatorStore(t)
	coordinator := mustCoordinator(t, store, func() bool { return false })
	initial := coordinator.Snapshot()
	callbackEntered := make(chan Snapshot, 1)
	releaseCallback := make(chan struct{})
	admissionDone := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		admissionDone <- coordinator.Admit(func(snapshot Snapshot) error {
			calls.Add(1)
			callbackEntered <- snapshot
			<-releaseCallback
			if snapshot.Generation() != initial.Generation() {
				return errors.New("admission snapshot changed while callback was running")
			}
			model := snapshot.Model()
			model.Generation++
			if snapshot.Generation() != initial.Generation() {
				return errors.New("admission snapshot exposed mutable state")
			}
			return nil
		})
	}()
	delivered := <-callbackEntered

	mutationStarted := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		close(mutationStarted)
		_, err := coordinator.SetProject(initial.TaskControl().Revision, "project-network", true, policyTestNow)
		mutationDone <- err
	}()
	<-mutationStarted
	close(releaseCallback)
	if err := <-admissionDone; err != nil {
		t.Fatal(err)
	}
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("admission callback calls = %d, want 1", calls.Load())
	}
	if delivered.Generation() != initial.Generation() || coordinator.Snapshot().Generation() != initial.Generation()+1 {
		t.Fatalf("generations = delivered %d current %d, want %d then %d", delivered.Generation(), coordinator.Snapshot().Generation(), initial.Generation(), initial.Generation()+1)
	}
}

func TestCoordinatorRejectsEveryMutationFamilyWhileAdmissionPending(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Coordinator) error
	}{
		{
			name: "settings agents and runtime",
			mutate: func(coordinator *Coordinator) error {
				current := SettingsView(coordinator.Snapshot())
				current.Agents.Principal.MaxAttempts++
				_, err := coordinator.UpdateSettings(current.Revision, current.Agents, current.Runtime, policyTestNow)
				return err
			},
		},
		{
			name: "registry with settings dependency",
			mutate: func(coordinator *Coordinator) error {
				current := coordinator.Snapshot()
				candidate := RegistryView(current)
				candidate.Rules[0].Enabled = !candidate.Rules[0].Enabled
				_, err := coordinator.UpdateRegistry(candidate.Revision, current.Settings().Revision, candidate, policyTestNow)
				return err
			},
		},
		{
			name: "workflow publish",
			mutate: func(coordinator *Coordinator) error {
				current := coordinator.Snapshot()
				_, err := coordinator.PublishWorkflow(current.Settings().Revision, 0, workflow.Definition{
					ID: "pending-publish", Name: "Pending publish", Enabled: false, Markdown: "# Pending\n",
				}, policyTestNow)
				return err
			},
		},
		{
			name: "workflow delete",
			mutate: func(coordinator *Coordinator) error {
				current := coordinator.Snapshot()
				definition, _ := current.Workflow("deletable")
				_, err := coordinator.DeleteWorkflow(current.Settings().Revision, definition.Revision, definition.ID, policyTestNow)
				return err
			},
		},
		{
			name: "protected feedback",
			mutate: func(coordinator *Coordinator) error {
				current := coordinator.Snapshot()
				_, err := coordinator.UpdateProtectedFeedback(current.Settings().Revision, "custom-review", policyTestNow)
				return err
			},
		},
		{
			name: "native project activation",
			mutate: func(coordinator *Coordinator) error {
				current := coordinator.Snapshot()
				_, err := coordinator.SetProject(current.TaskControl().Revision, "project-network", true, policyTestNow)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateCoordinatorStore(t)
			var predicateCalls atomic.Int32
			coordinator := mustCoordinator(t, store, func() bool {
				predicateCalls.Add(1)
				return true
			})
			before, err := coordinator.Snapshot().Digest()
			if err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(coordinator); !errors.Is(err, ErrAdmissionPending) {
				t.Fatalf("mutation error = %v, want %v", err, ErrAdmissionPending)
			}
			after, err := coordinator.Snapshot().Digest()
			if err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("pending mutation changed canonical policy")
			}
			if predicateCalls.Load() != 1 {
				t.Fatalf("pending predicate calls = %d, want 1", predicateCalls.Load())
			}
		})
	}
}

func TestCoordinatorPreservesTaskControlNoOpGeneration(t *testing.T) {
	store := mustCreateCoordinatorStore(t)
	coordinator := mustCoordinator(t, store, func() bool { return false })
	before := coordinator.Snapshot()
	control, err := coordinator.SetProject(before.TaskControl().Revision, "project-factory", true, policyTestNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	after := coordinator.Snapshot()
	if after.Generation() != before.Generation() || control.Revision != before.TaskControl().Revision ||
		!control.UpdatedAt.Equal(before.TaskControl().UpdatedAt) {
		t.Fatalf("no-op advanced policy: generation %d -> %d, task revision %d -> %d", before.Generation(), after.Generation(), before.TaskControl().Revision, control.Revision)
	}
}

func TestCoordinatorPreservesIndependentRevisionDomains(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Coordinator, Snapshot) error
		verify func(*testing.T, Snapshot, Snapshot)
	}{
		{
			name: "settings",
			mutate: func(coordinator *Coordinator, before Snapshot) error {
				configuration := SettingsView(before)
				configuration.Agents.Principal.MaxAttempts++
				_, err := coordinator.UpdateSettings(configuration.Revision, configuration.Agents, configuration.Runtime, policyTestNow)
				return err
			},
			verify: func(t *testing.T, before, after Snapshot) {
				assertCoordinatorRevisions(t, before, after, 1, 1, 0, 0)
			},
		},
		{
			name: "registry",
			mutate: func(coordinator *Coordinator, before Snapshot) error {
				registry := RegistryView(before)
				registry.Rules[0].Enabled = !registry.Rules[0].Enabled
				_, err := coordinator.UpdateRegistry(registry.Revision, before.Settings().Revision, registry, policyTestNow)
				return err
			},
			verify: func(t *testing.T, before, after Snapshot) {
				assertCoordinatorRevisions(t, before, after, 1, 0, 1, 0)
			},
		},
		{
			name: "workflow publish",
			mutate: func(coordinator *Coordinator, before Snapshot) error {
				_, err := coordinator.PublishWorkflow(before.Settings().Revision, 0, workflow.Definition{
					ID: "published", Name: "Published", Enabled: false, Markdown: "# Published\n",
				}, policyTestNow)
				return err
			},
			verify: func(t *testing.T, before, after Snapshot) {
				assertCoordinatorRevisions(t, before, after, 1, 1, 0, 0)
				definition, found := after.Workflow("published")
				if !found || definition.Revision != 1 {
					t.Fatalf("published workflow = %#v, found=%t", definition, found)
				}
			},
		},
		{
			name: "workflow delete",
			mutate: func(coordinator *Coordinator, before Snapshot) error {
				definition, _ := before.Workflow("deletable")
				_, err := coordinator.DeleteWorkflow(before.Settings().Revision, definition.Revision, definition.ID, policyTestNow)
				return err
			},
			verify: func(t *testing.T, before, after Snapshot) {
				assertCoordinatorRevisions(t, before, after, 1, 1, 0, 0)
				if _, found := after.Workflow("deletable"); found {
					t.Fatal("workflow delete did not reach the Store")
				}
			},
		},
		{
			name: "protected feedback",
			mutate: func(coordinator *Coordinator, before Snapshot) error {
				_, err := coordinator.UpdateProtectedFeedback(before.Settings().Revision, "custom-review", policyTestNow)
				return err
			},
			verify: func(t *testing.T, before, after Snapshot) {
				assertCoordinatorRevisions(t, before, after, 1, 1, 0, 0)
			},
		},
		{
			name: "native project activation",
			mutate: func(coordinator *Coordinator, before Snapshot) error {
				_, err := coordinator.SetProject(before.TaskControl().Revision, "project-network", true, policyTestNow)
				return err
			},
			verify: func(t *testing.T, before, after Snapshot) {
				assertCoordinatorRevisions(t, before, after, 1, 0, 0, 1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := mustCoordinator(t, mustCreateCoordinatorStore(t), func() bool { return false })
			before := coordinator.Snapshot()
			if err := test.mutate(coordinator, before); err != nil {
				t.Fatal(err)
			}
			test.verify(t, before, coordinator.Snapshot())
		})
	}
}

func TestCoordinatorPreservesStoreConflictErrors(t *testing.T) {
	store := mustCreateCoordinatorStore(t)
	coordinator := mustCoordinator(t, store, func() bool { return false })
	current := coordinator.Snapshot()
	configuration := SettingsView(current)
	registry := RegistryView(current)
	definition, _ := configuration.Workflow("custom-review")

	tests := []struct {
		name string
		err  error
		call func() error
	}{
		{name: "settings", err: ErrSettingsConflict, call: func() error {
			_, err := coordinator.UpdateSettings(configuration.Revision-1, configuration.Agents, configuration.Runtime, policyTestNow)
			return err
		}},
		{name: "registry settings dependency", err: ErrSettingsConflict, call: func() error {
			_, err := coordinator.UpdateRegistry(registry.Revision, configuration.Revision-1, registry, policyTestNow)
			return err
		}},
		{name: "registry", err: ErrRegistryConflict, call: func() error {
			candidate := registry.Clone()
			candidate.Revision--
			_, err := coordinator.UpdateRegistry(candidate.Revision, configuration.Revision, candidate, policyTestNow)
			return err
		}},
		{name: "workflow", err: ErrWorkflowConflict, call: func() error {
			definition.Revision--
			_, err := coordinator.PublishWorkflow(configuration.Revision, definition.Revision, definition, policyTestNow)
			return err
		}},
		{name: "task control", err: ErrTaskControlConflict, call: func() error {
			_, err := coordinator.SetProject(current.TaskControl().Revision-1, "project-network", true, policyTestNow)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
		})
	}
}

func mustCreateCoordinatorStore(t *testing.T) *Store {
	t.Helper()
	model := mustConvertSources(t, populatedSources()).Model()
	model.Workflows = append(model.Workflows, Workflow{
		ID: "deletable", Revision: 1, Name: "Deletable", Enabled: false,
		Markdown: "# Deletable\n", UpdatedAt: policyTestNow,
	})
	snapshot, err := NewSnapshot(model)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Create(t.TempDir()+"/policy.json", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustCoordinator(t *testing.T, store *Store, pending PendingAdmission) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(store, pending)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func assertBlocked(t *testing.T, channel <-chan struct{}, message string) {
	t.Helper()
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-channel:
		t.Fatal(message)
	case <-timer.C:
	}
}

func assertCoordinatorRevisions(t *testing.T, before, after Snapshot, generation, settingsRevision, registryRevision, taskRevision uint64) {
	t.Helper()
	if after.Generation() != before.Generation()+generation ||
		after.Settings().Revision != before.Settings().Revision+settingsRevision ||
		after.Registry().Revision != before.Registry().Revision+registryRevision ||
		after.TaskControl().Revision != before.TaskControl().Revision+taskRevision {
		t.Fatalf("revisions = generation %d, settings %d, registry %d, task %d", after.Generation(), after.Settings().Revision, after.Registry().Revision, after.TaskControl().Revision)
	}
}
