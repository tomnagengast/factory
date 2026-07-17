package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
)

const maxPolicyBytes = 2 << 20

var (
	ErrSettingsConflict    = errors.New("policy: settings revision conflict")
	ErrRegistryConflict    = errors.New("policy: registry revision conflict")
	ErrTaskControlConflict = errors.New("policy: task-control revision conflict")
	ErrWorkflowConflict    = errors.New("policy: workflow revision conflict")
)

type Store struct {
	mu     sync.RWMutex
	path   string
	state  Snapshot
	writer snapshotWriter
}

type snapshotWriter func(string, Snapshot) (bool, error)

// Create writes a converted canonical snapshot. It never replaces an existing
// policy artifact; generation construction must choose a new destination.
func Create(path string, snapshot Snapshot) (*Store, error) {
	if path == "" {
		return nil, errors.New("policy: path is required")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("policy: invalid initial snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("policy: create directory: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, errors.New("policy: create: artifact already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("policy: inspect artifact: %w", err)
	}
	if _, err := writeSnapshot(path, snapshot); err != nil {
		return nil, err
	}
	return &Store{path: path, state: snapshot, writer: writeSnapshot}, nil
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("policy: path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("policy: inspect: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("policy: artifact must be a regular nonsymlink file")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("policy: artifact permissions are %04o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: read: %w", err)
	}
	if len(data) > maxPolicyBytes {
		return nil, errors.New("policy: file is too large")
	}
	model, err := decodeModel(data)
	if err != nil {
		return nil, err
	}
	snapshot, err := NewSnapshot(model)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, state: snapshot, writer: writeSnapshot}, nil
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{model: cloneModel(s.state.model)}
}

// UpdateSettings preserves the independent public settings revision. Like the
// existing settings endpoint, every accepted request advances it, including a
// semantic no-op.
func (s *Store) UpdateSettings(expected uint64, candidate Settings, now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.model
	if expected != current.Settings.Revision || candidate.Revision != expected {
		return s.snapshotLocked(), ErrSettingsConflict
	}
	if !candidate.UpdatedAt.IsZero() && !candidate.UpdatedAt.Equal(current.Settings.UpdatedAt) {
		return s.snapshotLocked(), errors.New("policy: settings server-owned fields changed")
	}
	next := cloneModel(current)
	revision, err := incrementRevision(current.Settings.Revision)
	if err != nil {
		return s.snapshotLocked(), err
	}
	next.Settings = candidate
	next.Settings.Revision = revision
	next.Settings.UpdatedAt = now.UTC()
	return s.persistLocked(next)
}

// UpdateRegistry requires both public dependencies but advances only the
// registry revision and changed per-entry revisions.
func (s *Store) UpdateRegistry(expectedRegistry, expectedSettings uint64, candidate Registry, now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.model
	if expectedSettings != current.Settings.Revision {
		return s.snapshotLocked(), ErrSettingsConflict
	}
	if expectedRegistry != current.Registry.Revision || candidate.Revision != expectedRegistry {
		return s.snapshotLocked(), ErrRegistryConflict
	}
	if !candidate.UpdatedAt.IsZero() && !candidate.UpdatedAt.Equal(current.Registry.UpdatedAt) {
		return s.snapshotLocked(), errors.New("policy: registry server-owned fields changed")
	}
	next := cloneModel(current)
	next.Registry = cloneRegistry(candidate)
	canonicalizeRegistry(&next.Registry)
	if err := reconcileRegistryRevisions(current.Registry, &next.Registry); err != nil {
		return s.snapshotLocked(), err
	}
	revision, err := incrementRevision(current.Registry.Revision)
	if err != nil {
		return s.snapshotLocked(), err
	}
	next.Registry.Revision = revision
	next.Registry.UpdatedAt = now.UTC()
	return s.persistLocked(next)
}

func (s *Store) PublishWorkflow(expectedSettings, expectedWorkflow uint64, candidate Workflow, now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.model
	if expectedSettings != current.Settings.Revision {
		return s.snapshotLocked(), ErrSettingsConflict
	}
	if candidate.Revision != expectedWorkflow {
		return s.snapshotLocked(), ErrWorkflowConflict
	}
	index := workflowIndex(current.Workflows, candidate.ID)
	if (index < 0 && expectedWorkflow != 0) || (index >= 0 && current.Workflows[index].Revision != expectedWorkflow) {
		return s.snapshotLocked(), ErrWorkflowConflict
	}
	next := cloneModel(current)
	revision, err := incrementRevision(expectedWorkflow)
	if err != nil {
		return s.snapshotLocked(), err
	}
	candidate.Markdown = canonicalizeMarkdown(candidate.Markdown)
	candidate.Revision = revision
	candidate.UpdatedAt = now.UTC()
	if index < 0 {
		next.Workflows = append(next.Workflows, candidate)
	} else {
		next.Workflows[index] = candidate
	}
	settingsRevision, err := incrementRevision(current.Settings.Revision)
	if err != nil {
		return s.snapshotLocked(), err
	}
	next.Settings.Revision = settingsRevision
	next.Settings.UpdatedAt = now.UTC()
	return s.persistLocked(next)
}

func (s *Store) DeleteWorkflow(expectedSettings, expectedWorkflow uint64, id string, now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.model
	if expectedSettings != current.Settings.Revision {
		return s.snapshotLocked(), ErrSettingsConflict
	}
	index := workflowIndex(current.Workflows, id)
	if index < 0 || current.Workflows[index].Revision != expectedWorkflow {
		return s.snapshotLocked(), ErrWorkflowConflict
	}
	if current.ProtectedWorkflows.LinearFeedback.WorkflowID == id {
		return s.snapshotLocked(), fmt.Errorf("policy: workflow %q is referenced by protected feedback", id)
	}
	for _, rule := range current.Registry.Rules {
		if rule.WorkflowID == id {
			return s.snapshotLocked(), fmt.Errorf("policy: workflow %q is referenced by rule %q", id, rule.ID)
		}
	}
	next := cloneModel(current)
	next.Workflows = slices.Delete(next.Workflows, index, index+1)
	revision, err := incrementRevision(current.Settings.Revision)
	if err != nil {
		return s.snapshotLocked(), err
	}
	next.Settings.Revision = revision
	next.Settings.UpdatedAt = now.UTC()
	return s.persistLocked(next)
}

func (s *Store) UpdateProtectedFeedback(expectedSettings uint64, workflowID string, now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.model
	if expectedSettings != current.Settings.Revision {
		return s.snapshotLocked(), ErrSettingsConflict
	}
	next := cloneModel(current)
	next.ProtectedWorkflows.LinearFeedback.WorkflowID = workflowID
	revision, err := incrementRevision(current.Settings.Revision)
	if err != nil {
		return s.snapshotLocked(), err
	}
	next.Settings.Revision = revision
	next.Settings.UpdatedAt = now.UTC()
	return s.persistLocked(next)
}

// SetProject preserves native task-control semantics: an exact no-op changes
// neither its public revision nor the internal generation.
func (s *Store) SetProject(expected uint64, projectID string, enabled bool, now time.Time) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.model
	if expected != current.TaskControl.Revision {
		return s.snapshotLocked(), ErrTaskControlConflict
	}
	if !projectIDPattern.MatchString(projectID) {
		return s.snapshotLocked(), errors.New("policy: project ID is invalid")
	}
	found := slices.Contains(current.TaskControl.EnabledProjectIDs, projectID)
	if found == enabled {
		return s.snapshotLocked(), nil
	}
	next := cloneModel(current)
	if enabled {
		next.TaskControl.EnabledProjectIDs = append(next.TaskControl.EnabledProjectIDs, projectID)
	} else {
		next.TaskControl.EnabledProjectIDs = slices.DeleteFunc(next.TaskControl.EnabledProjectIDs, func(value string) bool {
			return value == projectID
		})
	}
	sort.Strings(next.TaskControl.EnabledProjectIDs)
	revision, err := incrementRevision(current.TaskControl.Revision)
	if err != nil {
		return s.snapshotLocked(), err
	}
	next.TaskControl.Revision = revision
	next.TaskControl.UpdatedAt = now.UTC()
	return s.persistLocked(next)
}

