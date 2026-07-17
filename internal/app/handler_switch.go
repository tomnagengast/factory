package app

import (
	"errors"
	"net/http"
	"sync/atomic"
)

// HandlerSwitch keeps one listener stable across staged health and selected
// runtime activation. Each request observes one complete handler pointer.
type HandlerSwitch struct{ current atomic.Pointer[handlerHolder] }

type handlerHolder struct{ handler http.Handler }

func NewHandlerSwitch(initial http.Handler) (*HandlerSwitch, error) {
	if initial == nil {
		return nil, errors.New("app handler switch: initial handler is required")
	}
	switcher := &HandlerSwitch{}
	switcher.Install(initial)
	return switcher, nil
}

func (s *HandlerSwitch) Install(handler http.Handler) {
	if s == nil || handler == nil {
		return
	}
	s.current.Store(&handlerHolder{handler: handler})
}

func (s *HandlerSwitch) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	holder := s.current.Load()
	if holder == nil || holder.handler == nil {
		http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	holder.handler.ServeHTTP(writer, request)
}
