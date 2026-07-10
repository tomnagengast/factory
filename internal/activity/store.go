package activity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	Type       string    `json:"type"`
	Action     string    `json:"action"`
	ReceivedAt time.Time `json:"receivedAt"`
}

type Snapshot struct {
	Total  uint64
	Events []Event
}

type record struct {
	DeliveryID string `json:"deliveryId"`
	Event
}

type state struct {
	Total  uint64   `json:"total"`
	Events []record `json:"events"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	limit int
	state state
}

func Open(path string, limit int) (*Store, error) {
	if path == "" {
		return nil, errors.New("activity store: path is required")
	}
	if limit < 1 {
		return nil, errors.New("activity store: limit must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("activity store: create directory: %w", err)
	}

	s := &Store{path: path, limit: limit}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("activity store: read: %w", err)
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("activity store: decode: %w", err)
	}
	if len(s.state.Events) > limit {
		s.state.Events = s.state.Events[:limit]
	}
	return s, nil
}

func (s *Store) Add(deliveryID string, event Event) (bool, error) {
	if deliveryID == "" {
		return false, errors.New("activity store: delivery ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.state.Events {
		if existing.DeliveryID == deliveryID {
			return false, nil
		}
	}

	next := state{
		Total:  s.state.Total + 1,
		Events: make([]record, 0, min(s.limit, len(s.state.Events)+1)),
	}
	next.Events = append(next.Events, record{DeliveryID: deliveryID, Event: event})
	next.Events = append(next.Events, s.state.Events...)
	if len(next.Events) > s.limit {
		next.Events = next.Events[:s.limit]
	}
	if err := writeState(s.path, next); err != nil {
		return false, err
	}
	s.state = next
	return true, nil
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	events := make([]Event, len(s.state.Events))
	for i, record := range s.state.Events {
		events[i] = record.Event
	}
	return Snapshot{Total: s.state.Total, Events: events}
}

func writeState(path string, value state) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".linear-activity-*")
	if err != nil {
		return fmt.Errorf("activity store: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("activity store: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		temp.Close()
		return fmt.Errorf("activity store: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("activity store: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("activity store: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("activity store: replace: %w", err)
	}
	return nil
}
