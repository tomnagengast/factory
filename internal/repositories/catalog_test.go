package repositories

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

func TestCatalogResolvesExactProjectAndTaskIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	state, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, []SetupSource{succeededSetup(root, "factory", now)})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	record, err := catalog.ResolveProject(ProjectMetadata{
		ProjectID: "project-factory", ProjectName: "Factory", Repository: "https://github.com/TOMNAGENGAST/FACTORY.git",
		LocalPath: filepath.Join(root, "projects", "factory"),
	})
	if err != nil || record.Repository != "tomnagengast/factory" {
		t.Fatalf("ResolveProject = %#v, %v", record, err)
	}

	linear := taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-47", Identifier: "ENG-47"}
	project := ProjectMetadata{
		ProjectID: record.Project.ID, ProjectName: record.Project.Name,
		Repository: record.Repository, LocalPath: record.LocalPath,
	}
	if resolved, err := catalog.ResolveTask(TaskLookup{Ref: linear, Project: &project}); err != nil || resolved.Repository != record.Repository {
		t.Fatalf("Linear ResolveTask = %#v, %v", resolved, err)
	}
	factory := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"}
	route := record.Route()
	if resolved, err := catalog.ResolveTask(TaskLookup{Ref: factory, ProjectID: record.Project.ID, Route: &route}); err != nil || resolved.Repository != record.Repository {
		t.Fatalf("Factory ResolveTask = %#v, %v", resolved, err)
	}
}

func TestCatalogTaskLookupFailsClosedWithoutFallback(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now().UTC()
	state, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, []SetupSource{succeededSetup(root, "factory", now)})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := catalog.Record("tomnagengast/factory")
	factory := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"}
	validRoute := record.Route()

	tests := []struct {
		name   string
		lookup TaskLookup
	}{
		{name: "missing project", lookup: TaskLookup{Ref: factory, Route: &validRoute}},
		{name: "unknown project", lookup: TaskLookup{Ref: factory, ProjectID: "project-other", Route: &validRoute}},
		{name: "missing pinned route", lookup: TaskLookup{Ref: factory, ProjectID: record.Project.ID}},
		{name: "noncanonical task", lookup: TaskLookup{Ref: taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "eng-47", Identifier: "eng-47"}, ProjectID: record.Project.ID}},
		{name: "linear project ID only", lookup: TaskLookup{Ref: taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-47", Identifier: "ENG-47"}, ProjectID: record.Project.ID}},
		{name: "linear pinned route", lookup: TaskLookup{Ref: taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-47", Identifier: "ENG-47"}, Project: &ProjectMetadata{ProjectID: record.Project.ID, ProjectName: record.Project.Name, Repository: record.Repository, LocalPath: record.LocalPath}, Route: &validRoute}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := catalog.ResolveTask(test.lookup)
			assertPermanent(t, err)
		})
	}

	routeMutations := []struct {
		name   string
		mutate func(*Route)
	}{
		{name: "project", mutate: func(value *Route) { value.ProjectID = "project-other" }},
		{name: "repository", mutate: func(value *Route) { value.Repository = "tomnagengast/other" }},
		{name: "origin", mutate: func(value *Route) { value.Origin = "git@github.com:tomnagengast/other.git" }},
		{name: "path", mutate: func(value *Route) { value.ManagedPath = filepath.Join(root, "managed", "other") }},
		{name: "root", mutate: func(value *Route) { value.ManagedRoot = filepath.Join(root, "other") }},
		{name: "branch", mutate: func(value *Route) { value.DefaultBranch = "release" }},
		{name: "bootstrap", mutate: func(value *Route) { value.Bootstrap = !value.Bootstrap }},
		{name: "cloud", mutate: func(value *Route) { value.CloudURL = "https://other.nags.cloud" }},
	}
	for _, test := range routeMutations {
		t.Run("stale "+test.name, func(t *testing.T) {
			route := validRoute
			test.mutate(&route)
			_, err := catalog.ResolveTask(TaskLookup{Ref: factory, ProjectID: record.Project.ID, Route: &route})
			assertPermanent(t, err)
		})
	}
}

