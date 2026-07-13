package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxSettingsBytes = 1 << 20

var ErrRevisionConflict = errors.New("settings: revision conflict")

type Store struct {
	mu    sync.RWMutex
	path  string
	state Snapshot
}

func Open(path string, defaults Snapshot) (*Store, error) {
	if path == "" {
		return nil, errors.New("settings: path is required")
	}
	if err := defaults.Validate(); err != nil {
		return nil, fmt.Errorf("settings: invalid defaults: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("settings: create directory: %w", err)
	}
	store := &Store{path: path, state: defaults.Clone()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settings: read: %w", err)
	}
	if len(data) > maxSettingsBytes {
		return nil, errors.New("settings: file is too large")
	}
	state, err := decode(data)
	if err != nil {
		return nil, err
	}
	if err := state.Validate(); err != nil {
		return nil, err
	}
	store.state = state.Clone()
	return store, nil
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Clone()
}

func (s *Store) Update(expectedRevision uint64, candidate Snapshot, now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRevision != s.state.Revision {
		return s.state.Clone(), ErrRevisionConflict
	}
	next := candidate.Clone()
	next.Schema = SchemaVersion
	next.Revision = s.state.Revision + 1
	next.UpdatedAt = now.UTC()
	if err := next.Validate(); err != nil {
		return s.state.Clone(), err
	}
	if err := write(s.path, next); err != nil {
		return s.state.Clone(), err
	}
	s.state = next
	return s.state.Clone(), nil
}

func decode(data []byte) (Snapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state Snapshot
	if err := decoder.Decode(&state); err != nil {
		return Snapshot{}, fmt.Errorf("settings: decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("settings: decode: trailing content")
	}
	return state, nil
}

func write(path string, state Snapshot) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return fmt.Errorf("settings: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("settings: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		temp.Close()
		return fmt.Errorf("settings: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("settings: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("settings: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("settings: replace: %w", err)
	}
	return nil
}
