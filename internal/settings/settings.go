package settings

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	SchemaVersion        = 1
	DefaultWorkflowID    = "full-sdlc"
	maxWorkflows         = 8
	maxWorkflowSteps     = 20
	maxWorkflowNameBytes = 80
	maxWorkflowStepBytes = 240
	maxLabelNameBytes    = 64
	minPrincipalAttempts = 1
	maxPrincipalAttempts = 5
	minConcurrentRuns    = 1
	maxConcurrentRuns    = 10
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	modelPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)
)

type Snapshot struct {
	Schema    int             `json:"schema"`
	Revision  uint64          `json:"revision"`
	UpdatedAt time.Time       `json:"updatedAt,omitempty"`
	Triggers  Triggers        `json:"triggers"`
	Workflows []Workflow      `json:"workflows"`
	Agents    AgentSettings   `json:"agents"`
	Runtime   RuntimeSettings `json:"runtime"`
}

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

type Workflow struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Runner  string   `json:"runner"`
	Steps   []string `json:"steps"`
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
	return Snapshot{
		Schema: SchemaVersion,
		Triggers: Triggers{
			LinearLabel: LinearLabelTrigger{
				Enabled:    true,
				Label:      "Factory",
				WorkflowID: DefaultWorkflowID,
			},
			LinearComment: Trigger{Enabled: true, WorkflowID: DefaultWorkflowID},
		},
		Workflows: []Workflow{{
			ID:      DefaultWorkflowID,
			Name:    "Full SDLC",
			Enabled: true,
			Runner:  "do",
			Steps: []string{
				"Research the issue and repository evidence",
				"Publish research and cross the Linear research gate",
				"Create and adversarially review the implementation plan",
				"Publish the plan and cross the Linear plan gate",
				"Implement and verify the approved plan",
				"Remediate review and CI, then checkpoint the exact verified head",
				"After human merge, deploy from updated main and clean up",
			},
		}},
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
	clone.Workflows = make([]Workflow, len(s.Workflows))
	for index, workflow := range s.Workflows {
		clone.Workflows[index] = workflow
		clone.Workflows[index].Steps = append([]string(nil), workflow.Steps...)
	}
	return clone
}

func (s Snapshot) Workflow(id string) (Workflow, bool) {
	for _, workflow := range s.Workflows {
		if workflow.ID == id {
			workflow.Steps = append([]string(nil), workflow.Steps...)
			return workflow, true
		}
	}
	return Workflow{}, false
}

func (s Snapshot) WorkflowForTrigger(kind string) (Workflow, error) {
	id := s.Triggers.LinearLabel.WorkflowID
	if kind == "linear-comment" {
		id = s.Triggers.LinearComment.WorkflowID
	}
	workflow, found := s.Workflow(id)
	if !found || !workflow.Enabled {
		return Workflow{}, fmt.Errorf("settings: trigger workflow %q is unavailable", id)
	}
	return workflow, nil
}

func (s Snapshot) Validate() error {
	if s.Schema != SchemaVersion {
		return fmt.Errorf("settings: schema is %d, want %d", s.Schema, SchemaVersion)
	}
	if !validText(s.Triggers.LinearLabel.Label, maxLabelNameBytes) {
		return errors.New("settings: Linear label must be trimmed printable text up to 64 bytes")
	}
	if len(s.Workflows) == 0 || len(s.Workflows) > maxWorkflows {
		return fmt.Errorf("settings: workflow count must be between 1 and %d", maxWorkflows)
	}

	workflows := make(map[string]Workflow, len(s.Workflows))
	for index, workflow := range s.Workflows {
		if err := workflow.Validate(); err != nil {
			return fmt.Errorf("settings: workflow %d: %w", index+1, err)
		}
		if _, duplicate := workflows[workflow.ID]; duplicate {
			return fmt.Errorf("settings: workflow ID %q is duplicated", workflow.ID)
		}
		workflows[workflow.ID] = workflow
	}

	for name, id := range map[string]string{
		"Linear label":   s.Triggers.LinearLabel.WorkflowID,
		"Linear comment": s.Triggers.LinearComment.WorkflowID,
	} {
		workflow, found := workflows[id]
		if !found || !workflow.Enabled {
			return fmt.Errorf("settings: %s trigger must reference an enabled workflow", name)
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

func (w Workflow) Validate() error {
	if !identifierPattern.MatchString(w.ID) {
		return errors.New("workflow ID is invalid")
	}
	if !validText(w.Name, maxWorkflowNameBytes) {
		return fmt.Errorf("workflow %q has an invalid name", w.ID)
	}
	if w.Runner != "do" {
		return fmt.Errorf("workflow %q runner must be do", w.ID)
	}
	if len(w.Steps) == 0 || len(w.Steps) > maxWorkflowSteps {
		return fmt.Errorf("workflow %q must have between 1 and %d steps", w.ID, maxWorkflowSteps)
	}
	for index, step := range w.Steps {
		if !validText(step, maxWorkflowStepBytes) {
			return fmt.Errorf("workflow %q step %d is invalid", w.ID, index+1)
		}
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
