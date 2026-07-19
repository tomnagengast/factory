package main

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaCreateUsesMultipartResourceAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.gif")
	want := []byte("GIF89a")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	var method, requestPath, filename, contentType string
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method, requestPath = request.Method, request.URL.Path
		_, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		reader := multipart.NewReader(request.Body, parameters["boundary"])
		part, err := reader.NextPart()
		if err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		filename, contentType = part.FileName(), part.Header.Get("Content-Type")
		uploaded, _ = io.ReadAll(part)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":3,"name":"sample.gif","url":"/api/media/3"}`))
	}))
	defer server.Close()

	var output bytes.Buffer
	if err := Run([]string{"--url", server.URL, "media", "create", path}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || requestPath != "/api/media" || filename != "sample.gif" ||
		contentType != "image/gif" || !bytes.Equal(uploaded, want) {
		t.Fatalf("request = %s %s %q %q %q", method, requestPath, filename, contentType, uploaded)
	}
	if !strings.Contains(output.String(), `"url": "/api/media/3"`) {
		t.Fatalf("output = %s", output.String())
	}
}

func TestTaskCommentUsesResourceAPI(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method, path = request.Method, request.URL.Path
		data, _ := io.ReadAll(request.Body)
		body = string(data)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":3}`))
	}))
	defer server.Close()
	var output bytes.Buffer
	err := Run([]string{
		"--url", server.URL, "task", "comment", "12", `{"content":"Looks good."}`,
	}, &output, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/api/tasks/12/comments" || !strings.Contains(body, "Looks good") {
		t.Fatalf("unexpected request: %s %s %s", method, path, body)
	}
	if !strings.Contains(output.String(), `"id": 3`) {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

func TestTaskCreateAndUpdateForwardInReviewStatus(t *testing.T) {
	type receivedRequest struct {
		method, path, body string
	}
	var received []receivedRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		data, _ := io.ReadAll(request.Body)
		received = append(received, receivedRequest{request.Method, request.URL.Path, string(data)})
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":12,"status":"in review"}`))
	}))
	defer server.Close()

	createBody := `{"title":"Review it","description":"Check the change.","parentTaskId":3,"status":"in review","projectId":1}`
	updateBody := `{"title":"Review it again","description":"Check the change.","parentTaskId":3,"status":"in review","projectId":1}`
	for _, args := range [][]string{
		{"--url", server.URL, "task", "create", createBody},
		{"--url", server.URL, "task", "update", "12", updateBody},
	} {
		if err := Run(args, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}

	want := []receivedRequest{
		{http.MethodPost, "/api/tasks", createBody},
		{http.MethodPut, "/api/tasks/12", updateBody},
	}
	if len(received) != len(want) {
		t.Fatalf("requests = %#v", received)
	}
	for index := range want {
		if received[index] != want[index] {
			t.Fatalf("request %d = %#v, want %#v", index, received[index], want[index])
		}
	}
}

func TestWorkflowUpdateIsAnAgentMessage(t *testing.T) {
	request, err := parse([]string{"workflow", "update", "7", `{"message":"Add a judge."}`})
	if err != nil {
		t.Fatal(err)
	}
	if request.method != http.MethodPut || request.path != "/api/workflows/7" {
		t.Fatalf("unexpected request: %#v", request)
	}
}

func TestTriggerUpdateForwardsEnabledState(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method, path = request.Method, request.URL.Path
		data, _ := io.ReadAll(request.Body)
		body = string(data)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":7,"eventType":"release.ready","workflowId":2,"enabled":false}`))
	}))
	defer server.Close()

	requestBody := `{"eventType":"release.ready","workflowId":2,"enabled":false}`
	var output bytes.Buffer
	if err := Run([]string{
		"--url", server.URL, "trigger", "update", "7", requestBody,
	}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/api/triggers/7" || body != requestBody {
		t.Fatalf("unexpected request: %s %s %s", method, path, body)
	}
	if !strings.Contains(output.String(), `"enabled": false`) {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

func TestSettingsUseSingletonAPI(t *testing.T) {
	getRequest, err := parse([]string{"settings", "get"})
	if err != nil {
		t.Fatal(err)
	}
	updateRequest, err := parse([]string{
		"settings", "update",
		`{"harness":"claude","model":"sonnet","reasoning":"high","workflowCapacity":6}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if getRequest.method != http.MethodGet || getRequest.path != "/api/settings" ||
		updateRequest.method != http.MethodPut || updateRequest.path != "/api/settings" {
		t.Fatalf("unexpected requests: %#v %#v", getRequest, updateRequest)
	}
}

func TestHistoryIsReadOnly(t *testing.T) {
	list, err := parse([]string{"history", "list"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := parse([]string{"history", "get", "12"})
	if err != nil {
		t.Fatal(err)
	}
	if list.path != "/api/history" || item.path != "/api/history/12" {
		t.Fatalf("unexpected history requests: %#v %#v", list, item)
	}
	if _, err := parse([]string{"history", "delete", "12"}); err == nil {
		t.Fatal("history delete was accepted")
	}
}

func TestRejectsInvalidIdentityAndJSON(t *testing.T) {
	for _, args := range [][]string{
		{"task", "get", "nope"},
		{"artifact", "create", "not-json"},
	} {
		if _, err := parse(args); err == nil {
			t.Fatalf("expected %v to fail", args)
		}
	}
}
