package eventwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	activityPendingFile      = "activity.pending.json"
	activityStagingDirectory = "activity-staged-payloads"
)

// ActivityStore owns the mutable home-activity projection and its private
// Linear payload corpus. A small intent record makes the otherwise multi-file
// add/prune operation restartable without accepting an orphan body or a
// projection that names a missing body.
type ActivityStore struct {
	mu          sync.RWMutex
	root        string
	payloadRoot string
	stagingRoot string
	pendingPath string
	limit       int
	projection  ActivityProjection
}

type activityPending struct {
	Schema         int      `json:"schema"`
	DeliveryID     string   `json:"deliveryId"`
	Payload        bool     `json:"payload"`
	PrunedPayloads []string `json:"prunedPayloads"`
}

// OpenActivityStore strictly opens and recovers a selected generation's
// canonical activity owner. Staged payloads are allowed because webhook
// publication can be interrupted before the authoritative wire dispatches the
// matching event; every committed payload must still have one projection
// reference and every unexplained final payload fails closed.
func OpenActivityStore(root string, limit int) (*ActivityStore, error) {
	root = filepath.Clean(root)
	if root == "." || root == string(os.PathSeparator) || limit < 1 {
		return nil, errors.New("event activity store: private root and positive limit are required")
	}
	store := &ActivityStore{
		root: root, payloadRoot: filepath.Join(root, activityPayloadDirectory),
		stagingRoot: filepath.Join(root, activityStagingDirectory),
		pendingPath: filepath.Join(root, activityPendingFile), limit: limit,
	}
	if err := store.open(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *ActivityStore) open() error {
	if err := requireActivityDirectory(s.root); err != nil {
		return err
	}
	if err := requireActivityDirectory(s.payloadRoot); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case activityProjectionFile, activityPayloadDirectory, activityStagingDirectory, activityPendingFile:
		default:
			return fmt.Errorf("event activity store: unknown artifact %s", entry.Name())
		}
	}
	projection, err := readActivityProjection(filepath.Join(s.root, activityProjectionFile))
	if err != nil {
		return err
	}
	s.projection = projection
	if err := s.recoverPending(); err != nil {
		return err
	}
	if err := s.validateFiles(); err != nil {
		return err
	}
	return s.removeEmptyStagingRoot()
}

func (s *ActivityStore) Add(deliveryID string, event ActivityEvent) (bool, error) {
	return s.add(deliveryID, event, false)
}

