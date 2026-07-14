package triggerscheduler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const schemaVersion = 1

type Cursor struct {
	ScheduleID       string    `json:"scheduleId"`
	ScheduleRevision uint64    `json:"scheduleRevision"`
	LastScheduledAt  time.Time `json:"lastScheduledAt"`
	Skipped          uint64    `json:"skipped"`
}

type cursorState struct {
	Schema  int      `json:"schema"`
	Cursors []Cursor `json:"cursors"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	cursors map[string]Cursor
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("trigger scheduler: cursor path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("trigger scheduler: create cursor directory: %w", err)
	}
	store := &Store{path: path, cursors: make(map[string]Cursor)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trigger scheduler: read cursors: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, errors.New("trigger scheduler: cursor file is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state cursorState
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("trigger scheduler: decode cursors: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("trigger scheduler: decode cursors: trailing content")
	}
	if state.Schema != schemaVersion {
		return nil, fmt.Errorf("trigger scheduler: unsupported cursor schema %d", state.Schema)
	}
	for _, cursor := range state.Cursors {
		if cursor.ScheduleID == "" || cursor.ScheduleRevision == 0 || cursor.LastScheduledAt.IsZero() {
			return nil, errors.New("trigger scheduler: invalid cursor")
		}
		if _, found := store.cursors[cursor.ScheduleID]; found {
			return nil, errors.New("trigger scheduler: duplicate cursor")
		}
		cursor.LastScheduledAt = cursor.LastScheduledAt.UTC()
		store.cursors[cursor.ScheduleID] = cursor
	}
	return store, nil
}

func (s *Store) Cursor(id string) (Cursor, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cursor, found := s.cursors[id]
	return cursor, found
}

func (s *Store) Advance(cursor Cursor) error {
	if cursor.ScheduleID == "" || cursor.ScheduleRevision == 0 || cursor.LastScheduledAt.IsZero() {
		return errors.New("trigger scheduler: invalid cursor advance")
	}
	cursor.LastScheduledAt = cursor.LastScheduledAt.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.cursors[cursor.ScheduleID]
	if found && current.ScheduleRevision == cursor.ScheduleRevision && cursor.LastScheduledAt.Before(current.LastScheduledAt) {
		return errors.New("trigger scheduler: cursor cannot move backward")
	}
	next := make(map[string]Cursor, len(s.cursors)+1)
	for id, value := range s.cursors {
		next[id] = value
	}
	next[cursor.ScheduleID] = cursor
	if err := writeCursors(s.path, next); err != nil {
		return err
	}
	s.cursors = next
	return nil
}

func writeCursors(path string, cursors map[string]Cursor) error {
	state := cursorState{Schema: schemaVersion}
	for _, cursor := range cursors {
		state.Cursors = append(state.Cursors, cursor)
	}
	slices.SortFunc(state.Cursors, func(left, right Cursor) int { return strings.Compare(left.ScheduleID, right.ScheduleID) })
	temp, err := os.CreateTemp(filepath.Dir(path), ".trigger-cursors-*")
	if err != nil {
		return fmt.Errorf("trigger scheduler: create cursor checkpoint: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if err := json.NewEncoder(temp).Encode(state); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("trigger scheduler: replace cursor checkpoint: %w", err)
	}
	return nil
}
