package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"sync"

	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

type keyedWorkflowLocks struct {
	mu    *sync.Mutex
	locks map[string]*sync.Mutex
}

func newKeyedWorkflowLocks() keyedWorkflowLocks {
	return keyedWorkflowLocks{mu: &sync.Mutex{}, locks: make(map[string]*sync.Mutex)}
}

func (l keyedWorkflowLocks) lock(id string) func() {
	l.mu.Lock()
	lock := l.locks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		l.locks[id] = lock
	}
	l.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

type workflowReference struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type workflowDocument struct {
	WorkflowID    string               `json:"workflowId"`
	Published     *workflow.Definition `json:"published,omitempty"`
	Draft         workflow.Draft       `json:"draft"`
	SavedDraft    bool                 `json:"savedDraft"`
	DraftConflict bool                 `json:"draftConflict,omitempty"`
	References    []workflowReference  `json:"references"`
}

type workflowsResponse struct {
	PolicyRevision uint64             `json:"policyRevision"`
	DraftAvailable bool               `json:"draftAvailable"`
	DraftError     string             `json:"draftError,omitempty"`
	Workflows      []workflowDocument `json:"workflows"`
}

type saveDraftRequest struct {
	ExpectedDraftRevision    uint64 `json:"expectedDraftRevision"`
	ExpectedWorkflowRevision uint64 `json:"expectedWorkflowRevision"`
	Name                     string `json:"name"`
	Enabled                  bool   `json:"enabled"`
	Markdown                 string `json:"markdown"`
}

type discardDraftRequest struct {
	ExpectedDraftRevision    uint64 `json:"expectedDraftRevision"`
	ExpectedWorkflowRevision uint64 `json:"expectedWorkflowRevision"`
}

type publishDraftRequest struct {
	ExpectedDraftRevision    uint64 `json:"expectedDraftRevision"`
	ExpectedWorkflowRevision uint64 `json:"expectedWorkflowRevision"`
	ExpectedPolicyRevision   uint64 `json:"expectedPolicyRevision"`
}

type deleteWorkflowRequest struct {
	ExpectedWorkflowRevision uint64 `json:"expectedWorkflowRevision"`
	ExpectedPolicyRevision   uint64 `json:"expectedPolicyRevision"`
}

func (s *appServer) getWorkflows(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.workflowsResponse())
}

func (s *appServer) workflowsResponse() workflowsResponse {
	configuration := s.settings.Snapshot()
	response := workflowsResponse{
		PolicyRevision: configuration.Revision,
		DraftAvailable: s.workflowDrafts != nil,
		DraftError:     s.workflowDraftError,
		Workflows:      []workflowDocument{},
	}
	drafts := map[string]workflow.Draft{}
	if s.workflowDrafts != nil {
		for _, draft := range s.workflowDrafts.Snapshot().Drafts {
			drafts[draft.WorkflowID] = draft
		}
	}
	published := make(map[string]workflow.Definition, len(configuration.Workflows))
	for _, definition := range configuration.Workflows {
		published[definition.ID] = definition
		draft, saved := drafts[definition.ID]
		conflict := saved && draft.BaseWorkflowRevision != definition.Revision
		if !saved {
			draft = workflow.Draft{
				WorkflowID: definition.ID, BaseWorkflowRevision: definition.Revision,
				Name: definition.Name, Enabled: definition.Enabled, Markdown: definition.Markdown,
			}
		}
		definitionCopy := definition
		response.Workflows = append(response.Workflows, workflowDocument{
			WorkflowID: definition.ID, Published: &definitionCopy, Draft: draft,
			SavedDraft: saved, DraftConflict: conflict, References: s.workflowReferences(definition.ID, configuration),
		})
	}
	for id, draft := range drafts {
		if _, found := published[id]; found || draft.BaseWorkflowRevision != 0 {
			continue
		}
		response.Workflows = append(response.Workflows, workflowDocument{
			WorkflowID: id, Draft: draft, SavedDraft: true, References: []workflowReference{},
		})
	}
	sort.Slice(response.Workflows, func(i, j int) bool { return response.Workflows[i].WorkflowID < response.Workflows[j].WorkflowID })
	return response
}

