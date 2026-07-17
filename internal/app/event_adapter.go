package app

import (
	"errors"
	"sync"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/linearidentity"
	"github.com/tomnagengast/factory/internal/taskstore"
)

// ActivityAdapter preserves the HTTP activity shape while every mutation lands
// only in the canonical event owner's activity artifact and private corpus.
type ActivityAdapter struct{ store *eventwire.ActivityStore }

func NewActivityAdapter(store *eventwire.ActivityStore) (*ActivityAdapter, error) {
	if store == nil {
		return nil, errors.New("app activity adapter: canonical store is required")
	}
	return &ActivityAdapter{store: store}, nil
}

func (a *ActivityAdapter) Add(deliveryID string, event activity.Event) (bool, error) {
	return a.store.Add(deliveryID, canonicalActivityEvent(event))
}

func (a *ActivityAdapter) StagePayload(deliveryID string, payload []byte) error {
	return a.store.StagePayload(deliveryID, payload)
}

func (a *ActivityAdapter) AddStaged(deliveryID string, event activity.Event) (bool, error) {
	return a.store.AddStaged(deliveryID, canonicalActivityEvent(event))
}

func (a *ActivityAdapter) StagedPayload(deliveryID string) ([]byte, error) {
	return a.store.StagedPayload(deliveryID)
}

func (a *ActivityAdapter) Snapshot() activity.Snapshot {
	snapshot := a.store.Snapshot()
	events := make([]activity.Event, len(snapshot.Events))
	for index, record := range snapshot.Events {
		events[index] = activity.Event{Type: record.Type, Action: record.Action, ReceivedAt: record.ReceivedAt}
	}
	return activity.Snapshot{Total: snapshot.Total, Events: events}
}

func canonicalActivityEvent(event activity.Event) eventwire.ActivityEvent {
	return eventwire.ActivityEvent{Type: event.Type, Action: event.Action, ReceivedAt: event.ReceivedAt}
}

// LinearIdentityAdapter folds webhook and provider-discovered identity writes
// into the canonical task journal while retaining the API's conflict sentinel.
type LinearIdentityAdapter struct{ store *taskstore.Store }

func NewLinearIdentityAdapter(store *taskstore.Store) (*LinearIdentityAdapter, error) {
	if store == nil {
		return nil, errors.New("app Linear identity adapter: canonical task store is required")
	}
	return &LinearIdentityAdapter{store: store}, nil
}

func (a *LinearIdentityAdapter) Bind(identifier, uuid string) (bool, error) {
	added, err := a.store.BindLinearIdentity(identifier, uuid)
	if errors.Is(err, taskstore.ErrLinearIdentityConflict) {
		return added, linearidentity.ErrConflict
	}
	return added, err
}

// ProviderProjectionCursor replaces the deleted provider projection journals.
// The authoritative wire owns durable events and channel cursors; this small
// in-memory view exists only for the retained server dispatch interface.
type ProviderProjectionCursor struct {
	mu     sync.Mutex
	github uint64
	linear uint64
}

func NewProviderProjectionCursor(github, linear uint64) *ProviderProjectionCursor {
	return &ProviderProjectionCursor{github: github, linear: linear}
}

type GitHubProjection struct{ Cursor *ProviderProjectionCursor }

func (p GitHubProjection) Add(githubhook.Event) (bool, error) {
	return false, errors.New("app provider projection: sequence is required")
}

func (p GitHubProjection) AddAt(sequence uint64, _ githubhook.Event) (bool, error) {
	if p.Cursor == nil || sequence == 0 {
		return false, errors.New("app provider projection: GitHub cursor is invalid")
	}
	p.Cursor.mu.Lock()
	defer p.Cursor.mu.Unlock()
	if sequence <= p.Cursor.github {
		return false, nil
	}
	p.Cursor.github = sequence
	return true, nil
}

func (p GitHubProjection) Total() uint64 {
	if p.Cursor == nil {
		return 0
	}
	p.Cursor.mu.Lock()
	defer p.Cursor.mu.Unlock()
	return p.Cursor.github
}

type LinearProjection struct{ Cursor *ProviderProjectionCursor }

func (p LinearProjection) Add(linearhook.Event) (bool, error) {
	return false, errors.New("app provider projection: sequence is required")
}

func (p LinearProjection) AddAt(sequence uint64, _ linearhook.Event) (bool, error) {
	if p.Cursor == nil || sequence == 0 {
		return false, errors.New("app provider projection: Linear cursor is invalid")
	}
	p.Cursor.mu.Lock()
	defer p.Cursor.mu.Unlock()
	if sequence <= p.Cursor.linear {
		return false, nil
	}
	p.Cursor.linear = sequence
	return true, nil
}

func (p LinearProjection) Total() uint64 {
	if p.Cursor == nil {
		return 0
	}
	p.Cursor.mu.Lock()
	defer p.Cursor.mu.Unlock()
	return p.Cursor.linear
}
