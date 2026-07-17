package app

import (
	"errors"
	"time"

	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

// PolicyAdapter exposes the retained HTTP and task-service contracts as
// non-owning views over the one canonical policy coordinator. Routing is a
// read-only projection supplied by the canonical Run owner.
type PolicyAdapter struct {
	coordinator *policy.Coordinator
	routing     func() triggerrouter.Snapshot
}

func NewPolicyAdapter(coordinator *policy.Coordinator, routing func() triggerrouter.Snapshot) (*PolicyAdapter, error) {
	if coordinator == nil || routing == nil {
		return nil, errors.New("app policy adapter: coordinator and routing projection are required")
	}
	return &PolicyAdapter{coordinator: coordinator, routing: routing}, nil
}

func (a *PolicyAdapter) Snapshot() settings.Snapshot {
	return policy.SettingsView(a.coordinator.Snapshot())
}

func (a *PolicyAdapter) SettingsSnapshot() settings.Snapshot { return a.Snapshot() }

func (a *PolicyAdapter) RegistrySnapshot() triggerregistry.Snapshot {
	return policy.RegistryView(a.coordinator.Snapshot())
}

func (a *PolicyAdapter) RoutingSnapshot() triggerrouter.Snapshot { return a.routing().Clone() }

func (a *PolicyAdapter) Update(expected uint64, candidate settings.Snapshot, now time.Time) (settings.Snapshot, error) {
	updated, err := a.coordinator.UpdateSettings(expected, candidate.Agents, candidate.Runtime, now)
	return updated, mapSettingsError(err)
}

func (a *PolicyAdapter) UpdateSettings(expected uint64, candidate settings.Snapshot, now time.Time) (settings.Snapshot, error) {
	return a.UpdateAgentSettings(expected, candidate.Agents, candidate.Runtime, now)
}

func (a *PolicyAdapter) UpdateAgentSettings(expected uint64, agents settings.AgentSettings, runtime settings.RuntimeSettings, now time.Time) (settings.Snapshot, error) {
	updated, err := a.coordinator.UpdateSettings(expected, agents, runtime, now)
	return updated, mapPolicyMutationError(err)
}

func (a *PolicyAdapter) UpdateRegistry(expectedRegistry, expectedSettings uint64, candidate triggerregistry.Snapshot, now time.Time) (triggerregistry.Snapshot, error) {
	updated, err := a.coordinator.UpdateRegistry(expectedRegistry, expectedSettings, candidate, now)
	return updated, mapPolicyMutationError(err)
}

func (a *PolicyAdapter) PublishWorkflow(expectedSettings, expectedWorkflow uint64, candidate workflow.Definition, now time.Time) (settings.Snapshot, error) {
	updated, err := a.coordinator.PublishWorkflow(expectedSettings, expectedWorkflow, candidate, now)
	return updated, mapPolicyMutationError(err)
}

func (a *PolicyAdapter) DeleteWorkflow(expectedSettings, expectedWorkflow uint64, id string, now time.Time) (settings.Snapshot, error) {
	updated, err := a.coordinator.DeleteWorkflow(expectedSettings, expectedWorkflow, id, now)
	return updated, mapPolicyMutationError(err)
}

func (a *PolicyAdapter) UpdateProtectedFeedback(expectedSettings uint64, workflowID string, now time.Time) (settings.Snapshot, error) {
	updated, err := a.coordinator.UpdateProtectedFeedback(expectedSettings, workflowID, now)
	return updated, mapPolicyMutationError(err)
}

func (a *PolicyAdapter) Enabled(projectID string) bool {
	return taskcontrolEnabled(policy.TaskControlView(a.coordinator.Snapshot()), projectID)
}

func (a *PolicyAdapter) ControlSnapshot() taskcontrol.Snapshot {
	return policy.TaskControlView(a.coordinator.Snapshot())
}

// SnapshotControl is named separately because SettingsStore and taskservice's
// Control interface both use Snapshot with incompatible return types. The
// narrow task adapter below supplies the exact Control method set.
func (a *PolicyAdapter) SetProject(expected uint64, projectID string, enabled bool, now time.Time) (taskcontrol.Snapshot, error) {
	updated, err := a.coordinator.SetProject(expected, projectID, enabled, now)
	if errors.Is(err, policy.ErrTaskControlConflict) {
		return updated, taskcontrol.ErrRevisionConflict
	}
	return updated, err
}

type TaskControlAdapter struct{ Policy *PolicyAdapter }

func (a TaskControlAdapter) Enabled(projectID string) bool  { return a.Policy.Enabled(projectID) }
func (a TaskControlAdapter) Snapshot() taskcontrol.Snapshot { return a.Policy.ControlSnapshot() }
func (a TaskControlAdapter) SetProject(expected uint64, projectID string, enabled bool, now time.Time) (taskcontrol.Snapshot, error) {
	return a.Policy.SetProject(expected, projectID, enabled, now)
}

func taskcontrolEnabled(snapshot taskcontrol.Snapshot, projectID string) bool {
	for _, enabled := range snapshot.EnabledProjectIDs {
		if enabled == projectID {
			return true
		}
	}
	return false
}

func mapSettingsError(err error) error {
	if errors.Is(err, policy.ErrSettingsConflict) {
		return settings.ErrRevisionConflict
	}
	return err
}

func mapPolicyMutationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, policy.ErrAdmissionPending):
		return triggerrouter.ErrPolicyPending
	case errors.Is(err, policy.ErrSettingsConflict), errors.Is(err, policy.ErrRegistryConflict),
		errors.Is(err, policy.ErrWorkflowConflict), errors.Is(err, policy.ErrTaskControlConflict):
		return triggerrouter.ErrPolicyConflict
	default:
		return errors.Join(triggerrouter.ErrPolicyValidation, err)
	}
}
