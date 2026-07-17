package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/repositories"
)

type repositoryProvisioner interface {
	Provision(context.Context, projectsetup.Spec) error
}

// RepositoryOnboarding owns project metadata admission and setup retries in
// the canonical repository artifact. It preserves the retained HTTP contract
// without a project-setup mirror or repository registrar.
type RepositoryOnboarding struct {
	store       *repositories.Store
	parser      *projectsetup.Parser
	provisioner repositoryProvisioner
	retryPoll   time.Duration
	logger      *slog.Logger
	now         func() time.Time
	wake        chan struct{}
	reconcileMu sync.Mutex
}

func NewRepositoryOnboarding(
	store *repositories.Store,
	parser *projectsetup.Parser,
	provisioner repositoryProvisioner,
	retryPoll time.Duration,
	logger *slog.Logger,
	now func() time.Time,
) (*RepositoryOnboarding, error) {
	if store == nil || parser == nil || provisioner == nil || retryPoll <= 0 || logger == nil || now == nil {
		return nil, errors.New("app repository onboarding: store, parser, provisioner, retry interval, logger, and clock are required")
	}
	return &RepositoryOnboarding{
		store: store, parser: parser, provisioner: provisioner, retryPoll: retryPoll,
		logger: logger, now: now, wake: make(chan struct{}, 1),
	}, nil
}

func (m *RepositoryOnboarding) Enqueue(_ context.Context, request projectsetup.Request) error {
	spec, complete, err := m.parser.Parse(request)
	if err != nil {
		return err
	}
	project := repositories.ProjectIdentity{ID: request.ProjectID, Name: request.ProjectName}
	if !complete {
		_, err := m.store.RecordAwaitingMetadata(project, m.now())
		return err
	}
	result, err := m.store.AdmitProject(repositories.ProjectAdmission{
		Project: project, Repository: spec.Repository, Origin: spec.RepoURL,
		LocalPath: spec.LocalPath, ManagedRoot: spec.ManagedRoot, CloudURL: spec.CloudURL,
		DefaultBranch: spec.BaseBranch, Bootstrap: spec.Bootstrap, Managed: spec.Managed,
	}, m.now())
	if err != nil {
		return err
	}
	if result.NeedsProvision {
		m.Notify()
	}
	return nil
}

func (m *RepositoryOnboarding) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *RepositoryOnboarding) Run(ctx context.Context) error {
	if _, err := m.store.RecoverSetups(m.now()); err != nil {
		return err
	}
	ticker := time.NewTicker(m.retryPoll)
	defer ticker.Stop()
	m.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.reconcile(ctx)
		case <-m.wake:
			m.reconcile(ctx)
		}
	}
}

func (m *RepositoryOnboarding) Reconcile(ctx context.Context) { m.reconcile(ctx) }

func (m *RepositoryOnboarding) reconcile(ctx context.Context) {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	for ctx.Err() == nil {
		record, found, err := m.store.ClaimSetup(m.now())
		if err != nil {
			m.logger.Error("claim repository setup", "error", err)
			return
		}
		if !found {
			return
		}
		spec := legacyProjectSpec(record)
		if err := m.provisioner.Provision(ctx, spec); err != nil {
			now := m.now().UTC()
			next := now.Add(repositoryRetryDelay(record.Setup.Attempts))
			if _, storeErr := m.store.FailSetup(record.Project.ID, err.Error(), next, now); storeErr != nil {
				m.logger.Error("record repository setup failure", "project_id", record.Project.ID, "error", storeErr)
				return
			}
			m.logger.Warn("repository setup pending retry", "project_id", record.Project.ID, "repository", record.Repository, "attempt", record.Setup.Attempts, "retry_at", next, "error", err)
			continue
		}
		if _, err := m.store.CompleteSetup(record.Project.ID, m.now()); err != nil {
			m.logger.Error("record repository setup completion", "project_id", record.Project.ID, "error", err)
			return
		}
		m.logger.Info("repository setup complete", "project_id", record.Project.ID, "repository", record.Repository, "local_path", record.LocalPath)
	}
}

func (m *RepositoryOnboarding) PublicSnapshot() projectsetup.PublicSnapshot {
	catalog, err := repositories.NewCatalog(m.store.Snapshot())
	if err != nil {
		return projectsetup.PublicSnapshot{}
	}
	snapshot := catalog.SetupSnapshot()
	return projectsetup.PublicSnapshot{
		Total: snapshot.Total, AwaitingMetadata: snapshot.AwaitingMetadata, Pending: snapshot.Pending,
		Running: snapshot.Running, Succeeded: snapshot.Succeeded, Failed: snapshot.Failed,
	}
}

func repositoryRetryDelay(attempt int) time.Duration {
	delay := 15 * time.Second
	for retry := 1; retry < attempt; retry++ {
		if delay >= 5*time.Minute {
			return 10 * time.Minute
		}
		delay *= 2
	}
	return min(delay, 10*time.Minute)
}
