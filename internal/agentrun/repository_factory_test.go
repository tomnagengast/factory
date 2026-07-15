package agentrun

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/taskstore"
)

func TestFactoryRepositoryResolverRechecksPinnedRoute(t *testing.T) {
	directory := t.TempDir()
	config := RepositoryConfig{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "https://github.com/tomnagengast/factory",
		RepoPath: filepath.Join(directory, "factory"), ManagedRoot: directory, ProjectPath: filepath.Join(directory, "factory"), BaseBranch: "main",
	}
	catalog, err := NewRepositoryCatalog([]RepositoryConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := taskstore.Open(filepath.Join(directory, "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	actor := taskstore.Actor{ID: "operator", Kind: taskstore.AuthorHuman}
	task, _, err := tasks.Create(taskstore.CreateCommand{Actor: actor, Title: "Native", ProjectID: "project-factory", ApprovalMode: taskstore.ApprovalGated, IdempotencyKey: "create"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	routing := taskstore.RoutingSnapshot{
		ProjectID: task.ProjectID, Repository: config.Repository, RepositoryURL: config.RepoURL, RepositoryPath: config.RepoPath,
		ManagedRoot: config.ManagedRoot, BaseBranch: config.BaseBranch, WorkflowID: "full-sdlc-provider-neutral", WorkflowDigest: "digest", AdmittedAt: time.Now(),
	}
	task, _, err = tasks.SetRouting(taskstore.RoutingCommand{Actor: actor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Routing: routing, IdempotencyKey: "route"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewFactoryRepositoryResolver(tasks, catalog)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.ResolveTask(context.Background(), task.Ref)
	if err != nil || resolved.Repository != config.Repository || resolved.RepoPath != config.RepoPath {
		t.Fatalf("resolved = %#v err=%v", resolved, err)
	}

	changed := config
	changed.RepoPath, changed.ProjectPath = filepath.Join(directory, "elsewhere"), filepath.Join(directory, "elsewhere")
	if err := catalog.Replace([]RepositoryConfig{changed}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveTask(context.Background(), task.Ref); err == nil {
		t.Fatal("stale pinned route was accepted")
	}
}
