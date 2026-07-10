package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

type healthResponse struct {
	Status string `json:"status"`
	App    string `json:"app"`
}

func New(web fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", healthz)
	mux.HandleFunc("POST /cdn-cgi/rum", cloudflareBeacon)
	mux.Handle("/", frontend(web))
	return mux
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
