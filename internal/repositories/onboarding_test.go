package repositories

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreRecordsAndAdmitsAwaitingMetadataWithoutAliasing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := createOnboardingStore(t, root, []CompiledSource{compiledSource(root, "factory")}, nil)
	created := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	project := ProjectIdentity{ID: "project-cellar", Name: "Cellar"}

	awaiting, err := store.RecordAwaitingMetadata(project, created)
	if err != nil {
		t.Fatal(err)
	}
	if awaiting.Project != project || awaiting.Setup.State != SetupStateAwaitingMetadata {
		t.Fatalf("awaiting = %#v", awaiting)
	}
	if got := store.Snapshot(); got.Generation != 2 || len(got.Awaiting) != 1 {
		t.Fatalf("recorded state = %#v", got)
	}

	awaiting.Project.Name = "Mutated"
	if got := store.Snapshot().Awaiting[0].Project.Name; got != "Cellar" {
		t.Fatalf("returned awaiting project aliases store: %q", got)
	}
	if _, err := store.RecordAwaitingMetadata(project, created.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if generation := store.Snapshot().Generation; generation != 2 {
		t.Fatalf("exact awaiting no-op generation = %d", generation)
	}

	renamed := ProjectIdentity{ID: project.ID, Name: "Cellar Platform"}
	awaiting, err = store.RecordAwaitingMetadata(renamed, created.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !awaiting.Setup.CreatedAt.Equal(created) || !awaiting.Setup.UpdatedAt.Equal(created.Add(2*time.Minute)) {
		t.Fatalf("renamed awaiting lifecycle = %#v", awaiting.Setup)
	}

	admission := managedAdmission(root, "cellar")
	admission.Project = renamed
	result, err := store.AdmitProject(admission, created.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsProvision || result.Record.Setup.State != SetupStatePending ||
		!result.Record.Setup.CreatedAt.Equal(created) || result.Record.Provenance != ProvenanceProject {
		t.Fatalf("admission = %#v", result)
	}
	state := store.Snapshot()
	if state.Generation != 4 || len(state.Awaiting) != 0 || len(state.Records) != 2 {
		t.Fatalf("admitted state = %#v", state)
	}
	if _, err := store.RecordAwaitingMetadata(renamed, created.Add(4*time.Minute)); err == nil {
		t.Fatal("admitted project returned to awaiting metadata")
	} else {
		assertPermanent(t, err)
	}
	if _, err := store.AdmitProject(admission, created.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if generation := store.Snapshot().Generation; generation != 4 {
		t.Fatalf("exact admission no-op generation = %d", generation)
	}
}

func TestStoreAdmissionPreservesCompiledIdentityAndCoordinatesCloudChanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := createOnboardingStore(t, root, []CompiledSource{
		compiledSource(root, "factory"), compiledSource(root, "network"),
	}, nil)
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	admission := compiledAdmission(root, "factory")

	result, err := store.AdmitProject(admission, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsProvision || !result.Record.Routable() || result.Record.Provenance != ProvenanceCompiled {
		t.Fatalf("compiled admission = %#v", result)
	}
	baseline := store.Snapshot().Records[0]
	if baseline.Repository != "tomnagengast/factory" {
		t.Fatalf("unexpected canonical order = %#v", store.Snapshot().Records)
	}

	result.Record.Project.Name = "Aliased"
	*result.Record.Setup.ProvisionedAt = now.Add(time.Hour)
	current, _ := store.catalog.Record("tomnagengast/factory")
	if current.Project.Name != "Factory" || !current.Setup.ProvisionedAt.Equal(now) {
		t.Fatalf("admission result aliases catalog: %#v", current)
	}

	invalid := admission
	invalid.LocalPath = filepath.Join(root, "projects", "other")
	if _, err := store.AdmitProject(invalid, now.Add(time.Minute)); err == nil {
		t.Fatal("compiled path change succeeded")
	} else {
		assertPermanent(t, err)
	}
	if generation := store.Snapshot().Generation; generation != 2 {
		t.Fatalf("rejected identity change generation = %d", generation)
	}

	admission.Project.Name = "Factory Platform"
	if _, err := store.AdmitProject(admission, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	admission.CloudURL = "https://factory.nags.cloud"
	result, err = store.AdmitProject(admission, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsProvision || result.Record.Setup.State != SetupStatePending || result.Record.Setup.ProviderCoordinated || result.Record.Routable() {
		t.Fatalf("Cloud recoordination = %#v", result)
	}
	claimed, found, err := store.ClaimSetup(now.Add(4 * time.Minute))
	if err != nil || !found {
		t.Fatalf("claim Cloud setup = %#v, %t, %v", claimed, found, err)
	}
	if _, err := store.CompleteSetup(admission.Project.ID, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	admission.CloudURL = ""
	result, err = store.AdmitProject(admission, now.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsProvision || result.Record.CloudURL != "" || result.Record.Setup.ProviderCoordinated || result.Record.Setup.State != SetupStatePending {
		t.Fatalf("Cloud removal recoordination = %#v", result)
	}

	updated := store.Snapshot().Records[0]
	if !sameCompiledIdentity(baseline, updated) {
		t.Fatalf("compiled identity changed: before=%#v after=%#v", baseline, updated)
	}
	duplicate := compiledAdmission(root, "factory")
	duplicate.Project = ProjectIdentity{ID: "project-other", Name: "Other"}
	if _, err := store.AdmitProject(duplicate, now.Add(7*time.Minute)); err == nil {
		t.Fatal("second project acquired admitted repository")
	} else {
		assertPermanent(t, err)
	}
}

func TestStoreSetupLifecyclePreservesRetryAndProviderCoordination(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := createOnboardingStore(t, root, []CompiledSource{compiledSource(root, "factory")}, nil)
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	admission := managedAdmission(root, "cellar")
	result, err := store.AdmitProject(admission, now)
	if err != nil || !result.NeedsProvision {
		t.Fatalf("AdmitProject = %#v, %v", result, err)
	}

	if _, err := store.CompleteSetup(admission.Project.ID, now.Add(time.Second)); err == nil {
		t.Fatal("pending setup completed without a claim")
	} else {
		assertPermanent(t, err)
	}
	if generation := store.Snapshot().Generation; generation != 2 {
		t.Fatalf("rejected completion generation = %d", generation)
	}

	claimed, found, err := store.ClaimSetup(now.Add(time.Minute))
	if err != nil || !found || claimed.Setup.State != SetupStateRunning || claimed.Setup.Attempts != 1 {
		t.Fatalf("first claim = %#v, %t, %v", claimed, found, err)
	}
	retryAt := now.Add(10 * time.Minute)
	detail := "  " + strings.Repeat("x", 2050) + "  "
	failed, err := store.FailSetup(admission.Project.ID, detail, retryAt, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if failed.Setup.State != SetupStateFailed || len(failed.Setup.LastError) != 2048 || failed.Setup.NextAttemptAt == nil ||
		!failed.Setup.NextAttemptAt.Equal(retryAt) {
		t.Fatalf("failed setup = %#v", failed.Setup)
	}
	mutatedRetry := retryAt.Add(time.Hour)
	*failed.Setup.NextAttemptAt = mutatedRetry
	stored, _ := store.catalog.Record(admission.Repository)
	if !stored.Setup.NextAttemptAt.Equal(retryAt) {
		t.Fatalf("failed result aliases store: %#v", stored.Setup)
	}

	beforeEarlyClaim := store.Snapshot().Generation
	if claimed, found, err := store.ClaimSetup(retryAt.Add(-time.Second)); err != nil || found || !claimed.Project.IsZero() {
		t.Fatalf("early claim = %#v, %t, %v", claimed, found, err)
	}
	if generation := store.Snapshot().Generation; generation != beforeEarlyClaim {
		t.Fatalf("early claim generation = %d, want %d", generation, beforeEarlyClaim)
	}

	start := make(chan struct{})
	results := make(chan struct {
		record Record
		found  bool
		err    error
	}, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			record, found, err := store.ClaimSetup(retryAt)
			results <- struct {
				record Record
				found  bool
				err    error
			}{record: record, found: found, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	claims := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.found {
			claims++
			if result.record.Setup.Attempts != 2 || result.record.Setup.State != SetupStateRunning {
				t.Fatalf("retry claim = %#v", result.record)
			}
		}
	}
	if claims != 1 {
		t.Fatalf("concurrent due claims = %d, want 1", claims)
	}

	completedAt := retryAt.Add(time.Minute)
	completed, err := store.CompleteSetup(admission.Project.ID, completedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !completed.Routable() || completed.Setup.ProvisionedAt == nil || !completed.Setup.ProvisionedAt.Equal(completedAt) {
		t.Fatalf("completed setup = %#v", completed)
	}
	generation := store.Snapshot().Generation
	duplicate, err := store.CompleteSetup(admission.Project.ID, completedAt.Add(time.Minute))
	if err != nil || !duplicate.Setup.ProvisionedAt.Equal(completedAt) || store.Snapshot().Generation != generation {
		t.Fatalf("idempotent completion = %#v, %v, generation=%d", duplicate, err, store.Snapshot().Generation)
	}
	if _, err := store.FailSetup(admission.Project.ID, "late", completedAt.Add(time.Hour), completedAt.Add(time.Minute)); err == nil {
		t.Fatal("succeeded setup failed without a new claim")
	} else {
		assertPermanent(t, err)
	}

	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.Snapshot(), store.Snapshot()) {
		t.Fatalf("reopened lifecycle differs: reopened=%#v current=%#v", reopened.Snapshot(), store.Snapshot())
	}
}

func TestStoreRecoverSetupsUsesOneGeneration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	created := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	running := succeededSetup(root, "factory", created)
	running.State = SetupStateRunning
	running.Attempts = 2
	running.ProvisionedAt = nil
	running.ProviderCoordinated = false
	uncoordinated := succeededSetup(root, "network", created)
	uncoordinated.CloudURL = "https://network.nags.cloud"
	uncoordinated.ProviderCoordinated = false
	store := createOnboardingStore(t, root, []CompiledSource{
		compiledSource(root, "factory"), compiledSource(root, "network"),
	}, []SetupSource{running, uncoordinated})

	recoveredAt := created.Add(time.Hour)
	recovered, err := store.RecoverSetups(recoveredAt)
	if err != nil || recovered != 2 {
		t.Fatalf("RecoverSetups = %d, %v", recovered, err)
	}
	state := store.Snapshot()
	if state.Generation != 2 {
		t.Fatalf("recovery generation = %d", state.Generation)
	}
	factory := findRecord(t, state.Records, "tomnagengast/factory")
	network := findRecord(t, state.Records, "tomnagengast/network")
	if factory.Setup.State != SetupStatePending || factory.Setup.Attempts != 2 || !factory.Setup.UpdatedAt.Equal(recoveredAt) {
		t.Fatalf("running recovery = %#v", factory.Setup)
	}
	if network.Setup.State != SetupStatePending || network.Setup.ProvisionedAt != nil || network.Setup.ProviderCoordinated {
		t.Fatalf("provider recovery = %#v", network.Setup)
	}
	if recovered, err := store.RecoverSetups(recoveredAt.Add(time.Minute)); err != nil || recovered != 0 || store.Snapshot().Generation != 2 {
		t.Fatalf("idempotent recovery = %d, %v, generation=%d", recovered, err, store.Snapshot().Generation)
	}
}

func TestStoreTypedOperationsRespectPersistenceFailureBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	project := ProjectIdentity{ID: "project-cellar", Name: "Cellar"}

	t.Run("before rename", func(t *testing.T) {
		store := createOnboardingStore(t, root, []CompiledSource{compiledSource(root, "factory")}, nil)
		injected := errors.New("injected pre-rename failure")
		store.writer = func(string, SourceState) (bool, error) { return false, injected }
		awaiting, err := store.RecordAwaitingMetadata(project, now)
		if !errors.Is(err, injected) || !awaiting.Project.IsZero() {
			t.Fatalf("RecordAwaitingMetadata = %#v, %v", awaiting, err)
		}
		if state := store.Snapshot(); state.Generation != 1 || len(state.Awaiting) != 0 {
			t.Fatalf("pre-rename state = %#v", state)
		}
	})

	t.Run("after rename", func(t *testing.T) {
		store := createOnboardingStore(t, t.TempDir(), []CompiledSource{compiledSource(t.TempDir(), "factory")}, nil)
		injected := errors.New("injected directory sync failure")
		calls := 0
		store.writer = func(path string, state SourceState) (bool, error) {
			calls++
			return writeSourceStateWithDirectorySync(path, state, func(*os.File) error { return injected })
		}
		awaiting, err := store.RecordAwaitingMetadata(project, now)
		if !errors.Is(err, injected) || awaiting.Project != project {
			t.Fatalf("RecordAwaitingMetadata = %#v, %v", awaiting, err)
		}
		if state := store.Snapshot(); state.Generation != 2 || len(state.Awaiting) != 1 {
			t.Fatalf("post-rename state = %#v", state)
		}
		reopened, openErr := Open(store.path)
		if openErr != nil || !reflect.DeepEqual(reopened.Snapshot(), store.Snapshot()) {
			t.Fatalf("post-rename reopen = %#v, %v", reopened, openErr)
		}
		if _, err := store.RecordAwaitingMetadata(project, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || store.Snapshot().Generation != 2 {
			t.Fatalf("idempotent retry calls=%d generation=%d", calls, store.Snapshot().Generation)
		}
	})

	t.Run("claim and terminal transition", func(t *testing.T) {
		subRoot := t.TempDir()
		store := createOnboardingStore(t, subRoot, []CompiledSource{compiledSource(subRoot, "factory")}, nil)
		admission := managedAdmission(subRoot, "cellar")
		if _, err := store.AdmitProject(admission, now); err != nil {
			t.Fatal(err)
		}

		preRename := errors.New("injected claim failure")
		store.writer = func(string, SourceState) (bool, error) { return false, preRename }
		claimed, found, err := store.ClaimSetup(now.Add(time.Minute))
		if !errors.Is(err, preRename) || found || !claimed.Project.IsZero() {
			t.Fatalf("failed claim = %#v, %t, %v", claimed, found, err)
		}
		if record, _ := store.catalog.Record(admission.Repository); record.Setup.State != SetupStatePending {
			t.Fatalf("failed claim changed state: %#v", record.Setup)
		}

		store.writer = writeSourceState
		if _, found, err := store.ClaimSetup(now.Add(2 * time.Minute)); err != nil || !found {
			t.Fatalf("durable claim = %t, %v", found, err)
		}
		postRename := errors.New("injected completion directory sync failure")
		store.writer = func(path string, state SourceState) (bool, error) {
			return writeSourceStateWithDirectorySync(path, state, func(*os.File) error { return postRename })
		}
		completed, err := store.CompleteSetup(admission.Project.ID, now.Add(3*time.Minute))
		if !errors.Is(err, postRename) || !completed.Routable() {
			t.Fatalf("post-rename completion = %#v, %v", completed, err)
		}
		generation := store.Snapshot().Generation
		store.writer = writeSourceState
		completed, err = store.CompleteSetup(admission.Project.ID, now.Add(4*time.Minute))
		if err != nil || !completed.Routable() || store.Snapshot().Generation != generation {
			t.Fatalf("completion retry = %#v, %v, generation=%d", completed, err, store.Snapshot().Generation)
		}
	})
}

func TestStoreConcurrentAwaitingAdmissionsDoNotClobberGenerations(t *testing.T) {
	root := t.TempDir()
	store := createOnboardingStore(t, root, []CompiledSource{compiledSource(root, "factory")}, nil)
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	const projects = 20
	start := make(chan struct{})
	errs := make(chan error, projects)
	var wait sync.WaitGroup
	for index := range projects {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.RecordAwaitingMetadata(ProjectIdentity{
				ID: fmt.Sprintf("project-%02d", index), Name: fmt.Sprintf("Project %02d", index),
			}, now.Add(time.Duration(index)*time.Second))
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state := store.Snapshot()
	if len(state.Awaiting) != projects || state.Generation != 1+projects {
		t.Fatalf("concurrent state has %d awaiting at generation %d", len(state.Awaiting), state.Generation)
	}
}

func TestStoreAdmissionRejectsInvalidAndUnknownExistingMetadataPermanently(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := createOnboardingStore(t, root, []CompiledSource{compiledSource(root, "factory")}, nil)
	valid := managedAdmission(root, "cellar")
	tests := []struct {
		name   string
		mutate func(*ProjectAdmission)
	}{
		{name: "project", mutate: func(value *ProjectAdmission) { value.Project.ID = " project-cellar" }},
		{name: "origin", mutate: func(value *ProjectAdmission) { value.Origin = "https://github.com/tomnagengast/cellar" }},
		{name: "path", mutate: func(value *ProjectAdmission) { value.LocalPath = value.ManagedRoot + "/nested/../cellar" }},
		{name: "branch", mutate: func(value *ProjectAdmission) { value.DefaultBranch = "../main" }},
		{name: "management", mutate: func(value *ProjectAdmission) { value.Bootstrap = false }},
		{name: "cloud", mutate: func(value *ProjectAdmission) { value.CloudURL = "https://api.cellar.nags.cloud" }},
		{name: "derived app", mutate: func(value *ProjectAdmission) {
			value.Repository = "tomnagengast/bad_name"
			value.Origin = "git@github.com:tomnagengast/bad_name.git"
			value.LocalPath = filepath.Join(value.ManagedRoot, "bad_name")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if _, err := store.AdmitProject(candidate, time.Now()); err == nil {
				t.Fatal("invalid admission succeeded")
			} else {
				assertPermanent(t, err)
			}
		})
	}
	unknown := valid
	unknown.Managed = false
	unknown.Bootstrap = false
	if _, err := store.AdmitProject(unknown, time.Now()); err == nil {
		t.Fatal("unknown unmanaged repository succeeded")
	} else {
		assertPermanent(t, err)
	}
	if state := store.Snapshot(); state.Generation != 1 || len(state.Records) != 1 {
		t.Fatalf("rejected admissions changed state: %#v", state)
	}
}

func TestCanonicalSetupErrorPreservesValidUTF8AtByteLimit(t *testing.T) {
	detail := " " + strings.Repeat("x", 2047) + "é" + string([]byte{0xff}) + " "
	canonical := canonicalSetupError(detail)
	if len(canonical) > 2048 || !strings.HasSuffix(canonical, "x") || strings.ContainsRune(canonical, '\uFFFD') {
		t.Fatalf("canonical setup error has %d bytes and suffix %q", len(canonical), canonical[len(canonical)-4:])
	}
}

func createOnboardingStore(t *testing.T, root string, compiled []CompiledSource, setups []SetupSource) *Store {
	t.Helper()
	state, err := ConvertSources(compiled, setups)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Create(filepath.Join(root, "state", "repositories.json"), state)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func managedAdmission(root, name string) ProjectAdmission {
	managedRoot := filepath.Join(root, "managed-dynamic")
	return ProjectAdmission{
		Project:    ProjectIdentity{ID: "project-" + name, Name: strings.ToUpper(name[:1]) + name[1:]},
		Repository: "tomnagengast/" + name, Origin: "git@github.com:tomnagengast/" + name + ".git",
		LocalPath: filepath.Join(managedRoot, name), ManagedRoot: managedRoot,
		DefaultBranch: "main", Bootstrap: true, Managed: true,
	}
}

func compiledAdmission(root, name string) ProjectAdmission {
	projectRoot := filepath.Join(root, "projects")
	return ProjectAdmission{
		Project:    ProjectIdentity{ID: "project-" + name, Name: strings.ToUpper(name[:1]) + name[1:]},
		Repository: "tomnagengast/" + name, Origin: "git@github.com:tomnagengast/" + name + ".git",
		LocalPath: filepath.Join(projectRoot, name), ManagedRoot: projectRoot, DefaultBranch: "main",
	}
}
