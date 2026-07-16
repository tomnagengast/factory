package projectsetup

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreChoicesAndResolutionRequireSucceededAdmission(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(filepath.Join(directory, "projects.json"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	succeeded := Spec{
		ProjectID: "project-factory", ProjectName: "Factory", Repository: "tomnagengast/factory",
		RepoURL: "git@github.com:tomnagengast/factory.git", LocalPath: filepath.Join(directory, "factory"), ManagedRoot: directory,
		BaseBranch: "main", Managed: false, Bootstrap: false,
	}
	if _, err := store.Upsert(succeeded, time.Now()); err != nil {
		t.Fatal(err)
	}
	pending := succeeded
	pending.ProjectID, pending.ProjectName, pending.Repository = "project-new", "New", "tomnagengast/new"
	pending.RepoURL, pending.LocalPath, pending.Managed, pending.Bootstrap = "git@github.com:tomnagengast/new.git", filepath.Join(directory, "new"), true, true
	if _, err := store.Upsert(pending, time.Now()); err != nil {
		t.Fatal(err)
	}
	choices := store.Choices()
	if len(choices) != 1 || choices[0].ProjectID != succeeded.ProjectID || choices[0].Repository != succeeded.Repository {
		t.Fatalf("choices = %#v", choices)
	}
	resolved, err := store.ResolveSucceeded(succeeded.ProjectID)
	if err != nil || resolved != succeeded {
		t.Fatalf("resolved = %#v err=%v", resolved, err)
	}
	if _, err := store.ResolveSucceeded(pending.ProjectID); err == nil {
		t.Fatal("pending project resolved as admitted")
	}
}
