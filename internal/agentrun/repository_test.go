package agentrun

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type repositoryRoundTripper func(*http.Request) (*http.Response, error)

func (f repositoryRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testRepositoryCatalog(t *testing.T) *RepositoryCatalog {
	t.Helper()
	catalog, err := NewRepositoryCatalog([]RepositoryConfig{
		{
			App: "network", Repository: "tomnagengast/network",
			RepoURL:  "git@github.com:tomnagengast/network.git",
			RepoPath: "/Users/tom/repos/tomnagengast/network", ManagedRoot: "/Users/tom/repos/tomnagengast", BaseBranch: "main",
			ReceiptPath: "/receipts/network.json", PendingReceipt: "/receipts/network-pending.json",
			HealthURL: "http://127.0.0.1:8090/healthz", SourcePath: "apps/network",
		},
		{
			App: "notebook", Repository: "tomnagengast/notebook",
			RepoURL:  "git@github.com:tomnagengast/notebook.git",
			RepoPath: "/Users/tom/repos/tomnagengast/notebook", ManagedRoot: "/Users/tom/repos/tomnagengast", BaseBranch: "main",
			ReceiptPath: "/receipts/notebook.json", PendingReceipt: "/receipts/notebook-pending.json",
			HealthURL: "http://127.0.0.1:8091/healthz",
		},
	})
	if err != nil {
		t.Fatalf("NewRepositoryCatalog: %v", err)
	}
	return catalog
}

func TestRepositoryCatalogResolvesOnlyExactLinearMetadata(t *testing.T) {
	t.Parallel()
	catalog := testRepositoryCatalog(t)
	config, err := catalog.ResolveProject("GitHub Repo: https://github.com/tomnagengast/notebook\nLocal Path: /Users/tom/repos/tomnagengast/notebook\n")
	if err != nil {
		t.Fatalf("ResolveProject: %v", err)
	}
	if config.Repository != "tomnagengast/notebook" || config.App != "notebook" {
		t.Fatalf("ResolveProject = %#v", config)
	}
	for _, description := range []string{
		"GitHub Repo: https://github.com/other/repo\nLocal Path: /tmp/repo\n",
		"GitHub Repo: https://github.com/tomnagengast/notebook\nLocal Path: /tmp/notebook\n",
		"GitHub Repo: https://github.com/tomnagengast/notebook\n",
	} {
		if _, err := catalog.ResolveProject(description); err == nil {
			t.Fatalf("ResolveProject(%q) succeeded", description)
		} else {
			var permanent interface{ Permanent() bool }
			if !errors.As(err, &permanent) || !permanent.Permanent() {
				t.Fatalf("ResolveProject(%q) error is not permanent: %v", description, err)
			}
		}
	}
}

func TestRepositoryCatalogRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	t.Parallel()
	valid := RepositoryConfig{
		App: "artifacts", Repository: "tomnagengast/artifacts",
		RepoURL: "git@github.com:tomnagengast/artifacts.git", RepoPath: "/Users/tom/repos/tomnagengast/artifacts",
		ManagedRoot: "/Users/tom/repos/tomnagengast", ProjectPath: "/Users/tom/repos/tomnagengast/artifacts",
		BaseBranch: "main", Bootstrap: true,
	}
	tests := []struct {
		name   string
		mutate func(*RepositoryConfig)
	}{
		{name: "remote mismatch", mutate: func(c *RepositoryConfig) { c.RepoURL = "git@github.com:other/artifacts.git" }},
		{name: "remote query", mutate: func(c *RepositoryConfig) { c.RepoURL = "https://github.com/tomnagengast/artifacts?unexpected=1" }},
		{name: "outside managed root", mutate: func(c *RepositoryConfig) { c.RepoPath = "/tmp/artifacts" }},
		{name: "relative managed root", mutate: func(c *RepositoryConfig) { c.ManagedRoot = "repos" }},
		{name: "partial deployment", mutate: func(c *RepositoryConfig) { c.ReceiptPath = "/receipt.json" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if _, err := NewRepositoryCatalog([]RepositoryConfig{candidate}); err == nil {
				t.Fatal("unsafe configuration succeeded")
			}
		})
	}
	if _, err := NewRepositoryCatalog([]RepositoryConfig{valid}); err != nil {
		t.Fatalf("valid non-deployable bootstrap configuration: %v", err)
	}
}

func TestLinearRepositoryResolverUsesAuthoritativeProjectDescription(t *testing.T) {
	t.Parallel()
	catalog := testRepositoryCatalog(t)
	client := &http.Client{Transport: repositoryRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "linear-key" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		body := "{\"data\":{\"issue\":{\"project\":{\"description\":\"GitHub Repo: https://github.com/tomnagengast/network\\nLocal Path: /Users/tom/repos/tomnagengast/network\\n\"}}}}"
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	resolver, err := NewLinearRepositoryResolver("https://api.linear.app/graphql", "linear-key", client, catalog)
	if err != nil {
		t.Fatalf("NewLinearRepositoryResolver: %v", err)
	}
	config, err := resolver.Resolve(context.Background(), "ENG-31")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if config.Repository != "tomnagengast/network" {
		t.Fatalf("Repository = %q", config.Repository)
	}
}
