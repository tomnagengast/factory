package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strconv"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
)

type Server struct {
	wire   *eventwire.Wire
	assets fs.FS
}

func New(wire *eventwire.Wire, assets fs.FS) (*Server, error) {
	if wire == nil || assets == nil {
		return nil, errors.New("server requires a wire and frontend")
	}
	return &Server{wire: wire, assets: assets}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/settings", s.settings)
	mux.HandleFunc("PUT /api/settings", s.settingsUpdate)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/events", s.eventCreate)
	mux.HandleFunc("GET /api/events/types", s.eventTypes)
	mux.HandleFunc("GET /api/events/stream", s.stream)
	mux.HandleFunc("GET /api/events/{event}", s.event)

	mux.HandleFunc("GET /api/projects", s.projects)
	mux.HandleFunc("POST /api/projects", s.projectCreate)
	mux.HandleFunc("GET /api/projects/{project}", s.project)
	mux.HandleFunc("PUT /api/projects/{project}", s.projectUpdate)
	mux.HandleFunc("DELETE /api/projects/{project}", s.projectDelete)

	mux.HandleFunc("GET /api/tasks", s.tasks)
	mux.HandleFunc("POST /api/tasks", s.taskCreate)
	mux.HandleFunc("GET /api/tasks/{task}", s.task)
	mux.HandleFunc("PUT /api/tasks/{task}", s.taskUpdate)
	mux.HandleFunc("DELETE /api/tasks/{task}", s.taskDelete)
	mux.HandleFunc("POST /api/tasks/{task}/comments", s.taskComment)

	mux.HandleFunc("GET /api/comments/{comment}", s.comment)
	mux.HandleFunc("PUT /api/comments/{comment}", s.commentUpdate)
	mux.HandleFunc("DELETE /api/comments/{comment}", s.commentDelete)

	mux.HandleFunc("GET /api/artifacts", s.artifacts)
	mux.HandleFunc("POST /api/artifacts", s.artifactCreate)
	mux.HandleFunc("GET /api/artifacts/{artifact}", s.artifact)
	mux.HandleFunc("PUT /api/artifacts/{artifact}", s.artifactUpdate)
	mux.HandleFunc("DELETE /api/artifacts/{artifact}", s.artifactDelete)

	mux.HandleFunc("GET /api/triggers", s.triggers)
	mux.HandleFunc("POST /api/triggers", s.triggerCreate)
	mux.HandleFunc("GET /api/triggers/{trigger}", s.trigger)
	mux.HandleFunc("PUT /api/triggers/{trigger}", s.triggerUpdate)
	mux.HandleFunc("DELETE /api/triggers/{trigger}", s.triggerDelete)

	mux.HandleFunc("GET /api/workflows", s.workflows)
	mux.HandleFunc("POST /api/workflows", s.workflowCreate)
	mux.HandleFunc("GET /api/workflows/{workflow}", s.workflow)
	mux.HandleFunc("PUT /api/workflows/{workflow}", s.workflowUpdate)
	mux.HandleFunc("DELETE /api/workflows/{workflow}", s.workflowDelete)
	mux.HandleFunc("POST /api/workflows/{workflow}/comments", s.workflowComment)

	mux.HandleFunc("GET /assets/app.js", s.asset("assets/app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /assets/styles.css", s.asset("assets/styles.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /api/{rest...}", http.NotFound)
	mux.HandleFunc("GET /", s.asset("index.html", "text/html; charset=utf-8"))
	return mux
}

func (s *Server) snapshot(writer http.ResponseWriter) (state.Snapshot, bool) {
	view, err := state.ProjectEvents(s.wire.Events(0))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return state.Snapshot{}, false
	}
	return view, true
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	view, ok := s.snapshot(writer)
	if !ok {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":          "ok",
		"app":             "factory",
		"commit":          os.Getenv("NAGS_SOURCE_COMMIT"),
		"tree":            os.Getenv("NAGS_SOURCE_TREE"),
		"buildId":         os.Getenv("NAGS_BUILD_ID"),
		"deploymentId":    os.Getenv("NAGS_DEPLOYMENT_ID"),
		"contractVersion": os.Getenv("NAGS_CONTRACT_VERSION"),
		"harness":         view.Settings.Harness,
		"events":          s.wire.LastID(),
		"tasks":           len(active(view.Tasks, func(value state.Task) bool { return value.DeletedAt == nil })),
		"projects":        len(active(view.Projects, func(value state.Project) bool { return value.DeletedAt == nil })),
		"triggers":        len(active(view.Triggers, func(value state.Trigger) bool { return value.DeletedAt == nil })),
		"workflows":       len(active(view.Workflows, func(value state.Workflow) bool { return value.DeletedAt == nil })),
	})
}

func (s *Server) asset(path, contentType string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		data, err := fs.ReadFile(s.assets, path)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, errors.New("frontend asset is unavailable"))
			return
		}
		writer.Header().Set("Content-Type", contentType)
		_, _ = writer.Write(data)
	}
}

func pathID(request *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(request.PathValue(name), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("%s must be an integer ID", name)
	}
	return id, nil
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func active[T any](values []T, keep func(T) bool) []T {
	filtered := make([]T, 0, len(values))
	for _, value := range values {
		if keep(value) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}
