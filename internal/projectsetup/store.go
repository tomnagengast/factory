package projectsetup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const storeVersion = 1

type State string

const (
	StateAwaitingMetadata State = "awaiting_metadata"
	StatePending          State = "pending"
	StateRunning          State = "running"
	StateSucceeded        State = "succeeded"
	StateFailed           State = "failed"
)

type Entry struct {
	Spec
	State         State      `json:"state"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"lastError,omitempty"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	ProvisionedAt *time.Time `json:"provisionedAt,omitempty"`
}

type Snapshot struct {
	Total            int     `json:"total"`
	AwaitingMetadata int     `json:"awaitingMetadata"`
	Pending          int     `json:"pending"`
	Running          int     `json:"running"`
	Succeeded        int     `json:"succeeded"`
	Failed           int     `json:"failed"`
	Entries          []Entry `json:"entries,omitempty"`
}

type PublicSnapshot struct {
	Total            int `json:"total"`
	AwaitingMetadata int `json:"awaitingMetadata"`
	Pending          int `json:"pending"`
	Running          int `json:"running"`
	Succeeded        int `json:"succeeded"`
	Failed           int `json:"failed"`
}

type diskState struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	state diskState
}

func Open(path string, now time.Time) (*Store, error) {
	if path == "" {
		return nil, errors.New("project setup store: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("project setup store: create directory: %w", err)
	}
	store := &Store{path: path, state: diskState{Version: storeVersion}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("project setup store: read: %w", err)
	}
	if err := json.Unmarshal(data, &store.state); err != nil {
		return nil, fmt.Errorf("project setup store: decode: %w", err)
	}
	if store.state.Version != storeVersion {
		return nil, fmt.Errorf("project setup store: unsupported version %d", store.state.Version)
	}
	recovered := false
	for i := range store.state.Entries {
		entry := &store.state.Entries[i]
		if entry.State == StateRunning {
			entry.State = StatePending
			entry.NextAttemptAt = nil
			entry.UpdatedAt = now.UTC()
			recovered = true
		}
		if err := validateEntry(*entry); err != nil {
			return nil, err
		}
	}
	if recovered {
		if err := writeStore(path, store.state); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) RecordIncomplete(request Request, now time.Time) error {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	for index, entry := range s.state.Entries {
		if entry.ProjectID != request.ProjectID {
			continue
		}
		if entry.Repository != "" {
			return permanent(errors.New("project setup: GitHub Repo and Local Path cannot be removed after project setup is admitted"))
		}
		if entry.ProjectName == request.ProjectName {
			return nil
		}
		next := cloneDiskState(s.state)
		next.Entries[index].ProjectName = request.ProjectName
		next.Entries[index].UpdatedAt = now
		if err := writeStore(s.path, next); err != nil {
			return err
		}
		s.state = next
		return nil
	}
	entry := Entry{
		Spec:  Spec{ProjectID: request.ProjectID, ProjectName: request.ProjectName},
		State: StateAwaitingMetadata, CreatedAt: now, UpdatedAt: now,
	}
	next := cloneDiskState(s.state)
	next.Entries = append(next.Entries, entry)
	if err := writeStore(s.path, next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) Upsert(spec Spec, now time.Time) (bool, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneDiskState(s.state)
	for i := range next.Entries {
		entry := &next.Entries[i]
		if entry.ProjectID != spec.ProjectID {
			continue
		}
		wasAwaiting := entry.Repository == ""
		if !wasAwaiting && (entry.Repository != spec.Repository || entry.LocalPath != spec.LocalPath) {
			return false, permanent(errors.New("project setup: repository and local path are immutable after project setup is admitted; create a new Linear project instead"))
		}
		changed := wasAwaiting || entry.ProjectName != spec.ProjectName || entry.CloudURL != spec.CloudURL
		needsProvision := spec.Managed && (wasAwaiting || entry.State == StateFailed)
		entry.Spec = spec
		entry.UpdatedAt = now
		if !spec.Managed {
			entry.State = StateSucceeded
			entry.LastError = ""
			entry.NextAttemptAt = nil
			if entry.ProvisionedAt == nil {
				entry.ProvisionedAt = timePointer(now)
			}
		} else if needsProvision {
			entry.State = StatePending
			entry.LastError = ""
			entry.NextAttemptAt = nil
		}
		if !changed && !needsProvision {
			return false, nil
		}
		if err := writeStore(s.path, next); err != nil {
			return false, err
		}
		s.state = next
		return needsProvision, nil
	}

	state := StateSucceeded
	var provisionedAt *time.Time
	if spec.Managed {
		state = StatePending
	} else {
		provisionedAt = timePointer(now)
	}
	next.Entries = append(next.Entries, Entry{
		Spec: spec, State: state, CreatedAt: now, UpdatedAt: now, ProvisionedAt: provisionedAt,
	})
	if err := writeStore(s.path, next); err != nil {
		return false, err
	}
	s.state = next
	return spec.Managed, nil
}

func (s *Store) Claim(now time.Time) (Entry, bool, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneDiskState(s.state)
	for i := range next.Entries {
		entry := &next.Entries[i]
		if !entry.Managed || (entry.State != StatePending && entry.State != StateFailed) {
			continue
		}
		if entry.NextAttemptAt != nil && entry.NextAttemptAt.After(now) {
			continue
		}
		entry.State = StateRunning
		entry.Attempts++
		entry.LastError = ""
		entry.NextAttemptAt = nil
		entry.UpdatedAt = now
		if err := writeStore(s.path, next); err != nil {
			return Entry{}, false, err
		}
		s.state = next
		return *entry, true, nil
	}
	return Entry{}, false, nil
}

func (s *Store) Complete(projectID string, now time.Time) error {
	return s.updateTerminal(projectID, StateSucceeded, "", time.Time{}, now)
}

func (s *Store) Fail(projectID, detail string, nextAttemptAt, now time.Time) error {
	detail = strings.TrimSpace(detail)
	if len(detail) > 2048 {
		detail = detail[:2048]
	}
	return s.updateTerminal(projectID, StateFailed, detail, nextAttemptAt, now)
}

func (s *Store) updateTerminal(projectID string, state State, detail string, nextAttemptAt, now time.Time) error {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneDiskState(s.state)
	for i := range next.Entries {
		entry := &next.Entries[i]
		if entry.ProjectID != projectID {
			continue
		}
		entry.State = state
		entry.LastError = detail
		entry.UpdatedAt = now
		entry.NextAttemptAt = nil
		if state == StateSucceeded {
			entry.ProvisionedAt = timePointer(now)
		} else {
			entry.NextAttemptAt = timePointer(nextAttemptAt.UTC())
		}
		if err := writeStore(s.path, next); err != nil {
			return err
		}
		s.state = next
		return nil
	}
	return fmt.Errorf("project setup store: project %s not found", projectID)
}

func (s *Store) RepositorySpecs() []Spec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	specs := make([]Spec, 0, len(s.state.Entries))
	for _, entry := range s.state.Entries {
		if entry.Repository != "" {
			specs = append(specs, entry.Spec)
		}
	}
	return specs
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := Snapshot{Total: len(s.state.Entries), Entries: slices.Clone(s.state.Entries)}
	for _, entry := range s.state.Entries {
		switch entry.State {
		case StateAwaitingMetadata:
			snapshot.AwaitingMetadata++
		case StatePending:
			snapshot.Pending++
		case StateRunning:
			snapshot.Running++
		case StateSucceeded:
			snapshot.Succeeded++
		case StateFailed:
			snapshot.Failed++
		}
	}
	return snapshot
}

func (s *Store) PublicSnapshot() PublicSnapshot {
	snapshot := s.Snapshot()
	return PublicSnapshot{
		Total: snapshot.Total, AwaitingMetadata: snapshot.AwaitingMetadata,
		Pending: snapshot.Pending, Running: snapshot.Running,
		Succeeded: snapshot.Succeeded, Failed: snapshot.Failed,
	}
}

func validateEntry(entry Entry) error {
	if entry.ProjectID == "" || entry.ProjectName == "" || entry.CreatedAt.IsZero() || entry.UpdatedAt.IsZero() {
		return errors.New("project setup store: invalid persisted entry identity")
	}
	if entry.State == StateAwaitingMetadata {
		if entry.Repository != "" {
			return errors.New("project setup store: awaiting entry has repository metadata")
		}
		return nil
	}
	if !validRepository(entry.Repository) || !filepath.IsAbs(entry.LocalPath) || !filepath.IsAbs(entry.ManagedRoot) || entry.BaseBranch != "main" {
		return errors.New("project setup store: invalid persisted repository metadata")
	}
	if entry.RepoURL != "git@github.com:"+entry.Repository+".git" || entry.Managed != entry.Bootstrap {
		return errors.New("project setup store: invalid persisted repository policy")
	}
	if entry.CloudURL != "" {
		cloudURL, err := normalizeCloudURL(entry.CloudURL)
		if err != nil || cloudURL != entry.CloudURL {
			return errors.New("project setup store: invalid persisted Cloud URL")
		}
	}
	switch entry.State {
	case StatePending, StateRunning, StateSucceeded, StateFailed:
		return nil
	default:
		return fmt.Errorf("project setup store: invalid persisted state %q", entry.State)
	}
}

func cloneDiskState(value diskState) diskState {
	return diskState{Version: value.Version, Entries: slices.Clone(value.Entries)}
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func writeStore(path string, value diskState) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".project-setups-*")
	if err != nil {
		return fmt.Errorf("project setup store: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("project setup store: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		temp.Close()
		return fmt.Errorf("project setup store: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("project setup store: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("project setup store: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("project setup store: replace: %w", err)
	}
	return nil
}
