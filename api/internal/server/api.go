package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tomnagengast/factory/api/internal/state"
)

func (s *Server) settings(writer http.ResponseWriter, _ *http.Request) {
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"settings": view.Settings, "harnesses": state.Harnesses,
	})
}

func (s *Server) settingsUpdate(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Harness          string `json:"harness"`
		Model            string `json:"model"`
		Reasoning        string `json:"reasoning"`
		WorkflowCapacity *int   `json:"workflowCapacity"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.WorkflowCapacity == nil {
		writeError(writer, http.StatusBadRequest, errors.New("workflow capacity is required"))
		return
	}
	settings := state.Settings{
		Harness: input.Harness, Model: input.Model, Reasoning: input.Reasoning,
		WorkflowCapacity: *input.WorkflowCapacity,
	}
	if !state.ValidSettings(settings) {
		writeError(
			writer,
			http.StatusBadRequest,
			errors.New("unknown harness, model, reasoning level, or workflow capacity"),
		)
		return
	}
	if _, err := s.wire.Publish(state.SettingsUpdated, settings); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, settings)
}

func (s *Server) projects(writer http.ResponseWriter, _ *http.Request) {
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	projects := active(view.Projects, func(value state.Project) bool { return value.DeletedAt == nil })
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].ID > projects[j].ID })
	writeJSON(writer, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) project(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "project")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	project, found := view.Project(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("project not found"))
		return
	}
	tasks := make([]state.Task, 0)
	for _, task := range view.Tasks {
		if task.ProjectID == id && task.DeletedAt == nil {
			tasks = append(tasks, task)
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"project": project, "tasks": tasks})
}

func (s *Server) projectCreate(writer http.ResponseWriter, request *http.Request) {
	var input state.ProjectData
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := prepareProject(&input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	event, err := s.wire.Publish(state.ProjectCreated, input)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ := state.ProjectEvents(s.wire.Events(0))
	project, _ := view.Project(event.ID)
	writeJSON(writer, http.StatusCreated, project)
}

func (s *Server) projectUpdate(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "project")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input state.ProjectData
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.ID = id
	if err := prepareProject(&input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if _, err := s.wire.Publish(state.ProjectUpdated, input); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ := state.ProjectEvents(s.wire.Events(0))
	project, found := view.Project(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("project not found"))
		return
	}
	writeJSON(writer, http.StatusOK, project)
}

func (s *Server) projectDelete(writer http.ResponseWriter, request *http.Request) {
	s.delete(writer, request, "project", state.ProjectDeleted)
}

func prepareProject(input *state.ProjectData) error {
	input.Name, input.Path = strings.TrimSpace(input.Name), strings.TrimSpace(input.Path)
	if input.Name == "" || input.Path == "" {
		return errors.New("project name and path are required")
	}
	if err := os.MkdirAll(input.Path, 0o777); err != nil {
		return fmt.Errorf("create project path: %w", err)
	}
	return nil
}

func (s *Server) tasks(writer http.ResponseWriter, _ *http.Request) {
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	tasks := active(view.Tasks, func(value state.Task) bool { return value.DeletedAt == nil })
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].ID > tasks[j].ID })
	writeJSON(writer, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) task(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "task")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	task, found := view.Task(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("task not found"))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"task": task, "comments": view.CommentsFor("task", id), "artifacts": view.ArtifactsFor("task", id),
	})
}

func (s *Server) taskCreate(writer http.ResponseWriter, request *http.Request) {
	var input state.TaskData
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	if err := validateTask(&input, view); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	event, err := s.wire.Publish(state.TaskCreated, input)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ = state.ProjectEvents(s.wire.Events(0))
	task, _ := view.Task(event.ID)
	writeJSON(writer, http.StatusCreated, task)
}

func (s *Server) taskUpdate(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "task")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input state.TaskData
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.ID = id
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	if err := validateTask(&input, view); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if _, err := s.wire.Publish(state.TaskUpdated, input); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ = state.ProjectEvents(s.wire.Events(0))
	task, found := view.Task(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("task not found"))
		return
	}
	writeJSON(writer, http.StatusOK, task)
}

func (s *Server) taskDelete(writer http.ResponseWriter, request *http.Request) {
	s.delete(writer, request, "task", state.TaskDeleted)
}

func (s *Server) taskReactionUpdate(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "task")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.reactionUpdate(writer, request, "task", id)
}

func validateTask(input *state.TaskData, view state.Snapshot) error {
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" {
		return errors.New("task title is required")
	}
	if input.Status == "" {
		input.Status = state.Backlog
	}
	if !slices.Contains(state.TaskStatuses, input.Status) {
		return errors.New("unknown task status")
	}
	if input.ProjectID < 1 {
		return errors.New("task project is required")
	}
	project, found := view.Project(input.ProjectID)
	if !found || project.DeletedAt != nil {
		return errors.New("task project not found")
	}
	return nil
}

func (s *Server) taskComment(writer http.ResponseWriter, request *http.Request) {
	taskID, err := pathID(request, "task")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input struct {
		Content         string `json:"content"`
		ParentCommentID *int64 `json:"parentCommentId"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.createComment(writer, state.CommentData{
		RelationType: "task", RelationID: taskID, ParentCommentID: input.ParentCommentID,
		Author: "user", Content: input.Content,
	})
}

