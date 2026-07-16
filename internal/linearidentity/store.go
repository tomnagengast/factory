package linearidentity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/tomnagengast/factory/internal/taskcompat"
	"github.com/tomnagengast/factory/internal/taskmodel"
)

const snapshotVersion = 1

var (
	ErrConflict = errors.New("Linear task identity conflicts with an existing binding")
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type Binding struct {
	Identifier string `json:"identifier"`
	UUID       string `json:"uuid"`
}

type snapshot struct {
	Version  int       `json:"version"`
	Bindings []Binding `json:"bindings"`
}

type Store struct {
	mu           sync.Mutex
	path         string
	byIdentifier map[string]string
	byUUID       map[string]string
	poisoned     error
}

func Open(path string) (*Store, error) {
	path = filepath.Clean(path)
	if path == "." || path == "" {
		return nil, errors.New("Linear identity store: path is required")
	}
	store := &Store{path: path, byIdentifier: make(map[string]string), byUUID: make(map[string]string)}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("Linear identity store: inspect: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("Linear identity store: file must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Linear identity store: read: %w", err)
	}
	if len(data) > 2<<20 {
		return nil, errors.New("Linear identity store: snapshot exceeds 2 MiB")
	}
	if _, err := taskcompat.Read(taskcompat.PathFor(path)); err != nil {
		return nil, fmt.Errorf("Linear identity store: compatibility boundary: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var persisted snapshot
	if err := decoder.Decode(&persisted); err != nil {
		return nil, fmt.Errorf("Linear identity store: decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return nil, errors.New("Linear identity store: snapshot contains trailing data")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("Linear identity store: decode trailing data: %w", err)
	}
	if persisted.Version != snapshotVersion {
		return nil, fmt.Errorf("Linear identity store: unsupported version %d", persisted.Version)
	}
	for _, binding := range persisted.Bindings {
		identifier, uuid, err := normalize(binding.Identifier, binding.UUID)
		if err != nil {
			return nil, fmt.Errorf("Linear identity store: invalid binding: %w", err)
		}
		if existing, found := store.byIdentifier[identifier]; found && existing != uuid {
			return nil, fmt.Errorf("Linear identity store: %w for identifier %s", ErrConflict, identifier)
		}
		if existing, found := store.byUUID[uuid]; found && existing != identifier {
			return nil, fmt.Errorf("Linear identity store: %w for UUID %s", ErrConflict, uuid)
		}
		if _, duplicate := store.byIdentifier[identifier]; duplicate {
			return nil, fmt.Errorf("Linear identity store: duplicate binding for %s", identifier)
		}
		store.byIdentifier[identifier] = uuid
		store.byUUID[uuid] = identifier
	}
	return store, nil
}

// Bind durably records the one-to-one relationship between a Linear display
// identifier and provider UUID. It returns false for an exact replay.
func (s *Store) Bind(identifier, uuid string) (bool, error) {
	identifier, uuid, err := normalize(identifier, uuid)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned != nil {
		return false, fmt.Errorf("Linear identity store: unavailable after persistence failure: %w", s.poisoned)
	}
	if existing, found := s.byIdentifier[identifier]; found {
		if existing == uuid {
			return false, nil
		}
		return false, fmt.Errorf("%w: identifier %s was already bound to %s", ErrConflict, identifier, existing)
	}
	if existing, found := s.byUUID[uuid]; found {
		return false, fmt.Errorf("%w: UUID %s was already bound to %s", ErrConflict, uuid, existing)
	}
	s.byIdentifier[identifier] = uuid
	s.byUUID[uuid] = identifier
	if err := s.persist(); err != nil {
		s.poisoned = err
		return false, err
	}
	return true, nil
}

func normalize(identifier, uuid string) (string, string, error) {
	identifier = strings.ToUpper(strings.TrimSpace(identifier))
	uuid = strings.ToLower(strings.TrimSpace(uuid))
	if !taskmodel.ValidLinearIdentifier(identifier) {
		return "", "", errors.New("Linear identity store: invalid identifier")
	}
	if !uuidPattern.MatchString(uuid) {
		return "", "", errors.New("Linear identity store: invalid UUID")
	}
	return identifier, uuid, nil
}

func (s *Store) persist() error {
	if err := taskcompat.Ensure(s.path); err != nil {
		return fmt.Errorf("Linear identity store: establish compatibility boundary: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("Linear identity store: create directory: %w", err)
	}
	bindings := make([]Binding, 0, len(s.byIdentifier))
	for identifier, uuid := range s.byIdentifier {
		bindings = append(bindings, Binding{Identifier: identifier, UUID: uuid})
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Identifier < bindings[j].Identifier })
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".linear-task-identities-*")
	if err != nil {
		return fmt.Errorf("Linear identity store: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("Linear identity store: set permissions: %w", err)
	}
	if err := json.NewEncoder(temporary).Encode(snapshot{Version: snapshotVersion, Bindings: bindings}); err != nil {
		temporary.Close()
		return fmt.Errorf("Linear identity store: encode: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("Linear identity store: sync: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("Linear identity store: close: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("Linear identity store: replace: %w", err)
	}
	directory, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return fmt.Errorf("Linear identity store: open directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("Linear identity store: sync directory: %w", err)
	}
	return nil
}
