package policy

import (
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/workflow"
)

// SettingsView projects canonical policy into the retained settings contract.
// The legacy trigger fields are compatibility-only and are reconstructed from
// the canonical label rule and protected feedback binding.
func SettingsView(snapshot Snapshot) settings.Snapshot {
	policySettings := snapshot.Settings()
	protected := snapshot.ProtectedWorkflows()
	label := settings.LinearLabelTrigger{
		Label:      "Factory",
		WorkflowID: workflow.ProviderNeutralID,
	}
	for _, rule := range snapshot.Registry().Rules {
		if rule.ID != string(CompiledLinearLabel) {
			continue
		}
		label.Enabled = rule.Enabled
		label.WorkflowID = rule.WorkflowID
		if value := rule.Filter.Attributes[triggerregistry.AttributeAddedLabel]; value != "" {
			label.Label = value
		}
		break
	}
	workflows := make([]workflow.Definition, 0, len(snapshot.Workflows()))
	for _, definition := range snapshot.Workflows() {
		workflows = append(workflows, workflow.Definition{
			ID: definition.ID, Revision: definition.Revision, Name: definition.Name,
			Enabled: definition.Enabled, Markdown: definition.Markdown, UpdatedAt: definition.UpdatedAt,
		})
	}
	return settings.Snapshot{
		Schema:    settings.SchemaVersion,
		Revision:  policySettings.Revision,
		UpdatedAt: policySettings.UpdatedAt,
		Triggers: settings.Triggers{
			LinearLabel: label,
			LinearComment: settings.Trigger{
				Enabled: true, WorkflowID: protected.LinearFeedback.WorkflowID,
			},
		},
		ProtectedWorkflows: settings.ProtectedWorkflowBindings{
			LinearFeedback: settings.WorkflowBinding{WorkflowID: protected.LinearFeedback.WorkflowID},
		},
		Workflows: workflows,
		Agents: settings.AgentSettings{
			Principal: settings.PrincipalSettings{
				ProviderSettings: settings.ProviderSettings{
					Model: policySettings.Agents.Principal.Model, Effort: policySettings.Agents.Principal.Effort,
				},
				MaxAttempts: policySettings.Agents.Principal.MaxAttempts,
			},
			CodexChild: settings.ProviderSettings{
				Model: policySettings.Agents.CodexChild.Model, Effort: policySettings.Agents.CodexChild.Effort,
			},
			ClaudeChild: settings.ProviderSettings{
				Model: policySettings.Agents.ClaudeChild.Model, Effort: policySettings.Agents.ClaudeChild.Effort,
			},
		},
		Runtime: settings.RuntimeSettings{MaxConcurrentRuns: policySettings.Runtime.MaxConcurrentRuns},
	}
}

// RegistryView projects canonical policy into the retained trigger API and
// scheduler contract. Operational schedule cursors remain separately owned.
func RegistryView(snapshot Snapshot) triggerregistry.Snapshot {
	registry := snapshot.Registry()
	rules := make([]triggerregistry.Rule, len(registry.Rules))
	for index, rule := range registry.Rules {
		rules[index] = triggerregistry.Rule{
			ID: rule.ID, Revision: rule.Revision, Name: rule.Name, Enabled: rule.Enabled,
			Filter: triggerregistry.Filter{
				Source: rule.Filter.Source, Type: rule.Filter.Type, Action: rule.Filter.Action,
				Subject: cloneString(rule.Filter.Subject), Attributes: cloneStringMap(rule.Filter.Attributes),
			},
			WorkflowID: rule.WorkflowID,
			Target: triggerregistry.TargetPolicy{
				Provider: rule.Target.Provider, Kind: rule.Target.Kind, Value: rule.Target.Value,
			},
			MaxHop: rule.MaxHop, MaxOutstanding: rule.MaxOutstanding, AdmissionsHour: rule.AdmissionsHour,
		}
	}
	schedules := make([]triggerregistry.Schedule, len(registry.Schedules))
	for index, schedule := range registry.Schedules {
		attributes := make(map[string][]string, len(schedule.Attributes))
		for key, values := range schedule.Attributes {
			attributes[key] = append([]string(nil), values...)
		}
		schedules[index] = triggerregistry.Schedule{
			ID: schedule.ID, Revision: schedule.Revision, Name: schedule.Name, Enabled: schedule.Enabled,
			Cron: schedule.Cron, Timezone: schedule.Timezone, Subject: schedule.Subject, Attributes: attributes,
		}
	}
	return triggerregistry.Snapshot{
		Schema: triggerregistry.SchemaVersion, Revision: registry.Revision,
		UpdatedAt: registry.UpdatedAt, Rules: rules, Schedules: schedules,
	}
}

// RegistryCandidate converts the retained trigger mutation contract into the
// canonical registry shape. Store validation remains authoritative.
func RegistryCandidate(candidate triggerregistry.Snapshot) Registry {
	rules := make([]Rule, len(candidate.Rules))
	for index, rule := range candidate.Rules {
		rules[index] = ruleFromSource(rule)
	}
	schedules := make([]Schedule, len(candidate.Schedules))
	for index, schedule := range candidate.Schedules {
		schedules[index] = scheduleFromSource(schedule)
	}
	return Registry{
		Revision: candidate.Revision, UpdatedAt: candidate.UpdatedAt,
		Rules: rules, Schedules: schedules,
	}
}

// WorkflowCandidate converts the retained workflow transport contract into
// the canonical policy shape. Store validation owns revision and timestamps.
func WorkflowCandidate(candidate workflow.Definition) Workflow {
	return workflowFromSource(candidate)
}

// TaskControlView projects canonical native-project activation into the
// retained task API contract.
func TaskControlView(snapshot Snapshot) taskcontrol.Snapshot {
	control := snapshot.TaskControl()
	return taskcontrol.Snapshot{
		Version: 1, Revision: control.Revision, UpdatedAt: control.UpdatedAt,
		EnabledProjectIDs: append([]string(nil), control.EnabledProjectIDs...),
	}
}
