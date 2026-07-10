package agentrun

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"
)

const stateVersion = 1

var issueIdentifierPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*$`)

type State string

const (
	StatePending   State = "pending"
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateBlocked   State = "blocked"
	StateFailed    State = "failed"
)

func (s State) Active() bool {
	return s == StatePending || s == StateStarting || s == StateRunning
}

type Trigger struct {
	DeliveryID      string
	IssueIdentifier string
	Kind            string
}

type Run struct {
	ID                string     `json:"id"`
	IssueIdentifier   string     `json:"issueIdentifier"`
	TriggerKind       string     `json:"triggerKind"`
	DeliveryIDs       []string   `json:"deliveryIds"`
	State             State      `json:"state"`
	SessionName       string     `json:"sessionName,omitempty"`
	RunDirectory      string     `json:"runDirectory,omitempty"`
	Attempts          int        `json:"attempts"`
	DuplicateTriggers uint64     `json:"duplicateTriggers"`
	Detail            string     `json:"detail,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
}

type PublicRun struct {
	ID                string     `json:"id"`
	State             State      `json:"state"`
	Attempts          int        `json:"attempts"`
	DuplicateTriggers uint64     `json:"duplicateTriggers"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
}

type Snapshot struct {
	Total  uint64
	Active int
	Runs   []Run
}

type PublicSnapshot struct {
	Total  uint64      `json:"total"`
	Active int         `json:"active"`
	Runs   []PublicRun `json:"runs"`
}

type diskState struct {
	Version int    `json:"version"`
	Total   uint64 `json:"total"`
	Runs    []Run  `json:"runs"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	limit int
	state diskState
}

