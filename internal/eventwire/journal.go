package eventwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const stateVersion = 1

type Record struct {
	Sequence         uint64            `json:"sequence"`
	ChannelSequences map[string]uint64 `json:"channelSequences,omitempty"`
	Event            Event             `json:"event"`
}

type Batch struct {
	Cursor uint64  `json:"cursor"`
	Events []Event `json:"events"`
}

type Rejection struct {
	Sequence   uint64    `json:"sequence"`
	EventID    string    `json:"eventId"`
	Source     Source    `json:"source"`
	Type       string    `json:"type"`
	Action     string    `json:"action"`
	Subject    string    `json:"subject,omitempty"`
	Reason     string    `json:"reason"`
	RejectedAt time.Time `json:"rejectedAt"`
}

type Status struct {
	Total         uint64     `json:"total"`
	Dispatched    uint64     `json:"dispatched"`
	Pending       uint64     `json:"pending"`
	RejectedTotal uint64     `json:"rejectedTotal"`
	LastRejection *Rejection `json:"lastRejection,omitempty"`
}

type diskLine struct {
	Kind          string            `json:"kind"`
	Version       int               `json:"version,omitempty"`
	Total         uint64            `json:"total,omitempty"`
	ChannelTotals map[string]uint64 `json:"channelTotals,omitempty"`
	Dispatched    uint64            `json:"dispatched,omitempty"`
	ChannelAcks   map[string]uint64 `json:"channelAcks,omitempty"`
	Record        *Record           `json:"record,omitempty"`
	RejectedTotal uint64            `json:"rejectedTotal,omitempty"`
	Rejection     *Rejection        `json:"rejection,omitempty"`
}

type journalState struct {
	total         uint64
	channelTotals map[string]uint64
	dispatched    uint64
	channelAcks   map[string]uint64
	records       []Record
	rejectedTotal uint64
	rejections    []Rejection
}

type Journal struct {
	mu       sync.Mutex
	path     string
	limit    int
	state    journalState
	ids      map[string]int
	poisoned error
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
}

