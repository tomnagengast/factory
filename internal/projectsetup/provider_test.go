package projectsetup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestLinearProviderCoordinatorCreatesLabeledIssue(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requestCount++
		payload := decodeProviderRequest(t, r)
		if r.Header.Get("Authorization") != "linear-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch requestCount {
		case 1:
			writeProviderResponse(t, w, providerWorkspaceResponse())
		case 2:
			writeProviderResponse(t, w, map[string]any{"data": map[string]any{"project": map[string]any{
				"issues": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}},
			}}})
		case 3:
			input := payload.Variables["input"].(map[string]any)
			if input["teamId"] != "team-eng" || input["projectId"] != "project-network" {
				t.Errorf("create input routing = %#v", input)
			}
			if got := stringSlice(input["labelIds"]); !reflect.DeepEqual(got, []string{"label-factory", "label-yolo"}) {
				t.Errorf("labelIds = %#v", got)
			}
			description, _ := input["description"].(string)
			if !strings.Contains(description, providerIssueMarker("project-cellar", "https://cellar.nags.cloud")) || !strings.Contains(description, "cellar.nags.cloud") {
				t.Errorf("description = %q", description)
			}
			writeProviderResponse(t, w, map[string]any{"data": map[string]any{"issueCreate": map[string]any{
				"success": true,
				"issue": map[string]any{"id": "issue-provider", "identifier": "ENG-88", "description": description,
					"labels": map[string]any{"nodes": []map[string]any{{"id": "label-factory", "name": "Factory"}, {"id": "label-yolo", "name": "Yolo"}}}},
			}}})
		default:
			t.Errorf("unexpected request %d: %s", requestCount, payload.Query)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	coordinator := mustProviderCoordinator(t, server.URL, server.Client())
	issue, err := coordinator.Ensure(t.Context(), providerTestSpec())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if issue != (ProviderIssue{ID: "issue-provider", Identifier: "ENG-88"}) {
		t.Fatalf("issue = %#v", issue)
	}
	if requestCount != 3 {
		t.Fatalf("request count = %d", requestCount)
	}
}

func TestLinearProviderCoordinatorReusesMarkedIssueAndRestoresLabels(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	requestCount := 0
	spec := providerTestSpec()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requestCount++
		payload := decodeProviderRequest(t, r)
		switch requestCount {
		case 1:
			writeProviderResponse(t, w, providerWorkspaceResponse())
		case 2:
			writeProviderResponse(t, w, map[string]any{"data": map[string]any{"project": map[string]any{
				"issues": map[string]any{
					"nodes": []map[string]any{{
						"id": "issue-provider", "identifier": "ENG-88", "description": providerIssueMarker(spec.ProjectID, spec.CloudURL),
						"labels": map[string]any{"nodes": []map[string]any{{"id": "label-existing", "name": "Existing"}}},
					}},
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
				},
			}}})
		case 3:
			if payload.Variables["id"] != "issue-provider" {
				t.Errorf("updated issue = %#v", payload.Variables["id"])
			}
			input := payload.Variables["input"].(map[string]any)
			if got := stringSlice(input["labelIds"]); !reflect.DeepEqual(got, []string{"label-existing", "label-factory", "label-yolo"}) {
				t.Errorf("updated labels = %#v", got)
			}
			writeProviderResponse(t, w, map[string]any{"data": map[string]any{"issueUpdate": map[string]any{
				"success": true,
				"issue": map[string]any{"id": "issue-provider", "identifier": "ENG-88", "description": providerIssueMarker(spec.ProjectID, spec.CloudURL),
					"labels": map[string]any{"nodes": []map[string]any{{"id": "label-existing", "name": "Existing"}, {"id": "label-factory", "name": "Factory"}, {"id": "label-yolo", "name": "Yolo"}}}},
			}}})
		default:
			t.Errorf("unexpected request %d: %s", requestCount, payload.Query)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	coordinator := mustProviderCoordinator(t, server.URL, server.Client())
	issue, err := coordinator.Ensure(t.Context(), spec)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if issue.Identifier != "ENG-88" || requestCount != 3 {
		t.Fatalf("issue = %#v, requests = %d", issue, requestCount)
	}
}

type providerGraphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func decodeProviderRequest(t *testing.T, r *http.Request) providerGraphQLRequest {
	t.Helper()
	var payload providerGraphQLRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Errorf("decode request: %v", err)
	}
	return payload
}

func writeProviderResponse(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func providerWorkspaceResponse() map[string]any {
	return map[string]any{"data": map[string]any{
		"projects": map[string]any{"nodes": []map[string]any{{
			"id": "project-network", "name": "Network", "teams": map[string]any{"nodes": []map[string]any{{"id": "team-eng"}}},
		}}},
		"issueLabels": map[string]any{"nodes": []map[string]any{{"id": "label-yolo", "name": "Yolo"}, {"id": "label-factory", "name": "Factory"}}},
	}}
}

func mustProviderCoordinator(t *testing.T, linearURL string, client *http.Client) *LinearProviderCoordinator {
	t.Helper()
	coordinator, err := NewLinearProviderCoordinator(linearURL, "linear-key", "Network", []string{"Factory", "Yolo"}, client)
	if err != nil {
		t.Fatalf("NewLinearProviderCoordinator: %v", err)
	}
	return coordinator
}

func providerTestSpec() Spec {
	return Spec{
		ProjectID: "project-cellar", ProjectName: "Cellar", Repository: "tomnagengast/cellar",
		RepoURL: "git@github.com:tomnagengast/cellar.git", LocalPath: "/Users/tom/repos/tomnagengast/cellar",
		ManagedRoot: "/Users/tom/repos/tomnagengast", CloudURL: "https://cellar.nags.cloud",
		BaseBranch: "main", Bootstrap: true, Managed: true,
	}
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.(string))
	}
	return result
}
