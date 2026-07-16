package taskcontrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"
)

const storeVersion = 1

var (
	ErrRevisionConflict = errors.New("task control: revision conflict")
	projectIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

type Snapshot struct {
	Version           int       `json:"version"`
	Revision          uint64    `json:"revision"`
	EnabledProjectIDs []string  `json:"enabledProjectIds"`
	UpdatedAt         time.Time `json:"updatedAt,omitempty"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	state Snapshot
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("task control: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("task control: create directory: %w", err)
	}
	store := &Store{path: path, state: Snapshot{Version: storeVersion, EnabledProjectIDs: []string{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("task control: read: %w", err)
	}
	if err := json.Unmarshal(data, &store.state); err != nil {
		return nil, fmt.Errorf("task control: decode: %w", err)
	}
	if err := validate(store.state); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.state)
}

func (s *Store) Enabled(projectID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Contains(s.state.EnabledProjectIDs, projectID)
}

func (s *Store) SetProject(expected uint64, projectID string, enabled bool, now time.Time) (Snapshot, error) {
	if !projectIDPattern.MatchString(projectID) {
		return s.Snapshot(), errors.New("task control: project ID is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != s.state.Revision {
		return clone(s.state), ErrRevisionConflict
	}
	next := clone(s.state)
	found := slices.Contains(next.EnabledProjectIDs, projectID)
	if found == enabled {
		return next, nil
	}
	if enabled {
		next.EnabledProjectIDs = append(next.EnabledProjectIDs, projectID)
	} else {
		next.EnabledProjectIDs = slices.DeleteFunc(next.EnabledProjectIDs, func(value string) bool { return value == projectID })
	}
	sort.Strings(next.EnabledProjectIDs)
	next.Revision++
	next.UpdatedAt = now.UTC()
	if err := write(s.path, next); err != nil {
		return clone(s.state), err
	}
	s.state = next
	return clone(s.state), nil
}

func validate(snapshot Snapshot) error {
	if snapshot.Version != storeVersion {
		return fmt.Errorf("task control: unsupported version %d", snapshot.Version)
	}
	seen := make(map[string]bool, len(snapshot.EnabledProjectIDs))
	for _, projectID := range snapshot.EnabledProjectIDs {
		if !projectIDPattern.MatchString(projectID) || seen[projectID] {
			return errors.New("task control: enabled projects are invalid")
		}
		seen[projectID] = true
	}
	if !sort.StringsAreSorted(snapshot.EnabledProjectIDs) {
		return errors.New("task control: enabled projects are not canonical")
	}
	return nil
}

func clone(snapshot Snapshot) Snapshot {
	snapshot.EnabledProjectIDs = slices.Clone(snapshot.EnabledProjectIDs)
	return snapshot
}

func write(path string, snapshot Snapshot) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".task-control-*")
	if err != nil {
		return fmt.Errorf("task control: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("task control: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		temp.Close()
		return fmt.Errorf("task control: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("task control: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("task control: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("task control: replace: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("task control: open directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("task control: sync directory: %w", err)
	}
	return nil
}
