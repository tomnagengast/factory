package app

import (
	"context"
	"errors"
	"time"
)

type boundedRunReconciler interface {
	Reconcile(context.Context)
}

// RunService owns the canonical Run manager's clock and wake channel. Notify
// is edge-triggered and nonblocking: one pending wake is sufficient because a
// reconcile pass derives all work from the durable Run journal.
type RunService struct {
	manager  boundedRunReconciler
	interval time.Duration
	wake     chan struct{}
}

func NewRunService(manager boundedRunReconciler, interval time.Duration) (*RunService, error) {
	if manager == nil || interval <= 0 {
		return nil, errors.New("app Run service: manager and positive interval are required")
	}
	return &RunService{manager: manager, interval: interval, wake: make(chan struct{}, 1)}, nil
}

func (s *RunService) Notify() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *RunService) Reconcile(ctx context.Context) { s.manager.Reconcile(ctx) }

func (s *RunService) Run(ctx context.Context) error {
	if s == nil || s.manager == nil || s.interval <= 0 || s.wake == nil {
		return errors.New("app Run service: unavailable")
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.manager.Reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.manager.Reconcile(ctx)
		case <-s.wake:
			s.manager.Reconcile(ctx)
		}
	}
}