func TestCatalogExposesChoicesLaunchAndCompletionFromOneRecord(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now().UTC()
	compiled := []CompiledSource{compiledSource(root, "factory"), compiledSource(root, "network")}
	setups := []SetupSource{succeededSetup(root, "network", now), succeededSetup(root, "factory", now)}
	state, err := ConvertSources(compiled, setups)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	choices := catalog.Choices()
	wantChoices := []Choice{
		{ProjectID: "project-factory", ProjectName: "Factory", Repository: "tomnagengast/factory"},
		{ProjectID: "project-network", ProjectName: "Network", Repository: "tomnagengast/network"},
	}
	if !reflect.DeepEqual(choices, wantChoices) {
		t.Fatalf("choices = %#v", choices)
	}
	launch := catalog.LaunchConfigs()
	completion := catalog.CompletionIdentities()
	if len(launch) != 2 || len(completion) != 2 || launch[0].Repository != completion[0].Repository {
		t.Fatalf("launch = %#v, completion = %#v", launch, completion)
	}
	if launch[0].Origin != "git@github.com:tomnagengast/factory.git" || launch[0].Path != filepath.Join(root, "managed", "factory") {
		t.Fatalf("factory launch = %#v", launch[0])
	}
	if got := completion[0].RemoteURLs; !reflect.DeepEqual(got, []string{
		"git@github.com:tomnagengast/factory.git",
		"https://github.com/tomnagengast/factory",
		"https://github.com/tomnagengast/factory.git",
	}) {
		t.Fatalf("completion origins = %#v", got)
	}
	if !completion[0].Deployment.Required() || completion[0].Path != launch[0].Path || completion[0].DefaultBranch != launch[0].DefaultBranch {
		t.Fatalf("completion identity = %#v", completion[0])
	}
	selectedLaunch, err := catalog.LaunchConfig("tomnagengast/factory")
	if err != nil || selectedLaunch != launch[0] {
		t.Fatalf("selected launch = %#v, %v", selectedLaunch, err)
	}
	selectedCompletion, err := catalog.CompletionIdentity("tomnagengast/factory")
	if err != nil || !reflect.DeepEqual(selectedCompletion, completion[0]) {
		t.Fatalf("selected completion = %#v, %v", selectedCompletion, err)
	}
}

func TestCatalogChoicesAndTaskLookupRequireSuccessfulProviderCoordination(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now().UTC()
	pending := succeededSetup(root, "factory", now)
	pending.State = SetupStatePending
	pending.ProviderCoordinated = false
	awaiting := SetupSource{
		ProjectID: "project-awaiting", ProjectName: "Awaiting", State: SetupStateAwaitingMetadata,
		CreatedAt: now, UpdatedAt: now,
	}
	state, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, []SetupSource{pending, awaiting})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	if choices := catalog.Choices(); len(choices) != 0 {
		t.Fatalf("choices = %#v", choices)
	}
	if snapshot := catalog.SetupSnapshot(); snapshot != (SetupSnapshot{Total: 2, AwaitingMetadata: 1, Pending: 1}) {
		t.Fatalf("setup snapshot = %#v", snapshot)
	}
	ref := taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-47", Identifier: "ENG-47"}
	project := ProjectMetadata{
		ProjectID: pending.ProjectID, ProjectName: pending.ProjectName,
		Repository: pending.Repository, LocalPath: pending.LocalPath,
	}
	_, err = catalog.ResolveTask(TaskLookup{Ref: ref, Project: &project})
	assertPermanent(t, err)
}

func TestCatalogReplaceIsAtomicOnConflict(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initial, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(initial)
	if err != nil {
		t.Fatal(err)
	}
	broken := initial.Clone()
	duplicate := broken.Records[0]
	duplicate.App = "other"
	broken.Records = append(broken.Records, duplicate)
	if err := replaceCatalog(catalog, broken); err == nil {
		t.Fatal("Replace accepted duplicate repository")
	}
	if snapshot := catalog.Snapshot(); len(snapshot.Records) != 1 || snapshot.Records[0].Repository != "tomnagengast/factory" {
		t.Fatalf("catalog changed after rejected replace: %#v", snapshot)
	}
}

func TestCatalogReplacePreservesAdmittedIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	initial, err := ConvertSources(
		[]CompiledSource{compiledSource(root, "factory"), compiledSource(root, "network")},
		[]SetupSource{succeededSetup(root, "factory", now)},
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*SourceState)
	}{
		{name: "deletion", mutate: func(state *SourceState) {
			record := &state.Records[0]
			record.Project = ProjectIdentity{}
			record.Setup = Setup{State: SetupStateCompiled}
		}},
		{name: "reassignment", mutate: func(state *SourceState) {
			admitted := state.Records[0]
			state.Records[0].Project = ProjectIdentity{}
			state.Records[0].Setup = Setup{State: SetupStateCompiled}
			state.Records[1].Project = admitted.Project
			state.Records[1].Setup = admitted.Setup
		}},
		{name: "repository", mutate: func(state *SourceState) {
			state.Records[0].Repository = "tomnagengast/renamed"
			state.Records[0].Origin = "git@github.com:tomnagengast/renamed.git"
		}},
		{name: "local path", mutate: func(state *SourceState) {
			state.Records[0].LocalPath = filepath.Join(root, "projects", "other")
		}},
		{name: "managed path", mutate: func(state *SourceState) {
			state.Records[0].ManagedPath = filepath.Join(root, "managed", "other")
		}},
		{name: "managed root", mutate: func(state *SourceState) {
			state.Records[0].ManagedRoot = root
		}},
		{name: "managed policy", mutate: func(state *SourceState) {
			state.Records[0].Bootstrap = true
		}},
		{name: "completion app", mutate: func(state *SourceState) {
			state.Records[0].App = "renamed"
		}},
		{name: "completion branch", mutate: func(state *SourceState) {
			state.Records[0].DefaultBranch = "release"
		}},
		{name: "completion deployment", mutate: func(state *SourceState) {
			state.Records[0].Deployment.HealthURL = "http://127.0.0.1:9090/healthz"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := NewCatalog(initial)
			if err != nil {
				t.Fatal(err)
			}
			candidate := initial.Clone()
			test.mutate(&candidate)
			if _, err := NewCatalog(candidate); err != nil {
				t.Fatalf("candidate is independently invalid: %v", err)
			}
			if err := replaceCatalog(catalog, candidate); err == nil {
				t.Fatal("Replace accepted an admitted identity change")
			}
			if got := catalog.Snapshot(); !reflect.DeepEqual(got, initial) {
				t.Fatalf("catalog changed after rejected replace: %#v", got)
			}
		})
	}
}

func TestCatalogReplacePreservesCompiledBaselineBeforeAndAfterProjectOverlay(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	compiled := []CompiledSource{compiledSource(root, "factory"), compiledSource(root, "network")}

	stages := []struct {
		name   string
		setups []SetupSource
	}{
		{name: "before project overlay"},
		{name: "after project overlay", setups: []SetupSource{succeededSetup(root, "network", now)}},
	}
	mutations := []struct {
		name   string
		mutate func(*SourceState)
	}{
		{name: "removal", mutate: func(state *SourceState) {
			for index := range state.Records {
				if state.Records[index].Repository == "tomnagengast/network" {
					state.Records = append(state.Records[:index], state.Records[index+1:]...)
					return
				}
			}
		}},
		{name: "compiled baseline addition", mutate: func(state *SourceState) {
			record, err := convertCompiled(compiledSource(root, "cellar"))
			if err != nil {
				t.Fatal(err)
			}
			state.Records = append(state.Records, record)
		}},
		{name: "provenance", mutate: func(state *SourceState) {
			record := recordForMutation(t, state, "tomnagengast/network")
			if record.Project.IsZero() {
				record.Project = ProjectIdentity{ID: "project-network", Name: "Network"}
				record.Setup = Setup{
					State: SetupStateSucceeded, CreatedAt: now, UpdatedAt: now,
					ProvisionedAt: timePointer(now), ProviderCoordinated: true,
				}
			}
			record.Provenance = ProvenanceProject
		}},
		{name: "app", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").App = "network-next"
		}},
		{name: "repository and origin", mutate: func(state *SourceState) {
			record := recordForMutation(t, state, "tomnagengast/network")
			record.Repository = "tomnagengast/network-next"
			record.Origin = "git@github.com:tomnagengast/network-next.git"
		}},
		{name: "local path", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").LocalPath = filepath.Join(root, "local-next", "network")
		}},
		{name: "managed path", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").ManagedPath = filepath.Join(root, "managed", "network-next")
		}},
		{name: "managed root", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").ManagedRoot = root
		}},
		{name: "branch", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").DefaultBranch = "release"
		}},
		{name: "bootstrap", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").Bootstrap = true
		}},
		{name: "deployment receipt", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").Deployment.ReceiptPath = filepath.Join(root, "network", "next-current.json")
		}},
		{name: "deployment pending receipt", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").Deployment.PendingReceiptPath = filepath.Join(root, "network", "next-pending.json")
		}},
		{name: "deployment health", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").Deployment.HealthURL = "http://127.0.0.1:8090/healthz"
		}},
		{name: "deployment source path", mutate: func(state *SourceState) {
			recordForMutation(t, state, "tomnagengast/network").Deployment.SourcePath = "apps/network"
		}},
	}

	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			initial, err := ConvertSources(compiled, stage.setups)
			if err != nil {
				t.Fatal(err)
			}
			for _, mutation := range mutations {
				t.Run(mutation.name, func(t *testing.T) {
					catalog, err := NewCatalog(initial)
					if err != nil {
						t.Fatal(err)
					}
					candidate := initial.Clone()
					mutation.mutate(&candidate)
					if _, err := NewCatalog(candidate); err != nil {
						t.Fatalf("candidate is independently invalid: %v", err)
					}
					if err := replaceCatalog(catalog, candidate); err == nil {
						t.Fatal("Replace accepted a compiled baseline change")
					} else {
						assertPermanent(t, err)
					}
					if got := catalog.Snapshot(); !reflect.DeepEqual(got, initial) {
						t.Fatalf("catalog changed after rejected replace: %#v", got)
					}
				})
			}
		})
	}
}

