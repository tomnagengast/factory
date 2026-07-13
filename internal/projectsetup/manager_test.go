package projectsetup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

type recordingRegistrar struct {
	specs []Spec
	err   error
}

func (r *recordingRegistrar) SyncRepositories(specs []Spec) error {
	r.specs = append([]Spec(nil), specs...)
	return r.err
}

type recordingProvisioner struct {
	specs []Spec
	err   error
}

func (p *recordingProvisioner) Provision(_ context.Context, spec Spec) error {
	p.specs = append(p.specs, spec)
	return p.err
}

func TestManagerAdmitsBeforeProvisioningAndCompletesIdempotently(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "repos", "tomnagengast")
	parser, err := NewParser("tomnagengast", root, nil)
	if err != nil {
		t.Fatalf("new parser: %v", err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "project-setups.json"), now)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	registrar := &recordingRegistrar{}
	provisioner := &recordingProvisioner{}
	manager, err := NewManager(store, parser, registrar, provisioner, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	request := Request{
		ProjectID: "project-1", ProjectName: "Cellar",
		Description: "GitHub Repo: tomnagengast/cellar\nLocal Path: " + filepath.Join(root, "cellar") + "\nCloud URL: https://cellar.nags.cloud",
	}
	if err := manager.Enqueue(context.Background(), request); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(registrar.specs) != 1 || len(provisioner.specs) != 0 {
		t.Fatalf("registrar = %#v, provisioner = %#v", registrar.specs, provisioner.specs)
	}
	if got := manager.PublicSnapshot(); got.Pending != 1 || got.Total != 1 {
		t.Fatalf("pending snapshot = %#v", got)
	}
	manager.Reconcile(context.Background())
	if len(provisioner.specs) != 1 || manager.PublicSnapshot().Succeeded != 1 {
		t.Fatalf("provisioned = %#v, snapshot = %#v", provisioner.specs, manager.PublicSnapshot())
	}
	if err := manager.Enqueue(context.Background(), request); err != nil {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	manager.Reconcile(context.Background())
	if len(provisioner.specs) != 1 {
		t.Fatalf("duplicate provision calls = %d, want 1", len(provisioner.specs))
	}
}

func TestManagerRetriesFailuresAndProtectsAcceptedIdentity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "repos", "tomnagengast")
	parser, err := NewParser("tomnagengast", root, nil)
	if err != nil {
		t.Fatalf("new parser: %v", err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "project-setups.json"), now)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	provisioner := &recordingProvisioner{err: errors.New("GitHub unavailable")}
	manager, err := NewManager(store, parser, &recordingRegistrar{}, provisioner, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	request := Request{
		ProjectID: "project-1", ProjectName: "Cellar",
		Description: "GitHub Repo: tomnagengast/cellar\nLocal Path: " + filepath.Join(root, "cellar"),
	}
	if err := manager.Enqueue(context.Background(), request); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	manager.Reconcile(context.Background())
	if got := manager.PublicSnapshot(); got.Failed != 1 {
		t.Fatalf("failed snapshot = %#v", got)
	}
	manager.Reconcile(context.Background())
	if len(provisioner.specs) != 1 {
		t.Fatalf("early retries = %d, want 1", len(provisioner.specs))
	}
	now = now.Add(15 * time.Second)
	provisioner.err = nil
	manager.Reconcile(context.Background())
	if len(provisioner.specs) != 2 || manager.PublicSnapshot().Succeeded != 1 {
		t.Fatalf("retry calls = %d, snapshot = %#v", len(provisioner.specs), manager.PublicSnapshot())
	}

	changedIdentity := request
	changedIdentity.Description = "GitHub Repo: tomnagengast/other\nLocal Path: " + filepath.Join(root, "other")
	if err := manager.Enqueue(context.Background(), changedIdentity); err == nil {
		t.Fatal("identity change succeeded")
	} else {
		var classified interface{ Permanent() bool }
		if !errors.As(err, &classified) || !classified.Permanent() {
			t.Fatalf("identity error is not permanent: %v", err)
		}
	}
	missingIdentity := request
	missingIdentity.Description = "Cloud URL: https://cellar.nags.cloud"
	if err := manager.Enqueue(context.Background(), missingIdentity); err == nil {
		t.Fatal("metadata removal succeeded")
	}
}

func TestStoreRecoversRunningEntryAfterRestart(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "project-setups.json")
	store, err := Open(path, now)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	spec := Spec{
		ProjectID: "project-1", ProjectName: "Cellar", Repository: "tomnagengast/cellar",
		RepoURL: "git@github.com:tomnagengast/cellar.git", LocalPath: "/Users/tom/repos/tomnagengast/cellar",
		ManagedRoot: "/Users/tom/repos/tomnagengast", BaseBranch: "main", Bootstrap: true, Managed: true,
	}
	if _, err := store.Upsert(spec, now); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, found, err := store.Claim(now); err != nil || !found {
		t.Fatalf("claim: found=%t, err=%v", found, err)
	}
	reopened, err := Open(path, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.PublicSnapshot(); got.Pending != 1 || got.Running != 0 {
		t.Fatalf("snapshot = %#v", got)
	}
}
