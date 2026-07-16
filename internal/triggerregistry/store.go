package triggerregistry

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

	"github.com/tomnagengast/factory/internal/settings"
)

const maxRegistryBytes = 1 << 20

var ErrRevisionConflict = errors.New("trigger registry: revision conflict")

type Store struct {
	mu    sync.RWMutex
	path  string
	state Snapshot
}

func Open(path string, defaults Snapshot, configuration settings.Snapshot) (*Store, error) {
	if path == "" {
		return nil, errors.New("trigger registry: path is required")
	}
	CanonicalizeTargets(&defaults)
	if err := defaults.Validate(configuration); err != nil {
		return nil, fmt.Errorf("trigger registry: invalid defaults: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("trigger registry: create directory: %w", err)
	}
	Sort(&defaults)
	store := &Store{path: path, state: defaults.Clone()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trigger registry: read: %w", err)
	}
	if len(data) > maxRegistryBytes {
		return nil, errors.New("trigger registry: file is too large")
	}
	state, err := decode(data)
	if err != nil {
		return nil, err
	}
	CanonicalizeTargets(&state)
	if err := state.Validate(configuration); err != nil {
		return nil, err
	}
	Sort(&state)
	store.state = state.Clone()
	return store, nil
}

// Read validates a retained registry without creating directories or changing files.
func Read(path string, configuration settings.Snapshot) (Snapshot, error) {
	if path == "" {
		return Snapshot{}, errors.New("trigger registry: path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("trigger registry: read: %w", err)
	}
	if len(data) > maxRegistryBytes {
		return Snapshot{}, errors.New("trigger registry: file is too large")
	}
	state, err := decode(data)
	if err != nil {
		return Snapshot{}, err
	}
	CanonicalizeTargets(&state)
	if err := state.Validate(configuration); err != nil {
		return Snapshot{}, err
	}
	Sort(&state)
	return state.Clone(), nil
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Clone()
}

func (s *Store) Update(expectedRevision uint64, candidate Snapshot, configuration settings.Snapshot, now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRevision != s.state.Revision || candidate.Revision != expectedRevision {
		return s.state.Clone(), ErrRevisionConflict
	}
	if candidate.Schema != SchemaVersion || (!candidate.UpdatedAt.IsZero() && !candidate.UpdatedAt.Equal(s.state.UpdatedAt)) || candidate.LegacyRollbackIncompatible != s.state.LegacyRollbackIncompatible {
		return s.state.Clone(), errors.New("trigger registry: server-owned fields changed")
	}
	next := candidate.Clone()
	CanonicalizeTargets(&next)
	Sort(&next)
	if err := reconcileRevisions(s.state, &next); err != nil {
		return s.state.Clone(), err
	}
	next.Schema = SchemaVersion
	next.Revision = s.state.Revision + 1
	next.UpdatedAt = now.UTC()
	if err := next.Validate(configuration); err != nil {
		return s.state.Clone(), err
	}
	if err := write(s.path, next); err != nil {
		return s.state.Clone(), err
	}
	s.state = next
	return s.state.Clone(), nil
}

func (s *Store) MarkLegacyRollbackIncompatible(now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.LegacyRollbackIncompatible {
		return s.state.Clone(), nil
	}
	next := s.state.Clone()
	next.LegacyRollbackIncompatible = true
	next.Revision++
	next.UpdatedAt = now.UTC()
	if err := write(s.path, next); err != nil {
		return s.state.Clone(), err
	}
	s.state = next
	return s.state.Clone(), nil
}

func reconcileRevisions(current Snapshot, next *Snapshot) error {
	currentRules := make(map[string]Rule, len(current.Rules))
	for _, rule := range current.Rules {
		currentRules[rule.ID] = rule
	}
	for i := range next.Rules {
		candidate := &next.Rules[i]
		previous, found := currentRules[candidate.ID]
		if !found {
			if candidate.Revision != 0 {
				return fmt.Errorf("trigger registry: new rule %q revision must be zero", candidate.ID)
			}
			candidate.Revision = 1
			continue
		}
		if candidate.Revision != previous.Revision {
			return fmt.Errorf("trigger registry: rule %q revision changed", candidate.ID)
		}
		if !candidate.SemanticEqual(previous) {
			candidate.Revision++
		}
	}
	currentSchedules := make(map[string]Schedule, len(current.Schedules))
	for _, schedule := range current.Schedules {
		currentSchedules[schedule.ID] = schedule
	}
	for i := range next.Schedules {
		candidate := &next.Schedules[i]
		previous, found := currentSchedules[candidate.ID]
		if !found {
			if candidate.Revision != 0 {
				return fmt.Errorf("trigger registry: new schedule %q revision must be zero", candidate.ID)
			}
			candidate.Revision = 1
			continue
		}
		if candidate.Revision != previous.Revision {
			return fmt.Errorf("trigger registry: schedule %q revision changed", candidate.ID)
		}
		if !candidate.SemanticEqual(previous) {
			candidate.Revision++
		}
	}
	return nil
}

func decode(data []byte) (Snapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state Snapshot
	if err := decoder.Decode(&state); err != nil {
		return Snapshot{}, fmt.Errorf("trigger registry: decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("trigger registry: decode: trailing content")
	}
	return state, nil
}

func write(path string, state Snapshot) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".triggers-*")
	if err != nil {
		return fmt.Errorf("trigger registry: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("trigger registry: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		temp.Close()
		return fmt.Errorf("trigger registry: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("trigger registry: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("trigger registry: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("trigger registry: replace: %w", err)
	}
	return nil
}
