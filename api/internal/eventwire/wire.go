package eventwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var ErrClosed = errors.New("event wire is closed")

// Event is the only durable fact in Factory. Its ordered ID is also used as
// the integer ID for an entity created by the event.
type Event struct {
	ID   int64           `json:"id"`
	Type string          `json:"type"`
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"`
}

// Wire owns one append-only JSONL file. Consumers catch up from the log after
// every coalesced wake, so there is no broker or delivery state to maintain.
type Wire struct {
	mu      sync.RWMutex
	file    *os.File
	events  []Event
	changed chan struct{}
	closed  bool
}

func Open(path string) (*Wire, error) {
	if path == "" {
		return nil, errors.New("event wire path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return nil, fmt.Errorf("create event wire directory: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read event wire: %w", err)
	}
	events, err := decode(data)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o666)
	if err != nil {
		return nil, fmt.Errorf("open event wire: %w", err)
	}
	return &Wire{file: file, events: events, changed: make(chan struct{})}, nil
}

func decode(data []byte) ([]Event, error) {
	lines := bytes.Split(data, []byte{'\n'})
	events := make([]Event, 0, len(lines))
	for index, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode event wire line %d: %w", index+1, err)
		}
		if event.ID != int64(len(events)+1) || event.Type == "" || event.At.IsZero() {
			return nil, fmt.Errorf("event wire line %d is invalid", index+1)
		}
		events = append(events, event)
	}
	return events, nil
}

func (w *Wire) Publish(eventType string, payload any) (Event, error) {
	event, _, err := w.publish(0, false, eventType, payload)
	return event, err
}

func (w *Wire) PublishIfCurrent(expectedLastID int64, eventType string, payload any) (Event, bool, error) {
	return w.publish(expectedLastID, true, eventType, payload)
}

func (w *Wire) publish(expectedLastID int64, conditional bool, eventType string, payload any) (Event, bool, error) {
	if eventType == "" {
		return Event{}, false, errors.New("event type is required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Event{}, false, fmt.Errorf("encode event payload: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return Event{}, false, ErrClosed
	}
	if conditional && expectedLastID != int64(len(w.events)) {
		return Event{}, false, nil
	}
	event := Event{
		ID:   int64(len(w.events) + 1),
		Type: eventType,
		At:   time.Now().UTC(),
		Data: data,
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return Event{}, false, fmt.Errorf("encode event: %w", err)
	}
	if _, err := w.file.Write(append(encoded, '\n')); err != nil {
		return Event{}, false, fmt.Errorf("append event: %w", err)
	}
	w.events = append(w.events, event)
	close(w.changed)
	w.changed = make(chan struct{})
	return clone(event), true, nil
}

func (w *Wire) Events(after int64) []Event {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return eventsAfter(w.events, after)
}

func (w *Wire) Event(id int64) (Event, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if id < 1 || id > int64(len(w.events)) {
		return Event{}, false
	}
	return clone(w.events[id-1]), true
}

func (w *Wire) Types() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	seen := make(map[string]bool)
	types := make([]string, 0)
	for _, event := range w.events {
		if !seen[event.Type] {
			seen[event.Type] = true
			types = append(types, event.Type)
		}
	}
	sort.Strings(types)
	return types
}

func (w *Wire) LastID() int64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return int64(len(w.events))
}

func (w *Wire) Wait(ctx context.Context, after int64) ([]Event, error) {
	for {
		w.mu.RLock()
		events := eventsAfter(w.events, after)
		changed := w.changed
		closed := w.closed
		w.mu.RUnlock()
		if len(events) > 0 {
			return events, nil
		}
		if closed {
			return nil, ErrClosed
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (w *Wire) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	close(w.changed)
	return w.file.Close()
}

func eventsAfter(events []Event, after int64) []Event {
	index := sort.Search(len(events), func(index int) bool {
		return events[index].ID > after
	})
	cloned := make([]Event, len(events)-index)
	for offset := range cloned {
		cloned[offset] = clone(events[index+offset])
	}
	return cloned
}

func clone(event Event) Event {
	event.Data = append(json.RawMessage(nil), event.Data...)
	return event
}
