package repositories

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const maxSourceStateBytes = 2 << 20

// Store owns the canonical repository artifact and its validated catalog
// projection. Raw persistence remains package-private; exported mutations are
// expressed as typed onboarding operations.
type Store struct {
	mu      sync.Mutex
	path    string
	catalog *Catalog
	writer  sourceStateWriter
}

type sourceStateWriter func(string, SourceState) (bool, error)

// Create writes a converted canonical repository artifact. It never replaces
// an artifact that already exists; generation construction must choose a new
// destination.
func Create(path string, state SourceState) (*Store, error) {
	if path == "" {
		return nil, errors.New("repositories: path is required")
	}
	index, err := buildIndex(state)
	if err != nil {
		return nil, fmt.Errorf("repositories: invalid initial state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("repositories: create directory: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, errors.New("repositories: create: artifact already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("repositories: inspect artifact: %w", err)
	}
	replaced, err := writeNewSourceState(path, index.state)
	if !replaced {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("repositories: create completed without installing artifact")
	}
	store := &Store{
		path: path, catalog: &Catalog{index: index}, writer: writeSourceState,
	}
	if err != nil {
		return store, err
	}
	return store, nil
}

// Open strictly validates the canonical repository artifact before exposing
// its catalog projection.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("repositories: path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("repositories: inspect: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("repositories: artifact must be a regular nonsymlink file")
	}
	if info.Mode() != 0o600 {
		return nil, fmt.Errorf("repositories: artifact permissions are %04o, want exact 0600", info.Mode().Perm())
	}
	if info.Size() > maxSourceStateBytes {
		return nil, errors.New("repositories: file is too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("repositories: read: %w", err)
	}
	if len(data) > maxSourceStateBytes {
		return nil, errors.New("repositories: file is too large")
	}
	state, err := decodeSourceState(data)
	if err != nil {
		return nil, err
	}
	index, err := buildIndex(state)
	if err != nil {
		return nil, fmt.Errorf("repositories: invalid state: %w", err)
	}
	return &Store{
		path: path, catalog: &Catalog{index: index}, writer: writeSourceState,
	}, nil
}

func (s *Store) Snapshot() SourceState {
	return s.catalog.Snapshot()
}

// persist is the package-private durability boundary for later typed
// onboarding operations. It owns generation advancement, replacement
// validation, disk replacement, and publication of the matching catalog.
func (s *Store) persist(next SourceState) (SourceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistLocked(next)
}

func (s *Store) persistLocked(next SourceState) (SourceState, error) {
	s.catalog.mu.RLock()
	current := s.catalog.index
	s.catalog.mu.RUnlock()
	if current.state.Generation == ^uint64(0) {
		return s.catalog.Snapshot(), errors.New("repositories: generation exhausted")
	}
	next = next.Clone()
	next.Generation = current.state.Generation + 1
	candidate, err := buildIndex(next)
	if err != nil {
		return s.catalog.Snapshot(), err
	}
	if err := validateReplacement(current, candidate); err != nil {
		return s.catalog.Snapshot(), err
	}
	replaced, err := s.writer(s.path, candidate.state)
	if replaced {
		s.catalog.mu.Lock()
		s.catalog.index = candidate
		s.catalog.mu.Unlock()
	}
	if err != nil {
		return s.catalog.Snapshot(), err
	}
	if !replaced {
		return s.catalog.Snapshot(), errors.New("repositories: write completed without replacing artifact")
	}
	return s.catalog.Snapshot(), nil
}

func decodeSourceState(data []byte) (SourceState, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state SourceState
	if err := decoder.Decode(&state); err != nil {
		return SourceState{}, fmt.Errorf("repositories: decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SourceState{}, errors.New("repositories: decode: trailing content")
	}
	return state, nil
}

func writeSourceState(path string, state SourceState) (bool, error) {
	return writeSourceStateFile(path, state, false, func(directory *os.File) error {
		return directory.Sync()
	})
}

func writeNewSourceState(path string, state SourceState) (bool, error) {
	return writeSourceStateFile(path, state, true, func(directory *os.File) error {
		return directory.Sync()
	})
}

func writeSourceStateWithDirectorySync(
	path string,
	state SourceState,
	syncDirectory func(*os.File) error,
) (bool, error) {
	return writeSourceStateFile(path, state, false, syncDirectory)
}

func writeSourceStateFile(
	path string,
	state SourceState,
	createNoReplace bool,
	syncDirectory func(*os.File) error,
) (bool, error) {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		return false, fmt.Errorf("repositories: encode: %w", err)
	}
	if data.Len() > maxSourceStateBytes {
		return false, errors.New("repositories: encoded file is too large")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".repositories-*")
	if err != nil {
		return false, fmt.Errorf("repositories: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return false, fmt.Errorf("repositories: set permissions: %w", err)
	}
	if _, err := temporary.Write(data.Bytes()); err != nil {
		temporary.Close()
		return false, fmt.Errorf("repositories: write: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, fmt.Errorf("repositories: sync: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("repositories: close: %w", err)
	}
	reservedDestination := false
	if createNoReplace {
		reservation, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return false, errors.New("repositories: create: artifact already exists")
			}
			return false, fmt.Errorf("repositories: reserve artifact: %w", err)
		}
		reservedDestination = true
		if err := reservation.Close(); err != nil {
			os.Remove(path)
			return false, fmt.Errorf("repositories: close artifact reservation: %w", err)
		}
	}
	defer func() {
		if reservedDestination {
			os.Remove(path)
		}
	}()
	if err := os.Rename(temporaryPath, path); err != nil {
		return false, fmt.Errorf("repositories: replace: %w", err)
	}
	reservedDestination = false
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return true, fmt.Errorf("repositories: open directory: %w", err)
	}
	defer directory.Close()
	if err := syncDirectory(directory); err != nil {
		return true, fmt.Errorf("repositories: sync directory: %w", err)
	}
	return true, nil
}
