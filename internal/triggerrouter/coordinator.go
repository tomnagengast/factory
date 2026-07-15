package triggerrouter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

type RegistryStore interface {
	Snapshot() triggerregistry.Snapshot
	MarkLegacyRollbackIncompatible(time.Time) (triggerregistry.Snapshot, error)
}

type SettingsStore interface {
	Snapshot() settings.Snapshot
	MarkWorkflowRollbackIncompatible(time.Time) (settings.Snapshot, error)
}

type registryMutationStore interface {
	Update(uint64, triggerregistry.Snapshot, settings.Snapshot, time.Time) (triggerregistry.Snapshot, error)
}

type settingsMutationStore interface {
	Update(uint64, settings.Snapshot, time.Time) (settings.Snapshot, error)
}

var ErrPolicyConflict = errors.New("trigger router: coordinated policy conflict")
var ErrPolicyPending = errors.New("trigger router: pending event admission")
var ErrPolicyValidation = errors.New("trigger router: coordinated policy validation")

type CoordinatedWire struct {
	policy   sync.Mutex
	events   *eventwire.Wire
	registry RegistryStore
	settings SettingsStore
	routing  *Store
	now      func() time.Time
}

func NewCoordinatedWire(events *eventwire.Wire, registry RegistryStore, configuration SettingsStore, routing *Store, now func() time.Time) (*CoordinatedWire, error) {
	if events == nil || registry == nil || configuration == nil || routing == nil || now == nil {
		return nil, errors.New("trigger router: coordinated wire dependencies are required")
	}
	wire := &CoordinatedWire{events: events, registry: registry, settings: configuration, routing: routing, now: now}
	if err := events.HandleBatch(wire.admit); err != nil {
		return nil, err
	}
	return wire, nil
}

func (w *CoordinatedWire) Handle(filter eventwire.Filter, handler eventwire.Handler) error {
	return w.events.Handle(filter, handler)
}

func (w *CoordinatedWire) Publish(ctx context.Context, event eventwire.Event) (eventwire.Record, bool, error) {
	w.policy.Lock()
	defer w.policy.Unlock()
	record, added, err := w.events.Publish(ctx, event)
	if err == nil {
		err = w.routing.Prune(w.events.RetainedEventIDs())
	}
	return record, added, err
}

func (w *CoordinatedWire) PublishBatch(ctx context.Context, events []eventwire.Event) ([]eventwire.Record, error) {
	w.policy.Lock()
	defer w.policy.Unlock()
	records, err := w.events.PublishBatch(ctx, events)
	if err == nil {
		err = w.routing.Prune(w.events.RetainedEventIDs())
	}
	return records, err
}

func (w *CoordinatedWire) CatchUp(ctx context.Context) error {
	w.policy.Lock()
	defer w.policy.Unlock()
	if err := w.events.CatchUp(ctx); err != nil {
		return err
	}
	return w.routing.Prune(w.events.RetainedEventIDs())
}

func (w *CoordinatedWire) Status() eventwire.Status { return w.events.Status() }

func (w *CoordinatedWire) Query(query eventwire.Query) (eventwire.Page, error) {
	return w.events.Query(query)
}

func (w *CoordinatedWire) Record(sequence uint64) (eventwire.Record, bool) {
	return w.events.Record(sequence)
}

func (w *CoordinatedWire) RegistrySnapshot() triggerregistry.Snapshot { return w.registry.Snapshot() }

func (w *CoordinatedWire) SettingsSnapshot() settings.Snapshot { return w.settings.Snapshot() }

func (w *CoordinatedWire) RoutingSnapshot() Snapshot { return w.routing.Snapshot() }

func (w *CoordinatedWire) UpdateRegistry(expectedRegistry, expectedSettings uint64, candidate triggerregistry.Snapshot, now time.Time) (triggerregistry.Snapshot, error) {
	w.policy.Lock()
	defer w.policy.Unlock()
	if !w.pendingDecisionsComplete() {
		return w.registry.Snapshot(), ErrPolicyPending
	}
	configuration := w.settings.Snapshot()
	if configuration.Revision != expectedSettings {
		return w.registry.Snapshot(), ErrPolicyConflict
	}
	store, ok := w.registry.(registryMutationStore)
	if !ok {
		return w.registry.Snapshot(), errors.New("trigger router: registry is read-only")
	}
	return store.Update(expectedRegistry, candidate, configuration, now)
}

func (w *CoordinatedWire) UpdateSettings(expected uint64, candidate settings.Snapshot, now time.Time) (settings.Snapshot, error) {
	w.policy.Lock()
	defer w.policy.Unlock()
	if !w.pendingDecisionsComplete() {
		return w.settings.Snapshot(), ErrPolicyPending
	}
	if err := w.registry.Snapshot().Validate(candidate); err != nil {
		return w.settings.Snapshot(), fmt.Errorf("%w: %v", ErrPolicyValidation, err)
	}
	store, ok := w.settings.(settingsMutationStore)
	if !ok {
		return w.settings.Snapshot(), errors.New("trigger router: settings are read-only")
	}
	return store.Update(expected, candidate, now)
}

func (w *CoordinatedWire) UpdateAgentSettings(expected uint64, agents settings.AgentSettings, runtime settings.RuntimeSettings, now time.Time) (settings.Snapshot, error) {
	candidate := w.settings.Snapshot()
	candidate.Agents = agents
	candidate.Runtime = runtime
	return w.UpdateSettings(expected, candidate, now)
}

