package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/taskservice"
)

var (
	_ taskservice.Catalog  = (*RepositoryAdapter)(nil)
	_ taskservice.Projects = (*RepositoryAdapter)(nil)
)

func TestRepositoryAdapterProjectsOneCanonicalRecord(t *testing.T) {
	root := t.TempDir()
	managedRoot := filepath.Join(root, "managed")
	managedPath := filepath.Join(managedRoot, "factory")
	localPath := filepath.Join(root, "factory")
	receipt := filepath.Join(root, "deployments", "current.json")
	pending := filepath.Join(root, "deployments", "pending.json")
	now := time.Date(2026, time.July, 17, 5, 30, 0, 0, time.UTC)
	state, err := repositories.ConvertSources([]repositories.CompiledSource{{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git",
		RepoPath: managedPath, ManagedRoot: managedRoot, ProjectPath: localPath, BaseBranch: "main",
		ReceiptPath: receipt, PendingReceipt: pending, HealthURL: "http://127.0.0.1:8092/api/healthz",
	}}, []repositories.SetupSource{{
		ProjectID: "project-factory", ProjectName: "Factory", Repository: "tomnagengast/factory",
		RepoURL: "git@github.com:tomnagengast/factory.git", LocalPath: localPath, ManagedRoot: filepath.Dir(localPath),
		BaseBranch: "main", State: repositories.SetupStateSucceeded,
		CreatedAt: now, UpdatedAt: now, ProvisionedAt: &now, ProviderCoordinated: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := repositories.Create(filepath.Join(root, "repositories.json"), state)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewRepositoryAdapter(store)
	if err != nil {
		t.Fatal(err)
	}
	config, err := adapter.ResolveRepository("tomnagengast/factory")
	if err != nil {
		t.Fatal(err)
	}
	if config.RepoPath != managedPath || config.ProjectPath != localPath || config.ReceiptPath != receipt || config.PendingReceipt != pending {
		t.Fatalf("repository config = %#v", config)
	}
	spec, err := adapter.ResolveSucceeded("project-factory")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Repository != config.Repository || spec.LocalPath != localPath || spec.Managed {
		t.Fatalf("project spec = %#v", spec)
	}
	choices := adapter.Choices()
	if len(choices) != 1 || choices[0].ProjectID != spec.ProjectID {
		t.Fatalf("choices = %#v", choices)
	}
	configs, err := adapter.Configs()
	if err != nil || len(configs) != 1 || configs[0].Repository != config.Repository {
		t.Fatalf("configs = %#v, err=%v", configs, err)
	}
}
