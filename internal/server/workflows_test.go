package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tomnagengast/factory/internal/workflow"
)

func TestWorkflowDraftPublishAndProtectedBindingLifecycle(t *testing.T) {
	t.Parallel()
	handler, policy, _ := testTriggerHandler(t)

	initial := authenticatedRequest(t, handler, "/api/workflows")
	var catalog workflowsResponse
	if err := json.NewDecoder(initial.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	defaultDocument := workflowDocument{}
	for _, candidate := range catalog.Workflows {
		if candidate.WorkflowID == workflow.DefaultID {
			defaultDocument = candidate
		}
	}
	if initial.Code != http.StatusOK || !catalog.DraftAvailable || defaultDocument.Draft.Markdown == "" {
		t.Fatalf("initial workflow catalog = %d %#v", initial.Code, catalog)
	}

	createdResponse := authenticatedJSONRequest(t, handler, http.MethodPost, "/api/workflow-drafts", nil, "")
	var created workflow.Draft
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if createdResponse.Code != http.StatusCreated || created.Revision != 1 || created.BaseWorkflowRevision != 0 || created.Enabled {
		t.Fatalf("created draft = %d %#v", createdResponse.Code, created)
	}

	savedResponse := authenticatedJSONRequest(t, handler, http.MethodPut, "/api/workflow-drafts/"+created.WorkflowID, saveDraftRequest{
		ExpectedDraftRevision: 1, ExpectedWorkflowRevision: 0,
		Name: "Incident response", Enabled: true, Markdown: "# Incident response\n\nVerify the incident.\n",
	}, "")
	var saved workflow.Draft
	if err := json.NewDecoder(savedResponse.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if savedResponse.Code != http.StatusOK || saved.Revision != 2 || !saved.Enabled {
		t.Fatalf("saved draft = %d %#v", savedResponse.Code, saved)
	}

	stale := authenticatedJSONRequest(t, handler, http.MethodPut, "/api/workflow-drafts/"+created.WorkflowID, saveDraftRequest{
		ExpectedDraftRevision: 1, ExpectedWorkflowRevision: 0,
		Name: "Stale", Enabled: true, Markdown: "# Stale",
	}, "")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale save = %d %s", stale.Code, stale.Body.String())
	}

	published := authenticatedJSONRequest(t, handler, http.MethodPost, "/api/workflow-drafts/"+created.WorkflowID+"/publish", publishDraftRequest{
		ExpectedDraftRevision: 2, ExpectedWorkflowRevision: 0, ExpectedPolicyRevision: catalog.PolicyRevision,
	}, "")
	if published.Code != http.StatusOK {
		t.Fatalf("publish = %d %s", published.Code, published.Body.String())
	}
	definition, found := policy.SettingsSnapshot().Workflow(created.WorkflowID)
	if !found || definition.Revision != 1 || definition.Markdown != saved.Markdown {
		t.Fatalf("published workflow = %#v, found=%t", definition, found)
	}

	bound := authenticatedJSONRequest(t, handler, http.MethodPut, "/api/triggers/protected/linear-feedback", protectedFeedbackRequest{
		ExpectedPolicyRevision: policy.SettingsSnapshot().Revision, WorkflowID: created.WorkflowID,
	}, "")
	if bound.Code != http.StatusOK || policy.SettingsSnapshot().ProtectedWorkflows.LinearFeedback.WorkflowID != created.WorkflowID {
		t.Fatalf("protected binding = %d %s policy=%#v", bound.Code, bound.Body.String(), policy.SettingsSnapshot())
	}

	catalogResponse := authenticatedRequest(t, handler, "/api/workflows")
	if err := json.NewDecoder(catalogResponse.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	var document workflowDocument
	for _, candidate := range catalog.Workflows {
		if candidate.WorkflowID == created.WorkflowID {
			document = candidate
		}
	}
	if document.Draft.Revision != 3 || document.Draft.BaseWorkflowRevision != 1 || len(document.References) != 1 || document.References[0].Kind != "protected" {
		t.Fatalf("published document = %#v", document)
	}

	disabled := authenticatedJSONRequest(t, handler, http.MethodPut, "/api/workflow-drafts/"+created.WorkflowID, saveDraftRequest{
		ExpectedDraftRevision: 3, ExpectedWorkflowRevision: 1,
		Name: document.Draft.Name, Enabled: false, Markdown: document.Draft.Markdown,
	}, "")
	var disabledDraft workflow.Draft
	if err := json.NewDecoder(disabled.Body).Decode(&disabledDraft); err != nil {
		t.Fatal(err)
	}
	rejected := authenticatedJSONRequest(t, handler, http.MethodPost, "/api/workflow-drafts/"+created.WorkflowID+"/publish", publishDraftRequest{
		ExpectedDraftRevision: disabledDraft.Revision, ExpectedWorkflowRevision: 1,
		ExpectedPolicyRevision: policy.SettingsSnapshot().Revision,
	}, "")
	publishedAfterReject, found := policy.SettingsSnapshot().Workflow(created.WorkflowID)
	if rejected.Code != http.StatusBadRequest || !found || !publishedAfterReject.Enabled {
		t.Fatalf("referenced disable = %d %s policy=%#v", rejected.Code, rejected.Body.String(), policy.SettingsSnapshot())
	}

	deleted := authenticatedJSONRequest(t, handler, http.MethodDelete, "/api/workflows/"+created.WorkflowID, deleteWorkflowRequest{
		ExpectedWorkflowRevision: 1, ExpectedPolicyRevision: policy.SettingsSnapshot().Revision,
	}, "")
	if deleted.Code != http.StatusBadRequest {
		t.Fatalf("referenced delete = %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestWorkflowAuthoringUnavailableKeepsPublishedCatalogReadable(t *testing.T) {
	t.Parallel()
	handler := testHandler(t)
	response := authenticatedRequest(t, handler, "/api/workflows")
	var catalog workflowsResponse
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || catalog.DraftAvailable || len(catalog.Workflows) != 2 {
		t.Fatalf("read-only catalog = %d %#v", response.Code, catalog)
	}
	mutation := authenticatedJSONRequest(t, handler, http.MethodPost, "/api/workflow-drafts", nil, "")
	if mutation.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable mutation = %d %s", mutation.Code, mutation.Body.String())
	}
}

func authenticatedJSONRequest(t *testing.T, handler http.Handler, method, target string, body any, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
	request.AddCookie(viewerSessionCookie(t, handler))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
