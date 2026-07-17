package policy

import (
	"errors"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

var ErrAdmissionPending = errors.New("policy: admission decisions are incomplete")

// PendingAdmission reports whether an event still lacks a durable admission
// decision. The predicate must be read-only and must not call back into the
// Coordinator.
type PendingAdmission func() bool

// AdmissionCallback admits work against one immutable policy snapshot.
// Implementations must use the supplied snapshot throughout the decision and
// must not call Admit or a Coordinator mutation recursively.
type AdmissionCallback func(Snapshot) error

// Coordinator serializes policy mutation with admission. Its mutex is
// deliberately non-reentrant.
//
// Lock order is: a caller-owned keyed workflow lock, when present, then the
// Coordinator mutex, then the Store mutex, then downstream domain locks. Store
// methods release their mutex before returning. The pending predicate and the
// admission callback run with the Coordinator mutex held but no Store mutex
// held, so they may briefly acquire downstream read or journal locks without
// inverting the Store order. Neither may call Admit or a mutation method: doing
// so would attempt to reacquire the non-reentrant Coordinator mutex.
//
// Snapshot is the only exception to the Coordinator lock. It returns the
// Store's immutable snapshot directly and is suitable for independent reads.
type Coordinator struct {
	mu               sync.Mutex
	store            *Store
	pendingAdmission PendingAdmission
}

func NewCoordinator(store *Store, pendingAdmission PendingAdmission) (*Coordinator, error) {
	if store == nil || pendingAdmission == nil {
		return nil, errors.New("policy: coordinator dependencies are required")
	}
	return &Coordinator{store: store, pendingAdmission: pendingAdmission}, nil
}

// Snapshot returns an immutable Store snapshot without acquiring the
// Coordinator mutex. A caller needing a cross-domain admission view must use
// Admit instead.
func (c *Coordinator) Snapshot() Snapshot { return c.store.Snapshot() }

// Admit captures exactly one immutable policy snapshot while holding the
// Coordinator mutex and supplies it to callback. Policy mutation cannot begin
// until callback returns.
func (c *Coordinator) Admit(callback AdmissionCallback) error {
	if callback == nil {
		return errors.New("policy: admission callback is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return callback(c.store.Snapshot())
}

// UpdateSettings changes only the retained agent and runtime settings surface.
func (c *Coordinator) UpdateSettings(expected uint64, agents settings.AgentSettings, runtime settings.RuntimeSettings, now time.Time) (settings.Snapshot, error) {
	snapshot, err := c.mutate(func() (Snapshot, error) {
		candidate := c.store.Snapshot().Settings()
		candidate.Agents = agentSettingsFromSource(agents)
		candidate.Runtime = RuntimeSettings{MaxConcurrentRuns: runtime.MaxConcurrentRuns}
		return c.store.UpdateSettings(expected, candidate, now)
	})
	return SettingsView(snapshot), err
}

// UpdateRegistry preserves the registry revision and settings dependency used
// by the retained trigger API.
func (c *Coordinator) UpdateRegistry(expectedRegistry, expectedSettings uint64, candidate triggerregistry.Snapshot, now time.Time) (triggerregistry.Snapshot, error) {
	canonical := RegistryCandidate(candidate)
	snapshot, err := c.mutate(func() (Snapshot, error) {
		return c.store.UpdateRegistry(expectedRegistry, expectedSettings, canonical, now)
	})
	return RegistryView(snapshot), err
}

func (c *Coordinator) PublishWorkflow(expectedSettings, expectedWorkflow uint64, candidate workflow.Definition, now time.Time) (settings.Snapshot, error) {
	canonical := WorkflowCandidate(candidate)
	snapshot, err := c.mutate(func() (Snapshot, error) {
		return c.store.PublishWorkflow(expectedSettings, expectedWorkflow, canonical, now)
	})
	return SettingsView(snapshot), err
}

func (c *Coordinator) DeleteWorkflow(expectedSettings, expectedWorkflow uint64, id string, now time.Time) (settings.Snapshot, error) {
	snapshot, err := c.mutate(func() (Snapshot, error) {
		return c.store.DeleteWorkflow(expectedSettings, expectedWorkflow, id, now)
	})
	return SettingsView(snapshot), err
}

func (c *Coordinator) UpdateProtectedFeedback(expectedSettings uint64, workflowID string, now time.Time) (settings.Snapshot, error) {
	snapshot, err := c.mutate(func() (Snapshot, error) {
		return c.store.UpdateProtectedFeedback(expectedSettings, workflowID, now)
	})
	return SettingsView(snapshot), err
}

func (c *Coordinator) SetProject(expected uint64, projectID string, enabled bool, now time.Time) (taskcontrol.Snapshot, error) {
	snapshot, err := c.mutate(func() (Snapshot, error) {
		return c.store.SetProject(expected, projectID, enabled, now)
	})
	return TaskControlView(snapshot), err
}

func (c *Coordinator) mutate(apply func() (Snapshot, error)) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingAdmission() {
		return c.store.Snapshot(), ErrAdmissionPending
	}
	return apply()
}