func (s *Store) persistLocked(next Model) (Snapshot, error) {
	generation, err := incrementRevision(s.state.model.Generation)
	if err != nil {
		return s.snapshotLocked(), err
	}
	next.Generation = generation
	snapshot, err := NewSnapshot(next)
	if err != nil {
		return s.snapshotLocked(), err
	}
	replaced, err := s.writer(s.path, snapshot)
	if replaced {
		s.state = snapshot
	}
	if err != nil {
		return s.snapshotLocked(), err
	}
	if !replaced {
		return s.snapshotLocked(), errors.New("policy: write completed without replacing artifact")
	}
	return s.snapshotLocked(), nil
}

func (s *Store) snapshotLocked() Snapshot {
	return Snapshot{model: cloneModel(s.state.model)}
}

func reconcileRegistryRevisions(current Registry, next *Registry) error {
	currentRules := make(map[string]Rule, len(current.Rules))
	for _, rule := range current.Rules {
		currentRules[rule.ID] = rule
	}
	for index := range next.Rules {
		candidate := &next.Rules[index]
		previous, found := currentRules[candidate.ID]
		if !found {
			if candidate.Revision != 0 {
				return fmt.Errorf("policy: new rule %q revision must be zero", candidate.ID)
			}
			candidate.Revision = 1
			continue
		}
		if candidate.Revision != previous.Revision {
			return fmt.Errorf("policy: rule %q revision changed", candidate.ID)
		}
		if !ruleSemanticEqual(*candidate, previous) {
			revision, err := incrementRevision(candidate.Revision)
			if err != nil {
				return err
			}
			candidate.Revision = revision
		}
	}
	currentSchedules := make(map[string]Schedule, len(current.Schedules))
	for _, schedule := range current.Schedules {
		currentSchedules[schedule.ID] = schedule
	}
	for index := range next.Schedules {
		candidate := &next.Schedules[index]
		previous, found := currentSchedules[candidate.ID]
		if !found {
			if candidate.Revision != 0 {
				return fmt.Errorf("policy: new schedule %q revision must be zero", candidate.ID)
			}
			candidate.Revision = 1
			continue
		}
		if candidate.Revision != previous.Revision {
			return fmt.Errorf("policy: schedule %q revision changed", candidate.ID)
		}
		if !scheduleSemanticEqual(*candidate, previous) {
			revision, err := incrementRevision(candidate.Revision)
			if err != nil {
				return err
			}
			candidate.Revision = revision
		}
	}
	return nil
}

