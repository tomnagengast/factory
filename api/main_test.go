package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/quiescence"
	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/store"
)

type httpResult struct {
	response *http.Response
	err      error
}

func TestParseConfigAcceptsExplicitMediaPath(t *testing.T) {
	var output bytes.Buffer
	configuration, err := parseConfig([]string{
		"-data", "/tmp/factory.db",
		"-media", "/tmp/factory-media",
		"-workflow-workspace", "/tmp/factory-workflows",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.MediaPath != "/tmp/factory-media" {
		t.Fatalf("media path = %q", configuration.MediaPath)
	}
}

func TestParseConfigRejectsEmptyMediaPath(t *testing.T) {
	if _, err := parseConfig([]string{"-media", ""}, io.Discard); err == nil {
		t.Fatal("empty media path was accepted")
	}
}

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

func TestDeploymentStartupBlocksHTTPUntilRecoveryAndResumption(t *testing.T) {
	directory := t.TempDir()
	dataPath := filepath.Join(directory, "factory.db")
	workflowWorkspace := filepath.Join(directory, "workflows")
	mediaPath := filepath.Join(directory, "media")
	entered := filepath.Join(directory, "discovery-entered")
	release := filepath.Join(directory, "release-discovery")
	workflowCommand := filepath.Join(directory, "workflow")
	script := "#!/bin/sh\n" +
		"if [ \"$3\" = \"list\" ]; then " +
		"touch \"" + entered + "\"; " +
		"while [ ! -f \"" + release + "\" ]; do sleep 0.01; done; " +
		"printf '[]'; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(workflowCommand, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	eventStore, err := store.Open(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	workflowEvent, _ := eventStore.Append(state.WorkflowDiscovered, state.WorkflowData{Name: "review"})
	source, _ := eventStore.Append("release.ready", map[string]bool{"ready": true})
	started, _ := eventStore.Append(state.WorkflowRunStarted, state.WorkflowRunData{
		TriggerID: 99, WorkflowID: workflowEvent.ID, WorkflowName: "review", SourceEventID: source.ID,
	})
	if err := eventStore.Close(); err != nil {
		t.Fatal(err)
	}

	address := availableAddress(t)
	t.Setenv("FACTORY_RELEASE_COMMIT", "commit-1")
	t.Setenv("FACTORY_RELEASE_TREE", "tree-1")
	t.Setenv("FACTORY_RELEASE_BUILD", "build-1")
	t.Setenv("FACTORY_RELEASE_DEPLOYMENT", "deployment-1")
	t.Setenv("FACTORY_RELEASE_CONTRACT", "1")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stopped := make(chan error, 1)
	go func() {
		stopped <- run(ctx, config{
			Address: address, DataPath: dataPath, MediaPath: mediaPath,
			WorkflowWorkspace: workflowWorkspace, WorkflowCommand: workflowCommand,
			CodexCommand: "codex", ClaudeCommand: "claude", FactoryCommand: "factory",
		})
	}()
	waitForFile(t, entered)

	client := &http.Client{Timeout: 2 * time.Second}
	healthResult := make(chan httpResult, 1)
	quiescenceResult := make(chan httpResult, 1)
	go func() {
		response, requestErr := client.Get("http://" + address + "/api/health")
		healthResult <- httpResult{response: response, err: requestErr}
	}()
	go func() {
		request, requestErr := http.NewRequest(http.MethodPost, "http://"+address+"/api/quiescence", nil)
		if requestErr != nil {
			quiescenceResult <- httpResult{err: requestErr}
			return
		}
		response, requestErr := client.Do(request)
		quiescenceResult <- httpResult{response: response, err: requestErr}
	}()
	select {
	case result := <-healthResult:
		t.Fatalf("health responded before startup completed: %#v", result)
	case result := <-quiescenceResult:
		t.Fatalf("quiescence responded before startup completed: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
	blockedStore, err := store.Open(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	blockedEvents, err := blockedStore.EventsAfter(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if eventID(blockedEvents, state.DeploymentStarted) == 0 ||
		eventID(blockedEvents, state.DeploymentResumed) != 0 {
		t.Fatalf("blocked startup events = %#v", blockedEvents)
	}
	if err := blockedStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("ready"), 0o644); err != nil {
		t.Fatal(err)
	}

	health := receiveHTTPResult(t, healthResult, "health")
	defer health.Body.Close()
	var healthData map[string]any
	if health.StatusCode != http.StatusOK || json.NewDecoder(health.Body).Decode(&healthData) != nil ||
		healthData["commit"] != "commit-1" {
		t.Fatalf("health = %d %#v", health.StatusCode, healthData)
	}
	quiescenceResponse := receiveHTTPResult(t, quiescenceResult, "quiescence")
	defer quiescenceResponse.Body.Close()
	var lease quiescence.Lease
	if quiescenceResponse.StatusCode != http.StatusOK ||
		json.NewDecoder(quiescenceResponse.Body).Decode(&lease) != nil || lease.Token == "" {
		t.Fatalf("quiescence = %d %#v", quiescenceResponse.StatusCode, lease)
	}

	finalStore, err := store.Open(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := finalStore.EventsAfter(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := finalStore.Close(); err != nil {
		t.Fatal(err)
	}
	recoveryID := int64(0)
	for _, event := range events {
		if event.Type != state.WorkflowRunFailed {
			continue
		}
		var data state.WorkflowRunData
		if json.Unmarshal(event.Data, &data) == nil && data.SourceEventID == source.ID {
			recoveryID = event.ID
		}
	}
	resumedID := eventID(events, state.DeploymentResumed)
	quiescingID := eventID(events, state.DeploymentQuiescing)
	if !(started.ID < recoveryID && recoveryID < resumedID && resumedID < quiescingID) {
		t.Fatalf("started=%d recovery=%d resumed=%d quiescing=%d events=%#v",
			started.ID, recoveryID, resumedID, quiescingID, events)
	}
	for _, event := range events {
		if event.ID < resumedID && event.Type == state.WorkflowRunResumed {
			t.Fatalf("workflow resumed before deployment resumption: %#v", event)
		}
		if event.ID > started.ID && event.ID < resumedID && event.Type == state.WorkflowRunStarted {
			t.Fatalf("workflow started before deployment resumption: %#v", event)
		}
	}

	cancel()
	select {
	case err := <-stopped:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("run stopped with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestDeploymentStartedFailureStopsStartup(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "factory.db")
	eventStore, err := store.Open(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := eventStore.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TRIGGER reject_deployment_started
		BEFORE INSERT ON events
		WHEN NEW.type = 'deployment.started'
		BEGIN
			SELECT RAISE(FAIL, 'deployment start rejected');
		END;
	`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	err = run(context.Background(), config{
		Address: "127.0.0.1:0", DataPath: dataPath,
		MediaPath:         filepath.Join(directory, "media"),
		WorkflowWorkspace: filepath.Join(directory, "workflows"),
		WorkflowCommand:   "workflow", CodexCommand: "codex", ClaudeCommand: "claude",
		FactoryCommand: "factory",
	})
	if err == nil || !strings.Contains(err.Error(), "record deployment start") ||
		!strings.Contains(err.Error(), "deployment start rejected") {
		t.Fatalf("run error = %v", err)
	}
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", path)
		case <-time.After(time.Millisecond):
		}
	}
}

func receiveHTTPResult(t *testing.T, results <-chan httpResult, label string) *http.Response {
	t.Helper()
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatalf("%s request: %v", label, result.err)
		}
		return result.response
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s response", label)
		return nil
	}
}

func eventID(events []eventwire.Event, eventType string) int64 {
	for _, event := range events {
		if event.Type == eventType {
			return event.ID
		}
	}
	return 0
}
