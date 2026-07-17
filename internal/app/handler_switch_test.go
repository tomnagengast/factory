package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerSwitchAtomicallyInstallsSelectedRuntime(t *testing.T) {
	t.Parallel()
	switcher, err := NewHandlerSwitch(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
	}))
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	switcher.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if first.Code != http.StatusAccepted {
		t.Fatalf("bootstrap status = %d", first.Code)
	}
	switcher.Install(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	selected := httptest.NewRecorder()
	switcher.ServeHTTP(selected, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if selected.Code != http.StatusOK {
		t.Fatalf("selected status = %d", selected.Code)
	}
	if _, err := NewHandlerSwitch(nil); err == nil {
		t.Fatal("nil initial handler accepted")
	}
}
