package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/triggerscheduler"
	"github.com/tomnagengast/factory/internal/workflow"
)

const maxTriggersBody = 1 << 20

type triggerResponse struct {
	Registry         triggerregistry.Snapshot  `json:"registry"`
	SettingsRevision uint64                    `json:"settingsRevision"`
	Workflows        []workflow.Summary        `json:"workflows"`
	ObservedSources  []string                  `json:"observedSources"`
	RuleStatus       []ruleStatus              `json:"ruleStatus"`
	Schedules        []triggerscheduler.Status `json:"scheduleStatus"`
	Recent           []invocationSummary       `json:"recentInvocations"`
	ProtectedRoutes  []protectedRoute          `json:"protectedRoutes"`
}

type ruleStatus struct {
	RuleID      string `json:"ruleId"`
	Outstanding int    `json:"outstanding"`
	LastHour    int    `json:"admissionsLastHour"`
}

type invocationSummary struct {
	ID              string    `json:"id"`
	EventID         string    `json:"eventId"`
	RuleID          string    `json:"ruleId"`
	RuleRevision    uint64    `json:"ruleRevision"`
	WorkflowID      string    `json:"workflowId"`
	IssueIdentifier string    `json:"issueIdentifier"`
	State           string    `json:"state"`
	RunID           string    `json:"runId,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type protectedRoute struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	WorkflowID  string `json:"workflowId,omitempty"`
	Enabled     bool   `json:"enabled"`
	Protected   bool   `json:"protected"`
}

func (s *appServer) getTriggers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.triggerResponse())
}

func (s *appServer) putTriggers(w http.ResponseWriter, r *http.Request) {
	if !s.requireReady(w) {
		return
	}
	if !sameOrigin(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTriggersBody))
	decoder.DisallowUnknownFields()
	var candidate triggerregistry.Snapshot
	if err := decoder.Decode(&candidate); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	configuration := s.triggerPolicy.SettingsSnapshot()
	for _, rule := range candidate.Rules {
		if rule.Target.Kind != triggerregistry.TargetFixedIssue {
			continue
		}
		if _, err := s.repositoryResolver.Resolve(r.Context(), rule.Target.Value); err != nil {
			http.Error(w, "fixed target is not routable", http.StatusBadRequest)
			return
		}
	}
	_, err = s.triggerPolicy.UpdateRegistry(candidate.Revision, configuration.Revision, candidate, s.now())
	if errors.Is(err, triggerregistry.ErrRevisionConflict) || errors.Is(err, triggerrouter.ErrPolicyConflict) {
		writeJSON(w, http.StatusConflict, s.triggerResponse())
		return
	}
	if errors.Is(err, triggerrouter.ErrPolicyPending) {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		slog.Error("update trigger registry", "error", err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.triggerResponse())
}

func (s *appServer) triggerResponse() triggerResponse {
	registry := s.triggerPolicy.RegistrySnapshot()
	configuration := s.triggerPolicy.SettingsSnapshot()
	routing := s.triggerPolicy.RoutingSnapshot()
	response := triggerResponse{
		Registry: registry, SettingsRevision: configuration.Revision,
		Workflows:       enabledWorkflowSummaries(configuration.Workflows),
		ObservedSources: []string{},
		RuleStatus:      []ruleStatus{},
		Schedules:       slices.Clone(s.scheduleStatus.Statuses(s.now())),
		Recent:          []invocationSummary{},
		ProtectedRoutes: []protectedRoute{
			{ID: "linear-feedback", Name: "Linear feedback continuation", Description: "Resumes the protected lifecycle after human feedback.", WorkflowID: configuration.ProtectedWorkflows.LinearFeedback.WorkflowID, Enabled: true, Protected: true},
			{ID: "github-remediation", Name: "GitHub remediation", Description: "Resumes the protected lifecycle for pull request and check changes.", Enabled: true, Protected: true},
			{ID: "post-merge", Name: "Post-merge completion", Description: "Preserves verified-head deployment and cleanup after human merge.", Enabled: true, Protected: true},
		},
	}
	if response.Workflows == nil {
		response.Workflows = []workflow.Summary{}
	}
	if response.Schedules == nil {
		response.Schedules = []triggerscheduler.Status{}
	}
	if response.Registry.Rules == nil {
		response.Registry.Rules = []triggerregistry.Rule{}
	}
	if response.Registry.Schedules == nil {
		response.Registry.Schedules = []triggerregistry.Schedule{}
	}
	sources := make(map[string]bool)
	for _, decision := range routing.Decisions {
		sources[string(decision.Source)] = true
	}
	for source := range sources {
		response.ObservedSources = append(response.ObservedSources, source)
	}
	sort.Strings(response.ObservedSources)
	cutoff := s.now().UTC().Add(-time.Hour).Truncate(time.Minute)
	statusByRule := make(map[string]int)
	for _, rule := range registry.Rules {
		response.RuleStatus = append(response.RuleStatus, ruleStatus{RuleID: rule.ID})
		statusByRule[rule.ID] = len(response.RuleStatus) - 1
	}
	for _, invocation := range routing.Invocations {
		if invocation.Nonterminal() {
			if index, found := statusByRule[invocation.Rule.ID]; found {
				response.RuleStatus[index].Outstanding++
			}
		}
	}
	for _, bucket := range routing.RateBuckets {
		if !bucket.Minute.Before(cutoff) {
			if index, found := statusByRule[bucket.RuleID]; found {
				response.RuleStatus[index].LastHour += bucket.Count
			}
		}
	}
	invocations := make(map[string]triggerrouter.Invocation, len(routing.Invocations))
	for _, invocation := range routing.Invocations {
		invocations[invocation.ID] = invocation
	}
	for decisionIndex := len(routing.Decisions) - 1; decisionIndex >= 0 && len(response.Recent) < 50; decisionIndex-- {
		decision := routing.Decisions[decisionIndex]
		for outcomeIndex := len(decision.Outcomes) - 1; outcomeIndex >= 0 && len(response.Recent) < 50; outcomeIndex-- {
			outcome := decision.Outcomes[outcomeIndex]
			if outcome.Kind == triggerrouter.OutcomeInvocation {
				invocation, found := invocations[outcome.InvocationID]
				if !found {
					continue
				}
				response.Recent = append(response.Recent, invocationSummary{
					ID: invocation.ID, EventID: invocation.EventID, RuleID: invocation.Rule.ID,
					RuleRevision: invocation.Rule.Revision, WorkflowID: invocation.Workflow.ID,
					IssueIdentifier: invocation.IssueIdentifier, State: invocation.State,
					RunID: invocation.RunID, Reason: visibleInvocationReason(invocation), UpdatedAt: invocation.UpdatedAt,
				})
				continue
			}
			response.Recent = append(response.Recent, invocationSummary{
				ID: decision.EventID + ":" + outcome.RuleID, EventID: decision.EventID,
				RuleID: outcome.RuleID, RuleRevision: outcome.RuleRevision,
				State: outcome.Kind, Reason: outcome.Reason, UpdatedAt: decision.DecidedAt,
			})
		}
	}
	return response
}

func enabledWorkflowSummaries(definitions []workflow.Definition) []workflow.Summary {
	summaries := make([]workflow.Summary, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Enabled {
			summaries = append(summaries, definition.Summary())
		}
	}
	return summaries
}

func visibleInvocationReason(invocation triggerrouter.Invocation) string {
	if invocation.State == triggerrouter.StateRejected {
		return invocation.Reason
	}
	return ""
}
