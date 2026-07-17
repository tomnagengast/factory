package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/repositories"
)

type recordingRepositoryProvisioner struct {
	err   error
	specs []projectsetup.Spec
}

func (p *recordingRepositoryProvisioner) Provision(_ context.Context, spec projectsetup.Spec) error {
	p.specs = append(p.specs, spec)
	return p.err
}

func TestRepositoryOnboardingOwnsMetadataAdmissionAndProvisioning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	managed := filepath.Join(root, "repos")
	existing := filepath.Join(root, "factory")
	store := onboardingRepositoryStore(t, root, managed, existing)
	parser, err := projectsetup.NewParser("tomnagengast", managed, []projectsetup.ExistingRepository{{
		Repository: "tomnagengast/factory", ProjectPath: existing,
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 17, 13, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	provisioner := &recordingRepositoryProvisioner{}
	manager, err := NewRepositoryOnboarding(store, parser, provisioner, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	if err != nil {
		t.Fatal(err)
	}

	incomplete := projectsetup.Request{ProjectID: "project-1", ProjectName: "One"}
	if err := manager.Enqueue(t.Context(), incomplete); err != nil {
		t.Fatal(err)
	}
	if snapshot := manager.PublicSnapshot(); snapshot.AwaitingMetadata != 1 || snapshot.Total != 1 {
		t.Fatalf("awaiting snapshot = %+v", snapshot)
	}

	complete := incomplete
	complete.Description = "GitHub Repo: tomnagengast/widget\nLocal Path: " + filepath.Join(managed, "widget")
	now = now.Add(time.Minute)
	if err := manager.Enqueue(t.Context(), complete); err != nil {
		t.Fatal(err)
	}
	manager.Reconcile(t.Context())
	if len(provisioner.specs) != 1 || provisioner.specs[0].Repository != "tomnagengast/widget" {
		t.Fatalf("provisioned = %+v", provisioner.specs)
	}
	if snapshot := manager.PublicSnapshot(); snapshot.Succeeded != 1 || snapshot.AwaitingMetadata != 0 {
		t.Fatalf("completed snapshot = %+v", snapshot)
	}
}

func TestRepositoryOnboardingDurablySchedulesProvisionFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	managed := filepath.Join(root, "repos")
	store := onboardingRepositoryStore(t, root, managed, filepath.Join(root, "factory"))
	parser, err := projectsetup.NewParser("tomnagengast", managed, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 17, 14, 0, 0, 0, time.UTC)
	provisioner := &recordingRepositoryProvisioner{err: errors.New("offline")}
	manager, err := NewRepositoryOnboarding(store, parser, provisioner, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	request := projectsetup.Request{
		ProjectID: "project-2", ProjectName: "Two",
		Description: "GitHub Repo: tomnagengast/widget\nLocal Path: " + filepath.Join(managed, "widget"),
	}
	if err := manager.Enqueue(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	manager.Reconcile(t.Context())
	if snapshot := manager.PublicSnapshot(); snapshot.Failed != 1 || snapshot.Total != 1 {
		t.Fatalf("failed snapshot = %+v", snapshot)
	}
}

func onboardingRepositoryStore(t *testing.T, root, managed, existing string) *repositories.Store {
	t.Helper()
	state, err := repositories.ConvertSources([]repositories.CompiledSource{{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git",
		RepoPath: filepath.Join(managed, "factory"), ManagedRoot: managed, ProjectPath: existing, BaseBranch: "main",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := repositories.Create(filepath.Join(root, "repositories.json"), state)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