func (s *Server) comment(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "comment")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	comment, found := view.Comment(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("comment not found"))
		return
	}
	replies := make([]state.Comment, 0)
	for _, candidate := range view.Comments {
		if candidate.ParentCommentID != nil && *candidate.ParentCommentID == id && candidate.DeletedAt == nil {
			replies = append(replies, candidate)
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"comment": comment, "replies": replies, "artifacts": view.ArtifactsFor("comment", id),
	})
}

func (s *Server) commentUpdate(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "comment")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	existing, found := view.Comment(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("comment not found"))
		return
	}
	var input struct {
		Content         string `json:"content"`
		ParentCommentID *int64 `json:"parentCommentId"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		writeError(writer, http.StatusBadRequest, errors.New("comment content is required"))
		return
	}
	if _, err := s.wire.Publish(state.CommentUpdated, state.CommentData{
		ID: id, RelationType: existing.RelationType, RelationID: existing.RelationID,
		ParentCommentID: input.ParentCommentID, Author: existing.Author, Content: input.Content,
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ = state.ProjectEvents(s.wire.Events(0))
	updated, _ := view.Comment(id)
	writeJSON(writer, http.StatusOK, updated)
}

func (s *Server) commentDelete(writer http.ResponseWriter, request *http.Request) {
	s.delete(writer, request, "comment", state.CommentDeleted)
}

func (s *Server) commentReactionUpdate(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "comment")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.reactionUpdate(writer, request, "comment", id)
}

func (s *Server) reactionUpdate(
	writer http.ResponseWriter,
	request *http.Request,
	targetType string,
	targetID int64,
) {
	var input struct {
		Emoji  string `json:"emoji"`
		Active *bool  `json:"active"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Active == nil {
		writeError(writer, http.StatusBadRequest, errors.New("reaction active state is required"))
		return
	}
	if !state.ValidReactionEmoji(input.Emoji) {
		writeError(writer, http.StatusBadRequest, errors.New("unsupported reaction emoji"))
		return
	}
	data := state.ReactionUpdatedData{
		TargetType: targetType, TargetID: targetID, Emoji: input.Emoji, Active: *input.Active,
	}
	for {
		if request.Context().Err() != nil {
			return
		}
		events := s.wire.Events(0)
		view, err := state.ProjectEvents(events)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		if status, err := validateReactionTarget(view, targetType, targetID); err != nil {
			writeError(writer, status, err)
			return
		}
		expectedLastID := int64(0)
		if len(events) > 0 {
			expectedLastID = events[len(events)-1].ID
		}
		event, published, err := s.wire.PublishIfCurrent(expectedLastID, state.ReactionUpdated, data)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		if !published {
			continue
		}
		view, err = state.ProjectEvents(append(events, event))
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		if targetType == "task" {
			task, _ := view.Task(targetID)
			writeJSON(writer, http.StatusOK, task)
			return
		}
		comment, _ := view.Comment(targetID)
		writeJSON(writer, http.StatusOK, comment)
		return
	}
}

