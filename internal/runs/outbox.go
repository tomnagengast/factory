package runs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/tomnagengast/factory/internal/eventwire"
)

// OutboxWire is the narrow publication and dispatch-status capability the
// dormant OutboxCollector needs. It is already satisfied by *eventwire.Wire.
type OutboxWire interface {
	Publish(context.Context, eventwire.Event) (eventwire.Record, bool, error)
	Status() eventwire.Status
}

var _ OutboxWire = (*eventwire.Wire)(nil)

// OutboxCollector owns publication and recovery for the Run-transition outbox.
// It republishes the deterministic body-free lifecycle event for every pending
// delivery, persists the authoritative wire sequence, and acknowledges only a
// contiguous published prefix that the wire reports globally dispatched. It
// never cancels a Run transition event. It is dormant: no production caller
// constructs it, so publication cannot begin before an explicit Phase 4 cutover.
type OutboxCollector struct {
	store *Store
	wire  OutboxWire
}

func NewOutboxCollector(store *Store, wire OutboxWire) (*OutboxCollector, error) {
	if store == nil {
		return nil, errors.New("runs: outbox collector store is required")
	}
	if wire == nil {
		return nil, errors.New("runs: outbox collector wire is required")
	}
	return &OutboxCollector{store: store, wire: wire}, nil
}

// Deliver publishes every pending transition delivery, then acknowledges the
// contiguous published prefix at or below the global dispatched cursor. It is
// idempotent across restarts: a pending delivery republishes the exact
// deterministic event, and a published delivery keeps its authoritative
// sequence rather than inventing a new one.
func (c *OutboxCollector) Deliver(ctx context.Context) error {
	if err := c.publishPending(ctx); err != nil {
		return err
	}
	return c.acknowledgeDispatched()
}

func (c *OutboxCollector) publishPending(ctx context.Context) error {
	snapshot, err := c.store.Snapshot()
	if err != nil {
		return err
	}
	model := snapshot.Model()
	for _, run := range model.Runs {
		for offset, delivery := range run.TransitionDeliveries {
			if delivery.State != DeliveryPending {
				continue
			}
			transition := run.Transitions[run.DeliveredThrough+offset]
			record, _, publishErr := c.wire.Publish(ctx, TransitionEvent(run, transition))
			// When the wire returns an authoritative record, persist the
			// publication evidence before surfacing any dispatch error so a
			// crash cannot lose the sequence and republish under a new one.
			if record.Sequence != 0 {
				if err := c.store.RecordPublication(run.ID, delivery.TransitionID, record.Sequence); err != nil {
					return err
				}
			}
			if publishErr != nil {
				return fmt.Errorf("runs: publish transition delivery %s: %w", delivery.TransitionID, publishErr)
			}
		}
	}
	return nil
}

func (c *OutboxCollector) acknowledgeDispatched() error {
	snapshot, err := c.store.Snapshot()
	if err != nil {
		return err
	}
	model := snapshot.Model()
	dispatched := c.wire.Status().Dispatched
	for _, run := range model.Runs {
		count := 0
		for _, delivery := range run.TransitionDeliveries {
			if delivery.State != DeliveryPublished || delivery.Sequence > dispatched {
				break
			}
			count++
		}
		if count > 0 {
			if err := c.store.AcknowledgeDeliveries(run.ID, count); err != nil {
				return err
			}
		}
	}
	return nil
}

// TransitionEvent builds the exact body-free legacy-compatible lifecycle event
// for one Run transition. Its identity and metadata match the retired
// agentrun collector so retained wire decoding does not change. A derived Run
// carries its causation as a derived event; a rootless (hop-zero) migrated
// direct Run stays a valid direct event with no parent identity.
func TransitionEvent(run Run, transition LifecycleTransition) eventwire.Event {
	event := eventwire.Event{
		ID:      RunTransitionEventID(transition.ID),
		Source:  eventwire.SourceFactory,
		Type:    "agent-run",
		Action:  string(transition.State),
		Subject: run.Causation.Task.Identifier,
		Attributes: map[string][]string{
			"runId":                       {run.ID},
			"attempts":                    {strconv.Itoa(transition.Attempts)},
			"taskSource":                  {string(run.Causation.Task.Source)},
			"taskProviderId":              {run.Causation.Task.ProviderID},
			"taskIdentifier":              {run.Causation.Task.Identifier},
			eventwire.AttributeProducer:   {"agent-collector"},
			eventwire.AttributeProvenance: {"factory"},
		},
		ReceivedAt: transition.At,
	}
	if run.Causation.Hop > 0 {
		event.RootEventID = run.Causation.RootEventID
		event.ParentInvocationID = run.Causation.AdmissionID
		event.ParentRunID = run.ID
		event.Hop = run.Causation.Hop
		event.AncestorRuleIDs = slices.Clone(run.Causation.AncestorRuleIDs)
	}
	return event
}
