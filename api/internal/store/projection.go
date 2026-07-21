package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
)

type runProjection struct {
	Run       state.WorkflowRun `json:"run"`
	Directory string            `json:"directory,omitempty"`
	Source    string            `json:"source,omitempty"`
	Settings  *state.Settings   `json:"settings,omitempty"`
	Arguments json.RawMessage   `json:"arguments,omitempty"`
}

func applyProjection(tx *sql.Tx, event eventwire.Event) error {
	switch event.Type {
	case state.ProjectCreated:
		var data state.ProjectData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value := state.Project{
			Record: newRecord(event), Name: data.Name, Description: data.Description,
			Repo: data.Repo, Path: data.Path, URL: data.URL,
		}
		return putProject(tx, value)
	case state.ProjectUpdated:
		var data state.ProjectData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := projectTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		value.Name, value.Description, value.Repo = data.Name, data.Description, data.Repo
		value.Path, value.URL, value.UpdatedAt = data.Path, data.URL, event.At
		return putProject(tx, value)
	case state.ProjectDeleted:
		var data state.IDData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := projectTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		deleteRecord(&value.Record, event.At)
		return putProject(tx, value)

	case state.TaskCreated:
		var data state.TaskData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value := state.Task{
			Record: newRecord(event), Title: data.Title, Description: data.Description,
			ParentTaskID: data.ParentTaskID, Status: data.Status, ProjectID: data.ProjectID,
			Reactions: []string{},
		}
		return putTask(tx, value)
	case state.TaskUpdated:
		var data state.TaskData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := taskTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		value.Title, value.Description, value.ParentTaskID = data.Title, data.Description, data.ParentTaskID
		value.Status, value.ProjectID, value.UpdatedAt = data.Status, data.ProjectID, event.At
		return putTask(tx, value)
	case state.TaskDeleted:
		var data state.IDData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := taskTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		deleteRecord(&value.Record, event.At)
		return putTask(tx, value)

	case state.CommentCreated:
		var data state.CommentData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		kind := data.Kind
		if kind == "" {
			kind = "message"
		}
		final := data.Author == "agent" && data.RelationType == "workflow" && data.ParentCommentID != nil
		if data.Final != nil {
			final = *data.Final
		}
		value := state.Comment{
			Record: newRecord(event), RelationType: data.RelationType, RelationID: data.RelationID,
			ParentCommentID: data.ParentCommentID, Author: data.Author, Kind: kind,
			Label: data.Label, Final: final, Content: data.Content, Reactions: []string{},
		}
		return putComment(tx, value)
	case state.CommentUpdated:
		var data state.CommentData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := commentTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		value.ParentCommentID, value.Content, value.UpdatedAt = data.ParentCommentID, data.Content, event.At
		return putComment(tx, value)
	case state.CommentDeleted:
		var data state.IDData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		return deleteCommentTree(tx, data.ID, event.At)
	case state.ReactionUpdated:
		var data state.ReactionUpdatedData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		if data.TargetID < 1 {
			return nil
		}
		settings, err := settingsTx(tx)
		if err != nil {
			return err
		}
		switch data.TargetType {
		case "task":
			value, found, err := taskTx(tx, data.TargetID)
			if err != nil || !found || value.DeletedAt != nil {
				return err
			}
			if !reactionUpdateAllowed(value.Reactions, settings.ReactionEmojis, data.Emoji, data.Active) {
				return nil
			}
			value.Reactions = updateReactions(value.Reactions, settings.ReactionEmojis, data.Emoji, data.Active)
			value.UpdatedAt = event.At
			return putTask(tx, value)
		case "comment":
			value, found, err := commentTx(tx, data.TargetID)
			if err != nil || !found || value.DeletedAt != nil || value.RelationType != "task" {
				return err
			}
			if !reactionUpdateAllowed(value.Reactions, settings.ReactionEmojis, data.Emoji, data.Active) {
				return nil
			}
			value.Reactions = updateReactions(value.Reactions, settings.ReactionEmojis, data.Emoji, data.Active)
			value.UpdatedAt = event.At
			return putComment(tx, value)
		}
		return nil

	case state.ArtifactCreated:
		var data state.ArtifactData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value := state.Artifact{
			Record: newRecord(event), Name: data.Name, Type: data.Type, Content: data.Content,
			RelationType: data.RelationType, RelationID: data.RelationID,
		}
		return putArtifact(tx, value)
	case state.ArtifactUpdated:
		var data state.ArtifactData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := artifactTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		value.Name, value.Type, value.Content = data.Name, data.Type, data.Content
		value.RelationType, value.RelationID, value.UpdatedAt = data.RelationType, data.RelationID, event.At
		return putArtifact(tx, value)
	case state.ArtifactDeleted:
		var data state.IDData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := artifactTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		deleteRecord(&value.Record, event.At)
		return putArtifact(tx, value)

	case state.MediaCreated:
		var data state.MediaData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		return putMedia(tx, state.Media{
			Record: newRecord(event), Name: data.Name, ContentType: data.ContentType,
			Size: data.Size, SHA256: data.SHA256,
		})

	case state.TriggerCreated:
		data := state.TriggerData{Enabled: true}
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		return putTrigger(tx, state.Trigger{
			Record: newRecord(event), EventType: data.EventType, Schedule: data.Schedule,
			WorkflowID: data.WorkflowID, Enabled: data.Enabled,
		}, event.ID, nil)
	case state.TriggerUpdated:
		data := state.TriggerData{Enabled: true}
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, boundary, lastCron, found, err := triggerTx(tx, data.ID)
		_ = boundary
		if err != nil || !found {
			return err
		}
		value.EventType, value.Schedule = data.EventType, data.Schedule
		value.WorkflowID, value.Enabled, value.UpdatedAt = data.WorkflowID, data.Enabled, event.At
		return putTrigger(tx, value, event.ID, lastCron)
	case state.TriggerDeleted:
		var data state.IDData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, boundary, lastCron, found, err := triggerTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		deleteRecord(&value.Record, event.At)
		return putTrigger(tx, value, boundary, lastCron)

	case state.WorkflowCreated, state.WorkflowDiscovered:
		var data state.WorkflowData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		name := data.Name
		if name == "" {
			name = fmt.Sprintf("Draft %d", event.ID)
		}
		return putWorkflow(tx, state.Workflow{
			Record: newRecord(event), Name: name, Description: data.Description,
			Path: data.Path, Scope: data.Scope, Phases: append([]string{}, data.Phases...),
			Mutating: data.Mutating,
		})
	case state.WorkflowUpdated:
		var data state.WorkflowData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := workflowTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		value.Name, value.Description, value.Path = data.Name, data.Description, data.Path
		value.Scope, value.Phases = data.Scope, append([]string{}, data.Phases...)
		value.Mutating, value.UpdatedAt = data.Mutating, event.At
		return putWorkflow(tx, value)
	case state.WorkflowDeleted:
		var data state.IDData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := workflowTx(tx, data.ID)
		if err != nil || !found {
			return err
		}
		deleteRecord(&value.Record, event.At)
		return putWorkflow(tx, value)

	case state.SettingsUpdated:
		value := state.DefaultSettings()
		if err := decodeEvent(event, &value); err != nil {
			return err
		}
		if !state.ValidSettings(value) {
			return fmt.Errorf("settings event %d is invalid", event.ID)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO settings(id, data) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, encoded); err != nil {
			return err
		}
		return reorderReactionProjections(tx, value.ReactionEmojis)

	case state.CronFired:
		var data state.CronData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE triggers SET last_cron_at = ? WHERE id = ?`, formatTime(event.At), data.TriggerID)
		return err

	case state.WorkflowRunStarted:
		return applyRunStarted(tx, event)
	case state.WorkflowRunEventRecorded:
		var data state.WorkflowRunEventData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		var runtime state.WorkflowRuntimeEvent
		if err := json.Unmarshal(data.Event, &runtime); err != nil {
			return fmt.Errorf("decode workflow event %d: %w", event.ID, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO workflow_run_event_index(event_id, run_id, sequence) VALUES(?, ?, ?)`,
			event.ID, data.RunID, runtime.Sequence,
		); err != nil {
			return fmt.Errorf("index workflow event %d: %w", event.ID, err)
		}
		value, found, err := runTx(tx, data.RunID)
		if err != nil || !found {
			return err
		}
		value.Run.UpdatedAt = event.At
		return putRun(tx, value)
	case state.WorkflowRunWaiting:
		var data state.WorkflowRunStateData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := runTx(tx, data.RunID)
		if err != nil || !found {
			return err
		}
		value.Run.UpdatedAt, value.Run.Status = event.At, "waiting"
		value.Run.WaitingGate, value.Run.GateCommentID = data.Gate, data.GateCommentID
		value.Run.ResponseCommentID = 0
		return putRun(tx, value)
	case state.WorkflowRunRetryRequested:
		var data state.WorkflowRunStateData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := runTx(tx, data.RunID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: %d", ErrWorkflowRunNotFound, data.RunID)
		}
		if value.Run.Status != "failed" {
			return fmt.Errorf("%w: run %d is %s", ErrWorkflowRunNotRetryable, data.RunID, value.Run.Status)
		}
		value.Run.UpdatedAt, value.Run.Status = event.At, "retrying"
		value.Run.Output, value.Run.Error = "", ""
		value.Run.WaitingGate = nil
		value.Run.GateCommentID, value.Run.ResponseCommentID = 0, 0
		return putRun(tx, value)
	case state.WorkflowRunResumed:
		var data state.WorkflowRunStateData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := runTx(tx, data.RunID)
		if err != nil || !found {
			return err
		}
		if value.Run.Status != "waiting" && value.Run.Status != "retrying" {
			return fmt.Errorf("workflow run %d cannot resume from %s", data.RunID, value.Run.Status)
		}
		value.Run.UpdatedAt, value.Run.Status = event.At, "running"
		value.Run.WaitingGate = nil
		value.Run.ResponseCommentID = data.ResponseCommentID
		if data.ResponseCommentID > 0 {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO human_responses(comment_id) VALUES(?)`, data.ResponseCommentID); err != nil {
				return err
			}
		}
		return putRun(tx, value)
	case state.WorkflowRunCompleted, state.WorkflowRunFailed:
		var data state.WorkflowRunData
		if err := decodeEvent(event, &data); err != nil {
			return err
		}
		value, found, err := runByClaimTx(tx, data.TriggerID, data.SourceEventID)
		if err != nil || !found {
			return err
		}
		value.Run.UpdatedAt, value.Run.Output, value.Run.Error = event.At, data.Output, data.Error
		if event.Type == state.WorkflowRunCompleted {
			value.Run.Status = "completed"
		} else {
			value.Run.Status = "failed"
		}
		return putRun(tx, value)
	}
	return nil
}

func applyRunStarted(tx *sql.Tx, event eventwire.Event) error {
	var data state.WorkflowRunData
	if err := decodeEvent(event, &data); err != nil {
		return err
	}
	taskID := data.TaskID
	if taskID == 0 {
		source, found, err := eventTx(tx, data.SourceEventID)
		if err != nil {
			return err
		}
		if found {
			taskID, err = taskIDForEvent(source)
			if err != nil {
				return err
			}
			if _, found, err := taskTx(tx, taskID); err != nil || !found {
				taskID = 0
			}
		}
	}
	if data.WorkflowName == "" {
		if selected, found, err := workflowTx(tx, data.WorkflowID); err != nil {
			return err
		} else if found {
			data.WorkflowName, data.WorkflowPhases = selected.Name, append([]string{}, selected.Phases...)
		}
	}
	projection := runProjection{
		Run: state.WorkflowRun{
			ID: event.ID, CreatedAt: event.At, UpdatedAt: event.At,
			TriggerID: data.TriggerID, WorkflowID: data.WorkflowID,
			WorkflowName: data.WorkflowName, WorkflowPhases: append([]string{}, data.WorkflowPhases...),
			SourceEventID: data.SourceEventID, TaskID: taskID, Status: "running",
		},
		Directory: data.Directory, Source: data.Source, Settings: data.Settings,
		Arguments: append(json.RawMessage(nil), data.Arguments...),
	}
	if err := putRun(tx, projection); err != nil {
		return err
	}
	workflowValue, found, err := workflowTx(tx, data.WorkflowID)
	if err != nil || !found {
		return err
	}
	workflowValue.RunCount++
	if taskID > 0 {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO workflow_tasks(workflow_id, task_id) VALUES(?, ?)`, data.WorkflowID, taskID); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM workflow_tasks WHERE workflow_id = ?`, data.WorkflowID).Scan(&workflowValue.TaskCount); err != nil {
			return err
		}
	}
	return putWorkflow(tx, workflowValue)
}

func rebuildProjectionTx(tx *sql.Tx) error {
	var after int64
	for {
		rows, err := tx.Query(`SELECT id, type, at, data FROM events WHERE id > ? ORDER BY id LIMIT 500`, after)
		if err != nil {
			return err
		}
		events := make([]eventwire.Event, 0, 500)
		for rows.Next() {
			var event eventwire.Event
			var at string
			var data []byte
			if err := rows.Scan(&event.ID, &event.Type, &at, &data); err != nil {
				rows.Close()
				return err
			}
			event.At, err = parseTime(at)
			if err != nil {
				rows.Close()
				return err
			}
			event.Data = append(json.RawMessage(nil), data...)
			events = append(events, event)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		for _, event := range events {
			if err := applyProjection(tx, event); err != nil {
				return fmt.Errorf("rebuild projection at event %d (%s): %w", event.ID, event.Type, err)
			}
			after = event.ID
		}
	}
}

func (s *Store) rebuildProjections(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(dropProjectionSchema); err != nil {
		return fmt.Errorf("drop stale projections: %w", err)
	}
	if _, err := tx.Exec(projectionSchema); err != nil {
		return fmt.Errorf("create projections: %w", err)
	}
	if err := rebuildProjectionTx(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE projection_meta SET version = ? WHERE id = 1`, projectionVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func decodeEvent(event eventwire.Event, target any) error {
	if err := json.Unmarshal(event.Data, target); err != nil {
		return fmt.Errorf("decode %s event %d: %w", event.Type, event.ID, err)
	}
	return nil
}

func newRecord(event eventwire.Event) state.Record {
	return state.Record{ID: event.ID, CreatedAt: event.At, UpdatedAt: event.At}
}

func deleteRecord(record *state.Record, at time.Time) {
	record.UpdatedAt, record.DeletedAt = at, &at
}

func taskIDForEvent(event eventwire.Event) (int64, error) {
	switch event.Type {
	case state.TaskCreated:
		return event.ID, nil
	case state.TaskUpdated, state.TaskDeleted:
		var data state.IDData
		if err := decodeEvent(event, &data); err != nil {
			return 0, err
		}
		return data.ID, nil
	}
	return 0, nil
}

func reactionUpdateAllowed(current, configured []string, emoji string, active bool) bool {
	return state.ReactionEmojiConfigured(configured, emoji) || !active && slices.Contains(current, emoji)
}

func updateReactions(current, configured []string, emoji string, active bool) []string {
	selected := make(map[string]bool, len(current)+1)
	for _, value := range current {
		selected[value] = true
	}
	selected[emoji] = active
	return orderedReactions(current, configured, selected)
}

func reorderReactions(current, configured []string) []string {
	selected := make(map[string]bool, len(current))
	for _, value := range current {
		selected[value] = true
	}
	return orderedReactions(current, configured, selected)
}

func orderedReactions(current, configured []string, selected map[string]bool) []string {
	result := make([]string, 0, len(selected))
	for _, value := range configured {
		if selected[value] {
			result = append(result, value)
		}
	}
	for _, value := range current {
		if selected[value] && !state.ReactionEmojiConfigured(configured, value) && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func settingsTx(tx *sql.Tx) (state.Settings, error) {
	settings, found, err := settingsQuery(tx)
	if err != nil {
		return state.Settings{}, err
	}
	if !found {
		return state.DefaultSettings(), nil
	}
	return settings, nil
}

func reorderReactionProjections(tx *sql.Tx, configured []string) error {
	tasks, err := queryJSON[state.Task](tx, `SELECT data FROM tasks ORDER BY id`)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		task.Reactions = reorderReactions(task.Reactions, configured)
		if err := putTask(tx, task); err != nil {
			return err
		}
	}
	comments, err := queryJSON[state.Comment](tx, `SELECT data FROM comments WHERE relation_type = 'task' ORDER BY id`)
	if err != nil {
		return err
	}
	for _, comment := range comments {
		comment.Reactions = reorderReactions(comment.Reactions, configured)
		if err := putComment(tx, comment); err != nil {
			return err
		}
	}
	return nil
}

func deleteCommentTree(tx *sql.Tx, rootID int64, at time.Time) error {
	rows, err := tx.Query(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM comments WHERE id = ?
			UNION ALL
			SELECT comments.id FROM comments JOIN descendants ON comments.parent_id = descendants.id
		)
		SELECT id FROM descendants`, rootID)
	if err != nil {
		return err
	}
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		value, found, err := commentTx(tx, id)
		if err != nil {
			return err
		}
		if found {
			deleteRecord(&value.Record, at)
			if err := putComment(tx, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func marshal(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode projection: %w", err)
	}
	return encoded, nil
}

func getJSON[T any](tx *sql.Tx, query string, args ...any) (T, bool, error) {
	var zero T
	var encoded []byte
	err := tx.QueryRow(query, args...).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	if err := json.Unmarshal(encoded, &zero); err != nil {
		return zero, false, err
	}
	return zero, true, nil
}

func projectTx(tx *sql.Tx, id int64) (state.Project, bool, error) {
	return getJSON[state.Project](tx, `SELECT data FROM projects WHERE id = ?`, id)
}
func taskTx(tx *sql.Tx, id int64) (state.Task, bool, error) {
	return getJSON[state.Task](tx, `SELECT data FROM tasks WHERE id = ?`, id)
}
func commentTx(tx *sql.Tx, id int64) (state.Comment, bool, error) {
	return getJSON[state.Comment](tx, `SELECT data FROM comments WHERE id = ?`, id)
}
func artifactTx(tx *sql.Tx, id int64) (state.Artifact, bool, error) {
	return getJSON[state.Artifact](tx, `SELECT data FROM artifacts WHERE id = ?`, id)
}
func workflowTx(tx *sql.Tx, id int64) (state.Workflow, bool, error) {
	return getJSON[state.Workflow](tx, `SELECT data FROM workflows WHERE id = ?`, id)
}
func runTx(tx *sql.Tx, id int64) (runProjection, bool, error) {
	return getJSON[runProjection](tx, `SELECT data FROM workflow_runs WHERE id = ?`, id)
}
func runByClaimTx(tx *sql.Tx, triggerID, sourceEventID int64) (runProjection, bool, error) {
	return getJSON[runProjection](tx, `SELECT data FROM workflow_runs WHERE trigger_id = ? AND source_event_id = ?`, triggerID, sourceEventID)
}

func eventTx(tx *sql.Tx, id int64) (eventwire.Event, bool, error) {
	var event eventwire.Event
	var at string
	var data []byte
	err := tx.QueryRow(`SELECT id, type, at, data FROM events WHERE id = ?`, id).Scan(&event.ID, &event.Type, &at, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return eventwire.Event{}, false, nil
	}
	if err != nil {
		return eventwire.Event{}, false, err
	}
	event.At, err = parseTime(at)
	event.Data = append(json.RawMessage(nil), data...)
	return event, true, err
}

func triggerTx(tx *sql.Tx, id int64) (state.Trigger, int64, *time.Time, bool, error) {
	var encoded []byte
	var boundary int64
	var raw sql.NullString
	err := tx.QueryRow(`SELECT data, boundary_event_id, last_cron_at FROM triggers WHERE id = ?`, id).Scan(&encoded, &boundary, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state.Trigger{}, 0, nil, false, nil
	}
	if err != nil {
		return state.Trigger{}, 0, nil, false, err
	}
	var value state.Trigger
	if err := json.Unmarshal(encoded, &value); err != nil {
		return state.Trigger{}, 0, nil, false, err
	}
	var last *time.Time
	if raw.Valid {
		parsed, err := parseTime(raw.String)
		if err != nil {
			return state.Trigger{}, 0, nil, false, err
		}
		last = &parsed
	}
	return value, boundary, last, true, nil
}

func putProject(tx *sql.Tx, value state.Project) error {
	data, err := marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO projects(id, deleted, data) VALUES(?, ?, ?) ON CONFLICT(id) DO UPDATE SET deleted=excluded.deleted, data=excluded.data`, value.ID, boolInt(value.DeletedAt != nil), data)
	return err
}
func putTask(tx *sql.Tx, value state.Task) error {
	data, err := marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO tasks(id, project_id, deleted, data) VALUES(?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, deleted=excluded.deleted, data=excluded.data`, value.ID, value.ProjectID, boolInt(value.DeletedAt != nil), data)
	return err
}
func putComment(tx *sql.Tx, value state.Comment) error {
	data, err := marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO comments(id, relation_type, relation_id, parent_id, author, final, deleted, data) VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET relation_type=excluded.relation_type, relation_id=excluded.relation_id, parent_id=excluded.parent_id, author=excluded.author, final=excluded.final, deleted=excluded.deleted, data=excluded.data`, value.ID, value.RelationType, value.RelationID, nullableInt(pointerValue(value.ParentCommentID)), value.Author, boolInt(value.Final), boolInt(value.DeletedAt != nil), data)
	return err
}
func putArtifact(tx *sql.Tx, value state.Artifact) error {
	data, err := marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO artifacts(id, relation_type, relation_id, deleted, data) VALUES(?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET relation_type=excluded.relation_type, relation_id=excluded.relation_id, deleted=excluded.deleted, data=excluded.data`, value.ID, value.RelationType, value.RelationID, boolInt(value.DeletedAt != nil), data)
	return err
}
func putMedia(tx *sql.Tx, value state.Media) error {
	data, err := marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO media(id, data) VALUES(?, ?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, value.ID, data)
	return err
}
func putTrigger(tx *sql.Tx, value state.Trigger, boundary int64, lastCron *time.Time) error {
	data, err := marshal(value)
	if err != nil {
		return err
	}
	var cron any
	if lastCron != nil {
		cron = formatTime(*lastCron)
	}
	_, err = tx.Exec(`INSERT INTO triggers(id, event_type, workflow_id, enabled, boundary_event_id, last_cron_at, deleted, data) VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET event_type=excluded.event_type, workflow_id=excluded.workflow_id, enabled=excluded.enabled, boundary_event_id=excluded.boundary_event_id, last_cron_at=excluded.last_cron_at, deleted=excluded.deleted, data=excluded.data`, value.ID, value.EventType, value.WorkflowID, boolInt(value.Enabled), boundary, cron, boolInt(value.DeletedAt != nil), data)
	return err
}
func putWorkflow(tx *sql.Tx, value state.Workflow) error {
	data, err := marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO workflows(id, name, path, deleted, data) VALUES(?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, path=excluded.path, deleted=excluded.deleted, data=excluded.data`, value.ID, value.Name, nullableString(value.Path), boolInt(value.DeletedAt != nil), data)
	return err
}
func putRun(tx *sql.Tx, value runProjection) error {
	value.Run.Directory = value.Directory
	value.Run.Source = value.Source
	value.Run.Settings = value.Settings
	value.Run.Arguments = append(json.RawMessage(nil), value.Arguments...)
	data, err := marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO workflow_runs(id, trigger_id, workflow_id, source_event_id, task_id, status, data) VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET trigger_id=excluded.trigger_id, workflow_id=excluded.workflow_id, source_event_id=excluded.source_event_id, task_id=excluded.task_id, status=excluded.status, data=excluded.data`, value.Run.ID, value.Run.TriggerID, value.Run.WorkflowID, value.Run.SourceEventID, nullableInt(value.Run.TaskID), value.Run.Status, data)
	return err
}

func pointerValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
