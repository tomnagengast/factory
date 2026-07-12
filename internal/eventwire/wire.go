package eventwire

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type Handler func(context.Context, Record) error

type route struct {
	filter  Filter
	handler Handler
}

type Wire struct {
	journal    *Journal
	dispatchMu sync.Mutex
	routesMu   sync.RWMutex
	routes     []route
}

func New(journal *Journal) (*Wire, error) {
	if journal == nil {
		return nil, errors.New("event wire: journal is required")
	}
	return &Wire{journal: journal}, nil
}

func (w *Wire) Handle(filter Filter, handler Handler) error {
	if handler == nil {
		return errors.New("event wire: handler is required")
	}
	w.routesMu.Lock()
	defer w.routesMu.Unlock()
	w.routes = append(w.routes, route{filter: filter, handler: handler})
	return nil
}

func (w *Wire) Publish(ctx context.Context, event Event) (Record, bool, error) {
	w.dispatchMu.Lock()
	defer w.dispatchMu.Unlock()
	if err := w.catchUpLocked(ctx); err != nil {
		return Record{}, false, err
	}
	record, added, err := w.journal.Add(event)
	if err != nil {
		return Record{}, false, err
	}
	if err := w.catchUpLocked(ctx); err != nil {
		return record, added, err
	}
	return record, added, nil
}

func (w *Wire) PublishBatch(ctx context.Context, events []Event) ([]Record, error) {
	w.dispatchMu.Lock()
	defer w.dispatchMu.Unlock()
	if err := w.catchUpLocked(ctx); err != nil {
		return nil, err
	}
	records, err := w.journal.AddBatch(events)
	if err != nil {
		return nil, err
	}
	if err := w.catchUpLocked(ctx); err != nil {
		return records, err
	}
	return records, nil
}

func (w *Wire) CatchUp(ctx context.Context) error {
	w.dispatchMu.Lock()
	defer w.dispatchMu.Unlock()
	return w.catchUpLocked(ctx)
}

func (w *Wire) catchUpLocked(ctx context.Context) error {
	pending := w.journal.Pending()
	if len(pending) == 0 {
		return nil
	}
	_, _, channelAcks, _ := w.journal.Snapshot()
	w.routesMu.RLock()
	routes := append([]route(nil), w.routes...)
	w.routesMu.RUnlock()

	var last uint64
	for _, record := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, route := range routes {
			if !route.filter.Matches(record.Event) {
				continue
			}
			if err := route.handler(ctx, cloneRecord(record)); err != nil {
				return fmt.Errorf("event wire: dispatch %s: %w", record.Event.ID, err)
			}
		}
		last = record.Sequence
		for channel, sequence := range record.ChannelSequences {
			channelAcks[channel] = max(channelAcks[channel], sequence)
		}
	}
	return w.journal.Acknowledge(last, channelAcks)
}
