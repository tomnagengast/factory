package app

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
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

func TestHandlerSwitchInstallDrainsEnteredHandler(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	served := make(chan struct{})
	switcher, err := NewHandlerSwitch(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		switcher.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/settings", nil))
		close(served)
	}()
	<-entered

	installed := make(chan struct{})
	installStarted := make(chan struct{})
	go func() {
		close(installStarted)
		switcher.Install(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}))
		close(installed)
	}()
	<-installStarted
	deadline := time.Now().Add(time.Second)
	for switcher.mu.TryRLock() {
		switcher.mu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("replacement did not wait for the entered handler")
		}
		runtime.Gosched()
	}
	select {
	case <-installed:
		t.Fatal("replacement installed before entered handler drained")
	default:
	}
	close(release)
	select {
	case <-installed:
	case <-time.After(time.Second):
		t.Fatal("replacement did not install after entered handler drained")
	}
	<-served

	replacement := httptest.NewRecorder()
	switcher.ServeHTTP(replacement, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if replacement.Code != http.StatusServiceUnavailable {
		t.Fatalf("replacement status = %d", replacement.Code)
	}
}
