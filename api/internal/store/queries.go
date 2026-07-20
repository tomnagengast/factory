package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
)

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

type HealthCounts struct {
	Events    int64
	Tasks     int
	Projects  int
	Triggers  int
	Workflows int
}

type CronState struct {
	Trigger state.Trigger
	Last    *time.Time
}

func (s *Store) Snapshot() (state.Snapshot, int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return state.Snapshot{}, 0, err
	}
	defer tx.Rollback()
	view := state.Snapshot{Settings: state.DefaultSettings}
	if view.Projects, err = queryJSON[state.Project](tx, `SELECT data FROM projects ORDER BY id`); err != nil {
		return state.Snapshot{}, 0, err
	}
	if view.Tasks, err = queryJSON[state.Task](tx, `SELECT data FROM tasks ORDER BY id`); err != nil {
		return state.Snapshot{}, 0, err
	}
	if view.Comments, err = queryJSON[state.Comment](tx, `SELECT data FROM comments ORDER BY id`); err != nil {
		return state.Snapshot{}, 0, err
	}
	if view.Artifacts, err = queryJSON[state.Artifact](tx, `SELECT data FROM artifacts ORDER BY id`); err != nil {
		return state.Snapshot{}, 0, err
	}
	if view.MediaFiles, err = queryJSON[state.Media](tx, `SELECT data FROM media ORDER BY id`); err != nil {
		return state.Snapshot{}, 0, err
	}
	if view.Triggers, err = queryJSON[state.Trigger](tx, `SELECT data FROM triggers ORDER BY id`); err != nil {
		return state.Snapshot{}, 0, err
	}
	if view.Workflows, err = queryJSON[state.Workflow](tx, `SELECT data FROM workflows ORDER BY id`); err != nil {
		return state.Snapshot{}, 0, err
	}
	projections, err := queryJSON[runProjection](tx, `SELECT data FROM workflow_runs ORDER BY id`)
	if err != nil {
		return state.Snapshot{}, 0, err
	}
	view.Runs = make([]state.WorkflowRun, 0, len(projections))
	for _, projection := range projections {
		view.Runs = append(view.Runs, restoreRun(projection))
	}
	if selected, found, err := settingsQuery(tx); err != nil {
		return state.Snapshot{}, 0, err
	} else if found {
		view.Settings = selected
	}
	var checkpoint int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&checkpoint); err != nil {
		return state.Snapshot{}, 0, err
	}
	if err := tx.Commit(); err != nil {
		return state.Snapshot{}, 0, err
	}
	return view, checkpoint, nil
}

