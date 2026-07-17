package agentrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

const collectorStateVersion = 1

type EventPublisher interface {
	PublishBatch(context.Context, []eventwire.Event) ([]eventwire.Record, error)
}

type collectorState struct {
	Version  int               `json:"version"`
	Offsets  map[string]int64  `json:"offsets"`
	Prefixes map[string]string `json:"prefixes,omitempty"`
}

type Collector struct {
	mu             sync.Mutex
	store          TransitionAcknowledger
	publisher      EventPublisher
	runsRoot       string
	checkpointPath string
	state          collectorState
	fresh          bool
	transitions    bool
}

type TransitionAcknowledger interface {
	AcknowledgeTransitions([]string) error
}

func NewCollector(store *Store, publisher EventPublisher, stateRoot, checkpointPath string) (*Collector, error) {
	if store == nil {
		return nil, errors.New("agent event collector: run store is required")
	}
	if publisher == nil {
		return nil, errors.New("agent event collector: publisher is required")
	}
	if stateRoot == "" || checkpointPath == "" {
		return nil, errors.New("agent event collector: state root and checkpoint path are required")
	}
	c := &Collector{
		store:          store,
		publisher:      publisher,
		runsRoot:       filepath.Join(filepath.Clean(stateRoot), "runs"),
		checkpointPath: checkpointPath,
		state: collectorState{
			Version:  collectorStateVersion,
			Offsets:  make(map[string]int64),
			Prefixes: make(map[string]string),
		},
		transitions: true,
	}
	return openCollector(c)
}

// NewRecordCollector retains agent JSONL observation without owning lifecycle
// transition publication or acknowledgement. Canonical Runs publish those
// transitions through their journal outbox.
func NewRecordCollector(publisher EventPublisher, stateRoot, checkpointPath string) (*Collector, error) {
	if publisher == nil {
		return nil, errors.New("agent event collector: publisher is required")
	}
	if stateRoot == "" || checkpointPath == "" {
		return nil, errors.New("agent event collector: state root and checkpoint path are required")
	}
	c := &Collector{
		publisher: publisher, runsRoot: filepath.Join(filepath.Clean(stateRoot), "runs"), checkpointPath: checkpointPath,
		state: collectorState{Version: collectorStateVersion, Offsets: make(map[string]int64), Prefixes: make(map[string]string)},
	}
	return openCollector(c)
}

func openCollector(c *Collector) (*Collector, error) {
	data, err := os.ReadFile(c.checkpointPath)
	if errors.Is(err, os.ErrNotExist) {
		c.fresh = true
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent event collector: read checkpoint: %w", err)
	}
	if err := json.Unmarshal(data, &c.state); err != nil {
		return nil, fmt.Errorf("agent event collector: decode checkpoint: %w", err)
	}
	if c.state.Version != collectorStateVersion || c.state.Offsets == nil {
		return nil, fmt.Errorf("agent event collector: unsupported checkpoint version %d", c.state.Version)
	}
	if c.state.Prefixes == nil {
		c.state.Prefixes = make(map[string]string)
	}
	return c, nil
}

func (c *Collector) Collect(ctx context.Context, runs []Run) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	next := collectorState{
		Version:  collectorStateVersion,
		Offsets:  cloneOffsets(c.state.Offsets),
		Prefixes: clonePrefixes(c.state.Prefixes),
	}
	var events []eventwire.Event
	for _, run := range runs {
		files, err := historyEventFiles(run.RunDirectory)
		if err != nil {
			return err
		}
		for _, file := range files {
			collected, err := c.collectFile(run, file.path, &next)
			if err != nil {
				return err
			}
			events = append(events, collected...)
		}
	}

	var transitionIDs []string
	if c.transitions {
		transitionEvents, ids := lifecycleEvents(runs)
		events = append(events, transitionEvents...)
		transitionIDs = ids
	}
	if len(events) > 0 {
		if _, err := c.publisher.PublishBatch(ctx, events); err != nil {
			return fmt.Errorf("agent event collector: publish: %w", err)
		}
	}
	if err := writeCollectorState(c.checkpointPath, next); err != nil {
		return err
	}
	c.state = next
	c.fresh = false
	if c.transitions {
		if err := c.store.AcknowledgeTransitions(transitionIDs); err != nil {
			return fmt.Errorf("agent event collector: acknowledge transitions: %w", err)
		}
	}
	return nil
}

func (c *Collector) collectFile(run Run, path string, state *collectorState) ([]eventwire.Event, error) {
	relative, err := c.safeRelativePath(path)
	if err != nil {
		return nil, err
	}
	key := run.ID + "/" + relative
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent event collector: read %s: %w", relative, err)
	}
	prefix := contentPrefix(data)
	offset := state.Offsets[key]
	if offset < 0 || offset > int64(len(data)) || (offset > 0 && state.Prefixes[key] != "" && state.Prefixes[key] != prefix) {
		offset = 0
	}
	if c.fresh && !run.State.Active() && offset == 0 {
		state.Offsets[key] = int64(lastCompleteOffset(data))
		state.Prefixes[key] = prefix
		return nil, nil
	}

	remaining := data[offset:]
	complete := lastCompleteOffset(remaining)
	if complete == 0 {
		return nil, nil
	}
	remaining = remaining[:complete]
	lines := bytes.Split(remaining, []byte{'\n'})
	events := make([]eventwire.Event, 0, len(lines)-1)
	position := offset
	for _, line := range lines[:len(lines)-1] {
		length := int64(len(line) + 1)
		events = append(events, agentRecordEvent(run, relative, position, length, line))
		position += length
	}
	state.Offsets[key] = position
	state.Prefixes[key] = prefix
	return events, nil
}

