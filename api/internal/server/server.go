package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/quiescence"
	"github.com/tomnagengast/factory/api/internal/state"
)

const quiescenceLeaseDuration = 15 * time.Minute

type Server struct {
	wire      *eventwire.Wire
	assets    fs.FS
	mediaRoot string
	admission *quiescence.Controller
}

func New(
	wire *eventwire.Wire,
	assets fs.FS,
	mediaRoot string,
	admission *quiescence.Controller,
) (*Server, error) {
	if wire == nil || assets == nil || mediaRoot == "" || admission == nil {
		return nil, errors.New("server requires a wire, frontend, media path, and workflow admission controller")
	}
	if err := os.MkdirAll(mediaRoot, 0o777); err != nil {
		return nil, fmt.Errorf("create media directory: %w", err)
	}
	absolute, err := filepath.Abs(mediaRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve media directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve media directory links: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return nil, errors.New("media path must be a directory")
	}
	return &Server{
		wire: wire, assets: assets, mediaRoot: canonical, admission: admission,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/quiescence", s.quiescenceAcquire)
	mux.HandleFunc("DELETE /api/quiescence/{lease}", s.quiescenceRelease)
	mux.HandleFunc("GET /api/settings", s.settings)
	mux.HandleFunc("PUT /api/settings", s.settingsUpdate)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/events", s.eventCreate)
	mux.HandleFunc("GET /api/events/types", s.eventTypes)
	mux.HandleFunc("GET /api/events/stream", s.stream)
	mux.HandleFunc("GET /api/events/{event}", s.event)
	mux.HandleFunc("/api/ingest", s.ingest)
	mux.HandleFunc("/api/ingest/{rest...}", s.ingest)

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

	mux.HandleFunc("POST /api/media", s.mediaCreate)
	mux.HandleFunc("GET /api/media/{media}", s.media)

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

	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("GET /api/history/{item}", s.historyItem)

	mux.HandleFunc("GET /assets/{file}", func(writer http.ResponseWriter, request *http.Request) {
		file := request.PathValue("file")
		s.asset("assets/"+file, mime.TypeByExtension(path.Ext(file)))(writer, request)
	})
	mux.HandleFunc("/api/{rest...}", http.NotFound)
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		s.asset("index.html", "text/html; charset=utf-8")(writer, request)
	})
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
		"status":            "ok",
		"app":               "factory",
		"commit":            os.Getenv("FACTORY_RELEASE_COMMIT"),
		"tree":              os.Getenv("FACTORY_RELEASE_TREE"),
		"buildId":           os.Getenv("FACTORY_RELEASE_BUILD"),
		"deploymentId":      os.Getenv("FACTORY_RELEASE_DEPLOYMENT"),
		"contractVersion":   os.Getenv("FACTORY_RELEASE_CONTRACT"),
		"harness":           view.Settings.Harness,
		"workflowCapacity":  view.Settings.WorkflowCapacity,
		"workflowActive":    s.admission.Active(),
		"workflowQuiescing": !s.admission.Accepting(),
		"events":            s.wire.LastID(),
		"tasks":             len(active(view.Tasks, func(value state.Task) bool { return value.DeletedAt == nil })),
		"projects":          len(active(view.Projects, func(value state.Project) bool { return value.DeletedAt == nil })),
		"triggers":          len(active(view.Triggers, func(value state.Trigger) bool { return value.DeletedAt == nil })),
		"workflows":         len(active(view.Workflows, func(value state.Workflow) bool { return value.DeletedAt == nil })),
	})
}

func (s *Server) quiescenceAcquire(writer http.ResponseWriter, request *http.Request) {
	lease, err := s.admission.Acquire(request.Context(), quiescenceLeaseDuration)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, quiescence.ErrAlreadyHeld):
			writeError(writer, http.StatusConflict, err)
		case errors.Is(err, quiescence.ErrExpired):
			writeError(writer, http.StatusServiceUnavailable, err)
		case errors.Is(err, quiescence.ErrDrainFailed):
			writeError(writer, http.StatusServiceUnavailable, err)
		default:
			writeError(writer, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":    "quiescent",
		"lease":     lease.Token,
		"expiresAt": lease.ExpiresAt,
	})
}

func (s *Server) quiescenceRelease(writer http.ResponseWriter, request *http.Request) {
	if !s.admission.Release(request.PathValue("lease")) {
		writeError(writer, http.StatusNotFound, errors.New("quiescence lease not found"))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "released"})
}

func (s *Server) asset(path, contentType string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		data, err := fs.ReadFile(s.assets, path)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, errors.New("frontend asset is unavailable"))
			return
		}
		cacheControl := "public, max-age=31536000, immutable"
		if path == "index.html" {
			cacheControl = "no-cache, must-revalidate"
		}
		writer.Header().Set("Cache-Control", cacheControl)
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
