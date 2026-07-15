package taskservice

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLinearProviderScopesDetailAndIdempotentOperations(t *testing.T) {
	now := time.Date(2026, time.July, 15, 22, 0, 0, 0, time.UTC)
	comments := []map[string]any{{
		"id": "comment-human", "body": "Current scope", "createdAt": now.Add(-time.Minute), "parent": nil,
		"user": map[string]string{"id": "user-1", "name": "Tom"},
	}}
	commentMutations := 0
	attachmentMutations := 0
	stateMutations := 0
	stateType := "started"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if r.Header.Get("Authorization") != "linear-test" {
			t.Fatal("missing Linear authorization")
		}
		switch {
		case strings.Contains(request.Query, "query FactoryTask"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": map[string]any{
				"id": "issue-uuid", "identifier": "ENG-46", "title": "Native coexistence", "description": "Private detail",
				"url": "https://linear.app/nags/issue/ENG-46/native-coexistence", "updatedAt": now,
				"state":   map[string]string{"name": "In Progress", "type": stateType},
				"project": map[string]string{"id": "project-uuid", "name": "Factory"}, "team": map[string]string{"id": "team-uuid"},
				"comments": map[string]any{"nodes": comments},
			}}})
		case strings.Contains(request.Query, "mutation FactoryComment"):
			commentMutations++
			input := request.Variables["input"].(map[string]any)
			comment := map[string]any{"id": "comment-agent", "body": input["body"], "createdAt": now, "parent": nil, "user": nil}
			if parent, ok := input["parentId"]; ok {
				comment["parent"] = map[string]any{"id": parent}
			}
			comments = append(comments, comment)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": true, "comment": map[string]string{"id": "comment-agent"}}}})
		case strings.Contains(request.Query, "mutation FactoryAttachment"):
			attachmentMutations++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"attachmentCreate": map[string]any{"success": true, "attachment": map[string]string{"id": "attachment-1"}}}})
		case strings.Contains(request.Query, "query FactoryWorkflowStates"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"workflowStates": map[string]any{"nodes": []map[string]any{{"id": "state-complete", "type": "completed", "team": map[string]string{"id": "team-uuid"}}}}}})
		case strings.Contains(request.Query, "mutation FactoryIssueState"):
			stateMutations++
			stateType = "completed"
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]bool{"success": true}}})
		default:
			t.Fatalf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	provider, err := NewLinearProvider(server.URL, "linear-test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	issue, err := provider.Detail(t.Context(), "eng-46")
	if err != nil || issue.Ref.Identifier != "ENG-46" || issue.State != "in_progress" || len(issue.Messages) != 1 || issue.Messages[0].Author.Kind != "human" {
		t.Fatalf("detail=%#v err=%v", issue, err)
	}
	issue, err = provider.Comment(t.Context(), "ENG-46", "comment-human", "Addressed feedback", "reply", "retry-key")
	if err != nil || len(issue.Messages) != 2 || issue.Messages[1].ParentID != "comment-human" || !strings.Contains(issue.Messages[1].Body, "`codex-do:ENG-46:reply-") || !strings.HasSuffix(issue.Messages[1].Body, ":r1`") {
		t.Fatalf("comment detail=%#v err=%v", issue, err)
	}
	if _, err := provider.Comment(t.Context(), "ENG-46", "comment-human", "Addressed feedback", "reply", "retry-key"); err != nil || commentMutations != 1 {
		t.Fatalf("idempotent comment mutations=%d err=%v", commentMutations, err)
	}
	if _, err := provider.Comment(t.Context(), "ENG-46", "comment-other-issue", "Cross-issue reply", "reply", "cross-issue"); err == nil {
		t.Fatal("cross-issue reply accepted")
	}
	if _, err := provider.Comment(t.Context(), "ENG-46", "", "Unknown operation", "unknown", "unknown-operation"); err == nil {
		t.Fatal("unknown comment operation accepted")
	}
	if _, err := provider.Link(t.Context(), "ENG-46", "Pull request", "https://github.com/tomnagengast/factory/pull/15"); err != nil || attachmentMutations != 1 {
		t.Fatalf("attachment mutations=%d err=%v", attachmentMutations, err)
	}
	issue, err = provider.State(t.Context(), "ENG-46", "completed")
	if err != nil || issue.State != "completed" || stateMutations != 1 {
		t.Fatalf("state=%#v mutations=%d err=%v", issue, stateMutations, err)
	}
	if _, err := provider.Gate(t.Context(), "ENG-46", "plan", "gated", "https://example.com/plan", "gate-key"); err != nil || commentMutations != 2 {
		t.Fatalf("gate mutations=%d err=%v", commentMutations, err)
	}
}

func TestLinearProviderRejectsCrossIssueReplyAndUnsafeLinks(t *testing.T) {
	provider := &LinearProvider{}
	if _, err := provider.Link(t.Context(), "ENG-46", "Unsafe", "http://example.com"); err == nil {
		t.Fatal("unsafe attachment accepted")
	}
	if _, err := provider.Comment(t.Context(), "ENG-46", "", "", "comment", "key"); err == nil {
		t.Fatal("empty comment accepted")
	}
}

func TestNewLinearProviderRejectsMalformedEndpointWithoutPanicking(t *testing.T) {
	for _, endpoint := range []string{"https://linear.app/graphql\x00", "http://linear.app/graphql", "https://user@linear.app/graphql"} {
		if _, err := NewLinearProvider(endpoint, "linear-test", http.DefaultClient); err == nil {
			t.Fatalf("unsafe endpoint %q accepted", endpoint)
		}
	}
}
