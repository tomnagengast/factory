package repositories

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConvertSourcesPreservesCompiledAndAdmittedRepositoryIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	compiled := []CompiledSource{
		compiledSource(root, "factory"),
		{
			App: "network", Repository: "TOMNAGENGAST/NETWORK", RepoURL: "https://github.com/TomNagengast/Network.git",
			RepoPath: filepath.Join(root, "workspace", "network"), ManagedRoot: filepath.Join(root, "workspace"),
			ProjectPath: filepath.Join(root, "t9", "network"), BaseBranch: "main", SourcePath: "apps/network",
			ReceiptPath:    filepath.Join(root, "network", "current.json"),
			PendingReceipt: filepath.Join(root, "network", "pending.json"),
			HealthURL:      "http://127.0.0.1:8090/healthz",
		},
	}
	setups := []SetupSource{
		succeededSetup(root, "factory", now),
		{
			ProjectID: "project-network", ProjectName: "Network", Repository: "tomnagengast/network",
			RepoURL: "git@github.com:tomnagengast/network.git", LocalPath: filepath.Join(root, "t9", "network"),
			ManagedRoot: filepath.Join(root, "t9"), CloudURL: "https://network.nags.cloud", BaseBranch: "main",
			State: SetupStateSucceeded, CreatedAt: now, UpdatedAt: now, ProvisionedAt: timePointer(now), ProviderCoordinated: true,
		},
		{
			ProjectID: "project-cellar", ProjectName: "Cellar", Repository: "tomnagengast/cellar",
			RepoURL: "git@github.com:tomnagengast/cellar.git", LocalPath: filepath.Join(root, "repos", "cellar"),
			ManagedRoot: filepath.Join(root, "repos"), BaseBranch: "main", Bootstrap: true, Managed: true,
			State: SetupStatePending, CreatedAt: now, UpdatedAt: now,
		},
		{
			ProjectID: "project-awaiting", ProjectName: "Awaiting", State: SetupStateAwaitingMetadata,
			CreatedAt: now, UpdatedAt: now,
		},
	}

	state, err := ConvertSources(compiled, setups)
	if err != nil {
		t.Fatalf("ConvertSources: %v", err)
	}
	if len(state.Records) != 3 || len(state.Awaiting) != 1 {
		t.Fatalf("state = %#v", state)
	}
	network := findRecord(t, state.Records, "tomnagengast/network")
	if network.Origin != "git@github.com:tomnagengast/network.git" || network.LocalPath != filepath.Join(root, "t9", "network") ||
		network.ManagedPath != filepath.Join(root, "workspace", "network") || network.ManagedRoot != filepath.Join(root, "workspace") {
		t.Fatalf("network paths and origin = %#v", network)
	}
	if network.CloudURL != "https://network.nags.cloud" || network.Project.ID != "project-network" || !network.Routable() {
		t.Fatalf("network admission = %#v", network)
	}
	if !network.Deployment.Required() || network.Deployment.SourcePath != "apps/network" {
		t.Fatalf("network deployment = %#v", network.Deployment)
	}
	cellar := findRecord(t, state.Records, "tomnagengast/cellar")
	if cellar.LocalPath != cellar.ManagedPath || !cellar.Bootstrap || cellar.Setup.State != SetupStatePending || cellar.Routable() {
		t.Fatalf("cellar = %#v", cellar)
	}
	if state.Awaiting[0].Project.ID != "project-awaiting" || state.Awaiting[0].Setup.State != SetupStateAwaitingMetadata {
		t.Fatalf("awaiting = %#v", state.Awaiting)
	}
}

