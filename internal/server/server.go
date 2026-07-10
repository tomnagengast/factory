package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/activity"
	"github.com/tomnagengast/network/apps/factory/internal/agentrun"
)

const (
	maxWebhookBody = 1 << 20
	replayWindow   = time.Minute
	viewerUsername = "factory"
	viewerRealm    = `Basic realm="Factory agents", charset="UTF-8"`
)

var doCommandPattern = regexp.MustCompile(`(?i)^/do[[:space:]]+([A-Z][A-Z0-9]*-[1-9][0-9]*)[[:space:]]*$`)

type EventStore interface {
	Add(deliveryID string, event activity.Event) (bool, error)
	Snapshot() activity.Snapshot
}

type RunStore interface {
	Claim(trigger agentrun.Trigger, now time.Time) (agentrun.Run, bool, error)
	PublicSnapshot() agentrun.PublicSnapshot
}

type RunNotifier interface {
	Notify()
}

type AgentObserver interface {
	Observe(context.Context, string) (agentrun.AgentView, error)
}

type Config struct {
	Web            fs.FS
	ActivityStore  EventStore
	RunStore       RunStore
	RunNotifier    RunNotifier
	AgentObserver  AgentObserver
	LinearSecret   []byte
	TriggerActor   string
	ViewerPassword string
	Now            func() time.Time
}

type appServer struct {
	activityStore  EventStore
	runStore       RunStore
	runNotifier    RunNotifier
	agentObserver  AgentObserver
	linearSecret   []byte
	triggerActor   string
	viewerPassword []byte
	now            func() time.Time
}

type healthResponse struct {
	Status string `json:"status"`
	App    string `json:"app"`
}

type linearPayload struct {
	Type             string `json:"type"`
	Action           string `json:"action"`
	WebhookTimestamp int64  `json:"webhookTimestamp"`
	Actor            struct {
		ID string `json:"id"`
	} `json:"actor"`
	Data struct {
		Body string `json:"body"`
	} `json:"data"`
}

type activityResponse struct {
	Status         string                  `json:"status"`
	Total          uint64                  `json:"total"`
	LastReceivedAt *time.Time              `json:"lastReceivedAt"`
	Events         []activity.Event        `json:"events"`
	AgentRuns      agentrun.PublicSnapshot `json:"agentRuns"`
}

func New(config Config) (http.Handler, error) {
	if config.ActivityStore == nil {
		return nil, errors.New("server: activity store is required")
	}
	if len(config.LinearSecret) == 0 {
		return nil, errors.New("server: Linear webhook secret is required")
	}
	if config.RunStore == nil {
		return nil, errors.New("server: agent run store is required")
	}
	if config.RunNotifier == nil {
		return nil, errors.New("server: agent run notifier is required")
	}
	if config.AgentObserver == nil {
		return nil, errors.New("server: agent observer is required")
	}
	if config.TriggerActor == "" {
		return nil, errors.New("server: Linear trigger actor is required")
	}
	if config.ViewerPassword == "" {
		return nil, errors.New("server: viewer password is required")
	}
	if config.Now == nil {
		return nil, errors.New("server: clock is required")
	}

	app := &appServer{
		activityStore:  config.ActivityStore,
		runStore:       config.RunStore,
		runNotifier:    config.RunNotifier,
		agentObserver:  config.AgentObserver,
		linearSecret:   config.LinearSecret,
		triggerActor:   config.TriggerActor,
		viewerPassword: []byte(config.ViewerPassword),
		now:            config.Now,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", healthz)
	mux.HandleFunc("GET /api/activity", app.activity)
	mux.Handle("GET /api/agents/{id}", app.requireViewer(http.HandlerFunc(app.agent)))
	mux.HandleFunc("POST /api/webhooks/linear", app.linearWebhook)
	mux.HandleFunc("POST /cdn-cgi/rum", cloudflareBeacon)
	mux.Handle("GET /agents", app.requireViewer(frontend(config.Web)))
	mux.Handle("GET /agents/", app.requireViewer(frontend(config.Web)))
	mux.Handle("/", frontend(config.Web))
	return mux, nil
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok", App: "factory"})
}

func cloudflareBeacon(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *appServer) activity(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.activityStore.Snapshot()
	response := activityResponse{
		Status:    "listening",
		Total:     snapshot.Total,
		Events:    snapshot.Events,
		AgentRuns: s.runStore.PublicSnapshot(),
	}
	if len(snapshot.Events) > 0 {
		response.LastReceivedAt = &snapshot.Events[0].ReceivedAt
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *appServer) agent(w http.ResponseWriter, r *http.Request) {
	view, err := s.agentObserver.Observe(r.Context(), r.PathValue("id"))
	if errors.Is(err, agentrun.ErrRunNotFound) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *appServer) requireViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(viewerUsername)) == 1
		passwordMatch := subtle.ConstantTimeCompare([]byte(password), s.viewerPassword) == 1
		if !ok || !usernameMatch || !passwordMatch {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("WWW-Authenticate", viewerRealm)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *appServer) linearWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}
	if !validSignature(s.linearSecret, body, r.Header.Get("Linear-Signature")) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	var payload linearPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if payload.Type == "" || len(payload.Type) > 64 || payload.Action == "" || len(payload.Action) > 64 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	deliveryID := r.Header.Get("Linear-Delivery")
	if deliveryID == "" || len(deliveryID) > 128 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	now := s.now().UTC()
	sentAt := time.UnixMilli(payload.WebhookTimestamp)
	if sentAt.Before(now.Add(-replayWindow)) || sentAt.After(now.Add(replayWindow)) {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	_, err = s.activityStore.Add(deliveryID, activity.Event{
		Type:       payload.Type,
		Action:     payload.Action,
		ReceivedAt: now,
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if trigger, ok := agentTrigger(payload, deliveryID, s.triggerActor); ok {
		_, created, claimErr := s.runStore.Claim(trigger, now)
		if claimErr != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if created {
			s.runNotifier.Notify()
		}
	}
	w.WriteHeader(http.StatusOK)
}

func agentTrigger(payload linearPayload, deliveryID, allowedActorID string) (agentrun.Trigger, bool) {
	if payload.Type != "Comment" || payload.Action != "create" || payload.Actor.ID != allowedActorID {
		return agentrun.Trigger{}, false
	}
	match := doCommandPattern.FindStringSubmatch(payload.Data.Body)
	if len(match) != 2 {
		return agentrun.Trigger{}, false
	}
	return agentrun.Trigger{
		DeliveryID:      deliveryID,
		IssueIdentifier: strings.ToUpper(match[1]),
		Kind:            "linear-comment",
	}, true
}

func validSignature(secret, body []byte, signature string) bool {
	provided, err := hex.DecodeString(signature)
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), provided)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func frontend(web fs.FS) http.Handler {
	files := http.FileServerFS(web)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." {
			name = "index.html"
		}
		if _, err := fs.Stat(web, name); err == nil {
			files.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(name, "api/") {
			http.NotFound(w, r)
			return
		}

		indexRequest := r.Clone(r.Context())
		indexURL := *r.URL
		indexURL.Path = "/"
		indexRequest.URL = &indexURL
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, indexRequest)
	})
}
