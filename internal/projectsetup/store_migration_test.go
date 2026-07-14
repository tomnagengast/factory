package projectsetup

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMigratesCloudSetupsForProviderBackfill(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 18, 0, 0, 0, time.UTC)
	prior := now.Add(-time.Hour)
	root := t.TempDir()
	path := filepath.Join(root, "project-setups.json")
	managedCloud := migrationEntry(root, "cloud", true, "https://cloud.nags.cloud", prior)
	managedRepositoryOnly := migrationEntry(root, "repository-only", true, "", prior)
	existingCloud := migrationEntry(root, "existing", false, "https://existing.nags.cloud", prior)
	if err := writeStore(path, diskState{
		Version: storeVersion,
		Entries: []Entry{managedCloud, managedRepositoryOnly, existingCloud},
	}); err != nil {
		t.Fatalf("write legacy store: %v", err)
	}

	store, err := Open(path, now)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	snapshot := store.Snapshot()
	if snapshot.Pending != 2 || snapshot.Succeeded != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if got := snapshot.Entries[0]; got.State != StatePending || got.ProvisionedAt != nil || got.UpdatedAt != now {
		t.Fatalf("managed Cloud entry = %#v", got)
	}
	if got := snapshot.Entries[1]; got.State != StateSucceeded || got.ProvisionedAt == nil {
		t.Fatalf("repository-only entry = %#v", got)
	}
	if got := snapshot.Entries[2]; got.State != StatePending || got.ProvisionedAt != nil {
		t.Fatalf("existing Cloud entry = %#v", got)
	}

	for _, projectID := range []string{managedCloud.ProjectID, existingCloud.ProjectID} {
		claimed, found, claimErr := store.Claim(now)
		if claimErr != nil || !found || claimed.ProjectID != projectID {
			t.Fatalf("claim backfill = %#v, %t, %v", claimed, found, claimErr)
		}
		if err := store.Complete(claimed.ProjectID, now.Add(time.Minute)); err != nil {
			t.Fatalf("complete backfill: %v", err)
		}
	}
	reopened, err := Open(path, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	if got := reopened.Snapshot(); got.Pending != 0 || got.Succeeded != 3 || !got.Entries[0].ProviderCoordinated || !got.Entries[2].ProviderCoordinated {
		t.Fatalf("reopened snapshot = %#v", got)
	}
}

func TestUpsertRequeuesManagedSetupWhenCloudURLIsAdded(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 18, 0, 0, 0, time.UTC)
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "project-setups.json"), now)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	spec := migrationEntry(root, "later-cloud", true, "", now).Spec
	if needsProvision, err := store.Upsert(spec, now); err != nil || !needsProvision {
		t.Fatalf("initial Upsert = %t, %v", needsProvision, err)
	}
	if _, found, err := store.Claim(now); err != nil || !found {
		t.Fatalf("initial Claim = %t, %v", found, err)
	}
	if err := store.Complete(spec.ProjectID, now); err != nil {
		t.Fatalf("initial Complete: %v", err)
	}

	spec.CloudURL = "https://later-cloud.nags.cloud"
	needsProvision, err := store.Upsert(spec, now.Add(time.Minute))
	if err != nil || !needsProvision {
		t.Fatalf("Cloud Upsert = %t, %v", needsProvision, err)
	}
	entry := store.Snapshot().Entries[0]
	if entry.State != StatePending || entry.ProviderCoordinated {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestUpsertQueuesProviderCoordinationForExistingRepository(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 13, 18, 0, 0, 0, time.UTC)
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "project-setups.json"), now)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	spec := migrationEntry(root, "existing-cloud", false, "https://existing-cloud.nags.cloud", now).Spec
	needsProvision, err := store.Upsert(spec, now)
	if err != nil || !needsProvision {
		t.Fatalf("Upsert = %t, %v", needsProvision, err)
	}
	entry := store.Snapshot().Entries[0]
	if entry.State != StatePending || entry.Managed || entry.ProviderCoordinated {
		t.Fatalf("entry = %#v", entry)
	}
}

func migrationEntry(root, name string, managed bool, cloudURL string, at time.Time) Entry {
	managedRoot := root
	if !managed {
		managedRoot = filepath.Join(root, "existing-root")
	}
	provisionedAt := at
	return Entry{
		Spec: Spec{
			ProjectID: "project-" + name, ProjectName: name, Repository: "tomnagengast/" + name,
			RepoURL: "git@github.com:tomnagengast/" + name + ".git", LocalPath: filepath.Join(managedRoot, name),
			ManagedRoot: managedRoot, CloudURL: cloudURL, BaseBranch: "main", Bootstrap: managed, Managed: managed,
		},
		State: StateSucceeded, CreatedAt: at, UpdatedAt: at, ProvisionedAt: &provisionedAt,
	}
}
