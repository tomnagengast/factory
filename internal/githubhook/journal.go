package githubhook

import (
	"context"
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

const stateVersion = 1

type record struct {
	Sequence uint64 `json:"sequence"`
	Event
}

type diskState struct {
	Version int      `json:"version"`
	Total   uint64   `json:"total"`
	Events  []record `json:"events"`
}

type Journal struct {
	mu    sync.Mutex
	path  string
	limit int
	state diskState
}

type Filter struct {
	Repository  string
	PullRequest int
	HeadBranch  string
}

type Batch struct {
	Cursor uint64  `json:"cursor"`
	Events []Event `json:"events"`
}

func Open(path string, limit int) (*Journal, error) {
	if path == "" {
		return nil, errors.New("github journal: path is required")
	}
	if limit < 1 {
		return nil, errors.New("github journal: limit must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("github journal: create directory: %w", err)
	}

	j := &Journal{path: path, limit: limit, state: diskState{Version: stateVersion}}
	state, err := readState(path)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	state.Events = prune(state.Events, limit)
	j.state = state
	return j, nil
}

func (j *Journal) Add(event Event) (bool, error) {
	if event.DeliveryID == "" || event.Type == "" || event.Repository == "" {
		return false, errors.New("github journal: delivery, type, and repository are required")
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	for _, existing := range j.state.Events {
		if existing.DeliveryID == event.DeliveryID {
			return false, nil
		}
	}

	next := j.state
	next.Total++
	next.Events = slices.Clone(j.state.Events)
	next.Events = append([]record{{Sequence: next.Total, Event: event}}, next.Events...)
	next.Events = prune(next.Events, j.limit)
	if err := writeState(j.path, next); err != nil {
		return false, err
	}
	j.state = next
	return true, nil
}

func (f Filter) Validate() error {
	parts := strings.Split(f.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return errors.New("github journal: repository must be owner/name")
	}
	if f.PullRequest < 1 && strings.TrimSpace(f.HeadBranch) == "" {
		return errors.New("github journal: pull request or head branch is required")
	}
	return nil
}

func Read(path string, filter Filter, after uint64) (Batch, error) {
	if err := filter.Validate(); err != nil {
		return Batch{}, err
	}
	state, err := readState(path)
	if errors.Is(err, os.ErrNotExist) {
		return Batch{Cursor: after, Events: []Event{}}, nil
	}
	if err != nil {
		return Batch{}, err
	}

	batch := Batch{Cursor: max(after, state.Total), Events: []Event{}}
	for i := len(state.Events) - 1; i >= 0; i-- {
		record := state.Events[i]
		if record.Sequence <= after || !filter.matches(record.Event) {
			continue
		}
		batch.Events = append(batch.Events, record.Event)
	}
	return batch, nil
}

func Wait(ctx context.Context, path string, filter Filter, after uint64, interval time.Duration) (Batch, error) {
	if interval <= 0 {
		return Batch{}, errors.New("github journal: poll interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	cursor := after
	for {
		batch, err := Read(path, filter, cursor)
		if err != nil {
			return Batch{}, err
		}
		cursor = batch.Cursor
		if len(batch.Events) > 0 {
			return batch, nil
		}
		select {
		case <-ctx.Done():
			return Batch{Cursor: cursor, Events: []Event{}}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (f Filter) matches(event Event) bool {
	if !strings.EqualFold(f.Repository, event.Repository) {
		return false
	}
	if f.PullRequest > 0 && slices.Contains(event.PullRequests, f.PullRequest) {
		return true
	}
	return f.HeadBranch != "" && event.HeadBranch == f.HeadBranch
}

func prune(records []record, limit int) []record {
	if len(records) <= limit {
		return records
	}
	return records[:limit]
}

func readState(path string) (diskState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return diskState{}, err
	}
	var state diskState
	if err := json.Unmarshal(data, &state); err != nil {
		return diskState{}, fmt.Errorf("github journal: decode: %w", err)
	}
	if state.Version != stateVersion {
		return diskState{}, fmt.Errorf("github journal: unsupported state version %d", state.Version)
	}
	return state, nil
}

func writeState(path string, state diskState) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".github-events-*")
	if err != nil {
		return fmt.Errorf("github journal: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("github journal: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		temp.Close()
		return fmt.Errorf("github journal: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("github journal: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("github journal: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("github journal: replace: %w", err)
	}
	return nil
}