func Open(path string, limit int) (*Store, error) {
	if path == "" {
		return nil, errors.New("agent run store: path is required")
	}
	if limit < 1 {
		return nil, errors.New("agent run store: limit must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("agent run store: create directory: %w", err)
	}

	s := &Store{path: path, limit: limit, state: diskState{Version: stateVersion}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent run store: read: %w", err)
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("agent run store: decode: %w", err)
	}
	if s.state.Version != stateVersion {
		return nil, fmt.Errorf("agent run store: unsupported state version %d", s.state.Version)
	}
	s.state.Runs = prune(s.state.Runs, limit)
	return s, nil
}

func (s *Store) Claim(trigger Trigger, now time.Time) (Run, bool, error) {
	if trigger.DeliveryID == "" {
		return Run{}, false, errors.New("agent run store: delivery ID is required")
	}
	if !issueIdentifierPattern.MatchString(trigger.IssueIdentifier) {
		return Run{}, false, fmt.Errorf("agent run store: invalid issue identifier %q", trigger.IssueIdentifier)
	}
	if trigger.Kind == "" {
		return Run{}, false, errors.New("agent run store: trigger kind is required")
	}

	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.state.Runs {
		run := s.state.Runs[i]
		if slices.Contains(run.DeliveryIDs, trigger.DeliveryID) {
			return run, false, nil
		}
		if run.IssueIdentifier == trigger.IssueIdentifier && run.State.Active() {
			next := s.state
			next.Runs = slices.Clone(s.state.Runs)
			nextRun := &next.Runs[i]
			nextRun.DeliveryIDs = append(slices.Clone(nextRun.DeliveryIDs), trigger.DeliveryID)
			nextRun.DuplicateTriggers++
			nextRun.UpdatedAt = now
			if err := writeState(s.path, next); err != nil {
				return Run{}, false, err
			}
			s.state = next
			return *nextRun, false, nil
		}
	}

	id, err := newID()
	if err != nil {
		return Run{}, false, err
	}
	run := Run{
		ID:              id,
		IssueIdentifier: trigger.IssueIdentifier,
		TriggerKind:     trigger.Kind,
		DeliveryIDs:     []string{trigger.DeliveryID},
		State:           StatePending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	next := s.state
	next.Total++
	next.Runs = append([]Run{run}, next.Runs...)
	next.Runs = prune(next.Runs, s.limit)
	if err := writeState(s.path, next); err != nil {
		return Run{}, false, err
	}
	s.state = next
	return run, true, nil
}

func (s *Store) MarkStarting(id, sessionName, runDirectory string, now time.Time) error {
	if sessionName == "" || runDirectory == "" {
		return errors.New("agent run store: session name and run directory are required")
	}
	return s.update(id, now, func(run *Run) error {
		if run.State != StatePending {
			return fmt.Errorf("cannot start run in state %q", run.State)
		}
		run.State = StateStarting
		run.SessionName = sessionName
		run.RunDirectory = runDirectory
		run.Detail = ""
		return nil
	})
}

func (s *Store) MarkRunning(id string, attempts int, now time.Time) error {
	return s.update(id, now, func(run *Run) error {
		if run.State != StateStarting && run.State != StateRunning {
			return fmt.Errorf("cannot mark run running from state %q", run.State)
		}
		run.State = StateRunning
		if attempts > run.Attempts {
			run.Attempts = attempts
		}
		if run.StartedAt == nil {
			startedAt := now.UTC()
			run.StartedAt = &startedAt
		}
		run.Detail = ""
		return nil
	})
}

func (s *Store) Finish(id string, state State, attempts int, detail string, now time.Time) error {
	if state != StateSucceeded && state != StateBlocked && state != StateFailed {
		return fmt.Errorf("agent run store: invalid terminal state %q", state)
	}
	return s.update(id, now, func(run *Run) error {
		if !run.State.Active() {
			return fmt.Errorf("cannot finish run in state %q", run.State)
		}
		finishedAt := now.UTC()
		run.State = state
		run.Attempts = max(run.Attempts, attempts)
		run.Detail = detail
		run.FinishedAt = &finishedAt
		return nil
	})
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	runs := slices.Clone(s.state.Runs)
	active := 0
	for _, run := range runs {
		if run.State.Active() {
			active++
		}
	}
	return Snapshot{Total: s.state.Total, Active: active, Runs: runs}
}

func (s *Store) PublicSnapshot() PublicSnapshot {
	snapshot := s.Snapshot()
	runs := make([]PublicRun, len(snapshot.Runs))
	for i, run := range snapshot.Runs {
		runs[i] = PublicRun{
			ID:                run.ID,
			State:             run.State,
			Attempts:          run.Attempts,
			DuplicateTriggers: run.DuplicateTriggers,
			CreatedAt:         run.CreatedAt,
			UpdatedAt:         run.UpdatedAt,
			StartedAt:         run.StartedAt,
			FinishedAt:        run.FinishedAt,
		}
	}
	return PublicSnapshot{Total: snapshot.Total, Active: snapshot.Active, Runs: runs}
}

func (s *Store) update(id string, now time.Time, mutate func(*Run) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.state
	next.Runs = slices.Clone(s.state.Runs)
	for i := range next.Runs {
		if next.Runs[i].ID != id {
			continue
		}
		if err := mutate(&next.Runs[i]); err != nil {
			return fmt.Errorf("agent run store: update %s: %w", id, err)
		}
		next.Runs[i].UpdatedAt = now.UTC()
		if err := writeState(s.path, next); err != nil {
			return err
		}
		s.state = next
		return nil
	}
	return fmt.Errorf("agent run store: run %s not found", id)
}

func prune(runs []Run, limit int) []Run {
	if len(runs) <= limit {
		return runs
	}
	kept := make([]Run, 0, limit)
	for _, run := range runs {
		if len(kept) < limit || run.State.Active() {
			kept = append(kept, run)
		}
	}
	return kept
}

func newID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("agent run store: generate ID: %w", err)
	}
	return "run-" + hex.EncodeToString(value[:]), nil
}

func writeState(path string, value diskState) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".agent-runs-*")
	if err != nil {
		return fmt.Errorf("agent run store: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("agent run store: set permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		temp.Close()
		return fmt.Errorf("agent run store: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("agent run store: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("agent run store: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("agent run store: replace: %w", err)
	}
	return nil
}