func TestCatalogReplaceAllowsCompiledProjectSetupOverlay(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	compiled := []CompiledSource{compiledSource(root, "factory"), compiledSource(root, "network")}
	initial, err := ConvertSources(compiled, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(initial)
	if err != nil {
		t.Fatal(err)
	}

	overlaid, err := ConvertSources(compiled, []SetupSource{succeededSetup(root, "network", now)})
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceCatalog(catalog, overlaid); err != nil {
		t.Fatalf("initial project overlay: %v", err)
	}

	candidate := catalog.Snapshot()
	record := recordForMutation(t, &candidate, "tomnagengast/network")
	record.Project.Name = "Network Platform"
	record.CloudURL = "https://network.nags.cloud"
	record.Setup.State = SetupStatePending
	record.Setup.ProviderCoordinated = false
	record.Setup.UpdatedAt = now.Add(time.Minute)
	if err := replaceCatalog(catalog, candidate); err != nil {
		t.Fatalf("project name, setup, and Cloud overlay: %v", err)
	}

	invalid := catalog.Snapshot()
	record = recordForMutation(t, &invalid, "tomnagengast/network")
	record.CloudURL = "https://network-next.nags.cloud"
	record.Setup.State = SetupStateSucceeded
	record.Setup.ProviderCoordinated = true
	if err := replaceCatalog(catalog, invalid); err == nil {
		t.Fatal("Replace accepted a coordinated Cloud URL change")
	} else {
		assertPermanent(t, err)
	}
}

func TestCatalogReplaceRejectsCompiledCloudChangeBeforeProjectOverlay(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initial, err := ConvertSources(
		[]CompiledSource{compiledSource(root, "factory"), compiledSource(root, "network")}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(initial)
	if err != nil {
		t.Fatal(err)
	}
	candidate := initial.Clone()
	recordForMutation(t, &candidate, "tomnagengast/network").CloudURL = "https://network.nags.cloud"
	if _, err := NewCatalog(candidate); err != nil {
		t.Fatalf("candidate is independently invalid: %v", err)
	}
	if err := replaceCatalog(catalog, candidate); err == nil {
		t.Fatal("Replace accepted a Cloud URL change without a project overlay")
	} else {
		assertPermanent(t, err)
	}
}

func TestCatalogReplaceCloudChangeClearsProjectCoordination(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	initial, err := ConvertSources(
		[]CompiledSource{compiledSource(root, "factory")},
		[]SetupSource{managedSetup(root, "cellar", now)},
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(initial)
	if err != nil {
		t.Fatal(err)
	}

	coordinated := initial.Clone()
	recordForMutation(t, &coordinated, "tomnagengast/cellar").CloudURL = "https://cellar.nags.cloud"
	if err := replaceCatalog(catalog, coordinated); err == nil {
		t.Fatal("Replace accepted a coordinated project Cloud URL change")
	} else {
		assertPermanent(t, err)
	}

	pending := initial.Clone()
	record := recordForMutation(t, &pending, "tomnagengast/cellar")
	record.CloudURL = "https://cellar.nags.cloud"
	record.Setup.State = SetupStatePending
	record.Setup.ProviderCoordinated = false
	record.Setup.UpdatedAt = now.Add(time.Minute)
	if err := replaceCatalog(catalog, pending); err != nil {
		t.Fatalf("Replace project Cloud URL recoordination: %v", err)
	}
	if got, found := catalog.Record("tomnagengast/cellar"); !found || got.Routable() || got.CloudURL != "https://cellar.nags.cloud" {
		t.Fatalf("recoordination record = %#v, found=%t", got, found)
	}
}

func TestCatalogReplaceAllowsLegacySetupLifecycleProgression(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	created := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	pending := succeededSetup(root, "factory", created)
	pending.State = SetupStatePending
	pending.ProvisionedAt = nil
	pending.ProviderCoordinated = false
	state, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, []SetupSource{pending})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}

	retryAt := created.Add(2 * time.Minute)
	provisionedAt := created.Add(3 * time.Minute)
	transitions := []func(*Record){
		func(record *Record) {
			record.Setup.State = SetupStateRunning
			record.Setup.Attempts = 1
			record.Setup.UpdatedAt = created.Add(time.Minute)
		},
		func(record *Record) {
			record.Setup.State = SetupStateFailed
			record.Setup.LastError = "provider unavailable"
			record.Setup.NextAttemptAt = timePointer(retryAt)
			record.Setup.UpdatedAt = created.Add(2 * time.Minute)
		},
		func(record *Record) {
			record.Setup.State = SetupStateRunning
			record.Setup.Attempts = 2
			record.Setup.LastError = ""
			record.Setup.NextAttemptAt = nil
			record.Setup.UpdatedAt = created.Add(3 * time.Minute)
		},
		func(record *Record) {
			record.Setup.State = SetupStateSucceeded
			record.Setup.ProvisionedAt = timePointer(provisionedAt)
			record.Setup.ProviderCoordinated = true
			record.Setup.UpdatedAt = created.Add(4 * time.Minute)
		},
		func(record *Record) {
			record.CloudURL = "https://factory.nags.cloud"
			record.Setup.State = SetupStatePending
			record.Setup.ProviderCoordinated = false
			record.Setup.UpdatedAt = created.Add(5 * time.Minute)
		},
	}
	for index, transition := range transitions {
		candidate := catalog.Snapshot()
		transition(&candidate.Records[0])
		if err := replaceCatalog(catalog, candidate); err != nil {
			t.Fatalf("transition %d: %v", index, err)
		}
	}
}

func TestCatalogReplacePreservesAndAdmitsAwaitingProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	compiled := []CompiledSource{compiledSource(root, "factory")}
	awaiting := SetupSource{
		ProjectID: "project-cellar", ProjectName: "Cellar", State: SetupStateAwaitingMetadata,
		CreatedAt: now, UpdatedAt: now,
	}
	initial, err := ConvertSources(compiled, []SetupSource{awaiting})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(initial)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := ConvertSources(compiled, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceCatalog(catalog, deleted); err == nil {
		t.Fatal("Replace deleted an awaiting project")
	}

	admitted := managedSetup(root, "cellar", now)
	admitted.State = SetupStatePending
	admitted.ProvisionedAt = nil
	admitted.ProviderCoordinated = false
	candidate, err := ConvertSources(compiled, []SetupSource{admitted})
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceCatalog(catalog, candidate); err != nil {
		t.Fatalf("Replace awaiting project admission: %v", err)
	}
	if got, found := catalog.Record("tomnagengast/cellar"); !found || got.Project.ID != awaiting.ProjectID || got.Setup.State != SetupStatePending {
		t.Fatalf("admitted project = %#v, %t", got, found)
	}
}

func TestCatalogResolvesInitialFactoryRouteByProjectID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now().UTC()
	state, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, []SetupSource{succeededSetup(root, "factory", now)})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	record, err := catalog.ResolveProjectID("project-factory")
	if err != nil || record.Route().ProjectID != "project-factory" || record.Repository != "tomnagengast/factory" {
		t.Fatalf("ResolveProjectID = %#v, %v", record, err)
	}

	for _, projectID := range []string{"", " project-factory", "project-missing"} {
		if _, err := catalog.ResolveProjectID(projectID); err == nil {
			t.Fatalf("ResolveProjectID(%q) succeeded", projectID)
		} else {
			assertPermanent(t, err)
		}
	}
}

