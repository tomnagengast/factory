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
	"time"
)

const ActivitySchemaVersion = 1

const activityProjectionFile = "activity.json"
const activityPayloadDirectory = "activity-payloads"

type ActivityEvent struct {
	Type       string    `json:"type"`
	Action     string    `json:"action"`
	ReceivedAt time.Time `json:"receivedAt"`
}

type ActivityPayloadReference struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type ActivityRecord struct {
	DeliveryID string                    `json:"deliveryId"`
	Payload    *ActivityPayloadReference `json:"payload,omitempty"`
	ActivityEvent
}

// ActivityProjection is the body-free activity view owned by the canonical
// event artifact. Events remain in retained source order and Total preserves
// the lifetime count even after pruning.
type ActivityProjection struct {
	Schema int              `json:"schema"`
	Total  uint64           `json:"total"`
	Events []ActivityRecord `json:"events"`
}

type ActivitySourceRecord struct {
	DeliveryID       string
	PayloadAvailable bool
	Event            ActivityEvent
}

func (p ActivityProjection) Clone() ActivityProjection {
	clone := p
	clone.Events = make([]ActivityRecord, len(p.Events))
	for index, record := range p.Events {
		clone.Events[index] = record
		if record.Payload != nil {
			reference := *record.Payload
			clone.Events[index].Payload = &reference
		}
	}
	return clone
}

func (p ActivityProjection) Validate() error {
	if p.Schema != ActivitySchemaVersion || p.Total < uint64(len(p.Events)) {
		return errors.New("event activity: invalid projection identity")
	}
	seenDeliveries := make(map[string]bool, len(p.Events))
	seenFiles := make(map[string]bool, len(p.Events))
	for _, record := range p.Events {
		if strings.TrimSpace(record.DeliveryID) == "" || len(record.DeliveryID) > 512 || seenDeliveries[record.DeliveryID] ||
			strings.TrimSpace(record.Type) == "" || len(record.Type) > maxFieldLength || strings.TrimSpace(record.Action) == "" || len(record.Action) > maxFieldLength ||
			record.ReceivedAt.IsZero() || record.ReceivedAt.Location() != time.UTC {
			return errors.New("event activity: invalid retained event")
		}
		seenDeliveries[record.DeliveryID] = true
		if record.Payload == nil {
			continue
		}
		expectedFile := activityPayloadFile(record.DeliveryID)
		if record.Payload.File != expectedFile || len(record.Payload.SHA256) != 64 || record.Payload.Size <= 0 || seenFiles[record.Payload.File] {
			return errors.New("event activity: invalid private payload reference")
		}
		if _, err := hex.DecodeString(record.Payload.SHA256); err != nil {
			return errors.New("event activity: invalid private payload digest")
		}
		seenFiles[record.Payload.File] = true
	}
	return nil
}

// ConvertActivity creates the canonical body-free projection from the complete
// legacy retained index and its private bodies. It rejects missing and orphaned
// bodies and preserves source ordering exactly.
func ConvertActivity(total uint64, records []ActivitySourceRecord, payloads map[string][]byte) (ActivityProjection, map[string][]byte, error) {
	projection := ActivityProjection{Schema: ActivitySchemaVersion, Total: total, Events: make([]ActivityRecord, 0, len(records))}
	corpus := make(map[string][]byte, len(payloads))
	referenced := make(map[string]bool, len(payloads))
	for _, source := range records {
		record := ActivityRecord{DeliveryID: source.DeliveryID, ActivityEvent: source.Event}
		record.ReceivedAt = record.ReceivedAt.UTC()
		if source.PayloadAvailable {
			body, found := payloads[source.DeliveryID]
			if !found || !json.Valid(body) {
				return ActivityProjection{}, nil, fmt.Errorf("event activity: private payload %s is missing or invalid", source.DeliveryID)
			}
			digest := sha256.Sum256(body)
			record.Payload = &ActivityPayloadReference{
				File: activityPayloadFile(source.DeliveryID), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)),
			}
			corpus[source.DeliveryID] = slices.Clone(body)
			referenced[source.DeliveryID] = true
		}
		projection.Events = append(projection.Events, record)
	}
	for deliveryID := range payloads {
		if !referenced[deliveryID] {
			return ActivityProjection{}, nil, fmt.Errorf("event activity: orphan private payload %s", deliveryID)
		}
	}
	if err := projection.Validate(); err != nil {
		return ActivityProjection{}, nil, err
	}
	return projection, corpus, nil
}

