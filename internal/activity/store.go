package activity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	DeliveryID       string `json:"deliveryId"`
	PayloadAvailable bool   `json:"payloadAvailable,omitempty"`
	Event
}

type state struct {
	Total  uint64   `json:"total"`
	Events []record `json:"events"`
}

type Store struct {
	mu         sync.RWMutex
	path       string
	payloadDir string
	limit      int
	state      state
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

	s := &Store{
		path:       path,
		payloadDir: strings.TrimSuffix(path, filepath.Ext(path)) + "-payloads",
		limit:      limit,
	}
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
	return s.add(deliveryID, event, false)
}

func (s *Store) AddWithPayload(deliveryID string, event Event, payload []byte) (bool, error) {
	if err := s.StagePayload(deliveryID, payload); err != nil {
		return false, err
	}
	return s.AddStaged(deliveryID, event)
}

func (s *Store) StagePayload(deliveryID string, payload []byte) error {
	if deliveryID == "" {
		return errors.New("activity store: delivery ID is required")
	}
	if !json.Valid(payload) {
		return errors.New("activity store: payload must be valid JSON")
	}
	return s.writePayload(eventID(deliveryID), payload)
}

func (s *Store) AddStaged(deliveryID string, event Event) (bool, error) {
	if deliveryID == "" {
		return false, errors.New("activity store: delivery ID is required")
	}
	if _, err := os.Stat(s.payloadPath(eventID(deliveryID))); err != nil {
		return false, fmt.Errorf("activity store: inspect staged payload: %w", err)
	}
	return s.add(deliveryID, event, true)
}

func (s *Store) StagedPayload(deliveryID string) ([]byte, error) {
	if deliveryID == "" {
		return nil, errors.New("activity store: delivery ID is required")
	}
	payload, err := os.ReadFile(s.payloadPath(eventID(deliveryID)))
	if err != nil {
		return nil, fmt.Errorf("activity store: read staged payload: %w", err)
	}
	if !json.Valid(payload) {
		return nil, errors.New("activity store: staged payload is not valid JSON")
	}
	return payload, nil
}

func (s *Store) add(deliveryID string, event Event, payloadAvailable bool) (bool, error) {
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

	added := record{
		DeliveryID:       deliveryID,
		PayloadAvailable: payloadAvailable,
		Event:            event,
	}
	next := state{
		Total:  s.state.Total + 1,
		Events: make([]record, 0, min(s.limit, len(s.state.Events)+1)),
	}
	next.Events = append(next.Events, added)
	next.Events = append(next.Events, s.state.Events...)
	var pruned []record
	if len(next.Events) > s.limit {
		pruned = slices.Clone(next.Events[s.limit:])
		next.Events = next.Events[:s.limit]
	}
	if err := writeState(s.path, next); err != nil {
		return false, err
	}
	s.state = next
	for _, record := range pruned {
		if record.PayloadAvailable {
			_ = os.Remove(s.payloadPath(eventID(record.DeliveryID)))
		}
	}
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

func eventID(deliveryID string) string {
	digest := sha256.Sum256([]byte(deliveryID))
	return hex.EncodeToString(digest[:])
}

func (s *Store) payloadPath(id string) string {
	return filepath.Join(s.payloadDir, id+".json")
}

func (s *Store) writePayload(id string, payload []byte) error {
	if err := os.MkdirAll(s.payloadDir, 0o700); err != nil {
		return fmt.Errorf("activity store: create payload directory: %w", err)
	}
	temp, err := os.CreateTemp(s.payloadDir, ".linear-payload-*")
	if err != nil {
		return fmt.Errorf("activity store: create payload: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("activity store: set payload permissions: %w", err)
	}
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return fmt.Errorf("activity store: write payload: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("activity store: sync payload: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("activity store: close payload: %w", err)
	}
	if err := os.Rename(tempPath, s.payloadPath(id)); err != nil {
		return fmt.Errorf("activity store: replace payload: %w", err)
	}
	return nil
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
