package projectsetup

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

type Registrar interface {
	SyncRepositories([]Spec) error
}

type Provisioner interface {
	Provision(context.Context, Spec) error
}

type Manager struct {
	store       *Store
	parser      *Parser
	registrar   Registrar
	provisioner Provisioner
	retryPoll   time.Duration
	logger      *slog.Logger
	now         func() time.Time
	notify      chan struct{}
	reconcileMu sync.Mutex
}

func NewManager(store *Store, parser *Parser, registrar Registrar, provisioner Provisioner, retryPoll time.Duration, logger *slog.Logger, now func() time.Time) (*Manager, error) {
	if store == nil || parser == nil || registrar == nil || provisioner == nil || retryPoll <= 0 || logger == nil || now == nil {
		return nil, errors.New("project setup manager: store, parser, registrar, provisioner, retry interval, logger, and clock are required")
	}
	return &Manager{
		store: store, parser: parser, registrar: registrar, provisioner: provisioner,
		retryPoll: retryPoll, logger: logger, now: now, notify: make(chan struct{}, 1),
	}, nil
}

func (m *Manager) Enqueue(_ context.Context, request Request) error {
	spec, complete, err := m.parser.Parse(request)
	if err != nil {
		return err
	}
	if !complete {
		return m.store.RecordIncomplete(request, m.now())
	}
	needsProvision, err := m.store.Upsert(spec, m.now())
	if err != nil {
		return err
	}
	if err := m.registrar.SyncRepositories(m.store.Specs()); err != nil {
		return err
	}
	if needsProvision {
		m.Notify()
	}
	return nil
}

func (m *Manager) Notify() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.retryPoll)
	defer ticker.Stop()
	m.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcile(ctx)
		case <-m.notify:
			m.reconcile(ctx)
		}
	}
}

func (m *Manager) Reconcile(ctx context.Context) {
	m.reconcile(ctx)
}

func (m *Manager) reconcile(ctx context.Context) {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	for ctx.Err() == nil {
		entry, found, err := m.store.Claim(m.now())
		if err != nil {
			m.logger.Error("claim project setup", "error", err)
			return
		}
		if !found {
			return
		}
		if err := m.provisioner.Provision(ctx, entry.Spec); err != nil {
			now := m.now().UTC()
			next := now.Add(retryDelay(entry.Attempts))
			if storeErr := m.store.Fail(entry.ProjectID, err.Error(), next, now); storeErr != nil {
				m.logger.Error("record project setup failure", "project_id", entry.ProjectID, "error", storeErr)
				return
			}
			m.logger.Warn("project setup pending retry", "project_id", entry.ProjectID, "repository", entry.Repository, "attempt", entry.Attempts, "retry_at", next, "error", err)
			continue
		}
		if err := m.store.Complete(entry.ProjectID, m.now()); err != nil {
			m.logger.Error("record project setup completion", "project_id", entry.ProjectID, "error", err)
			return
		}
		m.logger.Info("project setup complete", "project_id", entry.ProjectID, "repository", entry.Repository, "local_path", entry.LocalPath)
	}
}

func (m *Manager) PublicSnapshot() PublicSnapshot {
	return m.store.PublicSnapshot()
}

func retryDelay(attempt int) time.Duration {
	delay := 15 * time.Second
	for range max(attempt-1, 0) {
		if delay >= 5*time.Minute {
			return 10 * time.Minute
		}
		delay *= 2
	}
	return min(delay, 10*time.Minute)
}
