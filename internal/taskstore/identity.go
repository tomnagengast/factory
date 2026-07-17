package taskstore

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

var (
	ErrLinearIdentityConflict = errors.New("task store: Linear identity conflicts with an existing binding")
	linearUUIDPattern         = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// BindLinearIdentity durably records the one-to-one relationship between a
// Linear display identifier and its provider UUID. It returns false for an
// exact replay and rejects either direction of a changed mapping.
func (s *Store) BindLinearIdentity(identifier, uuid string) (bool, error) {
	binding, err := normalizeLinearBinding(LinearBinding{Identifier: identifier, UUID: uuid})
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkWritable(); err != nil {
		return false, err
	}
	if existing := s.linearByID[binding.Identifier]; existing != "" {
		if existing == binding.UUID {
			return false, nil
		}
		return false, fmt.Errorf("%w: identifier %s was already bound to %s", ErrLinearIdentityConflict, binding.Identifier, existing)
	}
	if existing := s.linearByUUID[binding.UUID]; existing != "" {
		return false, fmt.Errorf("%w: UUID %s was already bound to %s", ErrLinearIdentityConflict, binding.UUID, existing)
	}
	if err := s.persistLocked(diskOperation{Kind: operationLinearBind, LinearBinding: &binding}); err != nil {
		return false, transient(err)
	}
	return true, nil
}

func (s *Store) LinearUUID(identifier string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	uuid, found := s.linearByID[strings.ToUpper(strings.TrimSpace(identifier))]
	return uuid, found
}

func (s *Store) LinearIdentifier(uuid string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identifier, found := s.linearByUUID[strings.ToLower(strings.TrimSpace(uuid))]
	return identifier, found
}

func (s *Store) LinearBindings() []LinearBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bindings := make([]LinearBinding, 0, len(s.linearByID))
	for identifier, uuid := range s.linearByID {
		bindings = append(bindings, LinearBinding{Identifier: identifier, UUID: uuid})
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Identifier < bindings[j].Identifier })
	return bindings
}

func (s *Store) applyLinearBindingLocked(operation diskOperation) error {
	if operation.Schema != 0 || operation.Checkpoint != nil || operation.Outcome != nil || operation.Task != nil || operation.Message != nil ||
		operation.Link != nil || operation.Gate != nil || operation.TaskOperation != nil || operation.LinearBinding == nil {
		return errors.New("task store: invalid Linear identity envelope")
	}
	return s.installLinearBinding(*operation.LinearBinding)
}

func (s *Store) installCheckpointLinearBinding(binding LinearBinding) error {
	return s.installLinearBinding(binding)
}

func (s *Store) installLinearBinding(binding LinearBinding) error {
	normalized, err := normalizeLinearBinding(binding)
	if err != nil || normalized != binding {
		return errors.New("task store: invalid Linear identity binding")
	}
	if existing := s.linearByID[binding.Identifier]; existing != "" {
		return errors.New("task store: duplicate Linear identifier")
	}
	if s.linearByUUID[binding.UUID] != "" {
		return errors.New("task store: duplicate Linear UUID")
	}
	s.linearByID[binding.Identifier] = binding.UUID
	s.linearByUUID[binding.UUID] = binding.Identifier
	return nil
}

func normalizeLinearBinding(binding LinearBinding) (LinearBinding, error) {
	binding.Identifier = strings.ToUpper(strings.TrimSpace(binding.Identifier))
	binding.UUID = strings.ToLower(strings.TrimSpace(binding.UUID))
	if !taskmodel.ValidLinearIdentifier(binding.Identifier) {
		return LinearBinding{}, errors.New("task store: invalid Linear identifier")
	}
	if !linearUUIDPattern.MatchString(binding.UUID) {
		return LinearBinding{}, errors.New("task store: invalid Linear UUID")
	}
	return binding, nil
}

// ConvertLinearBindings folds a complete legacy bijection into a prospective
// task checkpoint without reading retained tasks as an identity source. It is
// pure so migration can validate a disposable generation before activation.
func ConvertLinearBindings(snapshot Snapshot, bindings []LinearBinding) (Snapshot, error) {
	converted := snapshot.Clone()
	if len(converted.LinearBindings) != 0 {
		return Snapshot{}, errors.New("task store: canonical snapshot already contains Linear bindings")
	}
	byIdentifier := make(map[string]string, len(bindings))
	byUUID := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		normalized, err := normalizeLinearBinding(binding)
		if err != nil {
			return Snapshot{}, err
		}
		if byIdentifier[normalized.Identifier] != "" {
			return Snapshot{}, fmt.Errorf("task store: duplicate Linear identifier %s", normalized.Identifier)
		}
		if byUUID[normalized.UUID] != "" {
			return Snapshot{}, fmt.Errorf("task store: duplicate Linear UUID %s", normalized.UUID)
		}
		byIdentifier[normalized.Identifier] = normalized.UUID
		byUUID[normalized.UUID] = normalized.Identifier
		converted.LinearBindings = append(converted.LinearBindings, normalized)
	}
	sort.Slice(converted.LinearBindings, func(i, j int) bool {
		return converted.LinearBindings[i].Identifier < converted.LinearBindings[j].Identifier
	})
	return converted, nil
}

// ValidateSnapshot performs the same strict checkpoint replay used by Open
// without creating a file. Migration uses it to reject a prospective task
// artifact before a generation is ever written or selected.
func ValidateSnapshot(snapshot Snapshot) error {
	store := &Store{}
	store.reset()
	clone := snapshot.Clone()
	return store.applyCheckpointLocked(diskOperation{Kind: operationCheckpoint, Schema: SchemaVersion, Checkpoint: &clone})
}