// MaterializeActivity writes a disposable or staged canonical event activity
// artifact. The projection contains hashes only; private JSON bodies are kept
// in a private sibling directory under deterministic names.
func MaterializeActivity(root string, projection ActivityProjection, payloads map[string][]byte) error {
	root = filepath.Clean(root)
	if root == "." || root == string(os.PathSeparator) {
		return errors.New("event activity: destination is required")
	}
	if err := projection.Validate(); err != nil {
		return err
	}
	if err := validateActivityCorpus(projection, payloads); err != nil {
		return err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return fmt.Errorf("event activity: create destination: %w", err)
	}
	payloadRoot := filepath.Join(root, activityPayloadDirectory)
	if err := os.Mkdir(payloadRoot, 0o700); err != nil {
		return fmt.Errorf("event activity: create payload directory: %w", err)
	}
	for _, record := range projection.Events {
		if record.Payload == nil {
			continue
		}
		if err := writePrivateActivityFile(filepath.Join(payloadRoot, record.Payload.File), payloads[record.DeliveryID]); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return fmt.Errorf("event activity: encode projection: %w", err)
	}
	data = append(data, '\n')
	if err := writePrivateActivityFile(filepath.Join(root, activityProjectionFile), data); err != nil {
		return err
	}
	return syncActivityDirectory(payloadRoot, root)
}

func ReadActivity(root string) (ActivityProjection, map[string][]byte, error) {
	root = filepath.Clean(root)
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return ActivityProjection{}, nil, errors.New("event activity: source directory is unsafe")
	}
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		return ActivityProjection{}, nil, err
	}
	if len(rootEntries) != 2 || rootEntries[0].Name() != activityPayloadDirectory || rootEntries[1].Name() != activityProjectionFile {
		return ActivityProjection{}, nil, errors.New("event activity: source directory contains unknown artifacts")
	}
	data, err := readPrivateActivityFile(filepath.Join(root, activityProjectionFile))
	if err != nil {
		return ActivityProjection{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var projection ActivityProjection
	if err := decoder.Decode(&projection); err != nil {
		return ActivityProjection{}, nil, fmt.Errorf("event activity: decode projection: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ActivityProjection{}, nil, errors.New("event activity: projection has trailing content")
	}
	if err := projection.Validate(); err != nil {
		return ActivityProjection{}, nil, err
	}
	payloadRoot := filepath.Join(root, activityPayloadDirectory)
	if info, err := os.Lstat(payloadRoot); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return ActivityProjection{}, nil, errors.New("event activity: payload directory is unsafe")
	}
	corpus := make(map[string][]byte)
	expected := make(map[string]string)
	for _, record := range projection.Events {
		if record.Payload != nil {
			expected[record.Payload.File] = record.DeliveryID
		}
	}
	entries, err := os.ReadDir(payloadRoot)
	if err != nil {
		return ActivityProjection{}, nil, err
	}
	for _, entry := range entries {
		deliveryID, found := expected[entry.Name()]
		if !found || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return ActivityProjection{}, nil, fmt.Errorf("event activity: orphan or unsafe private payload %s", entry.Name())
		}
		body, err := readPrivateActivityFile(filepath.Join(payloadRoot, entry.Name()))
		if err != nil {
			return ActivityProjection{}, nil, err
		}
		corpus[deliveryID] = body
	}
	if err := validateActivityCorpus(projection, corpus); err != nil {
		return ActivityProjection{}, nil, err
	}
	return projection.Clone(), cloneActivityCorpus(corpus), nil
}

func validateActivityCorpus(projection ActivityProjection, payloads map[string][]byte) error {
	references := 0
	for _, record := range projection.Events {
		if record.Payload == nil {
			continue
		}
		references++
		body, found := payloads[record.DeliveryID]
		if !found || !json.Valid(body) || int64(len(body)) != record.Payload.Size {
			return fmt.Errorf("event activity: private payload %s conflicts with its reference", record.DeliveryID)
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != record.Payload.SHA256 {
			return fmt.Errorf("event activity: private payload %s digest conflicts", record.DeliveryID)
		}
	}
	if references != len(payloads) {
		return errors.New("event activity: private payload corpus is incomplete or orphaned")
	}
	return nil
}

func activityPayloadFile(deliveryID string) string {
	digest := sha256.Sum256([]byte(deliveryID))
	return hex.EncodeToString(digest[:]) + ".json"
}

func writePrivateActivityFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("event activity: create private file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("event activity: write private file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("event activity: sync private file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("event activity: close private file: %w", err)
	}
	return nil
}

func readPrivateActivityFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("event activity: private file is missing or unsafe: %s", filepath.Base(path))
	}
	return os.ReadFile(path)
}

func syncActivityDirectory(paths ...string) error {
	for _, path := range paths {
		directory, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("event activity: open directory for sync: %w", err)
		}
		err = directory.Sync()
		closeErr := directory.Close()
		if err != nil {
			return fmt.Errorf("event activity: sync directory: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("event activity: close directory: %w", closeErr)
		}
	}
	return nil
}

func cloneActivityCorpus(corpus map[string][]byte) map[string][]byte {
	clone := make(map[string][]byte, len(corpus))
	for deliveryID, body := range corpus {
		clone[deliveryID] = slices.Clone(body)
	}
	return clone
}
