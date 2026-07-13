package agentrun

import (
	"path/filepath"
	"testing"
)

func TestRepositoryCatalogReplacesRuntimeEntriesAndAcceptsProjectSlug(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := RepositoryConfig{
		App: "base", Repository: "tomnagengast/base", RepoURL: "git@github.com:tomnagengast/base.git",
		RepoPath: filepath.Join(root, "base"), ManagedRoot: root, ProjectPath: filepath.Join(root, "base"), BaseBranch: "main",
	}
	catalog, err := NewRepositoryCatalog([]RepositoryConfig{base})
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}
	dynamic := RepositoryConfig{
		App: "cellar", Repository: "tomnagengast/cellar", RepoURL: "git@github.com:tomnagengast/cellar.git",
		RepoPath: filepath.Join(root, "cellar"), ManagedRoot: root, ProjectPath: filepath.Join(root, "cellar"), BaseBranch: "main", Bootstrap: true,
	}
	if err := catalog.Replace([]RepositoryConfig{base, dynamic}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	resolved, err := catalog.ResolveProject("GitHub Repo: tomnagengast/cellar\nLocal Path: " + dynamic.ProjectPath)
	if err != nil {
		t.Fatalf("resolve slug: %v", err)
	}
	if resolved.Repository != dynamic.Repository || !resolved.Bootstrap {
		t.Fatalf("resolved = %#v", resolved)
	}
	resolved, err = catalog.ResolveProject("GitHub Repo: https://github.com/TOMNAGENGAST/CELLAR.git\nLocal Path: " + dynamic.ProjectPath)
	if err != nil || resolved.Repository != dynamic.Repository {
		t.Fatalf("resolve URL = %#v, %v", resolved, err)
	}
}
