package policy

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

var ErrReservedWorkflowConflict = errors.New("policy: customized reserved workflow conflicts with compiled policy")

// Sources are already-decoded legacy policy owners. A nil Registry means the
// optional triggers.json source is absent and must be synthesized exactly as
// the current runtime does. Draft workflows and schedule cursors are separate
// operational owners and deliberately have no field here.
type Sources struct {
	Settings       settings.Snapshot
	Registry       *triggerregistry.Snapshot
	TaskControl    taskcontrol.Snapshot
	TriggerActorID string
}

// ConvertSources builds generation 1 without writing or mutating any source.
// It consolidates only exact compiled defaults; customized policy remains
// visible, and ambiguous uses of reserved policy fail closed.
func ConvertSources(sources Sources) (Snapshot, error) {
	if err := sources.Settings.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("policy: validate source settings: %w", err)
	}
	registry := triggerregistry.Snapshot{}
	if sources.Registry == nil {
		if sources.TriggerActorID == "" {
			return Snapshot{}, errors.New("policy: trigger actor is required when registry is implicit")
		}
		registry = triggerregistry.Defaults(sources.Settings, sources.TriggerActorID)
	} else {
		registry = sources.Registry.Clone()
	}
	if err := registry.Validate(sources.Settings); err != nil {
		return Snapshot{}, fmt.Errorf("policy: validate source registry: %w", err)
	}
	if sources.TaskControl.Version != 1 {
		return Snapshot{}, fmt.Errorf("policy: unsupported source task-control version %d", sources.TaskControl.Version)
	}

	workflows := make([]Workflow, 0, len(sources.Settings.Workflows))
	hasProviderNeutral := false
	for _, definition := range sources.Settings.Workflows {
		converted := workflowFromSource(definition)
		switch definition.ID {
		case workflow.DefaultID:
			if kind, recognized := RecognizeCompiledWorkflow(converted); !recognized || kind != CompiledFullSDLC {
				return Snapshot{}, fmt.Errorf("%w: %s", ErrReservedWorkflowConflict, definition.ID)
			}
			continue
		case workflow.ProviderNeutralID:
			if kind, recognized := RecognizeCompiledWorkflow(converted); !recognized || kind != CompiledProviderNeutral {
				return Snapshot{}, fmt.Errorf("%w: %s", ErrReservedWorkflowConflict, definition.ID)
			}
			hasProviderNeutral = true
		}
		workflows = append(workflows, converted)
	}
	if !hasProviderNeutral {
		workflows = append(workflows, workflowFromSource(workflow.ProviderNeutralDefault(time.Time{})))
	}

	rules := make([]Rule, 0, len(registry.Rules))
	for _, rule := range registry.Rules {
		converted := ruleFromSource(rule)
		triggerActor := converted.Filter.Attributes[triggerregistry.AttributeActorID]
		if kind, recognized := RecognizeCompiledRule(converted, sources.Settings, triggerActor); recognized {
			switch kind {
			case CompiledLinearComment:
				continue
			case CompiledLinearLabel:
				converted.WorkflowID = workflow.ProviderNeutralID
			}
		} else if converted.WorkflowID == workflow.DefaultID {
			return Snapshot{}, fmt.Errorf("%w: rule %s references %s", ErrReservedWorkflowConflict, converted.ID, workflow.DefaultID)
		}
		rules = append(rules, converted)
	}
	schedules := make([]Schedule, len(registry.Schedules))
	for index, schedule := range registry.Schedules {
		schedules[index] = scheduleFromSource(schedule)
	}

	return NewSnapshot(Model{
		Schema:     SchemaVersion,
		Generation: 1,
		Settings: Settings{
			Revision: sources.Settings.Revision, UpdatedAt: sources.Settings.UpdatedAt,
			Agents:  agentSettingsFromSource(sources.Settings.Agents),
			Runtime: RuntimeSettings{MaxConcurrentRuns: sources.Settings.Runtime.MaxConcurrentRuns},
		},
		ProtectedWorkflows: ProtectedWorkflowBindings{LinearFeedback: WorkflowBinding{
			WorkflowID: workflow.ProviderNeutralID,
		}},
		Workflows: workflows,
		Registry: Registry{
			Revision: registry.Revision, UpdatedAt: registry.UpdatedAt,
			Rules: rules, Schedules: schedules,
		},
		TaskControl: TaskControl{
			Revision: sources.TaskControl.Revision, UpdatedAt: sources.TaskControl.UpdatedAt,
			EnabledProjectIDs: slices.Clone(sources.TaskControl.EnabledProjectIDs),
		},
	})
}

func workflowFromSource(definition workflow.Definition) Workflow {
	return Workflow{
		ID: definition.ID, Revision: definition.Revision, Name: definition.Name,
		Enabled: definition.Enabled, Markdown: definition.Markdown, UpdatedAt: definition.UpdatedAt,
	}
}

func ruleFromSource(rule triggerregistry.Rule) Rule {
	return Rule{
		ID: rule.ID, Revision: rule.Revision, Name: rule.Name, Enabled: rule.Enabled,
		Filter: Filter{
			Source: rule.Filter.Source, Type: rule.Filter.Type, Action: rule.Filter.Action,
			Subject: cloneString(rule.Filter.Subject), Attributes: cloneStringMap(rule.Filter.Attributes),
		},
		WorkflowID: rule.WorkflowID,
		Target:     TargetPolicy{Provider: rule.Target.Provider, Kind: rule.Target.Kind, Value: rule.Target.Value},
		MaxHop:     rule.MaxHop, MaxOutstanding: rule.MaxOutstanding, AdmissionsHour: rule.AdmissionsHour,
	}
}

func scheduleFromSource(schedule triggerregistry.Schedule) Schedule {
	attributes := make(map[string][]string, len(schedule.Attributes))
	for key, values := range schedule.Attributes {
		attributes[key] = slices.Clone(values)
	}
	return Schedule{
		ID: schedule.ID, Revision: schedule.Revision, Name: schedule.Name, Enabled: schedule.Enabled,
		Cron: schedule.Cron, Timezone: schedule.Timezone, Subject: schedule.Subject, Attributes: attributes,
	}
}

func agentSettingsFromSource(value settings.AgentSettings) AgentSettings {
	return AgentSettings{
		Principal: PrincipalSettings{
			ProviderSettings: ProviderSettings{Model: value.Principal.Model, Effort: value.Principal.Effort},
			MaxAttempts:      value.Principal.MaxAttempts,
		},
		CodexChild:  ProviderSettings{Model: value.CodexChild.Model, Effort: value.CodexChild.Effort},
		ClaudeChild: ProviderSettings{Model: value.ClaudeChild.Model, Effort: value.ClaudeChild.Effort},
	}
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneStringMap(values map[string]string) map[string]string {
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
