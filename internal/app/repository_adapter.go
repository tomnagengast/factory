package app

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/repositories"
)

// RepositoryAdapter creates read-only compatibility projections directly from
// the canonical repository store. It never caches a second authority, so an
// admitted onboarding transition is visible on the next operation.
type RepositoryAdapter struct{ store *repositories.Store }

func NewRepositoryAdapter(store *repositories.Store) (*RepositoryAdapter, error) {
	if store == nil {
		return nil, errors.New("app repository adapter: canonical store is required")
	}
	return &RepositoryAdapter{store: store}, nil
}

func (a *RepositoryAdapter) ResolveRepository(repository string) (agentrun.RepositoryConfig, error) {
	record, found, err := a.record(repository)
	if err != nil {
		return agentrun.RepositoryConfig{}, err
	}
	if !found {
		return agentrun.RepositoryConfig{}, fmt.Errorf("repository catalog: %s is not allowlisted", repository)
	}
	return legacyRepositoryConfig(record), nil
}

func (a *RepositoryAdapter) ResolveSucceeded(projectID string) (projectsetup.Spec, error) {
	catalog, err := a.catalog()
	if err != nil {
		return projectsetup.Spec{}, err
	}
	record, err := catalog.ResolveProjectID(projectID)
	if err != nil {
		return projectsetup.Spec{}, err
	}
	if !record.Routable() {
		return projectsetup.Spec{}, fmt.Errorf("project setup: project %s is not successfully coordinated", projectID)
	}
	return legacyProjectSpec(record), nil
}

func (a *RepositoryAdapter) Choices() []projectsetup.Choice {
	catalog, err := a.catalog()
	if err != nil {
		return []projectsetup.Choice{}
	}
	canonical := catalog.Choices()
	choices := make([]projectsetup.Choice, len(canonical))
	for index, choice := range canonical {
		choices[index] = projectsetup.Choice{ProjectID: choice.ProjectID, ProjectName: choice.ProjectName, Repository: choice.Repository}
	}
	return choices
}

func (a *RepositoryAdapter) Configs() ([]agentrun.RepositoryConfig, error) {
	catalog, err := a.catalog()
	if err != nil {
		return nil, err
	}
	state := catalog.Snapshot()
	configs := make([]agentrun.RepositoryConfig, len(state.Records))
	for index, record := range state.Records {
		configs[index] = legacyRepositoryConfig(record)
	}
	return configs, nil
}

func (a *RepositoryAdapter) record(repository string) (repositories.Record, bool, error) {
	catalog, err := a.catalog()
	if err != nil {
		return repositories.Record{}, false, err
	}
	record, found := catalog.Record(repository)
	return record, found, nil
}

func (a *RepositoryAdapter) catalog() (*repositories.Catalog, error) {
	return repositories.NewCatalog(a.store.Snapshot())
}

func legacyRepositoryConfig(record repositories.Record) agentrun.RepositoryConfig {
	return agentrun.RepositoryConfig{
		App: record.App, Repository: record.Repository, RepoURL: record.Origin,
		RepoPath: record.ManagedPath, ManagedRoot: record.ManagedRoot, ProjectPath: record.LocalPath,
		BaseBranch: record.DefaultBranch, Bootstrap: record.Bootstrap, CloudURL: record.CloudURL,
		ReceiptPath: record.Deployment.ReceiptPath, PendingReceipt: record.Deployment.PendingReceiptPath,
		HealthURL: record.Deployment.HealthURL, SourcePath: record.Deployment.SourcePath,
	}
}

func legacyProjectSpec(record repositories.Record) projectsetup.Spec {
	return projectsetup.Spec{
		ProjectID: record.Project.ID, ProjectName: record.Project.Name, Repository: record.Repository,
		RepoURL: record.Origin, LocalPath: record.LocalPath, ManagedRoot: filepath.Dir(record.LocalPath),
		CloudURL: record.CloudURL, BaseBranch: record.DefaultBranch, Bootstrap: record.Bootstrap, Managed: record.Bootstrap,
	}
}
