package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Component is one service-owned in-process lifecycle. Run must stay active
// until its context is canceled or it returns an operational error.
type Component struct {
	Name string
	Run  func(context.Context) error
}

// Supervisor starts every component, propagates the first operational failure
// through cancellation, and joins every component before returning. Durable
// worker tmux sessions are deliberately not Components because the Run owner,
// not the service process, reconciles their lifetime.
type Supervisor struct {
	components []Component
}

func NewSupervisor(components ...Component) (*Supervisor, error) {
	if len(components) == 0 {
		return nil, errors.New("app supervisor: at least one component is required")
	}
	seen := make(map[string]bool, len(components))
	for _, component := range components {
		if component.Name == "" || component.Run == nil || seen[component.Name] {
			return nil, errors.New("app supervisor: component names and runners must be unique")
		}
		seen[component.Name] = true
	}
	return &Supervisor{components: append([]Component(nil), components...)}, nil
}

func (s *Supervisor) Run(ctx context.Context) error {
	if s == nil || len(s.components) == 0 {
		return errors.New("app supervisor: unavailable")
	}
	groupContext, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(s.components))
	var started sync.WaitGroup
	started.Add(len(s.components))
	for _, component := range s.components {
		component := component
		go func() {
			started.Done()
			err := component.Run(groupContext)
			if err == nil && groupContext.Err() == nil {
				err = errors.New("stopped before service cancellation")
			}
			results <- result{name: component.Name, err: err}
		}()
	}
	started.Wait()

	var primary error
	for range s.components {
		result := <-results
		if result.err != nil && !errors.Is(result.err, context.Canceled) && primary == nil {
			primary = fmt.Errorf("app component %s: %w", result.name, result.err)
			cancel()
		}
	}
	return primary
}

// WaitForReady gates an advancing component behind recovery and activation.
// A closed ready channel starts the component; cancellation wins without a
// leaked waiter.
func WaitForReady(ctx context.Context, ready <-chan struct{}) error {
	if ready == nil {
		return errors.New("app supervisor: readiness gate is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ready:
		return nil
	}
}