func (c *Collector) safeRelativePath(path string) (string, error) {
	root, err := filepath.EvalSymlinks(c.runsRoot)
	if errors.Is(err, os.ErrNotExist) {
		root = c.runsRoot
	} else if err != nil {
		return "", fmt.Errorf("agent event collector: inspect runs root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("agent event collector: inspect event file: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("agent event collector: event file is outside run root")
	}
	return filepath.ToSlash(relative), nil
}

func agentRecordEvent(run Run, file string, offset, length int64, line []byte) eventwire.Event {
	digest := sha256.Sum256(line)
	digestText := hex.EncodeToString(digest[:])
	recordType := "malformed"
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &header) == nil {
		recordType = safeRecordType(header.Type)
	}
	idDigest := sha256.Sum256([]byte(run.ID + "\x00" + file + "\x00" + strconv.FormatInt(offset, 10) + "\x00" + digestText))
	event := eventwire.Event{
		ID:      "factory:agent-record:" + hex.EncodeToString(idDigest[:]),
		Source:  eventwire.SourceFactory,
		Type:    "agent-record",
		Action:  recordType,
		Subject: run.IssueIdentifier,
		Attributes: map[string][]string{
			"runId":                       {run.ID},
			"taskSource":                  {string(run.Task.Source)},
			"taskProviderId":              {run.Task.ProviderID},
			"taskIdentifier":              {run.Task.Identifier},
			"file":                        {file},
			"offset":                      {strconv.FormatInt(offset, 10)},
			"length":                      {strconv.FormatInt(length, 10)},
			"sha256":                      {digestText},
			eventwire.AttributeProducer:   {"agent-collector"},
			eventwire.AttributeProvenance: {"factory"},
		},
		ReceivedAt: time.Now().UTC(),
	}
	applyRunCausation(&event, run)
	return event
}

func safeRecordType(value string) string {
	switch value {
	case "assistant", "error", "item.completed", "item.started", "rate_limit_event", "result",
		"system", "thread.started", "turn.completed", "turn.started", "user":
		return value
	case "":
		return "malformed"
	default:
		return "other"
	}
}

func lifecycleEvents(runs []Run) ([]eventwire.Event, []string) {
	var events []eventwire.Event
	var ids []string
	for _, run := range runs {
		for _, transition := range run.Transitions {
			if run.InvocationID != "" && (transition.State == StateSucceeded || transition.State == StateBlocked || transition.State == StateFailed) && run.InvocationReflectedAt == nil {
				continue
			}
			event := eventwire.Event{
				ID:      "factory:run-transition:" + transition.ID,
				Source:  eventwire.SourceFactory,
				Type:    "agent-run",
				Action:  string(transition.State),
				Subject: run.IssueIdentifier,
				Attributes: map[string][]string{
					"runId": {run.ID}, "attempts": {strconv.Itoa(transition.Attempts)},
					"taskSource": {string(run.Task.Source)}, "taskProviderId": {run.Task.ProviderID}, "taskIdentifier": {run.Task.Identifier},
					eventwire.AttributeProducer: {"agent-collector"}, eventwire.AttributeProvenance: {"factory"},
				},
				ReceivedAt: transition.At,
			}
			applyRunCausation(&event, run)
			events = append(events, event)
			ids = append(ids, transition.ID)
		}
	}
	return events, ids
}

func applyRunCausation(event *eventwire.Event, run Run) {
	if run.InvocationID == "" {
		return
	}
	event.RootEventID = run.InvocationRootEventID
	event.ParentInvocationID = run.InvocationID
	event.ParentRunID = run.ID
	event.Hop = run.InvocationHop
	event.AncestorRuleIDs = slices.Clone(run.InvocationAncestorRuleIDs)
}

func lastCompleteOffset(data []byte) int {
	index := bytes.LastIndexByte(data, '\n')
	if index < 0 {
		return 0
	}
	return index + 1
}

func cloneOffsets(offsets map[string]int64) map[string]int64 {
	cloned := make(map[string]int64, len(offsets))
	for key, value := range offsets {
		cloned[key] = value
	}
	return cloned
}

func clonePrefixes(prefixes map[string]string) map[string]string {
	cloned := make(map[string]string, len(prefixes))
	for key, value := range prefixes {
		cloned[key] = value
	}
	return cloned
}

func contentPrefix(data []byte) string {
	digest := sha256.Sum256(data[:min(len(data), 256)])
	return hex.EncodeToString(digest[:])
}

func writeCollectorState(path string, state collectorState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("agent event collector: create checkpoint directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".agent-event-offsets-*")
	if err != nil {
		return fmt.Errorf("agent event collector: create checkpoint: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("agent event collector: protect checkpoint: %w", err)
	}
	if err := json.NewEncoder(temp).Encode(state); err != nil {
		temp.Close()
		return fmt.Errorf("agent event collector: encode checkpoint: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("agent event collector: sync checkpoint: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("agent event collector: close checkpoint: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("agent event collector: replace checkpoint: %w", err)
	}
	return nil
}