func (w *CoordinatedWire) PublishWorkflow(expectedPolicy, expectedWorkflow uint64, candidate workflow.Definition, now time.Time) (settings.Snapshot, error) {
	w.policy.Lock()
	defer w.policy.Unlock()
	if !w.pendingDecisionsComplete() {
		return w.settings.Snapshot(), ErrPolicyPending
	}
	current := w.settings.Snapshot()
	if current.Revision != expectedPolicy || candidate.Revision != expectedWorkflow {
		return current, ErrPolicyConflict
	}
	index := -1
	for i, definition := range current.Workflows {
		if definition.ID == candidate.ID {
			index = i
			break
		}
	}
	if (index < 0 && expectedWorkflow != 0) || (index >= 0 && current.Workflows[index].Revision != expectedWorkflow) {
		return current, ErrPolicyConflict
	}
	next := current.Clone()
	candidate.Markdown = workflow.CanonicalizeMarkdown(candidate.Markdown)
	candidate.Revision = expectedWorkflow + 1
	candidate.UpdatedAt = now.UTC()
	if index < 0 {
		next.Workflows = append(next.Workflows, candidate)
	} else {
		next.Workflows[index] = candidate
	}
	if err := validateWorkflowPolicy(next, w.registry.Snapshot(), "publish"); err != nil {
		return current, err
	}
	store, ok := w.settings.(settingsMutationStore)
	if !ok {
		return current, errors.New("trigger router: settings are read-only")
	}
	return store.Update(expectedPolicy, next, now)
}

func (w *CoordinatedWire) DeleteWorkflow(expectedPolicy, expectedWorkflow uint64, id string, now time.Time) (settings.Snapshot, error) {
	w.policy.Lock()
	defer w.policy.Unlock()
	if !w.pendingDecisionsComplete() {
		return w.settings.Snapshot(), ErrPolicyPending
	}
	current := w.settings.Snapshot()
	if current.Revision != expectedPolicy {
		return current, ErrPolicyConflict
	}
	index := -1
	for i, definition := range current.Workflows {
		if definition.ID == id && definition.Revision == expectedWorkflow {
			index = i
			break
		}
	}
	if index < 0 {
		return current, ErrPolicyConflict
	}
	if workflowReferenced(id, current, w.registry.Snapshot()) {
		return current, fmt.Errorf("%w: workflow %q is referenced", ErrPolicyValidation, id)
	}
	next := current.Clone()
	next.Workflows = slices.Delete(next.Workflows, index, index+1)
	if err := validateWorkflowPolicy(next, w.registry.Snapshot(), "delete"); err != nil {
		return current, err
	}
	store, ok := w.settings.(settingsMutationStore)
	if !ok {
		return current, errors.New("trigger router: settings are read-only")
	}
	return store.Update(expectedPolicy, next, now)
}

func (w *CoordinatedWire) UpdateProtectedFeedback(expectedPolicy uint64, workflowID string, now time.Time) (settings.Snapshot, error) {
	w.policy.Lock()
	defer w.policy.Unlock()
	if !w.pendingDecisionsComplete() {
		return w.settings.Snapshot(), ErrPolicyPending
	}
	current := w.settings.Snapshot()
	if current.Revision != expectedPolicy {
		return current, ErrPolicyConflict
	}
	next := current.Clone()
	next.ProtectedWorkflows.LinearFeedback.WorkflowID = workflowID
	if err := validateWorkflowPolicy(next, w.registry.Snapshot(), "protected feedback update"); err != nil {
		return current, err
	}
	store, ok := w.settings.(settingsMutationStore)
	if !ok {
		return current, errors.New("trigger router: settings are read-only")
	}
	return store.Update(expectedPolicy, next, now)
}

func validateWorkflowPolicy(configuration settings.Snapshot, registry triggerregistry.Snapshot, operation string) error {
	if err := configuration.Validate(); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrPolicyValidation, operation, err)
	}
	if err := registry.Validate(configuration); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrPolicyValidation, operation, err)
	}
	return nil
}

func workflowReferenced(id string, configuration settings.Snapshot, registry triggerregistry.Snapshot) bool {
	if configuration.ProtectedWorkflows.LinearFeedback.WorkflowID == id {
		return true
	}
	for _, rule := range registry.Rules {
		if rule.WorkflowID == id {
			return true
		}
	}
	return false
}

func (w *CoordinatedWire) pendingDecisionsComplete() bool {
	status := w.events.Status()
	for sequence := status.Dispatched + 1; sequence <= status.Total; sequence++ {
		record, found := w.events.Record(sequence)
		if !found || !w.routing.HasDecision(record.Event.ID, record.Sequence) {
			return false
		}
	}
	return true
}

func (w *CoordinatedWire) admit(_ context.Context, records []eventwire.Record) error {
	registry := w.registry.Snapshot()
	configuration := w.settings.Snapshot()
	if mayAdmitMarkdown(records, registry, configuration) {
		var err error
		configuration, err = w.settings.MarkWorkflowRollbackIncompatible(w.now())
		if err != nil {
			return err
		}
	}
	for _, record := range records {
		if record.Event.Source != eventwire.SourceLinear && record.Event.Source != eventwire.SourceGitHub && record.Event.Source != eventwire.SourceFactory {
			var err error
			registry, err = w.registry.MarkLegacyRollbackIncompatible(w.now())
			if err != nil {
				return err
			}
			break
		}
	}
	_, err := w.routing.ApplyDecisionBatch(records, registry, configuration, w.now())
	return err
}

func mayAdmitMarkdown(records []eventwire.Record, registry triggerregistry.Snapshot, configuration settings.Snapshot) bool {
	for _, record := range records {
		for _, rule := range registry.Rules {
			if !rule.Enabled || !rule.Filter.Matches(record.Event) {
				continue
			}
			definition, found := configuration.Workflow(rule.WorkflowID)
			if found && definition.Enabled && definition.Revision > 0 {
				return true
			}
		}
	}
	return false
}
