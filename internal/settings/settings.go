package settings

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/tomnagengast/factory/internal/workflow"
)

const (
	SchemaVersion        = 2
	DefaultWorkflowID    = workflow.DefaultID
	maxLabelNameBytes    = 64
	minPrincipalAttempts = 1
	maxPrincipalAttempts = 5
	minConcurrentRuns    = 1
	maxConcurrentRuns    = 10
)

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)

type Snapshot struct {
	Schema                       int                       `json:"schema"`
	Revision                     uint64                    `json:"revision"`
	UpdatedAt                    time.Time                 `json:"updatedAt,omitempty"`
	WorkflowRollbackIncompatible bool                      `json:"workflowRollbackIncompatible,omitempty"`
	Triggers                     Triggers                  `json:"triggers"`
	ProtectedWorkflows           ProtectedWorkflowBindings `json:"protectedWorkflows"`
	Workflows                    []workflow.Definition     `json:"workflows"`
	Agents                       AgentSettings             `json:"agents"`
	Runtime                      RuntimeSettings           `json:"runtime"`
}

type ProtectedWorkflowBindings struct {
	LinearFeedback WorkflowBinding `json:"linearFeedback"`
}

type WorkflowBinding struct {
	WorkflowID string `json:"workflowId"`
}

// Triggers is retained only for schema-1 compatibility and legacy Run fallback.
// New trigger admission uses the registry; protected feedback uses ProtectedWorkflows.
type Triggers struct {
	LinearLabel   LinearLabelTrigger `json:"linearLabel"`
	LinearComment Trigger            `json:"linearComment"`
}

type LinearLabelTrigger struct {
	Enabled    bool   `json:"enabled"`
	Label      string `json:"label"`
	WorkflowID string `json:"workflowId"`
}

type Trigger struct {
	Enabled    bool   `json:"enabled"`
	WorkflowID string `json:"workflowId"`
}

type AgentSettings struct {
	Principal   PrincipalSettings `json:"principal"`
	CodexChild  ProviderSettings  `json:"codexChild"`
	ClaudeChild ProviderSettings  `json:"claudeChild"`
}

type PrincipalSettings struct {
	ProviderSettings
	MaxAttempts int `json:"maxAttempts"`
}

type ProviderSettings struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type RuntimeSettings struct {
	MaxConcurrentRuns int `json:"maxConcurrentRuns"`
}

func Defaults(maxConcurrent int) Snapshot {
	if maxConcurrent < minConcurrentRuns || maxConcurrent > maxConcurrentRuns {
		maxConcurrent = 3
	}
	definition := workflow.Default(time.Time{})
	providerNeutral := workflow.ProviderNeutralDefault(time.Time{})
	return Snapshot{
		Schema: SchemaVersion,
		Triggers: Triggers{
			LinearLabel:   LinearLabelTrigger{Enabled: true, Label: "Factory", WorkflowID: DefaultWorkflowID},
			LinearComment: Trigger{Enabled: true, WorkflowID: DefaultWorkflowID},
		},
		ProtectedWorkflows: ProtectedWorkflowBindings{
			LinearFeedback: WorkflowBinding{WorkflowID: DefaultWorkflowID},
		},
		Workflows: []workflow.Definition{definition, providerNeutral},
		Agents: AgentSettings{
			Principal: PrincipalSettings{
				ProviderSettings: ProviderSettings{Model: "gpt-5.6-sol", Effort: "high"},
				MaxAttempts:      3,
			},
			CodexChild:  ProviderSettings{Model: "gpt-5.6-sol", Effort: "high"},
			ClaudeChild: ProviderSettings{Model: "fable", Effort: "high"},
		},
		Runtime: RuntimeSettings{MaxConcurrentRuns: maxConcurrent},
	}
}

func (s Snapshot) Clone() Snapshot {
	clone := s
	clone.Workflows = append([]workflow.Definition(nil), s.Workflows...)
	return clone
}

func (s Snapshot) Workflow(id string) (workflow.Definition, bool) {
	for _, definition := range s.Workflows {
		if definition.ID == id {
			return definition.Clone(), true
		}
	}
	return workflow.Definition{}, false
}

func (s Snapshot) WorkflowForTrigger(kind string) (workflow.Definition, error) {
	id := s.Triggers.LinearLabel.WorkflowID
	if kind == "linear-comment" {
		id = s.ProtectedWorkflows.LinearFeedback.WorkflowID
	}
	definition, found := s.Workflow(id)
	if !found || !definition.Enabled {
		return workflow.Definition{}, fmt.Errorf("settings: trigger workflow %q is unavailable", id)
	}
	return definition, nil
}

func (s Snapshot) Validate() error {
	if s.Schema != SchemaVersion {
		return fmt.Errorf("settings: schema is %d, want %d", s.Schema, SchemaVersion)
	}
	if !validText(s.Triggers.LinearLabel.Label, maxLabelNameBytes) {
		return errors.New("settings: Linear label must be trimmed printable text up to 64 bytes")
	}
	if err := workflow.ValidateDefinitions(s.Workflows); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	bindingID := s.ProtectedWorkflows.LinearFeedback.WorkflowID
	definition, found := s.Workflow(bindingID)
	if !workflow.ValidID(bindingID) || !found || !definition.Enabled {
		return errors.New("settings: protected Linear feedback must reference an enabled workflow")
	}
	for name, id := range map[string]string{
		"legacy Linear label":   s.Triggers.LinearLabel.WorkflowID,
		"legacy Linear comment": s.Triggers.LinearComment.WorkflowID,
	} {
		if id != "" && !workflow.ValidID(id) {
			return fmt.Errorf("settings: %s workflow ID is invalid", name)
		}
	}
	if err := validateProvider("principal", s.Agents.Principal.ProviderSettings, codexEffort); err != nil {
		return err
	}
	if s.Agents.Principal.MaxAttempts < minPrincipalAttempts || s.Agents.Principal.MaxAttempts > maxPrincipalAttempts {
		return fmt.Errorf("settings: principal max attempts must be between %d and %d", minPrincipalAttempts, maxPrincipalAttempts)
	}
	if err := validateProvider("Codex child", s.Agents.CodexChild, codexEffort); err != nil {
		return err
	}
	if err := validateProvider("Claude child", s.Agents.ClaudeChild, claudeEffort); err != nil {
		return err
	}
	if s.Runtime.MaxConcurrentRuns < minConcurrentRuns || s.Runtime.MaxConcurrentRuns > maxConcurrentRuns {
		return fmt.Errorf("settings: max concurrent runs must be between %d and %d", minConcurrentRuns, maxConcurrentRuns)
	}
	return nil
}

func validateProvider(name string, provider ProviderSettings, effort func(string) bool) error {
	if !modelPattern.MatchString(provider.Model) {
		return fmt.Errorf("settings: %s model is invalid", name)
	}
	if !effort(provider.Effort) {
		return fmt.Errorf("settings: %s effort %q is unsupported", name, provider.Effort)
	}
	return nil
}

func codexEffort(value string) bool {
	return value == "low" || value == "medium" || value == "high" || value == "xhigh"
}

func claudeEffort(value string) bool {
	return value == "low" || value == "medium" || value == "high" || value == "max"
}

func validText(value string, maximum int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