func (s *ActivityStore) StagePayload(deliveryID string, payload []byte) error {
	if err := validateActivityDelivery(deliveryID); err != nil {
		return err
	}
	if !json.Valid(payload) {
		return errors.New("event activity store: payload must be valid JSON")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ensureActivityDirectory(s.stagingRoot); err != nil {
		return err
	}
	path := filepath.Join(s.stagingRoot, activityPayloadFile(deliveryID))
	if existing, err := readOptionalPrivateActivityFile(path); err != nil {
		return err
	} else if existing != nil {
		if !bytes.Equal(existing, payload) {
			return errors.New("event activity store: staged payload conflicts with its delivery")
		}
		return nil
	}
	if err := writePrivateActivityFile(path, payload); err != nil {
		return err
	}
	return syncActivityDirectory(s.stagingRoot)
}

func (s *ActivityStore) AddStaged(deliveryID string, event ActivityEvent) (bool, error) {
	return s.add(deliveryID, event, true)
}

func (s *ActivityStore) StagedPayload(deliveryID string) ([]byte, error) {
	if err := validateActivityDelivery(deliveryID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, record := range s.projection.Events {
		if record.DeliveryID == deliveryID && record.Payload != nil {
			return readPrivateActivityFile(filepath.Join(s.payloadRoot, record.Payload.File))
		}
	}
	body, err := readOptionalPrivateActivityFile(filepath.Join(s.stagingRoot, activityPayloadFile(deliveryID)))
	if err != nil {
		return nil, err
	}
	if body == nil {
		return nil, os.ErrNotExist
	}
	return body, nil
}

func (s *ActivityStore) Snapshot() ActivityProjection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.projection.Clone()
}

func (s *ActivityStore) add(deliveryID string, event ActivityEvent, payload bool) (bool, error) {
	if err := validateActivityDelivery(deliveryID); err != nil {
		return false, err
	}
	event.ReceivedAt = event.ReceivedAt.UTC()
	if strings.TrimSpace(event.Type) == "" || len(event.Type) > maxFieldLength || strings.TrimSpace(event.Action) == "" || len(event.Action) > maxFieldLength || event.ReceivedAt.IsZero() {
		return false, errors.New("event activity store: event is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.projection.Events {
		if record.DeliveryID == deliveryID {
			return false, nil
		}
	}

	var body []byte
	if payload {
		var err error
		body, err = readOptionalPrivateActivityFile(filepath.Join(s.stagingRoot, activityPayloadFile(deliveryID)))
		if err != nil {
			return false, err
		}
		if body == nil || !json.Valid(body) {
			return false, errors.New("event activity store: staged payload is missing or invalid")
		}
	}
	record := ActivityRecord{DeliveryID: deliveryID, ActivityEvent: event}
	if payload {
		digest := sha256.Sum256(body)
		record.Payload = &ActivityPayloadReference{File: activityPayloadFile(deliveryID), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
	}
	next := s.projection.Clone()
	next.Total++
	next.Events = append([]ActivityRecord{record}, next.Events...)
	var pruned []ActivityRecord
	if len(next.Events) > s.limit {
		pruned = slices.Clone(next.Events[s.limit:])
		next.Events = next.Events[:s.limit]
	}
	if err := next.Validate(); err != nil {
		return false, err
	}
	pending := activityPending{Schema: 1, DeliveryID: deliveryID, Payload: payload, PrunedPayloads: activityPayloadDeliveries(pruned)}
	if err := writeActivityJSON(s.pendingPath, pending); err != nil {
		return false, err
	}
	if payload {
		if err := os.Rename(filepath.Join(s.stagingRoot, record.Payload.File), filepath.Join(s.payloadRoot, record.Payload.File)); err != nil {
			return false, fmt.Errorf("event activity store: promote staged payload: %w", err)
		}
		if err := syncActivityDirectory(s.stagingRoot, s.payloadRoot); err != nil {
			return false, err
		}
	}
	if err := writeActivityProjection(filepath.Join(s.root, activityProjectionFile), next); err != nil {
		return false, err
	}
	s.projection = next
	if err := s.finishCommitted(pending); err != nil {
		return false, err
	}
	return true, nil
}

func (s *ActivityStore) recoverPending() error {
	data, err := readOptionalPrivateActivityFile(s.pendingPath)
	if err != nil {
		return err
	}
	if data == nil {
		return nil
	}
	var pending activityPending
	if err := decodeStrictActivityJSON(data, &pending); err != nil || pending.Schema != 1 || validateActivityDelivery(pending.DeliveryID) != nil {
		return errors.New("event activity store: pending operation is invalid")
	}
	committed := false
	for _, record := range s.projection.Events {
		if record.DeliveryID == pending.DeliveryID {
			committed = true
			if pending.Payload && record.Payload == nil {
				return errors.New("event activity store: pending payload conflicts with committed projection")
			}
			break
		}
	}
	if committed {
		if pending.Payload {
			if err := s.ensureCommittedPayload(pending.DeliveryID); err != nil {
				return err
			}
		}
		return s.finishCommitted(pending)
	}
	if pending.Payload {
		final := filepath.Join(s.payloadRoot, activityPayloadFile(pending.DeliveryID))
		staged := filepath.Join(s.stagingRoot, activityPayloadFile(pending.DeliveryID))
		if body, err := readOptionalPrivateActivityFile(final); err != nil {
			return err
		} else if body != nil {
			if err := ensureActivityDirectory(s.stagingRoot); err != nil {
				return err
			}
			if existing, readErr := readOptionalPrivateActivityFile(staged); readErr != nil {
				return readErr
			} else if existing != nil && !bytes.Equal(existing, body) {
				return errors.New("event activity store: recovered payload conflicts with staged payload")
			} else if existing == nil {
				if err := os.Rename(final, staged); err != nil {
					return err
				}
			} else if err := os.Remove(final); err != nil {
				return err
			}
		}
	}
	return s.clearPending()
}

func (s *ActivityStore) ensureCommittedPayload(deliveryID string) error {
	final := filepath.Join(s.payloadRoot, activityPayloadFile(deliveryID))
	if body, err := readOptionalPrivateActivityFile(final); err != nil {
		return err
	} else if body != nil {
		return nil
	}
	staged := filepath.Join(s.stagingRoot, activityPayloadFile(deliveryID))
	if body, err := readOptionalPrivateActivityFile(staged); err != nil {
		return err
	} else if body == nil {
		return errors.New("event activity store: committed payload is missing")
	}
	if err := os.Rename(staged, final); err != nil {
		return err
	}
	return syncActivityDirectory(s.stagingRoot, s.payloadRoot)
}

func (s *ActivityStore) finishCommitted(pending activityPending) error {
	for _, deliveryID := range pending.PrunedPayloads {
		if err := os.Remove(filepath.Join(s.payloadRoot, activityPayloadFile(deliveryID))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := syncActivityDirectory(s.payloadRoot); err != nil {
		return err
	}
	if err := s.clearPending(); err != nil {
		return err
	}
	return s.removeEmptyStagingRoot()
}

func (s *ActivityStore) clearPending() error {
	if err := os.Remove(s.pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncActivityDirectory(s.root)
}

func (s *ActivityStore) validateFiles() error {
	expected := make(map[string]ActivityPayloadReference)
	for _, record := range s.projection.Events {
		if record.Payload != nil {
			expected[record.Payload.File] = *record.Payload
		}
	}
	entries, err := os.ReadDir(s.payloadRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		reference, found := expected[entry.Name()]
		if !found || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("event activity store: orphan or unsafe private payload %s", entry.Name())
		}
		body, err := readPrivateActivityFile(filepath.Join(s.payloadRoot, entry.Name()))
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		if !json.Valid(body) || int64(len(body)) != reference.Size || hex.EncodeToString(digest[:]) != reference.SHA256 {
			return fmt.Errorf("event activity store: private payload %s conflicts with its reference", entry.Name())
		}
		delete(expected, entry.Name())
	}
	if len(expected) != 0 {
		return errors.New("event activity store: private payload corpus is incomplete")
	}
	if info, err := os.Lstat(s.stagingRoot); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return errors.New("event activity store: staging directory is unsafe")
		}
		entries, err := os.ReadDir(s.stagingRoot)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return errors.New("event activity store: staged payload is unsafe")
			}
			if _, err := readPrivateActivityFile(filepath.Join(s.stagingRoot, entry.Name())); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *ActivityStore) removeEmptyStagingRoot() error {
	entries, err := os.ReadDir(s.stagingRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || len(entries) != 0 {
		return err
	}
	if err := os.Remove(s.stagingRoot); err != nil {
		return err
	}
	return syncActivityDirectory(s.root)
}

func activityPayloadDeliveries(records []ActivityRecord) []string {
	result := make([]string, 0, len(records))
	for _, record := range records {
		if record.Payload != nil {
			result = append(result, record.DeliveryID)
		}
	}
	return result
}

func validateActivityDelivery(deliveryID string) error {
	if deliveryID == "" || deliveryID != strings.TrimSpace(deliveryID) || len(deliveryID) > 512 {
		return errors.New("event activity store: delivery ID is invalid")
	}
	return nil
}

func requireActivityDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("event activity store: directory is missing or unsafe: %s", filepath.Base(path))
	}
	return nil
}

func ensureActivityDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return requireActivityDirectory(path)
}

func readActivityProjection(path string) (ActivityProjection, error) {
	data, err := readPrivateActivityFile(path)
	if err != nil {
		return ActivityProjection{}, err
	}
	var projection ActivityProjection
	if err := decodeStrictActivityJSON(data, &projection); err != nil {
		return ActivityProjection{}, err
	}
	if err := projection.Validate(); err != nil {
		return ActivityProjection{}, err
	}
	return projection, nil
}

func decodeStrictActivityJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("event activity store: JSON has trailing content")
	}
	return nil
}

func writeActivityProjection(path string, projection ActivityProjection) error {
	return writeActivityJSON(path, projection)
}

func writeActivityJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".activity-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncActivityDirectory(filepath.Dir(path))
}

func readOptionalPrivateActivityFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("event activity store: private file is unsafe: %s", filepath.Base(path))
	}
	return os.ReadFile(path)
}