func TestConvertSourcesRejectsConflictingCompiledAndSetupState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	compiled := compiledSource(root, "factory")
	validSetup := succeededSetup(root, "factory", now)

	tests := []struct {
		name    string
		compile func(*CompiledSource)
		setup   func(*SetupSource)
		want    string
	}{
		{name: "origin mismatch", setup: func(value *SetupSource) { value.RepoURL = "git@github.com:tomnagengast/other.git" }, want: "conflicts with origin"},
		{name: "equivalent setup origin change", setup: func(value *SetupSource) { value.RepoURL = "https://github.com/tomnagengast/factory" }, want: "conflicts with origin"},
		{name: "compiled local path mismatch", setup: func(value *SetupSource) { value.LocalPath = filepath.Join(root, "other") }, want: "conflicts with compiled"},
		{name: "unclean setup path", setup: func(value *SetupSource) { value.LocalPath = root + "/projects/nested/../factory" }, want: "invalid path"},
		{name: "compiled branch mismatch", setup: func(value *SetupSource) { value.BaseBranch = "release" }, want: "conflicts with compiled"},
		{name: "compiled becomes managed", setup: func(value *SetupSource) { value.Managed, value.Bootstrap = true, true }, want: "cannot be managed"},
		{name: "orphan existing", compile: func(value *CompiledSource) {
			value.Repository, value.RepoURL = "tomnagengast/other", "git@github.com:tomnagengast/other.git"
		}, want: "no longer compiled"},
		{name: "partial deployment", compile: func(value *CompiledSource) { value.PendingReceipt = "" }, want: "configured together"},
		{name: "origin query", compile: func(value *CompiledSource) { value.RepoURL += "?ref=main" }, want: "compiled origin"},
		{name: "managed root escape", compile: func(value *CompiledSource) { value.ManagedRoot = filepath.Join(root, "other") }, want: "managed path"},
		{name: "source traversal", compile: func(value *CompiledSource) { value.SourcePath = "../factory" }, want: "source path"},
		{name: "invalid cloud", setup: func(value *SetupSource) { value.CloudURL = "https://api.factory.nags.cloud" }, want: "Cloud URL"},
		{name: "noncanonical cloud", setup: func(value *SetupSource) { value.CloudURL = "https://FACTORY.nags.cloud/" }, want: "not canonical"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateCompiled := compiled
			candidateSetup := validSetup
			if test.compile != nil {
				test.compile(&candidateCompiled)
			}
			if test.setup != nil {
				test.setup(&candidateSetup)
			}
			_, err := ConvertSources([]CompiledSource{candidateCompiled}, []SetupSource{candidateSetup})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestConvertSourcesEnforcesStateSpecificSetupLifecycle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	retryAt := now.Add(time.Minute)

	accepted := []struct {
		name  string
		setup SetupSource
	}{
		{name: "new pending", setup: func() SetupSource {
			value := succeededSetup(root, "factory", now)
			value.State, value.ProvisionedAt, value.ProviderCoordinated = SetupStatePending, nil, false
			return value
		}()},
		{name: "pending provider requeue after provisioning", setup: func() SetupSource {
			value := succeededSetup(root, "factory", now)
			value.State, value.Attempts, value.ProviderCoordinated = SetupStatePending, 1, false
			return value
		}()},
		{name: "running", setup: func() SetupSource {
			value := succeededSetup(root, "factory", now)
			value.State, value.Attempts, value.ProvisionedAt, value.ProviderCoordinated = SetupStateRunning, 1, nil, false
			return value
		}()},
		{name: "legacy uncoordinated success", setup: func() SetupSource {
			value := succeededSetup(root, "factory", now)
			value.ProviderCoordinated = false
			return value
		}()},
		{name: "failed after prior provisioning", setup: func() SetupSource {
			value := succeededSetup(root, "factory", now)
			value.State, value.Attempts, value.LastError, value.NextAttemptAt = SetupStateFailed, 1, "provider unavailable", timePointer(retryAt)
			return value
		}()},
	}
	for _, test := range accepted {
		t.Run("accepts "+test.name, func(t *testing.T) {
			state, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, []SetupSource{test.setup})
			if err != nil {
				t.Fatalf("ConvertSources: %v", err)
			}
			if test.setup.State == SetupStateSucceeded && !test.setup.ProviderCoordinated && state.Records[0].Routable() {
				t.Fatal("uncoordinated legacy success became routable")
			}
		})
	}

	valid := succeededSetup(root, "factory", now)
	rejected := []struct {
		name   string
		mutate func(*SetupSource)
	}{
		{name: "pending error", mutate: func(value *SetupSource) {
			value.State, value.LastError, value.ProvisionedAt, value.ProviderCoordinated = SetupStatePending, "stale", nil, false
		}},
		{name: "pending retry", mutate: func(value *SetupSource) {
			value.State, value.NextAttemptAt, value.ProvisionedAt, value.ProviderCoordinated = SetupStatePending, timePointer(retryAt), nil, false
		}},
		{name: "running without attempt", mutate: func(value *SetupSource) {
			value.State, value.ProvisionedAt, value.ProviderCoordinated = SetupStateRunning, nil, false
		}},
		{name: "running error", mutate: func(value *SetupSource) {
			value.State, value.Attempts, value.LastError, value.ProvisionedAt, value.ProviderCoordinated = SetupStateRunning, 1, "stale", nil, false
		}},
		{name: "running retry", mutate: func(value *SetupSource) {
			value.State, value.Attempts, value.NextAttemptAt, value.ProvisionedAt, value.ProviderCoordinated = SetupStateRunning, 1, timePointer(retryAt), nil, false
		}},
		{name: "succeeded error", mutate: func(value *SetupSource) { value.LastError = "stale" }},
		{name: "succeeded retry", mutate: func(value *SetupSource) { value.NextAttemptAt = timePointer(retryAt) }},
		{name: "succeeded without provisioning", mutate: func(value *SetupSource) { value.ProvisionedAt = nil }},
		{name: "failed without retry", mutate: func(value *SetupSource) {
			value.State, value.Attempts, value.LastError = SetupStateFailed, 1, "provider unavailable"
		}},
	}
	for _, test := range rejected {
		t.Run("rejects "+test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if _, err := ConvertSources([]CompiledSource{compiledSource(root, "factory")}, []SetupSource{candidate}); err == nil {
				t.Fatal("ConvertSources accepted contradictory lifecycle")
			}
		})
	}
}

