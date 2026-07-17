package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskstore"
)

var _ runs.RepositoryResolver = (*RepositoryResolver)(nil)

func TestRepositoryResolverUsesLiveLinearMetadataAndNativePinnedRoute(t *testing.T) {
	root := t.TempDir()
	repositoryStore, managedPath, managedRoot, localPath := appRepositoryStore(t, root)
	tasks, err := taskstore.Create(filepath.Join(root, "tasks.jsonl"), taskstore.Snapshot{Schema: taskstore.SchemaVersion, NextSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	parser, err := projectsetup.NewParser("tomnagengast", filepath.Join(root, "new"), []projectsetup.ExistingRepository{{
		Repository: "tomnagengast/factory", ProjectPath: localPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	reader := staticLinearProjectReader{request: projectsetup.Request{
		ProjectID: "project-factory", ProjectName: "Factory",
		Description: "GitHub Repo: tomnagengast/factory\nLocal Path: " + localPath,
	}}
	resolver, err := NewRepositoryResolver(repositoryStore, tasks, reader, parser)
	if err != nil {
		t.Fatal(err)
	}

	linear, err := resolver.ResolveRoute(context.Background(), runs.Run{Causation: runs.Causation{
		Task: taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-47", Identifier: "ENG-47"},
	}})
	if err != nil || linear.Repository != "tomnagengast/factory" || linear.ManagedPath != managedPath || linear.ManagedRoot != managedRoot {
		t.Fatalf("Linear route = %#v, %v", linear, err)
	}

	now := time.Date(2026, time.July, 17, 14, 0, 0, 0, time.UTC)
	actor := taskstore.Actor{ID: "human@example.com", Kind: taskstore.AuthorHuman}
	task, _, err := tasks.Create(taskstore.CreateCommand{
		Actor: actor, Title: "Native task", ProjectID: "project-factory", ApprovalMode: taskstore.ApprovalGated, IdempotencyKey: "create-native",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = tasks.SetRouting(taskstore.RoutingCommand{
		Actor: actor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, IdempotencyKey: "route-native",
		Routing: taskstore.RoutingSnapshot{
			ProjectID: "project-factory", Repository: "tomnagengast/factory",
			RepositoryURL: "git@github.com:tomnagengast/factory.git", RepositoryPath: managedPath, ManagedRoot: managedRoot,
			BaseBranch: "main", WorkflowID: "full-sdlc-provider-neutral", WorkflowDigest: "digest", AdmittedAt: now,
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	native, err := resolver.ResolveRoute(context.Background(), runs.Run{Causation: runs.Causation{Task: task.Ref}})
	if err != nil || native != linear {
		t.Fatalf("Factory route = %#v, %v; want %#v", native, err, linear)
	}
}

func TestRepositoryResolverFailsClosedOnMetadataDrift(t *testing.T) {
	root := t.TempDir()
	repositoryStore, _, _, localPath := appRepositoryStore(t, root)
	tasks, _ := taskstore.Create(filepath.Join(root, "tasks.jsonl"), taskstore.Snapshot{Schema: taskstore.SchemaVersion, NextSequence: 1})
	parser, _ := projectsetup.NewParser("tomnagengast", filepath.Join(root, "new"), []projectsetup.ExistingRepository{{
		Repository: "tomnagengast/factory", ProjectPath: localPath,
	}})
	resolver, _ := NewRepositoryResolver(repositoryStore, tasks, staticLinearProjectReader{request: projectsetup.Request{
		ProjectID: "project-factory", ProjectName: "Renamed",
		Description: "GitHub Repo: tomnagengast/factory\nLocal Path: " + localPath,
	}}, parser)
	_, err := resolver.ResolveRoute(context.Background(), runs.Run{Causation: runs.Causation{
		Task: taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-47", Identifier: "ENG-47"},
	}})
	var permanent interface{ Permanent() bool }
	if err == nil || !errors.As(err, &permanent) || !permanent.Permanent() {
		t.Fatalf("metadata drift error = %v", err)
	}
}

type staticLinearProjectReader struct {
	request projectsetup.Request
	err     error
}

func (r staticLinearProjectReader) ReadProject(context.Context, string) (projectsetup.Request, error) {
	return r.request, r.err
}

func appRepositoryStore(t *testing.T, root string) (*repositories.Store, string, string, string) {
	t.Helper()
	managedRoot := filepath.Join(root, "managed")
	managedPath := filepath.Join(managedRoot, "factory")
	localPath := filepath.Join(root, "factory")
	now := time.Date(2026, time.July, 17, 14, 0, 0, 0, time.UTC)
	state, err := repositories.ConvertSources([]repositories.CompiledSource{{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git",
		RepoPath: managedPath, ManagedRoot: managedRoot, ProjectPath: localPath, BaseBranch: "main",
	}}, []repositories.SetupSource{{
		ProjectID: "project-factory", ProjectName: "Factory", Repository: "tomnagengast/factory",
		RepoURL: "git@github.com:tomnagengast/factory.git", LocalPath: localPath, ManagedRoot: filepath.Dir(localPath),
		BaseBranch: "main", State: repositories.SetupStateSucceeded, CreatedAt: now, UpdatedAt: now,
		ProvisionedAt: &now, ProviderCoordinated: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := repositories.Create(filepath.Join(root, "repositories.json"), state)
	if err != nil {
		t.Fatal(err)
	}
	return store, managedPath, managedRoot, localPath
}
