package triggerrouter

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

type RegistryStore interface {
	Snapshot() triggerregistry.Snapshot
	MarkLegacyRollbackIncompatible(time.Time) (triggerregistry.Snapshot, error)
}

type SettingsStore interface {
	Snapshot() settings.Snapshot
}

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
	return w.events.Publish(ctx, event)
}

func (w *CoordinatedWire) PublishBatch(ctx context.Context, events []eventwire.Event) ([]eventwire.Record, error) {
	w.policy.Lock()
	defer w.policy.Unlock()
	return w.events.PublishBatch(ctx, events)
}

func (w *CoordinatedWire) CatchUp(ctx context.Context) error {
	w.policy.Lock()
	defer w.policy.Unlock()
	return w.events.CatchUp(ctx)
}

func (w *CoordinatedWire) Status() eventwire.Status { return w.events.Status() }

func (w *CoordinatedWire) Query(query eventwire.Query) (eventwire.Page, error) {
	return w.events.Query(query)
}

func (w *CoordinatedWire) Record(sequence uint64) (eventwire.Record, bool) {
	return w.events.Record(sequence)
}

func (w *CoordinatedWire) admit(_ context.Context, records []eventwire.Record) error {
	registry := w.registry.Snapshot()
	configuration := w.settings.Snapshot()
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
