package triggerrouter

import (
	"slices"
	"sort"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

const (
	SchemaVersion        = 1
	GlobalOutstandingMax = 100
	OutcomeInvocation    = "invocation"
	OutcomeRejected      = "rejected"
	OutcomeSuppressed    = "suppressed"
	StateQueued          = "queued"
	StateClaiming        = "claiming"
	StateClaimed         = "claimed"
	StateSucceeded       = "succeeded"
	StateBlocked         = "blocked"
	StateFailed          = "failed"
	StateRejected        = "rejected"
)

type Decision struct {
	EventID          string           `json:"eventId"`
	EventSequence    uint64           `json:"eventSequence"`
	Source           eventwire.Source `json:"source"`
	RegistryRevision uint64           `json:"registryRevision"`
	SettingsRevision uint64           `json:"settingsRevision"`
	DecidedAt        time.Time        `json:"decidedAt"`
	Outcomes         []Outcome        `json:"outcomes"`
}

type Outcome struct {
	Kind         string `json:"kind"`
	RuleID       string `json:"ruleId"`
	RuleRevision uint64 `json:"ruleRevision"`
	InvocationID string `json:"invocationId,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type Invocation struct {
	ID                 string               `json:"id"`
	EventID            string               `json:"eventId"`
	EventSequence      uint64               `json:"eventSequence"`
	Rule               triggerregistry.Rule `json:"rule"`
	Workflow           workflow.Pinned      `json:"workflow"`
	WorkflowDigest     string               `json:"workflowDigest"`
	PolicyRevision     uint64               `json:"policyRevision"`
	IssueIdentifier    string               `json:"issueIdentifier"`
	RootEventID        string               `json:"rootEventId"`
	ParentInvocationID string               `json:"parentInvocationId,omitempty"`
	ParentRunID        string               `json:"parentRunId,omitempty"`
	Hop                int                  `json:"hop"`
	AncestorRuleIDs    []string             `json:"ancestorRuleIds"`
	State              string               `json:"state"`
	RunID              string               `json:"runId,omitempty"`
	Reason             string               `json:"reason,omitempty"`
	AdmittedAt         time.Time            `json:"admittedAt"`
	UpdatedAt          time.Time            `json:"updatedAt"`
	ReflectedAt        *time.Time           `json:"reflectedAt,omitempty"`
}

type RateBucket struct {
	RuleID string    `json:"ruleId"`
	Minute time.Time `json:"minute"`
	Count  int       `json:"count"`
}

type Snapshot struct {
	Schema      int          `json:"schema"`
	Decisions   []Decision   `json:"decisions"`
	Invocations []Invocation `json:"invocations"`
	RateBuckets []RateBucket `json:"rateBuckets"`
}

func (d Decision) Clone() Decision {
	clone := d
	clone.Outcomes = slices.Clone(d.Outcomes)
	return clone
}

func (i Invocation) Clone() Invocation {
	clone := i
	clone.Rule = i.Rule.Clone()
	clone.Workflow = i.Workflow.Clone()
	clone.AncestorRuleIDs = slices.Clone(i.AncestorRuleIDs)
	if i.ReflectedAt != nil {
		value := *i.ReflectedAt
		clone.ReflectedAt = &value
	}
	return clone
}

func (i Invocation) Nonterminal() bool {
	return i.State == StateQueued || i.State == StateClaiming || i.State == StateClaimed
}

func (s Snapshot) Clone() Snapshot {
	clone := s
	clone.Decisions = make([]Decision, len(s.Decisions))
	for index, decision := range s.Decisions {
		clone.Decisions[index] = decision.Clone()
	}
	clone.Invocations = make([]Invocation, len(s.Invocations))
	for index, invocation := range s.Invocations {
		clone.Invocations[index] = invocation.Clone()
	}
	clone.RateBuckets = slices.Clone(s.RateBuckets)
	return clone
}

func sortSnapshot(snapshot *Snapshot) {
	sort.Slice(snapshot.Decisions, func(i, j int) bool {
		return snapshot.Decisions[i].EventSequence < snapshot.Decisions[j].EventSequence
	})
	sort.Slice(snapshot.Invocations, func(i, j int) bool {
		left, right := snapshot.Invocations[i], snapshot.Invocations[j]
		if left.EventSequence != right.EventSequence {
			return left.EventSequence < right.EventSequence
		}
		if left.Rule.ID != right.Rule.ID {
			return left.Rule.ID < right.Rule.ID
		}
		return left.ID < right.ID
	})
	sort.Slice(snapshot.RateBuckets, func(i, j int) bool {
		if snapshot.RateBuckets[i].RuleID != snapshot.RateBuckets[j].RuleID {
			return snapshot.RateBuckets[i].RuleID < snapshot.RateBuckets[j].RuleID
		}
		return snapshot.RateBuckets[i].Minute.Before(snapshot.RateBuckets[j].Minute)
	})
}