func TestResolveProjectRejectsConflictingMetadataPermanently(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now().UTC()
	state, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, []SetupSource{succeededSetup(root, "factory", now)})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	base := ProjectMetadata{
		ProjectID: "project-factory", ProjectName: "Factory", Repository: "tomnagengast/factory",
		LocalPath: filepath.Join(root, "projects", "factory"),
	}
	tests := []struct {
		name   string
		mutate func(*ProjectMetadata)
	}{
		{name: "unknown project", mutate: func(value *ProjectMetadata) { value.ProjectID = "project-other" }},
		{name: "renamed project", mutate: func(value *ProjectMetadata) { value.ProjectName = "Other" }},
		{name: "repository", mutate: func(value *ProjectMetadata) { value.Repository = "tomnagengast/other" }},
		{name: "path", mutate: func(value *ProjectMetadata) { value.LocalPath = filepath.Join(root, "other") }},
		{name: "origin query", mutate: func(value *ProjectMetadata) { value.Repository = "https://github.com/tomnagengast/factory?ref=main" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			_, err := catalog.ResolveProject(candidate)
			assertPermanent(t, err)
		})
	}
}

func assertPermanent(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("operation succeeded")
	}
	var classified interface{ Permanent() bool }
	if !errors.As(err, &classified) || !classified.Permanent() {
		t.Fatalf("error is not permanent: %v", err)
	}
}

