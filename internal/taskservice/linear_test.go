package taskservice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/linearidentity"
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
				"id": "11111111-1111-4111-8111-111111111111", "identifier": "ENG-46", "title": "Native coexistence", "description": "Private detail",
				"url": "https://linear.app/nags/issue/ENG-46/native-coexistence", "updatedAt": now,
				"state":   map[string]string{"name": "In Progress", "type": stateType},
				"project": map[string]string{"id": "project-uuid", "name": "Factory"}, "team": map[string]string{"id": "team-uuid"},
				"comments": map[string]any{"nodes": comments},
			}}})
		case strings.Contains(request.Query, "mutation FactoryComment"):
			commentMutations++
			input := request.Variables["input"].(map[string]any)
			commentID := fmt.Sprintf("comment-agent-%d", commentMutations)
			comment := map[string]any{"id": commentID, "body": input["body"], "createdAt": now, "parent": nil, "user": nil}
			if parent, ok := input["parentId"]; ok {
				comment["parent"] = map[string]any{"id": parent}
			}
			comments = append(comments, comment)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": true, "comment": map[string]string{"id": commentID}}}})
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

	provider, err := NewLinearProvider(server.URL, "linear-test", server.Client(), testLinearIdentityBinder(t))
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
		if _, err := NewLinearProvider(endpoint, "linear-test", http.DefaultClient, testLinearIdentityBinder(t)); err == nil {
			t.Fatalf("unsafe endpoint %q accepted", endpoint)
		}
	}
}

func TestLinearProviderPaginatesAndDeterministicallyOrdersComments(t *testing.T) {
	now := time.Date(2026, time.July, 15, 22, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if _, found := request.Variables["after"]; found {
				t.Fatal("first page included an after cursor")
			}
		} else if request.Variables["after"] != "cursor-1" {
			t.Fatalf("second page cursor = %#v", request.Variables["after"])
		}
		comments := []map[string]any{{"id": "comment-b", "body": "B", "createdAt": now, "parent": nil, "user": map[string]string{"id": "user", "name": "Tom"}}}
		pageInfo := map[string]any{"hasNextPage": true, "endCursor": "cursor-1"}
		if requests == 2 {
			comments = []map[string]any{{"id": "comment-a", "body": "A", "createdAt": now, "parent": nil, "user": map[string]string{"id": "user", "name": "Tom"}}}
			pageInfo = map[string]any{"hasNextPage": false, "endCursor": "cursor-2"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": map[string]any{
			"id": "11111111-1111-4111-8111-111111111111", "identifier": "ENG-46", "title": "Paged", "updatedAt": now,
			"state": map[string]string{"name": "In Progress", "type": "started"}, "team": map[string]string{"id": "team-uuid"},
			"comments": map[string]any{"nodes": comments, "pageInfo": pageInfo},
		}}})
	}))
	defer server.Close()

	provider, err := NewLinearProvider(server.URL, "linear-test", server.Client(), testLinearIdentityBinder(t))
	if err != nil {
		t.Fatal(err)
	}
	issue, err := provider.Detail(t.Context(), "ENG-46")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(issue.Messages) != 2 || issue.Messages[0].ID != "comment-a" || issue.Messages[0].Ordinal != 1 || issue.Messages[1].ID != "comment-b" || issue.Messages[1].Ordinal != 2 {
		t.Fatalf("requests=%d messages=%#v", requests, issue.Messages)
	}
}

func TestLinearProviderReadsMoreThan250Comments(t *testing.T) {
	now := time.Date(2026, time.July, 15, 22, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page := requests
		requests++
		count := 100
		if page == 2 {
			count = 51
		}
		comments := make([]map[string]any, 0, count)
		for index := 0; index < count; index++ {
			sequence := page*100 + index
			comments = append(comments, map[string]any{
				"id": fmt.Sprintf("comment-%03d", sequence), "body": "body", "createdAt": now.Add(time.Duration(sequence) * time.Second),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": map[string]any{
			"id": "11111111-1111-4111-8111-111111111111", "identifier": "ENG-46", "title": "Paged", "updatedAt": now,
			"state": map[string]string{"name": "In Progress", "type": "started"}, "team": map[string]string{"id": "team-uuid"},
			"comments": map[string]any{"nodes": comments, "pageInfo": map[string]any{"hasNextPage": page < 2, "endCursor": fmt.Sprintf("cursor-%d", page+1)}},
		}}})
	}))
	defer server.Close()
	provider, err := NewLinearProvider(server.URL, "linear-test", server.Client(), testLinearIdentityBinder(t))
	if err != nil {
		t.Fatal(err)
	}
	issue, err := provider.Detail(t.Context(), "ENG-46")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || len(issue.Messages) != 251 || issue.Messages[250].ID != "comment-250" || issue.Messages[250].Ordinal != 251 {
		t.Fatalf("requests=%d messages=%d last=%#v", requests, len(issue.Messages), issue.Messages[len(issue.Messages)-1])
	}
}

func TestLinearProviderRejectsDuplicateCommentsAndStalledCursor(t *testing.T) {
	for name, secondComment := range map[string]struct {
		secondComment string
		nextCursor    string
	}{
		"duplicate":      {secondComment: "comment-1", nextCursor: "cursor-2"},
		"stalled cursor": {secondComment: "comment-2", nextCursor: "cursor-1"},
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				commentID, cursor := "comment-1", "cursor-1"
				if requests == 2 {
					commentID, cursor = secondComment.secondComment, secondComment.nextCursor
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issue": map[string]any{
					"id": "11111111-1111-4111-8111-111111111111", "identifier": "ENG-46", "title": "Paged", "updatedAt": time.Now(),
					"state": map[string]string{"name": "In Progress", "type": "started"}, "team": map[string]string{"id": "team-uuid"},
					"comments": map[string]any{"nodes": []map[string]any{{"id": commentID, "body": "body", "createdAt": time.Now()}}, "pageInfo": map[string]any{"hasNextPage": true, "endCursor": cursor}},
				}}})
			}))
			defer server.Close()
			provider, err := NewLinearProvider(server.URL, "linear-test", server.Client(), testLinearIdentityBinder(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Detail(t.Context(), "ENG-46"); err == nil {
				t.Fatal("unsafe pagination unexpectedly succeeded")
			}
		})
	}
}

func testLinearIdentityBinder(t *testing.T) *linearidentity.Store {
	t.Helper()
	store, err := linearidentity.Open(filepath.Join(t.TempDir(), "linear-task-identities.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