func (s *appServer) workflowReferences(id string, configuration settings.Snapshot) []workflowReference {
	references := []workflowReference{}
	if configuration.ProtectedWorkflows.LinearFeedback.WorkflowID == id {
		references = append(references, workflowReference{Kind: "protected", ID: "linear-feedback", Name: "Linear feedback continuation", Enabled: true})
	}
	if s.triggerPolicy != nil {
		for _, rule := range s.triggerPolicy.RegistrySnapshot().Rules {
			if rule.WorkflowID == id {
				references = append(references, workflowReference{Kind: "rule", ID: rule.ID, Name: rule.Name, Enabled: rule.Enabled})
			}
		}
	}
	return references
}

func (s *appServer) postWorkflowDraft(w http.ResponseWriter, r *http.Request) {
	if !s.workflowMutationReady(w, r) {
		return
	}
	configuration := s.settings.Snapshot()
	ids := make(map[string]bool, len(configuration.Workflows)+len(s.workflowDrafts.Snapshot().Drafts))
	for _, definition := range configuration.Workflows {
		ids[definition.ID] = true
	}
	for _, draft := range s.workflowDrafts.Snapshot().Drafts {
		ids[draft.WorkflowID] = true
	}
	if len(ids) >= workflow.MaxDefinitions {
		http.Error(w, "workflow limit reached", http.StatusBadRequest)
		return
	}
	for attempt := 0; attempt < 16; attempt++ {
		id, err := newWorkflowID()
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if _, found := configuration.Workflow(id); found {
			continue
		}
		now := s.now().UTC()
		draft := workflow.Draft{
			WorkflowID: id, Revision: 1, Name: "Untitled workflow", Enabled: false,
			Markdown: "# Untitled workflow\n\nDescribe the procedure for this workflow.\n", UpdatedAt: now,
		}
		created, err := s.workflowDrafts.Create(draft)
		if errors.Is(err, workflow.ErrDraftConflict) {
			continue
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, created)
		return
	}
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func (s *appServer) putWorkflowDraft(w http.ResponseWriter, r *http.Request) {
	if !s.workflowMutationReady(w, r) {
		return
	}
	id := r.PathValue("id")
	if !workflow.ValidID(id) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	var request saveDraftRequest
	if !decodeWorkflowJSON(w, r, &request) {
		return
	}
	unlock := s.workflowLocks.lock(id)
	defer unlock()
	now := s.now().UTC()
	candidate := workflow.Draft{
		WorkflowID: id, Revision: request.ExpectedDraftRevision,
		BaseWorkflowRevision: request.ExpectedWorkflowRevision,
		Name:                 request.Name, Enabled: request.Enabled, Markdown: workflow.CanonicalizeMarkdown(request.Markdown), UpdatedAt: now,
	}
	var saved workflow.Draft
	var err error
	if request.ExpectedDraftRevision == 0 {
		definition, found := s.settings.Snapshot().Workflow(id)
		if !found || definition.Revision != request.ExpectedWorkflowRevision {
			s.writeWorkflowConflict(w)
			return
		}
		candidate.Revision = 1
		saved, err = s.workflowDrafts.Materialize(candidate, request.ExpectedWorkflowRevision)
	} else {
		saved, err = s.workflowDrafts.Save(id, request.ExpectedDraftRevision, request.ExpectedWorkflowRevision, candidate, now)
	}
	if errors.Is(err, workflow.ErrDraftConflict) {
		s.writeWorkflowConflict(w)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *appServer) deleteWorkflowDraft(w http.ResponseWriter, r *http.Request) {
	if !s.workflowMutationReady(w, r) {
		return
	}
	id := r.PathValue("id")
	var request discardDraftRequest
	if !workflow.ValidID(id) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if !decodeWorkflowJSON(w, r, &request) {
		return
	}
	unlock := s.workflowLocks.lock(id)
	defer unlock()
	if err := s.workflowDrafts.Discard(id, request.ExpectedDraftRevision, request.ExpectedWorkflowRevision); errors.Is(err, workflow.ErrDraftConflict) {
		s.writeWorkflowConflict(w)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *appServer) publishWorkflowDraft(w http.ResponseWriter, r *http.Request) {
	if !s.workflowMutationReady(w, r) {
		return
	}
	if s.triggerPolicy == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	var request publishDraftRequest
	if !workflow.ValidID(id) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if !decodeWorkflowJSON(w, r, &request) {
		return
	}
	unlock := s.workflowLocks.lock(id)
	defer unlock()
	draft, found := s.workflowDrafts.Draft(id)
	if !found || draft.Revision != request.ExpectedDraftRevision || draft.BaseWorkflowRevision != request.ExpectedWorkflowRevision {
		s.writeWorkflowConflict(w)
		return
	}
	now := s.now().UTC()
	current := s.settings.Snapshot()
	if published, found := current.Workflow(id); found && published.Revision == draft.BaseWorkflowRevision+1 && workflow.PublishedEqual(published, draft.Definition(published.Revision, published.UpdatedAt)) {
		advanced, err := s.workflowDrafts.AdvanceBase(id, draft.Revision, draft.BaseWorkflowRevision, published.Revision, now)
		if err != nil {
			s.writeWorkflowConflict(w)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Policy settings.Snapshot `json:"policy"`
			Draft  workflow.Draft    `json:"draft"`
		}{current, advanced})
		return
	}
	updated, err := s.triggerPolicy.PublishWorkflow(request.ExpectedPolicyRevision, request.ExpectedWorkflowRevision, draft.Definition(request.ExpectedWorkflowRevision, now), now)
	if !s.handleWorkflowPolicyError(w, err) {
		return
	}
	published, _ := updated.Workflow(id)
	advanced, err := s.workflowDrafts.AdvanceBase(id, draft.Revision, draft.BaseWorkflowRevision, published.Revision, now)
	if err != nil {
		http.Error(w, "workflow published but draft reconciliation is required", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Policy settings.Snapshot `json:"policy"`
		Draft  workflow.Draft    `json:"draft"`
	}{updated, advanced})
}

func (s *appServer) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	if !s.requireReady(w) {
		return
	}
	if !sameOrigin(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if s.triggerPolicy == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	var request deleteWorkflowRequest
	if !workflow.ValidID(id) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if !decodeWorkflowJSON(w, r, &request) {
		return
	}
	unlock := s.workflowLocks.lock(id)
	defer unlock()
	updated, err := s.triggerPolicy.DeleteWorkflow(request.ExpectedPolicyRevision, request.ExpectedWorkflowRevision, id, s.now())
	if !s.handleWorkflowPolicyError(w, err) {
		return
	}
	if s.workflowDrafts != nil {
		if draft, found := s.workflowDrafts.Draft(id); found {
			_ = s.workflowDrafts.Discard(id, draft.Revision, draft.BaseWorkflowRevision)
		}
	}
	writeJSON(w, http.StatusOK, publicSettings(updated))
}

func (s *appServer) workflowMutationReady(w http.ResponseWriter, r *http.Request) bool {
	if !s.requireReady(w) {
		return false
	}
	if !sameOrigin(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	if s.workflowDrafts == nil {
		http.Error(w, "workflow authoring is unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func decodeWorkflowJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, workflow.MaxAuthoringBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return false
	}
	return true
}

func (s *appServer) writeWorkflowConflict(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, s.workflowsResponse())
}

func (s *appServer) handleWorkflowPolicyError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, triggerrouter.ErrPolicyConflict), errors.Is(err, workflow.ErrDraftConflict):
		s.writeWorkflowConflict(w)
	case errors.Is(err, triggerrouter.ErrPolicyPending):
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
	case errors.Is(err, triggerrouter.ErrPolicyValidation):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
	return false
}

func newWorkflowID() (string, error) {
	var value [6]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("workflow ID: %w", err)
	}
	return "workflow-" + hex.EncodeToString(value[:]), nil
}