func (s *Store) Health() (HealthCounts, error) {
	var value HealthCounts
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM events),
		(SELECT COUNT(*) FROM tasks WHERE deleted = 0),
		(SELECT COUNT(*) FROM projects WHERE deleted = 0),
		(SELECT COUNT(*) FROM triggers WHERE deleted = 0),
		(SELECT COUNT(*) FROM workflows WHERE deleted = 0)`).Scan(
		&value.Events, &value.Tasks, &value.Projects, &value.Triggers, &value.Workflows,
	)
	return value, err
}

func (s *Store) Settings() (state.Settings, error) {
	value, found, err := settingsQuery(s.db)
	if err != nil {
		return state.Settings{}, err
	}
	if !found {
		return state.DefaultSettings, nil
	}
	return value, nil
}

func settingsQuery(q queryer) (state.Settings, bool, error) {
	return queryOneJSON[state.Settings](q, `SELECT data FROM settings WHERE id = 1`)
}

func (s *Store) Project(id int64) (state.Project, bool, error) {
	return queryOneJSON[state.Project](s.db, `SELECT data FROM projects WHERE id = ?`, id)
}
func (s *Store) Task(id int64) (state.Task, bool, error) {
	return queryOneJSON[state.Task](s.db, `SELECT data FROM tasks WHERE id = ?`, id)
}

func (s *Store) TaskWithCheckpoint(id int64) (state.Task, int64, bool, error) {
	return entityWithCheckpoint[state.Task](s.db, `SELECT data FROM tasks WHERE id = ?`, id)
}

func (s *Store) Comment(id int64) (state.Comment, bool, error) {
	return queryOneJSON[state.Comment](s.db, `SELECT data FROM comments WHERE id = ?`, id)
}

func (s *Store) CommentWithCheckpoint(id int64) (state.Comment, int64, bool, error) {
	return entityWithCheckpoint[state.Comment](s.db, `SELECT data FROM comments WHERE id = ?`, id)
}
func (s *Store) Artifact(id int64) (state.Artifact, bool, error) {
	return queryOneJSON[state.Artifact](s.db, `SELECT data FROM artifacts WHERE id = ?`, id)
}
func (s *Store) Media(id int64) (state.Media, bool, error) {
	return queryOneJSON[state.Media](s.db, `SELECT data FROM media WHERE id = ?`, id)
}
func (s *Store) Trigger(id int64) (state.Trigger, bool, error) {
	return queryOneJSON[state.Trigger](s.db, `SELECT data FROM triggers WHERE id = ?`, id)
}
func (s *Store) Workflow(id int64) (state.Workflow, bool, error) {
	return queryOneJSON[state.Workflow](s.db, `SELECT data FROM workflows WHERE id = ?`, id)
}
func (s *Store) WorkflowByPath(path string) (state.Workflow, bool, error) {
	return queryOneJSON[state.Workflow](s.db, `SELECT data FROM workflows WHERE path = ? ORDER BY id LIMIT 1`, path)
}
func (s *Store) WorkflowByName(name string) (state.Workflow, bool, error) {
	return queryOneJSON[state.Workflow](s.db, `SELECT data FROM workflows WHERE name = ? AND deleted = 0 ORDER BY id LIMIT 1`, name)
}

func (s *Store) Projects() ([]state.Project, error) {
	return queryJSON[state.Project](s.db, `SELECT data FROM projects WHERE deleted = 0 ORDER BY id DESC`)
}
func (s *Store) Tasks() ([]state.Task, error) {
	return queryJSON[state.Task](s.db, `SELECT data FROM tasks WHERE deleted = 0 ORDER BY id DESC`)
}
func (s *Store) TasksForProject(projectID int64) ([]state.Task, error) {
	return queryJSON[state.Task](s.db, `SELECT data FROM tasks WHERE project_id = ? AND deleted = 0 ORDER BY id`, projectID)
}
func (s *Store) CommentsFor(relationType string, relationID int64) ([]state.Comment, error) {
	return queryJSON[state.Comment](s.db, `SELECT data FROM comments WHERE relation_type = ? AND relation_id = ? AND deleted = 0 ORDER BY id`, relationType, relationID)
}
func (s *Store) Replies(commentID int64) ([]state.Comment, error) {
	return queryJSON[state.Comment](s.db, `SELECT data FROM comments WHERE parent_id = ? AND deleted = 0 ORDER BY id`, commentID)
}
func (s *Store) Artifacts(relationType string, relationID int64) ([]state.Artifact, error) {
	query := `SELECT data FROM artifacts WHERE deleted = 0`
	args := []any{}
	if relationType != "" {
		query += ` AND relation_type = ?`
		args = append(args, relationType)
	}
	if relationID > 0 {
		query += ` AND relation_id = ?`
		args = append(args, relationID)
	}
	query += ` ORDER BY id DESC`
	return queryJSON[state.Artifact](s.db, query, args...)
}
func (s *Store) Triggers() ([]state.Trigger, error) {
	return queryJSON[state.Trigger](s.db, `SELECT data FROM triggers WHERE deleted = 0 ORDER BY id DESC`)
}
func (s *Store) Workflows() ([]state.Workflow, error) {
	return queryJSON[state.Workflow](s.db, `SELECT data FROM workflows WHERE deleted = 0 ORDER BY name, id`)
}

func entityWithCheckpoint[T any](db *sql.DB, query string, args ...any) (T, int64, bool, error) {
	var zero T
	tx, err := db.Begin()
	if err != nil {
		return zero, 0, false, err
	}
	defer tx.Rollback()
	value, found, err := queryOneJSON[T](tx, query, args...)
	if err != nil {
		return zero, 0, false, err
	}
	var checkpoint int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&checkpoint); err != nil {
		return zero, 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return zero, 0, false, err
	}
	return value, checkpoint, found, nil
}

func (s *Store) Run(id int64) (state.WorkflowRun, bool, error) {
	value, found, err := queryOneJSON[runProjection](s.db, `SELECT data FROM workflow_runs WHERE id = ?`, id)
	if err != nil || !found {
		return state.WorkflowRun{}, found, err
	}
	return restoreRun(value), true, nil
}

func (s *Store) History(before int64, limit int) ([]state.WorkflowRun, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT data FROM workflow_runs`
	args := []any{}
	if before > 0 {
		query += ` WHERE id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	values, err := queryJSON[runProjection](s.db, query, args...)
	if err != nil {
		return nil, err
	}
	runs := make([]state.WorkflowRun, 0, len(values))
	for _, value := range values {
		runs = append(runs, restoreRun(value))
	}
	return runs, nil
}

func (s *Store) RunEvents(runID, before int64, limit int) ([]state.WorkflowRunEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT e.id, e.at, e.data FROM workflow_run_event_index i JOIN events e ON e.id = i.event_id WHERE i.run_id = ?`
	args := []any{runID}
	if before > 0 {
		query += ` AND e.id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY e.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]state.WorkflowRunEvent, 0)
	for rows.Next() {
		var id int64
		var recorded string
		var payload []byte
		if err := rows.Scan(&id, &recorded, &payload); err != nil {
			return nil, err
		}
		var data state.WorkflowRunEventData
		if err := json.Unmarshal(payload, &data); err != nil {
			return nil, err
		}
		var runtime state.WorkflowRuntimeEvent
		if err := json.Unmarshal(data.Event, &runtime); err != nil {
			return nil, err
		}
		at, err := parseTime(recorded)
		if err != nil {
			return nil, err
		}
		values = append(values, state.WorkflowRunEvent{
			Raw: append(json.RawMessage(nil), data.Event...), ID: id, RunID: data.RunID,
			RecordedAt: at, WorkflowRuntimeEvent: runtime,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(values)
	return values, nil
}

func (s *Store) RunJournal(runID int64) ([]json.RawMessage, error) {
	rows, err := s.db.Query(`
		SELECT json_extract(e.data, '$.event')
		FROM workflow_run_event_index i
		JOIN events e ON e.id = i.event_id
		WHERE i.run_id = ?
		ORDER BY i.sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]json.RawMessage, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		values = append(values, append(json.RawMessage(nil), raw...))
	}
	return values, rows.Err()
}

