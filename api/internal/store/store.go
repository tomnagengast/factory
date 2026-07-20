package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/tomnagengast/factory/api/internal/eventwire"

	_ "modernc.org/sqlite"
)

const projectionVersion = 1

var ErrClosed = errors.New("event store is closed")

type Store struct {
	db       *sql.DB
	appendMu sync.Mutex
	changed  chan struct{}
	closed   bool
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("event store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return nil, fmt.Errorf("create event store directory: %w", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve event store path: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute}).String()
	dsn += "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open event store: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	store := &Store{db: db, changed: make(chan struct{})}
	if err := store.prepare(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) prepare(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, eventSchema); err != nil {
		return fmt.Errorf("create event schema: %w", err)
	}
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM projection_meta WHERE id = 1`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.ExecContext(ctx, projectionSchema); err != nil {
			return fmt.Errorf("create projection schema: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO projection_meta(id, version) VALUES(1, ?)`, projectionVersion); err != nil {
			return fmt.Errorf("record projection version: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("read projection version: %w", err)
	case version == projectionVersion:
		return nil
	default:
		return s.rebuildProjections(ctx)
	}
}

func (s *Store) Append(eventType string, payload any) (eventwire.Event, error) {
	event, _, err := s.append(0, false, eventType, payload)
	return event, err
}

func (s *Store) AppendIfCurrent(expectedLastID int64, eventType string, payload any) (eventwire.Event, bool, error) {
	return s.append(expectedLastID, true, eventType, payload)
}

func (s *Store) append(expectedLastID int64, conditional bool, eventType string, payload any) (eventwire.Event, bool, error) {
	if eventType == "" {
		return eventwire.Event{}, false, errors.New("event type is required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return eventwire.Event{}, false, fmt.Errorf("encode event payload: %w", err)
	}

	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	if s.closed {
		return eventwire.Event{}, false, ErrClosed
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return eventwire.Event{}, false, fmt.Errorf("begin event append: %w", err)
	}
	defer tx.Rollback()
	lastID, err := lastIDTx(tx)
	if err != nil {
		return eventwire.Event{}, false, err
	}
	if conditional && expectedLastID != lastID {
		return eventwire.Event{}, false, nil
	}
	event := eventwire.Event{ID: lastID + 1, Type: eventType, At: time.Now().UTC(), Data: data}
	if err := insertEvent(tx, event); err != nil {
		return eventwire.Event{}, false, err
	}
	if err := applyProjection(tx, event); err != nil {
		return eventwire.Event{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return eventwire.Event{}, false, fmt.Errorf("commit event %d: %w", event.ID, err)
	}
	close(s.changed)
	s.changed = make(chan struct{})
	return cloneEvent(event), true, nil
}

func insertEvent(tx *sql.Tx, event eventwire.Event) error {
	if event.ID < 1 || event.Type == "" || event.At.IsZero() || !json.Valid(event.Data) {
		return fmt.Errorf("event %d is invalid", event.ID)
	}
	if _, err := tx.Exec(
		`INSERT INTO events(id, type, at, data) VALUES(?, ?, ?, ?)`,
		event.ID, event.Type, formatTime(event.At), []byte(event.Data),
	); err != nil {
		return fmt.Errorf("insert event %d: %w", event.ID, err)
	}
	return nil
}

func lastIDTx(tx *sql.Tx) (int64, error) {
	var id int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&id); err != nil {
		return 0, fmt.Errorf("read last event ID: %w", err)
	}
	return id, nil
}

func (s *Store) Event(id int64) (eventwire.Event, bool, error) {
	var event eventwire.Event
	var at string
	var data []byte
	err := s.db.QueryRow(`SELECT id, type, at, data FROM events WHERE id = ?`, id).Scan(
		&event.ID, &event.Type, &at, &data,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return eventwire.Event{}, false, nil
	}
	if err != nil {
		return eventwire.Event{}, false, fmt.Errorf("read event %d: %w", id, err)
	}
	event.At, err = parseTime(at)
	if err != nil {
		return eventwire.Event{}, false, fmt.Errorf("read event %d time: %w", id, err)
	}
	event.Data = append(json.RawMessage(nil), data...)
	return event, true, nil
}

func (s *Store) EventsAfter(after int64, limit int) ([]eventwire.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT id, type, at, data FROM events WHERE id > ? ORDER BY id LIMIT ?`, after, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("read events after %d: %w", after, err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (s *Store) EventsBefore(before int64, limit int) ([]eventwire.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT id, type, at, data FROM events`
	args := []any{}
	if before > 0 {
		query += ` WHERE id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("read event page: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

func scanEvents(rows *sql.Rows) ([]eventwire.Event, error) {
	events := make([]eventwire.Event, 0)
	for rows.Next() {
		var event eventwire.Event
		var at string
		var data []byte
		if err := rows.Scan(&event.ID, &event.Type, &at, &data); err != nil {
			return nil, err
		}
		parsed, err := parseTime(at)
		if err != nil {
			return nil, err
		}
		event.At, event.Data = parsed, append(json.RawMessage(nil), data...)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) Types() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT type FROM events ORDER BY type`)
	if err != nil {
		return nil, fmt.Errorf("read event types: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	sort.Strings(values)
	return values, rows.Err()
}

func (s *Store) LastID() (int64, error) {
	var id int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&id); err != nil {
		return 0, fmt.Errorf("read last event ID: %w", err)
	}
	return id, nil
}

func (s *Store) Wait(ctx context.Context, after int64, limit int) ([]eventwire.Event, error) {
	for {
		s.appendMu.Lock()
		changed, closed := s.changed, s.closed
		s.appendMu.Unlock()
		events, err := s.EventsAfter(after, limit)
		if err != nil {
			s.appendMu.Lock()
			closed = s.closed
			s.appendMu.Unlock()
			if closed {
				return nil, ErrClosed
			}
			return nil, err
		}
		if len(events) > 0 {
			return events, nil
		}
		if closed {
			return nil, ErrClosed
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (s *Store) Close() error {
	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.changed)
	return s.db.Close()
}

func cloneEvent(event eventwire.Event) eventwire.Event {
	event.Data = append(json.RawMessage(nil), event.Data...)
	return event
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableInt(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

const eventSchema = `
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY,
    type TEXT NOT NULL,
    at TEXT NOT NULL,
    data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS events_type_id ON events(type, id);
CREATE TABLE IF NOT EXISTS projection_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL
);`

const projectionSchema = `
CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY,
    deleted INTEGER NOT NULL,
    data BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL,
    deleted INTEGER NOT NULL,
    data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_project_id ON tasks(project_id, id);
CREATE TABLE IF NOT EXISTS comments (
    id INTEGER PRIMARY KEY,
    relation_type TEXT NOT NULL,
    relation_id INTEGER NOT NULL,
    parent_id INTEGER,
    author TEXT NOT NULL,
    final INTEGER NOT NULL,
    deleted INTEGER NOT NULL,
    data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_relation ON comments(relation_type, relation_id, id);
CREATE INDEX IF NOT EXISTS comments_parent ON comments(parent_id, id);
CREATE TABLE IF NOT EXISTS artifacts (
    id INTEGER PRIMARY KEY,
    relation_type TEXT NOT NULL,
    relation_id INTEGER NOT NULL,
    deleted INTEGER NOT NULL,
    data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS artifacts_relation ON artifacts(relation_type, relation_id, id);
CREATE TABLE IF NOT EXISTS media (
    id INTEGER PRIMARY KEY,
    data BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS triggers (
    id INTEGER PRIMARY KEY,
    event_type TEXT NOT NULL,
    workflow_id INTEGER NOT NULL,
    enabled INTEGER NOT NULL,
    boundary_event_id INTEGER NOT NULL,
    last_cron_at TEXT,
    deleted INTEGER NOT NULL,
    data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS triggers_match ON triggers(event_type, enabled, deleted, boundary_event_id, id);
CREATE TABLE IF NOT EXISTS workflows (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT,
    deleted INTEGER NOT NULL,
    data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS workflows_name ON workflows(name, deleted, id);
CREATE INDEX IF NOT EXISTS workflows_path ON workflows(path, id);
CREATE TABLE IF NOT EXISTS workflow_runs (
    id INTEGER PRIMARY KEY,
    trigger_id INTEGER NOT NULL,
    workflow_id INTEGER NOT NULL,
    source_event_id INTEGER NOT NULL,
    task_id INTEGER,
    status TEXT NOT NULL,
    data BLOB NOT NULL,
    UNIQUE(trigger_id, source_event_id)
);
CREATE INDEX IF NOT EXISTS workflow_runs_task ON workflow_runs(task_id, workflow_id, id);
CREATE INDEX IF NOT EXISTS workflow_runs_workflow ON workflow_runs(workflow_id, id);
CREATE INDEX IF NOT EXISTS workflow_runs_status ON workflow_runs(status, id);
CREATE TABLE IF NOT EXISTS workflow_run_event_index (
    event_id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL,
    sequence INTEGER NOT NULL,
    UNIQUE(run_id, sequence)
);
CREATE INDEX IF NOT EXISTS workflow_run_events_run ON workflow_run_event_index(run_id, sequence);
CREATE TABLE IF NOT EXISTS workflow_tasks (
    workflow_id INTEGER NOT NULL,
    task_id INTEGER NOT NULL,
    PRIMARY KEY(workflow_id, task_id)
);
CREATE TABLE IF NOT EXISTS human_responses (
    comment_id INTEGER PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    data BLOB NOT NULL
);`

const dropProjectionSchema = `
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS human_responses;
DROP TABLE IF EXISTS workflow_tasks;
DROP TABLE IF EXISTS workflow_run_event_index;
DROP TABLE IF EXISTS workflow_runs;
DROP TABLE IF EXISTS workflows;
DROP TABLE IF EXISTS triggers;
DROP TABLE IF EXISTS media;
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS comments;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS projects;`
