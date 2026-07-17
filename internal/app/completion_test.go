package app

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/repositories"
)

func TestNewCompletionValidatorBuildsAuthoritiesFromCanonicalCatalog(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	state, err := repositories.ConvertSources([]repositories.CompiledSource{{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git",
		RepoPath: filepath.Join(root, "managed", "factory"), ManagedRoot: filepath.Join(root, "managed"),
		ProjectPath: filepath.Join(root, "factory"), BaseBranch: "main",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := repositories.Create(filepath.Join(root, "repositories.json"), state)
	if err != nil {
		t.Fatal(err)
	}
	options := CompletionOptions{
		GitHubPath: "gh", GitDirectory: root, LinearURL: "https://api.linear.app/graphql", LinearAPIKey: "key",
		GitPath: "git", WorktrunkPath: "wt", HTTPClient: http.DefaultClient, Now: time.Now,
	}
	if _, err := NewCompletionValidator(store, options); err != nil {
		t.Fatalf("valid canonical catalog: %v", err)
	}
	if _, err := NewCompletionValidator(nil, options); err == nil {
		t.Fatal("nil catalog accepted")
	}
	options.HTTPClient = nil
	if _, err := NewCompletionValidator(store, options); err == nil {
		t.Fatal("nil HTTP client accepted")
	}
}