func canonicalizeRegistry(registry *Registry) {
	for index := range registry.Rules {
		registry.Rules[index].Target = canonicalTarget(registry.Rules[index].Target)
	}
	sort.Slice(registry.Rules, func(i, j int) bool { return registry.Rules[i].ID < registry.Rules[j].ID })
	for index := range registry.Schedules {
		for key := range registry.Schedules[index].Attributes {
			sort.Strings(registry.Schedules[index].Attributes[key])
		}
	}
	sort.Slice(registry.Schedules, func(i, j int) bool { return registry.Schedules[i].ID < registry.Schedules[j].ID })
}

func workflowIndex(workflows []Workflow, id string) int {
	for index := range workflows {
		if workflows[index].ID == id {
			return index
		}
	}
	return -1
}

func incrementRevision(value uint64) (uint64, error) {
	if value == math.MaxUint64 {
		return 0, errors.New("policy: revision exhausted")
	}
	return value + 1, nil
}

func decodeModel(data []byte) (Model, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var model Model
	if err := decoder.Decode(&model); err != nil {
		return Model{}, fmt.Errorf("policy: decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Model{}, errors.New("policy: decode: trailing content")
	}
	return model, nil
}

func writeSnapshot(path string, snapshot Snapshot) (bool, error) {
	return writeSnapshotWithDirectorySync(path, snapshot, func(directory *os.File) error {
		return directory.Sync()
	})
}

func writeSnapshotWithDirectorySync(path string, snapshot Snapshot, syncDirectory func(*os.File) error) (bool, error) {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot.model); err != nil {
		return false, fmt.Errorf("policy: encode: %w", err)
	}
	if data.Len() > maxPolicyBytes {
		return false, errors.New("policy: encoded file is too large")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".policy-*")
	if err != nil {
		return false, fmt.Errorf("policy: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return false, fmt.Errorf("policy: set permissions: %w", err)
	}
	if _, err := temporary.Write(data.Bytes()); err != nil {
		temporary.Close()
		return false, fmt.Errorf("policy: write: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, fmt.Errorf("policy: sync: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("policy: close: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return false, fmt.Errorf("policy: replace: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return true, fmt.Errorf("policy: open directory: %w", err)
	}
	defer directory.Close()
	if err := syncDirectory(directory); err != nil {
		return true, fmt.Errorf("policy: sync directory: %w", err)
	}
	return true, nil
}
