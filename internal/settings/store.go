package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/workflow"
)

const maxSettingsBytes = 2 << 20

var ErrRevisionConflict = errors.New("settings: revision conflict")

type Store struct {
	mu    sync.RWMutex
	path  string
	state Snapshot
}

type legacySnapshot struct {
	Schema    int                         `json:"schema"`
	Revision  uint64                      `json:"revision"`
	UpdatedAt time.Time                   `json:"updatedAt,omitempty"`
	Triggers  Triggers                    `json:"triggers"`
	Workflows []workflow.LegacyDefinition `json:"workflows"`
	Agents    AgentSettings               `json:"agents"`
	Runtime   RuntimeSettings             `json:"runtime"`
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
	schema, err := decodeSchema(data)
	if err != nil {
		return nil, err
	}
	if schema == 1 {
		legacy, err := decodeLegacy(data)
		if err != nil {
			return nil, err
		}
		if err := validateLegacy(legacy); err != nil {
			return nil, err
		}
		if err := preserveSchema1Backup(path, data); err != nil {
			return nil, err
		}
		state := migrateLegacy(legacy)
		if err := state.Validate(); err != nil {
			return nil, fmt.Errorf("settings: migrate schema 1: %w", err)
		}
		if err := write(path, state); err != nil {
			return nil, fmt.Errorf("settings: persist schema 2 migration: %w", err)
		}
		store.state = state.Clone()
		return store, nil
	}
	if schema != SchemaVersion {
		return nil, fmt.Errorf("settings: schema is %d, want 1 or %d", schema, SchemaVersion)
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
	if expectedRevision != s.state.Revision || candidate.Revision != expectedRevision {
		return s.state.Clone(), ErrRevisionConflict
	}
	if candidate.Schema != SchemaVersion || candidate.WorkflowRollbackIncompatible != s.state.WorkflowRollbackIncompatible ||
		(!candidate.UpdatedAt.IsZero() && !candidate.UpdatedAt.Equal(s.state.UpdatedAt)) {
		return s.state.Clone(), errors.New("settings: server-owned fields changed")
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

func (s *Store) ReconcileProviderNeutral(expectedRevision uint64, now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRevision != s.state.Revision {
		return s.state.Clone(), ErrRevisionConflict
	}
	desired := workflow.ProviderNeutralDefault(now)
	desiredDigest, err := workflow.Digest(desired)
	if err != nil {
		return s.state.Clone(), err
	}
	for _, existing := range s.state.Workflows {
		if existing.ID != workflow.ProviderNeutralID {
			continue
		}
		existingDigest, err := workflow.Digest(existing)
		if err != nil || existingDigest != desiredDigest {
			return s.state.Clone(), errors.New("settings: reserved provider-neutral workflow conflicts with the compiled definition")
		}
		return s.state.Clone(), nil
	}
	next := s.state.Clone()
	next.Workflows = append(next.Workflows, desired)
	next.Revision++
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

// MarkWorkflowRollbackIncompatible is intentionally a direct store mutation. Admission
// and continuation dispatch already run under CoordinatedWire's non-reentrant policy lock.
func (s *Store) MarkWorkflowRollbackIncompatible(now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.WorkflowRollbackIncompatible {
		return s.state.Clone(), nil
	}
	next := s.state.Clone()
	next.WorkflowRollbackIncompatible = true
	next.Revision++
	next.UpdatedAt = now.UTC()
	if err := write(s.path, next); err != nil {
		return s.state.Clone(), err
	}
	s.state = next
	return s.state.Clone(), nil
}

func ReadSchema1Backup(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("settings: read schema 1 backup: %w", err)
	}
	if len(data) > maxSettingsBytes {
		return Snapshot{}, errors.New("settings: schema 1 backup is too large")
	}
	legacy, err := decodeLegacy(data)
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateLegacy(legacy); err != nil {
		return Snapshot{}, err
	}
	return migrateLegacy(legacy), nil
}

func decodeSchema(data []byte) (int, error) {
	var envelope struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return 0, fmt.Errorf("settings: decode schema: %w", err)
	}
	return envelope.Schema, nil
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

func decodeLegacy(data []byte) (legacySnapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state legacySnapshot
	if err := decoder.Decode(&state); err != nil {
		return legacySnapshot{}, fmt.Errorf("settings: decode schema 1: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return legacySnapshot{}, errors.New("settings: decode schema 1: trailing content")
	}
	return state, nil
}

func validateLegacy(state legacySnapshot) error {
	if state.Schema != 1 {
		return fmt.Errorf("settings: legacy schema is %d, want 1", state.Schema)
	}
	if !validText(state.Triggers.LinearLabel.Label, maxLabelNameBytes) {
		return errors.New("settings: legacy Linear label is invalid")
	}
	if len(state.Workflows) == 0 || len(state.Workflows) > workflow.MaxDefinitions {
		return errors.New("settings: legacy workflow count is invalid")
	}
	definitions := make(map[string]workflow.LegacyDefinition, len(state.Workflows))
	for _, definition := range state.Workflows {
		if err := definition.Validate(); err != nil {
			return fmt.Errorf("settings: %w", err)
		}
		if _, duplicate := definitions[definition.ID]; duplicate {
			return fmt.Errorf("settings: legacy workflow ID %q is duplicated", definition.ID)
		}
		definitions[definition.ID] = definition
	}
	for name, id := range map[string]string{
		"Linear label": state.Triggers.LinearLabel.WorkflowID, "Linear comment": state.Triggers.LinearComment.WorkflowID,
	} {
		definition, found := definitions[id]
		if !found || !definition.Enabled {
			return fmt.Errorf("settings: legacy %s trigger must reference an enabled workflow", name)
		}
	}
	probe := Snapshot{
		Schema: SchemaVersion, Triggers: state.Triggers,
		ProtectedWorkflows: ProtectedWorkflowBindings{LinearFeedback: WorkflowBinding{WorkflowID: state.Triggers.LinearComment.WorkflowID}},
		Workflows:          []workflow.Definition{workflow.Default(time.Time{})}, Agents: state.Agents, Runtime: state.Runtime,
	}
	if err := validateProvider("principal", probe.Agents.Principal.ProviderSettings, codexEffort); err != nil {
		return err
	}
	if probe.Agents.Principal.MaxAttempts < minPrincipalAttempts || probe.Agents.Principal.MaxAttempts > maxPrincipalAttempts {
		return errors.New("settings: legacy principal max attempts are invalid")
	}
	if err := validateProvider("Codex child", probe.Agents.CodexChild, codexEffort); err != nil {
		return err
	}
	if err := validateProvider("Claude child", probe.Agents.ClaudeChild, claudeEffort); err != nil {
		return err
	}
	if probe.Runtime.MaxConcurrentRuns < minConcurrentRuns || probe.Runtime.MaxConcurrentRuns > maxConcurrentRuns {
		return errors.New("settings: legacy max concurrent Runs are invalid")
	}
	return nil
}

func migrateLegacy(state legacySnapshot) Snapshot {
	definitions := make([]workflow.Definition, 0, len(state.Workflows))
	for _, legacy := range state.Workflows {
		body := workflow.DefaultMarkdown()
		body += "\n\n## Migrated operator guidance\n"
		for _, step := range legacy.Steps {
			body += "\n- " + strings.TrimSpace(step)
		}
		body += "\n"
		definitions = append(definitions, workflow.Definition{
			ID: legacy.ID, Revision: 1, Name: legacy.Name, Enabled: legacy.Enabled,
			Markdown: workflow.CanonicalizeMarkdown(body), UpdatedAt: state.UpdatedAt,
		})
	}
	return Snapshot{
		Schema: SchemaVersion, Revision: state.Revision, UpdatedAt: state.UpdatedAt,
		Triggers: state.Triggers,
		ProtectedWorkflows: ProtectedWorkflowBindings{
			LinearFeedback: WorkflowBinding{WorkflowID: state.Triggers.LinearComment.WorkflowID},
		},
		Workflows: definitions, Agents: state.Agents, Runtime: state.Runtime,
	}
}

func preserveSchema1Backup(settingsPath string, data []byte) error {
	backupPath := filepath.Join(filepath.Dir(settingsPath), "settings.schema1.backup.json")
	existing, err := os.ReadFile(backupPath)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return errors.New("settings: schema 1 backup conflicts with current settings")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("settings: read schema 1 backup: %w", err)
	}
	return writeBytes(backupPath, data)
}

func write(path string, state Snapshot) error {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		return fmt.Errorf("settings: encode: %w", err)
	}
	return writeBytes(path, data.Bytes())
}

func writeBytes(path string, data []byte) error {
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
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("settings: write: %w", err)
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
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("settings: open directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("settings: sync directory: %w", err)
	}
	return nil
}
