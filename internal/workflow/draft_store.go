package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const DraftSchemaVersion = 1

var ErrDraftConflict = errors.New("workflow draft: revision conflict")

type DraftSnapshot struct {
	Schema int     `json:"schema"`
	Drafts []Draft `json:"drafts"`
}

type DraftStore struct {
	mu    sync.RWMutex
	path  string
	state DraftSnapshot
}

func OpenDraftStore(path string) (*DraftStore, error) {
	if path == "" {
		return nil, errors.New("workflow draft: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("workflow draft: create directory: %w", err)
	}
	store := &DraftStore{
		path: path,
		state: DraftSnapshot{
			Schema: DraftSchemaVersion,
			Drafts: []Draft{},
		},
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workflow draft: read: %w", err)
	}
	if len(data) > MaxAggregateMarkdown+MaxAuthoringBodyBytes {
		return nil, errors.New("workflow draft: file is too large")
	}
	state, err := decodeDraftSnapshot(data)
	if err != nil {
		return nil, err
	}
	if err := validateDraftSnapshot(state); err != nil {
		return nil, err
	}
	store.state = cloneDraftSnapshot(state)
	return store, nil
}

func (s *DraftStore) Snapshot() DraftSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneDraftSnapshot(s.state)
}

func (s *DraftStore) Draft(id string) (Draft, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, draft := range s.state.Drafts {
		if draft.WorkflowID == id {
			return draft, true
		}
	}
	return Draft{}, false
}

func (s *DraftStore) Create(draft Draft) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if draft.Revision != 1 || draft.BaseWorkflowRevision != 0 {
		return Draft{}, errors.New("workflow draft: new draft revisions are invalid")
	}
	if _, found := draftByID(s.state.Drafts, draft.WorkflowID); found {
		return Draft{}, ErrDraftConflict
	}
	if err := draft.Validate(); err != nil {
		return Draft{}, err
	}
	next := cloneDraftSnapshot(s.state)
	next.Drafts = append(next.Drafts, draft)
	if err := s.persist(next); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *DraftStore) Save(id string, expectedRevision, expectedBase uint64, candidate Draft, now time.Time) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, found := draftByID(s.state.Drafts, id)
	if !found || s.state.Drafts[index].Revision != expectedRevision || s.state.Drafts[index].BaseWorkflowRevision != expectedBase {
		return Draft{}, ErrDraftConflict
	}
	current := s.state.Drafts[index]
	if candidate.WorkflowID != id || candidate.Revision != expectedRevision || candidate.BaseWorkflowRevision != expectedBase {
		return Draft{}, errors.New("workflow draft: server-owned fields changed")
	}
	candidate.Markdown = CanonicalizeMarkdown(candidate.Markdown)
	candidate.Revision++
	candidate.UpdatedAt = now.UTC()
	if err := candidate.Validate(); err != nil {
		return Draft{}, err
	}
	if PublishedEqual(current.Definition(1, time.Time{}), candidate.Definition(1, time.Time{})) {
		candidate.Revision = current.Revision
		candidate.UpdatedAt = current.UpdatedAt
		return candidate, nil
	}
	next := cloneDraftSnapshot(s.state)
	next.Drafts[index] = candidate
	if err := s.persist(next); err != nil {
		return Draft{}, err
	}
	return candidate, nil
}

func (s *DraftStore) Materialize(draft Draft, expectedPublishedRevision uint64) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := draftByID(s.state.Drafts, draft.WorkflowID); found || draft.Revision != 1 || draft.BaseWorkflowRevision != expectedPublishedRevision || expectedPublishedRevision == 0 {
		return Draft{}, ErrDraftConflict
	}
	if err := draft.Validate(); err != nil {
		return Draft{}, err
	}
	next := cloneDraftSnapshot(s.state)
	next.Drafts = append(next.Drafts, draft)
	if err := s.persist(next); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *DraftStore) Discard(id string, expectedRevision, expectedBase uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, found := draftByID(s.state.Drafts, id)
	if !found || s.state.Drafts[index].Revision != expectedRevision || s.state.Drafts[index].BaseWorkflowRevision != expectedBase {
		return ErrDraftConflict
	}
	next := cloneDraftSnapshot(s.state)
	next.Drafts = append(next.Drafts[:index], next.Drafts[index+1:]...)
	return s.persist(next)
}

func (s *DraftStore) AdvanceBase(id string, expectedRevision, expectedBase, nextBase uint64, now time.Time) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, found := draftByID(s.state.Drafts, id)
	if !found || s.state.Drafts[index].Revision != expectedRevision || s.state.Drafts[index].BaseWorkflowRevision != expectedBase {
		return Draft{}, ErrDraftConflict
	}
	next := cloneDraftSnapshot(s.state)
	draft := next.Drafts[index]
	draft.Revision++
	draft.BaseWorkflowRevision = nextBase
	draft.UpdatedAt = now.UTC()
	next.Drafts[index] = draft
	if err := s.persist(next); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *DraftStore) persist(next DraftSnapshot) error {
	if err := validateDraftSnapshot(next); err != nil {
		return err
	}
	sort.Slice(next.Drafts, func(i, j int) bool { return next.Drafts[i].WorkflowID < next.Drafts[j].WorkflowID })
	if err := writeDraftSnapshot(s.path, next); err != nil {
		return err
	}
	s.state = cloneDraftSnapshot(next)
	return nil
}

func validateDraftSnapshot(state DraftSnapshot) error {
	if state.Schema != DraftSchemaVersion {
		return fmt.Errorf("workflow draft: schema is %d, want %d", state.Schema, DraftSchemaVersion)
	}
	if len(state.Drafts) > MaxDefinitions {
		return fmt.Errorf("workflow draft: count exceeds %d", MaxDefinitions)
	}
	seen := make(map[string]bool, len(state.Drafts))
	total := 0
	for _, draft := range state.Drafts {
		if err := draft.Validate(); err != nil {
			return err
		}
		if seen[draft.WorkflowID] {
			return fmt.Errorf("workflow draft: ID %q is duplicated", draft.WorkflowID)
		}
		seen[draft.WorkflowID] = true
		total += len(draft.Markdown)
	}
	if total > MaxAggregateMarkdown {
		return fmt.Errorf("workflow draft: Markdown exceeds %d bytes", MaxAggregateMarkdown)
	}
	return nil
}

func draftByID(drafts []Draft, id string) (int, bool) {
	for i := range drafts {
		if drafts[i].WorkflowID == id {
			return i, true
		}
	}
	return 0, false
}

func cloneDraftSnapshot(state DraftSnapshot) DraftSnapshot {
	clone := state
	clone.Drafts = append([]Draft(nil), state.Drafts...)
	if clone.Drafts == nil {
		clone.Drafts = []Draft{}
	}
	return clone
}

func decodeDraftSnapshot(data []byte) (DraftSnapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state DraftSnapshot
	if err := decoder.Decode(&state); err != nil {
		return DraftSnapshot{}, fmt.Errorf("workflow draft: decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return DraftSnapshot{}, errors.New("workflow draft: decode: trailing content")
	}
	return state, nil
}

func writeDraftSnapshot(path string, state DraftSnapshot) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".workflow-drafts-*")
	if err != nil {
		return fmt.Errorf("workflow draft: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("workflow draft: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		temp.Close()
		return fmt.Errorf("workflow draft: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("workflow draft: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("workflow draft: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("workflow draft: replace: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("workflow draft: open directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("workflow draft: sync directory: %w", err)
	}
	return nil
}
