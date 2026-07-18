package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestWorkflowUpdateIsAnAgentMessage(t *testing.T) {
	request, err := parse([]string{"workflow", "update", "7", `{"message":"Add a judge."}`})
	if err != nil {
		t.Fatal(err)
	}
	if request.method != http.MethodPut || request.path != "/api/workflows/7" {
		t.Fatalf("unexpected request: %#v", request)
	}
}

func TestSettingsUseSingletonAPI(t *testing.T) {
	getRequest, err := parse([]string{"settings", "get"})
	if err != nil {
		t.Fatal(err)
	}
	updateRequest, err := parse([]string{
		"settings", "update", `{"harness":"claude","model":"sonnet","reasoning":"high"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if getRequest.method != http.MethodGet || getRequest.path != "/api/settings" ||
		updateRequest.method != http.MethodPut || updateRequest.path != "/api/settings" {
		t.Fatalf("unexpected requests: %#v %#v", getRequest, updateRequest)
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
