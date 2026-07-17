package app

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/server"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/triggerrouter"
)

var (
	_ server.SettingsStore = (*PolicyAdapter)(nil)
	_ server.TriggerPolicy = (*PolicyAdapter)(nil)
	_ taskservice.Policy   = (*PolicyAdapter)(nil)
	_ taskservice.Control  = TaskControlAdapter{}
)

func TestPolicyAdapterPreservesViewsMutationsAndConflictContracts(t *testing.T) {
	pending := false
	adapter := newPolicyAdapterFixture(t, func() bool { return pending })
	before := adapter.Snapshot()
	candidate := before.Clone()
	candidate.Runtime.MaxConcurrentRuns++
	now := time.Date(2026, time.July, 17, 5, 0, 0, 0, time.UTC)
	updated, err := adapter.UpdateAgentSettings(before.Revision, candidate.Agents, candidate.Runtime, now)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != before.Revision+1 || updated.Runtime != candidate.Runtime || adapter.SettingsSnapshot().Revision != updated.Revision {
		t.Fatalf("settings view did not advance: before=%#v updated=%#v", before, updated)
	}
	if _, err := adapter.Update(before.Revision, candidate, now); !errors.Is(err, settings.ErrRevisionConflict) {
		t.Fatalf("retained settings conflict = %v", err)
	}
	if _, err := adapter.UpdateAgentSettings(before.Revision, candidate.Agents, candidate.Runtime, now); !errors.Is(err, triggerrouter.ErrPolicyConflict) {
		t.Fatalf("policy conflict = %v", err)
	}
	pending = true
	if _, err := adapter.UpdateAgentSettings(updated.Revision, updated.Agents, updated.Runtime, now.Add(time.Minute)); !errors.Is(err, triggerrouter.ErrPolicyPending) {
		t.Fatalf("pending admission error = %v", err)
	}
}

func TestTaskControlAdapterUsesCanonicalPolicyRevision(t *testing.T) {
	adapter := newPolicyAdapterFixture(t, func() bool { return false })
	control := TaskControlAdapter{Policy: adapter}
	before := control.Snapshot()
	updated, err := control.SetProject(before.Revision, "project-factory", true, time.Date(2026, time.July, 17, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !control.Enabled("project-factory") || updated.Revision != before.Revision+1 {
		t.Fatalf("canonical task activation did not advance: %#v", updated)
	}
	if _, err := control.SetProject(before.Revision, "project-other", true, time.Now().UTC()); !errors.Is(err, taskcontrol.ErrRevisionConflict) {
		t.Fatalf("task-control conflict = %v", err)
	}
}

func newPolicyAdapterFixture(t *testing.T, pending policy.PendingAdmission) *PolicyAdapter {
	t.Helper()
	snapshot, err := policy.ConvertSources(policy.Sources{
		Settings: settings.Defaults(3), TaskControl: taskcontrol.Snapshot{
			Version: 1, EnabledProjectIDs: []string{},
		}, TriggerActorID: "actor-tom",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := policy.Create(filepath.Join(t.TempDir(), "policy.json"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := policy.NewCoordinator(store, pending)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewPolicyAdapter(coordinator, func() triggerrouter.Snapshot {
		return triggerrouter.Snapshot{Schema: 1, Decisions: []triggerrouter.Decision{}, Invocations: []triggerrouter.Invocation{}, RateBuckets: []triggerrouter.RateBucket{}}
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}