func TestConvertSourcesRejectsCrossRecordConflicts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := compiledSource(root, "factory")

	t.Run("duplicate project", func(t *testing.T) {
		now := time.Now().UTC()
		one := succeededSetup(root, "factory", now)
		two := managedSetup(root, "cellar", now)
		two.ProjectID = one.ProjectID
		if _, err := ConvertSources([]CompiledSource{base}, []SetupSource{one, two}); err == nil || !strings.Contains(err.Error(), "project") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("nested managed path", func(t *testing.T) {
		other := compiledSource(root, "cellar")
		other.RepoPath = filepath.Join(base.RepoPath, "nested")
		other.ManagedRoot = filepath.Dir(base.RepoPath)
		if _, err := ConvertSources([]CompiledSource{base, other}, nil); err == nil || !strings.Contains(err.Error(), "managed paths") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("nested local path", func(t *testing.T) {
		other := compiledSource(root, "cellar")
		other.ProjectPath = filepath.Join(base.ProjectPath, "nested")
		if _, err := ConvertSources([]CompiledSource{base, other}, nil); err == nil || !strings.Contains(err.Error(), "local paths") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("duplicate app", func(t *testing.T) {
		other := compiledSource(root, "cellar")
		other.App = base.App
		if _, err := ConvertSources([]CompiledSource{base, other}, nil); err == nil || !strings.Contains(err.Error(), "share app") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestConvertRouteSourcePreservesExactPinnedIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	route, err := ConvertRouteSource(RouteSource{
		ProjectID: "project-factory", Repository: "tomnagengast/factory",
		RepositoryURL: "git@github.com:tomnagengast/factory.git", RepositoryPath: filepath.Join(root, "factory"),
		ManagedRoot: root, BaseBranch: "main", CloudURL: "https://factory.nags.cloud",
	})
	if err != nil {
		t.Fatalf("ConvertRouteSource: %v", err)
	}
	if route.Repository != "tomnagengast/factory" || route.Origin != "git@github.com:tomnagengast/factory.git" || route.CloudURL != "https://factory.nags.cloud" {
		t.Fatalf("route = %#v", route)
	}
	if _, err := ConvertRouteSource(RouteSource{
		ProjectID: "project-factory", Repository: "tomnagengast/factory",
		RepositoryURL: "git@github.com:tomnagengast/other.git", RepositoryPath: filepath.Join(root, "factory"),
		ManagedRoot: root, BaseBranch: "main",
	}); err == nil {
		t.Fatal("conflicting route succeeded")
	}
	if _, err := ConvertRouteSource(RouteSource{
		ProjectID: "project-factory", Repository: "tomnagengast/factory",
		RepositoryURL: "https://github.com/tomnagengast/factory", RepositoryPath: filepath.Join(root, "factory"),
		ManagedRoot: root, BaseBranch: "main",
	}); err == nil {
		t.Fatal("equivalent but changed pinned origin succeeded")
	}
	if _, err := ConvertRouteSource(RouteSource{
		ProjectID: "project-factory", Repository: "tomnagengast/factory",
		RepositoryURL: "git@github.com:tomnagengast/factory.git", RepositoryPath: root + "/nested/../factory",
		ManagedRoot: root, BaseBranch: "main",
	}); err == nil {
		t.Fatal("unclean pinned path succeeded")
	}
}

func compiledSource(root, name string) CompiledSource {
	return CompiledSource{
		App: name, Repository: "tomnagengast/" + name, RepoURL: "git@github.com:tomnagengast/" + name + ".git",
		RepoPath: filepath.Join(root, "managed", name), ManagedRoot: filepath.Join(root, "managed"),
		ProjectPath: filepath.Join(root, "projects", name), BaseBranch: "main",
		ReceiptPath: filepath.Join(root, name, "current.json"), PendingReceipt: filepath.Join(root, name, "pending.json"),
		HealthURL: "http://127.0.0.1/healthz",
	}
}

func succeededSetup(root, name string, now time.Time) SetupSource {
	return SetupSource{
		ProjectID: "project-" + name, ProjectName: strings.ToUpper(name[:1]) + name[1:], Repository: "tomnagengast/" + name,
		RepoURL: "git@github.com:tomnagengast/" + name + ".git", LocalPath: filepath.Join(root, "projects", name),
		ManagedRoot: filepath.Join(root, "projects"), BaseBranch: "main", State: SetupStateSucceeded,
		CreatedAt: now, UpdatedAt: now, ProvisionedAt: timePointer(now), ProviderCoordinated: true,
	}
}

func managedSetup(root, name string, now time.Time) SetupSource {
	return SetupSource{
		ProjectID: "project-" + name, ProjectName: strings.ToUpper(name[:1]) + name[1:], Repository: "tomnagengast/" + name,
		RepoURL: "git@github.com:tomnagengast/" + name + ".git", LocalPath: filepath.Join(root, "managed-dynamic", name),
		ManagedRoot: filepath.Join(root, "managed-dynamic"), BaseBranch: "main", Bootstrap: true, Managed: true,
		State: SetupStateSucceeded, CreatedAt: now, UpdatedAt: now, ProvisionedAt: timePointer(now), ProviderCoordinated: true,
	}
}

func findRecord(t *testing.T, records []Record, repository string) Record {
	t.Helper()
	for _, record := range records {
		if record.Repository == repository {
			return record
		}
	}
	t.Fatalf("record %s not found in %#v", repository, records)
	return Record{}
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}
