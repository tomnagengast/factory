package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerCancelsStreamingRequestsBeforeShutdown(t *testing.T) {
	baseContext, cancel := context.WithCancel(context.Background())
	requestDone := make(chan struct{})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(requestDone)
	})
	server := newHTTPServer(handler, baseContext)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	response, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	cancel()
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("streaming request did not observe server cancellation")
	}

	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if err := <-serveResult; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve result = %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
}
