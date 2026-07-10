package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/activity"
)

const (
	maxWebhookBody = 1 << 20
	replayWindow   = time.Minute
)

type EventStore interface {
	Add(deliveryID string, event activity.Event) (bool, error)
	Snapshot() activity.Snapshot
}

type appServer struct {
	activityStore EventStore
	linearSecret  []byte
	now           func() time.Time
}

type healthResponse struct {
	Status string `json:"status"`
	App    string `json:"app"`
}

type linearPayload struct {
	Type             string `json:"type"`
	Action           string `json:"action"`
	WebhookTimestamp int64  `json:"webhookTimestamp"`
}

type activityResponse struct {
	Status         string           `json:"status"`
	Total          uint64           `json:"total"`
	LastReceivedAt *time.Time       `json:"lastReceivedAt"`
	Events         []activity.Event `json:"events"`
}

func New(web fs.FS, store EventStore, linearSecret []byte, now func() time.Time) (http.Handler, error) {
	if store == nil {
		return nil, errors.New("server: activity store is required")
	}
	if len(linearSecret) == 0 {
		return nil, errors.New("server: Linear webhook secret is required")
	}
	if now == nil {
		return nil, errors.New("server: clock is required")
	}

	app := &appServer{activityStore: store, linearSecret: linearSecret, now: now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", healthz)
	mux.HandleFunc("GET /api/activity", app.activity)
	mux.HandleFunc("POST /api/webhooks/linear", app.linearWebhook)
	mux.HandleFunc("POST /cdn-cgi/rum", cloudflareBeacon)
	mux.Handle("/", frontend(web))
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
		Status: "listening",
		Total:  snapshot.Total,
		Events: snapshot.Events,
	}
	if len(snapshot.Events) > 0 {
		response.LastReceivedAt = &snapshot.Events[0].ReceivedAt
	}
	writeJSON(w, http.StatusOK, response)
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
	w.WriteHeader(http.StatusOK)
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