func validateReactionTarget(view state.Snapshot, targetType string, targetID int64) (int, error) {
	if targetType == "task" {
		task, found := view.Task(targetID)
		if !found || task.DeletedAt != nil {
			return http.StatusNotFound, errors.New("task not found")
		}
		return 0, nil
	}
	comment, found := view.Comment(targetID)
	if !found || comment.DeletedAt != nil {
		return http.StatusNotFound, errors.New("comment not found")
	}
	if comment.RelationType != "task" {
		return http.StatusBadRequest, errors.New("reactions are supported only on task comments")
	}
	return 0, nil
}

func (s *Server) createComment(writer http.ResponseWriter, data state.CommentData) {
	data.Content = strings.TrimSpace(data.Content)
	if data.Content == "" {
		writeError(writer, http.StatusBadRequest, errors.New("comment content is required"))
		return
	}
	event, err := s.wire.Publish(state.CommentCreated, data)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ := state.ProjectEvents(s.wire.Events(0))
	comment, _ := view.Comment(event.ID)
	writeJSON(writer, http.StatusCreated, comment)
}

func (s *Server) artifacts(writer http.ResponseWriter, request *http.Request) {
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	relationType := request.URL.Query().Get("relationType")
	relationID, _ := strconv.ParseInt(request.URL.Query().Get("relationId"), 10, 64)
	artifacts := active(view.Artifacts, func(value state.Artifact) bool {
		return value.DeletedAt == nil &&
			(relationType == "" || value.RelationType == relationType) &&
			(relationID == 0 || value.RelationID == relationID)
	})
	sort.SliceStable(artifacts, func(i, j int) bool { return artifacts[i].ID > artifacts[j].ID })
	writeJSON(writer, http.StatusOK, map[string]any{"artifacts": artifacts})
}

func (s *Server) artifact(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "artifact")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	artifact, found := view.Artifact(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("artifact not found"))
		return
	}
	writeJSON(writer, http.StatusOK, artifact)
}

func (s *Server) artifactCreate(writer http.ResponseWriter, request *http.Request) {
	var input state.ArtifactData
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := validateArtifact(input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	event, err := s.wire.Publish(state.ArtifactCreated, input)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ := state.ProjectEvents(s.wire.Events(0))
	artifact, _ := view.Artifact(event.ID)
	writeJSON(writer, http.StatusCreated, artifact)
}

func (s *Server) artifactUpdate(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "artifact")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input state.ArtifactData
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.ID = id
	if err := validateArtifact(input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if _, err := s.wire.Publish(state.ArtifactUpdated, input); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ := state.ProjectEvents(s.wire.Events(0))
	artifact, found := view.Artifact(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("artifact not found"))
		return
	}
	writeJSON(writer, http.StatusOK, artifact)
}

func (s *Server) artifactDelete(writer http.ResponseWriter, request *http.Request) {
	s.delete(writer, request, "artifact", state.ArtifactDeleted)
}

func validateArtifact(input state.ArtifactData) error {
	if !slices.Contains([]string{"text", "link", "image", "document"}, input.Type) {
		return errors.New("unknown artifact type")
	}
	if strings.TrimSpace(input.Content) == "" || input.RelationType == "" || input.RelationID < 1 {
		return errors.New("artifact content and relation are required")
	}
	return nil
}

