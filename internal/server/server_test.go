package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	handler := New(testWeb())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var got healthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := healthResponse{Status: "ok", App: "factory"}
	if got != want {
		t.Fatalf("response = %#v, want %#v", got, want)
	}
}

func TestFrontendFallsBackToIndex(t *testing.T) {
	t.Parallel()

	handler := New(testWeb())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/future-route", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got, want := recorder.Body.String(), "<h1>Factory</h1>"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestUnknownAPIIsNotFound(t *testing.T) {
	t.Parallel()

	handler := New(testWeb())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/missing", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func testWeb() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>Factory</h1>")},
	}
}
