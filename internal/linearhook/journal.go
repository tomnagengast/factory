package linearhook

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
	IssueIdentifier string
	IssueID         string
}

type Batch struct {
	Cursor uint64  `json:"cursor"`
	Events []Event `json:"events"`
}

func Open(path string, limit int) (*Journal, error) {
	if path == "" {
		return nil, errors.New("Linear comment journal: path is required")
	}
	if limit < 1 {
		return nil, errors.New("Linear comment journal: limit must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("Linear comment journal: create directory: %w", err)
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
	return j.add(0, event)
}

func (j *Journal) AddAt(sequence uint64, event Event) (bool, error) {
	if sequence < 1 {
		return false, errors.New("Linear comment journal: sequence must be positive")
	}
	return j.add(sequence, event)
}

func (j *Journal) add(sequence uint64, event Event) (bool, error) {
	if event.DeliveryID == "" || event.CommentID == "" || event.IssueID == "" {
		return false, errors.New("Linear comment journal: delivery, comment, and issue IDs are required")
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	for _, existing := range j.state.Events {
		if existing.DeliveryID == event.DeliveryID {
			return false, nil
		}
	}
	if sequence > 0 && sequence != j.state.Total+1 {
		return false, fmt.Errorf("Linear comment journal: sequence %d does not follow %d", sequence, j.state.Total)
	}

	next := j.state
	if sequence == 0 {
		next.Total++
	} else {
		next.Total = sequence
	}
	next.Events = slices.Clone(j.state.Events)
	next.Events = append([]record{{Sequence: next.Total, Event: event}}, next.Events...)
	next.Events = prune(next.Events, j.limit)
	if err := writeState(j.path, next); err != nil {
		return false, err
	}
	j.state = next
	return true, nil
}

func (j *Journal) Total() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.Total
}

func (f Filter) Validate() error {
	if strings.TrimSpace(f.IssueIdentifier) == "" && strings.TrimSpace(f.IssueID) == "" {
		return errors.New("Linear comment journal: issue identifier or ID is required")
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
		return Batch{}, errors.New("Linear comment journal: poll interval must be positive")
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
	if f.IssueIdentifier != "" && strings.EqualFold(f.IssueIdentifier, event.IssueIdentifier) {
		return true
	}
	return f.IssueID != "" && f.IssueID == event.IssueID
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
		return diskState{}, fmt.Errorf("Linear comment journal: decode: %w", err)
	}
	if state.Version != stateVersion {
		return diskState{}, fmt.Errorf("Linear comment journal: unsupported state version %d", state.Version)
	}
	return state, nil
}

func writeState(path string, state diskState) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".linear-comments-*")
	if err != nil {
		return fmt.Errorf("Linear comment journal: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("Linear comment journal: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		temp.Close()
		return fmt.Errorf("Linear comment journal: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("Linear comment journal: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("Linear comment journal: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("Linear comment journal: replace: %w", err)
	}
	return nil
}