func replaceCatalog(catalog *Catalog, state SourceState) error {
	state.Generation = catalog.Snapshot().Generation + 1
	return catalog.replace(state)
}

func recordForMutation(t *testing.T, state *SourceState, repository string) *Record {
	t.Helper()
	for index := range state.Records {
		if state.Records[index].Repository == repository {
			return &state.Records[index]
		}
	}
	t.Fatalf("record %s not found in %#v", repository, state.Records)
	return nil
}

func TestSourceStateSnapshotIsIndependent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	provisioned := time.Now().UTC()
	state, err := ConvertSources(
		[]CompiledSource{compiledSource(root, "factory")},
		[]SetupSource{succeededSetup(root, "factory", provisioned)},
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := catalog.Snapshot()
	snapshot.Records[0].Repository = "tomnagengast/changed"
	changed := provisioned.Add(time.Hour)
	*snapshot.Records[0].Setup.ProvisionedAt = changed
	record, found := catalog.Record("tomnagengast/factory")
	if !found {
		t.Fatal("factory record was not found")
	}
	*record.Setup.ProvisionedAt = changed
	current := catalog.Snapshot()
	if current.Records[0].Repository != "tomnagengast/factory" || !current.Records[0].Setup.ProvisionedAt.Equal(provisioned) {
		t.Fatalf("catalog snapshot was aliased: %#v", current)
	}
}

func TestRecordLookupRequiresExplicitRepositoryIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	state, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.Record(""); found {
		t.Fatal("empty repository resolved through a fallback")
	}
	if _, found := catalog.Record("tomnagengast/missing"); found {
		t.Fatal("unknown repository resolved through a fallback")
	}
	if _, err := catalog.LaunchConfig(""); err == nil {
		t.Fatal("empty repository received launch configuration")
	}
	if _, err := catalog.CompletionIdentity("tomnagengast/missing"); err == nil {
		t.Fatal("unknown repository received completion identity")
	}
	if record, found := catalog.Record("https://github.com/TOMNAGENGAST/FACTORY.git"); !found || !strings.EqualFold(record.Repository, "tomnagengast/factory") {
		t.Fatalf("canonical record lookup = %#v, %t", record, found)
	}
}
