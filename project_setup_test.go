package main

import (
	"path/filepath"
	"testing"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
)

func TestRepositoryConfigsWithSetupsPreservesStaticContractsAndAddsBootstrap(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	static := []agentrun.RepositoryConfig{{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git",
		RepoPath: filepath.Join(root, "factory"), ManagedRoot: root, ProjectPath: filepath.Join(root, "factory"),
		BaseBranch: "main", ReceiptPath: "/tmp/current", PendingReceipt: "/tmp/pending", HealthURL: "http://127.0.0.1/healthz",
	}}
	setups := []projectsetup.Spec{{
		ProjectID: "project-cellar", ProjectName: "Cellar", Repository: "tomnagengast/cellar",
		RepoURL: "git@github.com:tomnagengast/cellar.git", LocalPath: filepath.Join(root, "cellar"),
		ManagedRoot: root, CloudURL: "https://cellar.nags.cloud", BaseBranch: "main", Bootstrap: true, Managed: true,
	}}
	configs := repositoryConfigsWithSetups(static, setups)
	if len(configs) != 2 || !configs[0].DeploymentRequired() {
		t.Fatalf("configs = %#v", configs)
	}
	if got := configs[1]; got.App != "cellar" || got.Repository != "tomnagengast/cellar" || got.ProjectPath != filepath.Join(root, "cellar") || got.CloudURL != "https://cellar.nags.cloud" || !got.Bootstrap || got.DeploymentRequired() {
		t.Fatalf("dynamic config = %#v", got)
	}
}