func (s *Store) RunningRuns() ([]state.WorkflowRun, error) {
	values, err := queryJSON[runProjection](s.db, `SELECT data FROM workflow_runs WHERE status = 'running' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	runs := make([]state.WorkflowRun, 0, len(values))
	for _, value := range values {
		runs = append(runs, restoreRun(value))
	}
	return runs, nil
}

func (s *Store) PendingWorkflowComment() (state.Comment, bool, error) {
	return queryOneJSON[state.Comment](s.db, `
		SELECT c.data
		FROM comments c
		JOIN workflows w ON w.id = c.relation_id AND w.deleted = 0
		WHERE c.deleted = 0 AND c.author = 'user' AND c.relation_type = 'workflow'
		  AND NOT EXISTS (
			SELECT 1 FROM comments answer
			WHERE answer.deleted = 0 AND answer.author = 'agent' AND answer.final = 1
			  AND answer.parent_id = c.id
		  )
		ORDER BY c.id LIMIT 1`)
}

func (s *Store) PendingHumanResponse() (state.WorkflowRun, state.Comment, bool, error) {
	rows, err := s.db.Query(`SELECT data FROM workflow_runs WHERE status = 'waiting' ORDER BY id`)
	if err != nil {
		return state.WorkflowRun{}, state.Comment{}, false, err
	}
	projections := make([]runProjection, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			return state.WorkflowRun{}, state.Comment{}, false, err
		}
		var value runProjection
		if err := json.Unmarshal(encoded, &value); err != nil {
			rows.Close()
			return state.WorkflowRun{}, state.Comment{}, false, err
		}
		projections = append(projections, value)
	}
	if err := rows.Close(); err != nil {
		return state.WorkflowRun{}, state.Comment{}, false, err
	}
	for _, projection := range projections {
		run := restoreRun(projection)
		if run.WaitingGate == nil || run.TaskID < 1 || run.GateCommentID < 1 {
			continue
		}
		comment, found, err := queryOneJSON[state.Comment](s.db, `
			SELECT c.data FROM comments c
			WHERE c.id > ? AND c.deleted = 0 AND c.author = 'user'
			  AND c.relation_type = 'task' AND c.relation_id = ?
			  AND (c.parent_id IS NULL OR c.parent_id = ?)
			  AND NOT EXISTS (SELECT 1 FROM comments a WHERE a.deleted = 0 AND a.author = 'agent' AND a.parent_id = c.id)
			  AND NOT EXISTS (SELECT 1 FROM human_responses h WHERE h.comment_id = c.id)
			ORDER BY c.id LIMIT 1`, run.GateCommentID, run.TaskID, run.GateCommentID)
		if err != nil {
			return state.WorkflowRun{}, state.Comment{}, false, err
		}
		if found {
			return run, comment, true, nil
		}
	}
	return state.WorkflowRun{}, state.Comment{}, false, nil
}

func (s *Store) PendingTrigger() (state.Trigger, eventwire.Event, int64, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return state.Trigger{}, eventwire.Event{}, 0, false, err
	}
	defer tx.Rollback()
	var lastID int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&lastID); err != nil {
		return state.Trigger{}, eventwire.Event{}, 0, false, err
	}
	row := tx.QueryRow(`
		SELECT t.data, e.id, e.type, e.at, e.data
		FROM events e
		JOIN triggers t ON t.event_type = e.type
		WHERE t.enabled = 1 AND t.deleted = 0 AND e.id > t.boundary_event_id
		  AND NOT EXISTS (
			SELECT 1 FROM workflow_runs r
			WHERE r.trigger_id = t.id AND r.source_event_id = e.id
		  )
		  AND NOT (
			e.type IN (?, ?)
			AND CAST(json_extract(e.data, '$.workflowId') AS INTEGER) = t.workflow_id
		  )
		  AND (
			e.type <> ? OR CAST(json_extract(e.data, '$.triggerId') AS INTEGER) = t.id
		  )
		ORDER BY e.id, t.id LIMIT 1`,
		state.WorkflowRunCompleted, state.WorkflowRunFailed, state.CronFired,
	)
	var triggerData, eventData []byte
	var event eventwire.Event
	var at string
	err = row.Scan(&triggerData, &event.ID, &event.Type, &at, &eventData)
	if errors.Is(err, sql.ErrNoRows) {
		return state.Trigger{}, eventwire.Event{}, lastID, false, tx.Commit()
	}
	if err != nil {
		return state.Trigger{}, eventwire.Event{}, 0, false, err
	}
	var trigger state.Trigger
	if err := json.Unmarshal(triggerData, &trigger); err != nil {
		return state.Trigger{}, eventwire.Event{}, 0, false, err
	}
	event.At, err = parseTime(at)
	if err != nil {
		return state.Trigger{}, eventwire.Event{}, 0, false, err
	}
	event.Data = append(json.RawMessage(nil), eventData...)
	if err := tx.Commit(); err != nil {
		return state.Trigger{}, eventwire.Event{}, 0, false, err
	}
	return trigger, event, lastID, true, nil
}

func (s *Store) CronStates() ([]CronState, error) {
	rows, err := s.db.Query(`SELECT data, last_cron_at FROM triggers WHERE enabled = 1 AND deleted = 0 AND event_type = ? ORDER BY id`, state.CronFired)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]CronState, 0)
	for rows.Next() {
		var encoded []byte
		var raw sql.NullString
		if err := rows.Scan(&encoded, &raw); err != nil {
			return nil, err
		}
		var value CronState
		if err := json.Unmarshal(encoded, &value.Trigger); err != nil {
			return nil, err
		}
		if raw.Valid {
			parsed, err := parseTime(raw.String)
			if err != nil {
				return nil, err
			}
			value.Last = &parsed
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func queryJSON[T any](q queryer, query string, args ...any) ([]T, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]T, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		var value T
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func queryOneJSON[T any](q queryer, query string, args ...any) (T, bool, error) {
	var zero T
	var encoded []byte
	err := q.QueryRow(query, args...).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	if err := json.Unmarshal(encoded, &zero); err != nil {
		return zero, false, fmt.Errorf("decode projection: %w", err)
	}
	return zero, true, nil
}

func restoreRun(value runProjection) state.WorkflowRun {
	run := value.Run
	run.Directory, run.Source, run.Settings = value.Directory, value.Source, value.Settings
	run.Arguments = append(json.RawMessage(nil), value.Arguments...)
	return run
}
