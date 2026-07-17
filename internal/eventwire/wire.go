package eventwire

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	TaskSubmitted = "task.submitted"
	RunStarted    = "run.started"
	AgentOutput   = "agent.output"
	RunCompleted  = "run.completed"
	RunFailed     = "run.failed"
)

var ErrClosed = errors.New("event wire is closed")

// Event is the only durable fact in Factory. Task and Run state are projections
// of these ordered records.
type Event struct {
	Sequence uint64          `json:"sequence"`
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	At       time.Time       `json:"at"`
	TaskID   string          `json:"taskId,omitempty"`
	RunID    string          `json:"runId,omitempty"`
	Data     json.RawMessage `json:"data"`
}

// Wire owns one append-only JSONL file and broadcasts a wake whenever a record
// is appended. Consumers always catch up from the log, so a wake carries no
// payload and may be coalesced safely.
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
		expected := uint64(len(events) + 1)
		if event.Sequence != expected || event.ID == "" || event.Type == "" || event.At.IsZero() {
			return nil, fmt.Errorf("event wire line %d is invalid", index+1)
		}
		events = append(events, event)
	}
	return events, nil
}

func (w *Wire) Publish(eventType, taskID, runID string, payload any) (Event, error) {
	if eventType == "" {
		return Event{}, errors.New("event type is required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode event payload: %w", err)
	}
	id, err := NewID("evt")
	if err != nil {
		return Event{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return Event{}, ErrClosed
	}
	event := Event{
		Sequence: uint64(len(w.events) + 1),
		ID:       id,
		Type:     eventType,
		At:       time.Now().UTC(),
		TaskID:   taskID,
		RunID:    runID,
		Data:     data,
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("encode event: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := w.file.Write(encoded); err != nil {
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	w.events = append(w.events, event)
	close(w.changed)
	w.changed = make(chan struct{})
	return cloneEvent(event), nil
}

func (w *Wire) Events(after uint64) []Event {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return eventsAfter(w.events, after)
}

func (w *Wire) LastSequence() uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.events) == 0 {
		return 0
	}
	return w.events[len(w.events)-1].Sequence
}

// Wait returns every event after the requested sequence, waiting for the next
// append when the caller is current.
func (w *Wire) Wait(ctx context.Context, after uint64) ([]Event, error) {
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

func eventsAfter(events []Event, after uint64) []Event {
	index := sort.Search(len(events), func(index int) bool {
		return events[index].Sequence > after
	})
	cloned := make([]Event, len(events)-index)
	for offset := range cloned {
		cloned[offset] = cloneEvent(events[index+offset])
	}
	return cloned
}

func cloneEvent(event Event) Event {
	event.Data = append(json.RawMessage(nil), event.Data...)
	return event
}

func NewID(prefix string) (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create identity: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}
