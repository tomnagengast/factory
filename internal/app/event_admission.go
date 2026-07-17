package app

import (
	"context"
	"errors"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/runs"
)

// EventAdmission is the one event-to-Run decision owner. It registers as a
// batch handler, so every durable decision exists before retained HTTP routes
// project the same wire records into activity and provider views.
type EventAdmission struct {
	coordinator *policy.Coordinator
	admitter    *runs.Admitter
	notify      func()
	now         func() time.Time
}

func NewEventAdmission(coordinator *policy.Coordinator, admitter *runs.Admitter, notify func(), now func() time.Time) (*EventAdmission, error) {
	if coordinator == nil || admitter == nil || notify == nil || now == nil {
		return nil, errors.New("app event admission: dependencies are required")
	}
	return &EventAdmission{coordinator: coordinator, admitter: admitter, notify: notify, now: now}, nil
}

func (a *EventAdmission) Register(wire *eventwire.Wire) error {
	if wire == nil {
		return errors.New("app event admission: wire is required")
	}
	return wire.HandleBatch(a.admit)
}

func (a *EventAdmission) admit(_ context.Context, records []eventwire.Record) error {
	if len(records) == 0 {
		return nil
	}
	decisionTime := a.now().UTC()
	err := a.coordinator.Admit(func(snapshot policy.Snapshot) error {
		generic := make([]eventwire.Record, 0, len(records))
		flushGeneric := func() error {
			if len(generic) == 0 {
				return nil
			}
			_, err := a.admitter.AdmitBatch(generic, snapshot, decisionTime)
			generic = generic[:0]
			return err
		}
		for _, record := range records {
			if protectedLinearFeedback(record.Event) {
				if err := flushGeneric(); err != nil {
					return err
				}
				_, _, err := a.admitter.ContinueLinear(record, snapshot)
				if err != nil {
					return err
				}
				continue
			}
			generic = append(generic, record)
		}
		return flushGeneric()
	})
	if err != nil {
		return err
	}
	// Duplicate wakeups are harmless and avoid coupling admission to a second
	// Store read solely to distinguish a durable retry from a fresh Run.
	a.notify()
	return nil
}

func protectedLinearFeedback(event eventwire.Event) bool {
	values := event.Values(eventwire.AttributeProvenance)
	return event.Source == eventwire.SourceLinear && event.Type == "Comment" && event.Action == "create" && len(values) == 1 && values[0] == "human"
}
