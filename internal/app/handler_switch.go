package app

import (
	"errors"
	"net/http"
	"sync"
)

// HandlerSwitch keeps one listener stable across staged health and selected
// runtime activation. Installation waits for every request already admitted to
// the previous handler, so returning from Install also proves that handler can
// no longer mutate its retired runtime.
type HandlerSwitch struct {
	mu      sync.RWMutex
	current http.Handler
}

func NewHandlerSwitch(initial http.Handler) (*HandlerSwitch, error) {
	if initial == nil {
		return nil, errors.New("app handler switch: initial handler is required")
	}
	return &HandlerSwitch{current: initial}, nil
}

func (s *HandlerSwitch) Install(handler http.Handler) {
	if s == nil || handler == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = handler
}

func (s *HandlerSwitch) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	s.current.ServeHTTP(writer, request)
}