func Open(path string, limit int, channelSeeds map[string]uint64) (*Journal, error) {
	if path == "" {
		return nil, errors.New("event wire: journal path is required")
	}
	if limit < 1 {
		return nil, errors.New("event wire: journal limit must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("event wire: create journal directory: %w", err)
	}

	state, err := readJournal(path, true)
	if errors.Is(err, os.ErrNotExist) {
		state = newJournalState(channelSeeds)
		if err := writeJournal(path, state, nil); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	changed := false
	for channel, seed := range channelSeeds {
		if seed > state.channelTotals[channel] {
			state.channelTotals[channel] = seed
			changed = true
		}
		if seed > state.channelAcks[channel] {
			state.channelAcks[channel] = seed
			changed = true
		}
	}

	j := &Journal{
		path:  path,
		limit: limit,
		state: state,
		ids:   make(map[string]int, len(state.records)),
	}
	j.write = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	j.sync = func(file *os.File) error { return file.Sync() }
	for i := range state.records {
		j.ids[state.records[i].Event.ID] = i
	}
	if changed {
		j.mu.Lock()
		err = j.compactLocked()
		j.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	return j, nil
}

func newJournalState(seeds map[string]uint64) journalState {
	return journalState{
		channelTotals: cloneMap(seeds),
		channelAcks:   cloneMap(seeds),
		records:       []Record{},
	}
}

func (j *Journal) Add(event Event) (Record, bool, error) {
	records, err := j.AddBatch([]Event{event})
	if err != nil {
		return Record{}, false, err
	}
	if len(records) == 0 {
		j.mu.Lock()
		defer j.mu.Unlock()
		if index, found := j.ids[event.ID]; found {
			return cloneRecord(j.state.records[index]), false, nil
		}
		return Record{}, false, errors.New("event wire: duplicate event fell outside retained window")
	}
	return records[0], true, nil
}

func (j *Journal) AddBatch(events []Event) ([]Record, error) {
	for _, event := range events {
		if err := event.Validate(); err != nil {
			return nil, err
		}
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.poisoned != nil {
		return nil, fmt.Errorf("event wire: journal is poisoned: %w", j.poisoned)
	}

	known := make(map[string]bool, len(j.ids)+len(events))
	for id := range j.ids {
		known[id] = true
	}
	total := j.state.total
	channelTotals := cloneMap(j.state.channelTotals)
	added := make([]Record, 0, len(events))
	for _, event := range events {
		if known[event.ID] {
			continue
		}
		known[event.ID] = true
		total++
		record := Record{
			Sequence:         total,
			ChannelSequences: make(map[string]uint64, len(event.Channels)),
			Event:            cloneEvent(event),
		}
		for _, channel := range event.Channels {
			channelTotals[channel]++
			record.ChannelSequences[channel] = channelTotals[channel]
		}
		added = append(added, record)
	}
	if len(added) == 0 {
		return []Record{}, nil
	}

	var data bytes.Buffer
	for i := range added {
		encoded, err := json.Marshal(diskLine{Kind: "event", Record: &added[i]})
		if err != nil {
			return nil, fmt.Errorf("event wire: encode event: %w", err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := j.appendLocked(data.Bytes()); err != nil {
		return nil, err
	}

	j.state.total = total
	j.state.channelTotals = channelTotals
	for _, record := range added {
		j.ids[record.Event.ID] = len(j.state.records)
		j.state.records = append(j.state.records, record)
	}
	if len(j.state.records) > j.limit*2 {
		if err := j.compactLocked(); err != nil {
			return nil, err
		}
	}
	return cloneRecords(added), nil
}

func (j *Journal) Acknowledge(sequence uint64, channelAcks map[string]uint64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.poisoned != nil {
		return fmt.Errorf("event wire: journal is poisoned: %w", j.poisoned)
	}
	if err := j.validateAcknowledgmentLocked(sequence, channelAcks); err != nil {
		return err
	}
	if sequence == j.state.dispatched && mapsEqualAtLeast(j.state.channelAcks, channelAcks) {
		return nil
	}
	line := diskLine{Kind: "ack", Dispatched: sequence, ChannelAcks: cloneMap(channelAcks)}
	data, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("event wire: encode acknowledgment: %w", err)
	}
	data = append(data, '\n')
	if err := j.appendLocked(data); err != nil {
		return err
	}
	j.state.dispatched = max(j.state.dispatched, sequence)
	for channel, value := range channelAcks {
		j.state.channelAcks[channel] = max(j.state.channelAcks[channel], value)
	}
	return nil
}

func (j *Journal) Reject(record Record, channelAcks map[string]uint64, reason string, rejectedAt time.Time) error {
	if reason == "" || rejectedAt.IsZero() {
		return errors.New("event wire: rejection reason and time are required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.poisoned != nil {
		return fmt.Errorf("event wire: journal is poisoned: %w", j.poisoned)
	}
	if record.Sequence <= j.state.dispatched {
		return errors.New("event wire: rejected record is already dispatched")
	}
	if err := j.validateAcknowledgmentLocked(record.Sequence, channelAcks); err != nil {
		return err
	}
	rejection := Rejection{
		Sequence: record.Sequence, EventID: record.Event.ID, Source: record.Event.Source,
		Type: record.Event.Type, Action: record.Event.Action, Subject: record.Event.Subject,
		Reason: reason, RejectedAt: rejectedAt.UTC(),
	}
	rejectedTotal := j.state.rejectedTotal + 1
	line := diskLine{
		Kind: "ack", Dispatched: record.Sequence, ChannelAcks: cloneMap(channelAcks),
		RejectedTotal: rejectedTotal, Rejection: &rejection,
	}
	data, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("event wire: encode rejection: %w", err)
	}
	if err := j.appendLocked(append(data, '\n')); err != nil {
		return err
	}
	j.state.dispatched = record.Sequence
	for channel, value := range channelAcks {
		j.state.channelAcks[channel] = max(j.state.channelAcks[channel], value)
	}
	j.state.rejectedTotal = rejectedTotal
	j.state.rejections = append(j.state.rejections, rejection)
	if len(j.state.rejections) > j.limit {
		j.state.rejections = slices.Clone(j.state.rejections[len(j.state.rejections)-j.limit:])
	}
	return nil
}

func (j *Journal) validateAcknowledgmentLocked(sequence uint64, channelAcks map[string]uint64) error {
	if sequence < j.state.dispatched || sequence > j.state.total {
		return errors.New("event wire: invalid dispatch acknowledgment")
	}
	for channel, value := range channelAcks {
		if value < j.state.channelAcks[channel] || value > j.state.channelTotals[channel] {
			return fmt.Errorf("event wire: invalid acknowledgment for channel %s", channel)
		}
	}
	return nil
}

func (j *Journal) Pending() []Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	var pending []Record
	for _, record := range j.state.records {
		if record.Sequence > j.state.dispatched {
			pending = append(pending, cloneRecord(record))
		}
	}
	return pending
}

func (j *Journal) Snapshot() (uint64, uint64, map[string]uint64, []Record) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.total, j.state.dispatched, cloneMap(j.state.channelAcks), cloneRecords(j.state.records)
}

func (j *Journal) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	status := Status{
		Total: j.state.total, Dispatched: j.state.dispatched,
		Pending: j.state.total - j.state.dispatched, RejectedTotal: j.state.rejectedTotal,
	}
	if len(j.state.rejections) > 0 {
		last := j.state.rejections[len(j.state.rejections)-1]
		status.LastRejection = &last
	}
	return status
}

func (j *Journal) appendLocked(data []byte) error {
	file, err := os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("event wire: open journal append: %w", err)
	}
	defer file.Close()
	offset, err := file.Seek(0, 2)
	if err != nil {
		return fmt.Errorf("event wire: seek journal: %w", err)
	}
	written, writeErr := j.write(file, data)
	if writeErr == nil && written != len(data) {
		writeErr = ioShortWrite{written: written, wanted: len(data)}
	}
	if writeErr == nil {
		writeErr = j.sync(file)
	}
	if writeErr == nil {
		return nil
	}
	if rollbackErr := rollbackAppend(file, offset); rollbackErr != nil {
		j.poisoned = errors.Join(writeErr, rollbackErr)
		return fmt.Errorf("event wire: append failed and rollback failed: %w", j.poisoned)
	}
	return fmt.Errorf("event wire: append: %w", writeErr)
}

type ioShortWrite struct {
	written int
	wanted  int
}

func (e ioShortWrite) Error() string {
	return fmt.Sprintf("short write: wrote %d of %d bytes", e.written, e.wanted)
}

func rollbackAppend(file *os.File, offset int64) error {
	if err := file.Truncate(offset); err != nil {
		return err
	}
	return file.Sync()
}

func (j *Journal) compactLocked() error {
	retained := make([]Record, 0, min(len(j.state.records), j.limit))
	start := max(0, len(j.state.records)-j.limit)
	for i, record := range j.state.records {
		keep := i >= start || record.Sequence > j.state.dispatched
		for channel, sequence := range record.ChannelSequences {
			keep = keep || sequence > j.state.channelAcks[channel]
		}
		if keep {
			retained = append(retained, record)
		}
	}
	if err := writeJournal(j.path, j.state, retained); err != nil {
		return err
	}
	j.state.records = retained
	j.ids = make(map[string]int, len(retained))
	for i := range retained {
		j.ids[retained[i].Event.ID] = i
	}
	return nil
}

func writeJournal(path string, state journalState, records []Record) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".system-events-*")
	if err != nil {
		return fmt.Errorf("event wire: create journal: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("event wire: set journal permissions: %w", err)
	}
	encoder := json.NewEncoder(temp)
	header := diskLine{
		Kind:          "checkpoint",
		Version:       stateVersion,
		Total:         state.total,
		ChannelTotals: cloneMap(state.channelTotals),
		Dispatched:    state.dispatched,
		ChannelAcks:   cloneMap(state.channelAcks),
		RejectedTotal: state.rejectedTotal,
	}
	if err := encoder.Encode(header); err != nil {
		temp.Close()
		return fmt.Errorf("event wire: encode checkpoint: %w", err)
	}
	for i := range records {
		if err := encoder.Encode(diskLine{Kind: "event", Record: &records[i]}); err != nil {
			temp.Close()
			return fmt.Errorf("event wire: encode compacted event: %w", err)
		}
	}
	for i := range state.rejections {
		line := diskLine{Kind: "ack", Dispatched: state.dispatched, RejectedTotal: state.rejectedTotal, Rejection: &state.rejections[i]}
		if err := encoder.Encode(line); err != nil {
			temp.Close()
			return fmt.Errorf("event wire: encode compacted rejection: %w", err)
		}
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("event wire: sync journal: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("event wire: close journal: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("event wire: replace journal: %w", err)
	}
	return nil
}

func readJournal(path string, recoverTail bool) (journalState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return journalState{}, err
	}
	complete := len(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if index := bytes.LastIndexByte(data, '\n'); index >= 0 {
			complete = index + 1
		} else {
			complete = 0
		}
		if recoverTail {
			if err := os.Truncate(path, int64(complete)); err != nil {
				return journalState{}, fmt.Errorf("event wire: truncate incomplete journal tail: %w", err)
			}
		}
	}
	lines := bytes.Split(data[:complete], []byte{'\n'})
	state := newJournalState(nil)
	foundCheckpoint := false
	for _, raw := range lines {
		if len(raw) == 0 {
			continue
		}
		var line diskLine
		if err := json.Unmarshal(raw, &line); err != nil {
			return journalState{}, fmt.Errorf("event wire: decode journal line: %w", err)
		}
		switch line.Kind {
		case "checkpoint":
			if foundCheckpoint || line.Version != stateVersion {
				return journalState{}, errors.New("event wire: invalid checkpoint")
			}
			foundCheckpoint = true
			state.total = line.Total
			state.channelTotals = cloneMap(line.ChannelTotals)
			state.dispatched = line.Dispatched
			state.channelAcks = cloneMap(line.ChannelAcks)
			state.rejectedTotal = line.RejectedTotal
		case "event":
			if !foundCheckpoint || line.Record == nil {
				return journalState{}, errors.New("event wire: event precedes checkpoint")
			}
			if err := line.Record.Event.Validate(); err != nil {
				return journalState{}, err
			}
			state.records = append(state.records, cloneRecord(*line.Record))
			state.total = max(state.total, line.Record.Sequence)
			for channel, value := range line.Record.ChannelSequences {
				state.channelTotals[channel] = max(state.channelTotals[channel], value)
			}
		case "ack":
			if !foundCheckpoint {
				return journalState{}, errors.New("event wire: acknowledgment precedes checkpoint")
			}
			state.dispatched = max(state.dispatched, line.Dispatched)
			for channel, value := range line.ChannelAcks {
				state.channelAcks[channel] = max(state.channelAcks[channel], value)
			}
			state.rejectedTotal = max(state.rejectedTotal, line.RejectedTotal)
			if line.Rejection != nil {
				if line.Rejection.Sequence == 0 || line.Rejection.EventID == "" || line.Rejection.Reason == "" || line.Rejection.RejectedAt.IsZero() {
					return journalState{}, errors.New("event wire: invalid rejection")
				}
				state.rejections = append(state.rejections, *line.Rejection)
			}
		default:
			return journalState{}, fmt.Errorf("event wire: unknown journal line %q", line.Kind)
		}
	}
	if !foundCheckpoint {
		return journalState{}, errors.New("event wire: journal checkpoint is missing")
	}
	return state, nil
}

func Read(path string, filter Filter, after uint64) (Batch, error) {
	state, err := readJournal(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return Batch{Cursor: after, Events: []Event{}}, nil
	}
	if err != nil {
		return Batch{}, err
	}
	batch := Batch{Cursor: max(after, state.total), Events: []Event{}}
	for _, record := range state.records {
		if record.Sequence > after && filter.Matches(record.Event) {
			batch.Events = append(batch.Events, cloneEvent(record.Event))
		}
	}
	return batch, nil
}

func ReadChannel(path, channel string, filter Filter, after uint64) (Batch, error) {
	state, err := readJournal(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return Batch{Cursor: after, Events: []Event{}}, nil
	}
	if err != nil {
		return Batch{}, err
	}
	ack := state.channelAcks[channel]
	batch := Batch{Cursor: max(after, ack), Events: []Event{}}
	for _, record := range state.records {
		sequence, found := record.ChannelSequences[channel]
		if found && sequence > after && sequence <= ack && filter.Matches(record.Event) {
			batch.Events = append(batch.Events, cloneEvent(record.Event))
		}
	}
	return batch, nil
}

func Wait(ctx context.Context, read func(uint64) (Batch, error), after uint64, interval time.Duration) (Batch, error) {
	if interval <= 0 {
		return Batch{}, errors.New("event wire: poll interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	cursor := after
	for {
		batch, err := read(cursor)
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

func cloneEvent(event Event) Event {
	event.Channels = slices.Clone(event.Channels)
	if event.Attributes != nil {
		attributes := make(map[string][]string, len(event.Attributes))
		for key, values := range event.Attributes {
			attributes[key] = slices.Clone(values)
		}
		event.Attributes = attributes
	}
	return event
}

func cloneRecord(record Record) Record {
	record.Event = cloneEvent(record.Event)
	record.ChannelSequences = cloneMap(record.ChannelSequences)
	return record
}

func cloneRecords(records []Record) []Record {
	cloned := make([]Record, len(records))
	for i := range records {
		cloned[i] = cloneRecord(records[i])
	}
	return cloned
}

func cloneMap[K comparable](values map[K]uint64) map[K]uint64 {
	cloned := make(map[K]uint64, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func mapsEqualAtLeast(current, requested map[string]uint64) bool {
	for key, value := range requested {
		if current[key] < value {
			return false
		}
	}
	return true
}
