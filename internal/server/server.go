package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/state"
)

type Server struct {
	wire      *eventwire.Wire
	assets    fs.FS
	agentName string
}

func New(wire *eventwire.Wire, assets fs.FS, agentName string) (*Server, error) {
	if wire == nil || assets == nil || agentName == "" {
		return nil, errors.New("server requires a wire, frontend, and agent name")
	}
	return &Server{wire: wire, assets: assets, agentName: agentName}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/tasks", s.tasks)
	mux.HandleFunc("POST /api/tasks", s.submit)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/events/stream", s.stream)
	mux.HandleFunc("GET /{$}", s.asset("frontend/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /src/index.js", s.asset("frontend/src/index.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("GET /src/styles.css", s.asset("frontend/src/styles.css", "text/css; charset=utf-8"))
	return mux
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	tasks, err := state.Project(s.wire.Events(0))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "ok",
		"agent":  s.agentName,
		"events": s.wire.LastSequence(),
		"tasks":  len(tasks),
	})
}

func (s *Server) tasks(writer http.ResponseWriter, _ *http.Request) {
	tasks, err := state.Project(s.wire.Events(0))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) submit(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		http.Error(writer, "Task body must be JSON with a prompt.", http.StatusBadRequest)
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.Prompt == "" {
		http.Error(writer, "Prompt is required.", http.StatusBadRequest)
		return
	}
	taskID, err := eventwire.NewID("task")
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := s.wire.Publish(eventwire.TaskSubmitted, taskID, "", map[string]string{"prompt": input.Prompt}); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	tasks, err := state.Project(s.wire.Events(0))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, task := range tasks {
		if task.ID == taskID {
			writeJSON(writer, http.StatusCreated, task)
			return
		}
	}
	http.Error(writer, "Task was published but not projected.", http.StatusInternalServerError)
}

func (s *Server) events(writer http.ResponseWriter, request *http.Request) {
	after, err := afterSequence(request)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"events": s.wire.Events(after)})
}

func (s *Server) stream(writer http.ResponseWriter, request *http.Request) {
	after, err := afterSequence(request)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "Streaming is unavailable.", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
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
			after = event.Sequence
		}
		flusher.Flush()
	}
}

func (s *Server) asset(path, contentType string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		data, err := fs.ReadFile(s.assets, path)
		if err != nil {
			http.Error(writer, "Frontend asset is unavailable.", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", contentType)
		_, _ = writer.Write(data)
	}
}

func afterSequence(request *http.Request) (uint64, error) {
	value := request.URL.Query().Get("after")
	if value == "" {
		return 0, nil
	}
	after, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, errors.New("after must be an event sequence")
	}
	return after, nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