func (s *Server) triggers(writer http.ResponseWriter, _ *http.Request) {
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	triggers := active(view.Triggers, func(value state.Trigger) bool { return value.DeletedAt == nil })
	sort.SliceStable(triggers, func(i, j int) bool { return triggers[i].ID > triggers[j].ID })
	writeJSON(writer, http.StatusOK, map[string]any{"triggers": triggers})
}

func (s *Server) trigger(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "trigger")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	trigger, found := view.Trigger(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("trigger not found"))
		return
	}
	writeJSON(writer, http.StatusOK, trigger)
}

type triggerInput struct {
	EventType  string  `json:"eventType"`
	Schedule   *string `json:"schedule"`
	WorkflowID int64   `json:"workflowId"`
	Enabled    *bool   `json:"enabled"`
}

func (input triggerInput) data(id int64, defaultEnabled bool) state.TriggerData {
	enabled := defaultEnabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return state.TriggerData{
		ID: id, EventType: input.EventType, Schedule: input.Schedule,
		WorkflowID: input.WorkflowID, Enabled: enabled,
	}
}

func (s *Server) triggerCreate(writer http.ResponseWriter, request *http.Request) {
	var input triggerInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	data := input.data(0, true)
	if err := validateTrigger(data); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	event, err := s.wire.Publish(state.TriggerCreated, data)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ := state.ProjectEvents(s.wire.Events(0))
	trigger, _ := view.Trigger(event.ID)
	writeJSON(writer, http.StatusCreated, trigger)
}

func (s *Server) triggerUpdate(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "trigger")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var input triggerInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.Enabled == nil {
		writeError(writer, http.StatusBadRequest, errors.New("trigger enabled is required"))
		return
	}
	data := input.data(id, false)
	if err := validateTrigger(data); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if _, err := s.wire.Publish(state.TriggerUpdated, data); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ := state.ProjectEvents(s.wire.Events(0))
	trigger, found := view.Trigger(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("trigger not found"))
		return
	}
	writeJSON(writer, http.StatusOK, trigger)
}

func (s *Server) triggerDelete(writer http.ResponseWriter, request *http.Request) {
	s.delete(writer, request, "trigger", state.TriggerDeleted)
}

func validateTrigger(input state.TriggerData) error {
	if strings.TrimSpace(input.EventType) == "" || input.WorkflowID < 1 {
		return errors.New("trigger event type and workflow are required")
	}
	return nil
}

func (s *Server) workflows(writer http.ResponseWriter, _ *http.Request) {
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	workflows := active(view.Workflows, func(value state.Workflow) bool { return value.DeletedAt == nil })
	sort.SliceStable(workflows, func(i, j int) bool { return workflows[i].Name < workflows[j].Name })
	writeJSON(writer, http.StatusOK, map[string]any{"workflows": workflows})
}

func (s *Server) workflow(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "workflow")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	workflow, found := view.Workflow(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("workflow not found"))
		return
	}
	source := ""
	if workflow.Path != nil {
		if data, err := os.ReadFile(*workflow.Path); err == nil {
			source = string(data)
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"workflow": workflow, "comments": view.CommentsFor("workflow", id),
		"artifacts": view.ArtifactsFor("workflow", id), "source": source,
	})
}

func (s *Server) workflowCreate(writer http.ResponseWriter, request *http.Request) {
	message, err := messageBody(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	created, err := s.wire.Publish(state.WorkflowCreated, state.WorkflowData{Description: &message})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if _, err := s.wire.Publish(state.CommentCreated, state.CommentData{
		RelationType: "workflow", RelationID: created.ID, Author: "user", Content: message,
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	view, _ := state.ProjectEvents(s.wire.Events(0))
	workflow, _ := view.Workflow(created.ID)
	writeJSON(writer, http.StatusCreated, workflow)
}

func (s *Server) workflowUpdate(writer http.ResponseWriter, request *http.Request) {
	s.workflowComment(writer, request)
}

func (s *Server) workflowComment(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "workflow")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	message, err := messageBody(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.createComment(writer, state.CommentData{
		RelationType: "workflow", RelationID: id, Author: "user", Content: message,
	})
}

func (s *Server) workflowDelete(writer http.ResponseWriter, request *http.Request) {
	s.delete(writer, request, "workflow", state.WorkflowDeleted)
}

func (s *Server) history(writer http.ResponseWriter, _ *http.Request) {
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	runs := slices.Clone(view.Runs)
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	writeJSON(writer, http.StatusOK, map[string]any{"history": runs})
}

func (s *Server) historyItem(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "item")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	run, found := view.Run(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("workflow run not found"))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"run": run, "events": view.EventsFor(id)})
}

