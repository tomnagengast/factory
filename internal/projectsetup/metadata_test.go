package projectsetup

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestParserAcceptsCanonicalProjectMetadata(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "repos", "tomnagengast")
	parser, err := NewParser("tomnagengast", root, nil)
	if err != nil {
		t.Fatalf("new parser: %v", err)
	}
	spec, complete, err := parser.Parse(Request{
		ProjectID: "project-1", ProjectName: "Cellar",
		Description: "Build a cellar app.\n\nGitHub Repo: https://github.com/tomnagengast/cellar.git\nLocal Path: " + filepath.Join(root, "cellar") + "\nCloud URL: https://CELLAR.nags.cloud/",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !complete || spec.Repository != "tomnagengast/cellar" || spec.RepoURL != "git@github.com:tomnagengast/cellar.git" {
		t.Fatalf("spec = %#v, complete = %t", spec, complete)
	}
	if spec.LocalPath != filepath.Join(root, "cellar") || spec.ManagedRoot != root || spec.CloudURL != "https://cellar.nags.cloud" {
		t.Fatalf("spec = %#v", spec)
	}
	if !spec.Managed || !spec.Bootstrap || spec.BaseBranch != "main" {
		t.Fatalf("policy = %#v", spec)
	}
}

func TestParserAcceptsRepositorySlugAndWaitsForIncompleteCreate(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "repos", "tomnagengast")
	parser, err := NewParser("tomnagengast", root, nil)
	if err != nil {
		t.Fatalf("new parser: %v", err)
	}
	if spec, complete, err := parser.Parse(Request{
		ProjectID: "project-1", ProjectName: "Cellar", Description: "GitHub Repo: tomnagengast/cellar",
	}); err != nil || complete || spec.ProjectID != "project-1" {
		t.Fatalf("incomplete parse = %#v, %t, %v", spec, complete, err)
	}
	spec, complete, err := parser.Parse(Request{
		ProjectID: "project-1", ProjectName: "Cellar",
		Description: "GitHub Repo: tomnagengast/cellar\nLocal Path: " + filepath.Join(root, "cellar"),
	})
	if err != nil || !complete || spec.Repository != "tomnagengast/cellar" {
		t.Fatalf("complete parse = %#v, %t, %v", spec, complete, err)
	}
}

func TestParserRecognizesExistingFactoryRepositoryWithSpecialPath(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "repos", "tomnagengast")
	projectPath := filepath.Join(t.TempDir(), "mirror", "network")
	parser, err := NewParser("tomnagengast", root, []ExistingRepository{{
		Repository: "tomnagengast/network", ProjectPath: projectPath,
	}})
	if err != nil {
		t.Fatalf("new parser: %v", err)
	}
	spec, complete, err := parser.Parse(Request{
		ProjectID: "project-network", ProjectName: "Network",
		Description: "GitHub Repo: https://github.com/tomnagengast/network\nLocal Path: " + projectPath,
	})
	if err != nil || !complete {
		t.Fatalf("parse = %#v, %t, %v", spec, complete, err)
	}
	if spec.Managed || spec.Bootstrap || spec.Repository != "tomnagengast/network" {
		t.Fatalf("spec = %#v", spec)
	}
}

func TestParserRejectsUnsafeOrAmbiguousMetadataPermanently(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "repos", "tomnagengast")
	parser, err := NewParser("tomnagengast", root, nil)
	if err != nil {
		t.Fatalf("new parser: %v", err)
	}
	tests := []struct {
		name        string
		description string
	}{
		{name: "other owner", description: "GitHub Repo: other/cellar\nLocal Path: " + filepath.Join(root, "cellar")},
		{name: "wrong path", description: "GitHub Repo: tomnagengast/cellar\nLocal Path: " + filepath.Join(root, "other")},
		{name: "nested path", description: "GitHub Repo: tomnagengast/cellar\nLocal Path: " + filepath.Join(root, "nested", "cellar")},
		{name: "external URL", description: "GitHub Repo: tomnagengast/cellar\nLocal Path: " + filepath.Join(root, "cellar") + "\nCloud URL: https://cellar.example.com"},
		{name: "nested cloud host", description: "GitHub Repo: tomnagengast/cellar\nLocal Path: " + filepath.Join(root, "cellar") + "\nCloud URL: https://api.cellar.nags.cloud"},
		{name: "duplicate repo", description: "GitHub Repo: tomnagengast/cellar\nGitHub Repo: tomnagengast/other\nLocal Path: " + filepath.Join(root, "cellar")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parser.Parse(Request{ProjectID: "project-1", ProjectName: "Cellar", Description: test.description})
			if err == nil {
				t.Fatal("parse succeeded")
			}
			var classified interface{ Permanent() bool }
			if !errors.As(err, &classified) || !classified.Permanent() {
				t.Fatalf("error is not permanent: %v", err)
			}
		})
	}
}