func messageBody(request *http.Request) (string, error) {
	var input struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(request, &input); err != nil {
		return "", err
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" {
		return "", errors.New("message is required")
	}
	return input.Message, nil
}

func (s *Server) events(writer http.ResponseWriter, request *http.Request) {
	after, err := afterID(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"events": s.wire.Events(after)})
}

func (s *Server) event(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "event")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	event, found := s.wire.Event(id)
	if !found {
		writeError(writer, http.StatusNotFound, errors.New("event not found"))
		return
	}
	writeJSON(writer, http.StatusOK, event)
}

func (s *Server) eventTypes(writer http.ResponseWriter, _ *http.Request) {
	types := s.wire.Types()
	if !slices.Contains(types, state.CronFired) {
		types = append(types, state.CronFired)
		sort.Strings(types)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"eventTypes": types})
}

func (s *Server) eventCreate(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	input.Type = strings.TrimSpace(input.Type)
	if input.Type == "" {
		writeError(writer, http.StatusBadRequest, errors.New("event type is required"))
		return
	}
	if len(input.Data) == 0 {
		input.Data = json.RawMessage(`{}`)
	}
	var data any
	if err := json.Unmarshal(input.Data, &data); err != nil {
		writeError(writer, http.StatusBadRequest, errors.New("event data must be JSON"))
		return
	}
	event, err := s.wire.Publish(input.Type, data)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusCreated, event)
}

func (s *Server) ingest(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	encoding, content := "utf-8", string(body)
	if !utf8.Valid(body) {
		encoding, content = "base64", base64.StdEncoding.EncodeToString(body)
	}
	eventType := "ingress.received"
	if source := strings.TrimSpace(request.URL.Query().Get("source")); source != "" {
		eventType = "ingress." + source
	}
	if _, err := s.wire.Publish(eventType, map[string]any{
		"method": request.Method, "url": request.URL.RequestURI(), "headers": request.Header,
		"bodyEncoding": encoding, "body": content,
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	contentType := request.Header.Get("Content-Type")
	switch lower := strings.ToLower(contentType); {
	case strings.HasPrefix(lower, "application/json"):
		writer.Header().Set("Content-Type", contentType)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("{}"))
	case strings.HasPrefix(lower, "application/x-protobuf"):
		writer.Header().Set("Content-Type", contentType)
		writer.WriteHeader(http.StatusOK)
	default:
		writer.WriteHeader(http.StatusOK)
	}
}

func (s *Server) stream(writer http.ResponseWriter, request *http.Request) {
	after, err := afterID(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, errors.New("streaming is unavailable"))
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.WriteHeader(http.StatusOK)
	if _, err := writer.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	flusher.Flush()
	for {
		events, err := s.wire.Wait(request.Context(), after)
		if err != nil {
			return
		}
		for _, event := range events {
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", data); err != nil {
				return
			}
			after = event.ID
		}
		flusher.Flush()
	}
}

func afterID(request *http.Request) (int64, error) {
	value := request.URL.Query().Get("after")
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 0 {
		return 0, errors.New("after must be an event ID")
	}
	return id, nil
}

func (s *Server) delete(writer http.ResponseWriter, request *http.Request, pathName, eventType string) {
	id, err := pathID(request, pathName)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if _, err := s.wire.Publish(eventType, state.IDData{ID: id}); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
