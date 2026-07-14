package triggerregistry

import "github.com/tomnagengast/factory/internal/settings"

func Defaults(configuration settings.Snapshot, triggerActor string) Snapshot {
	label := configuration.Triggers.LinearLabel
	comment := configuration.Triggers.LinearComment
	return Snapshot{
		Schema: SchemaVersion,
		Rules: []Rule{
			{
				ID:       "linear-comment",
				Revision: 1,
				Name:     "Linear comment",
				Enabled:  comment.Enabled,
				Filter: Filter{
					Source: "linear", Type: "Comment", Action: "create",
					Attributes: map[string]string{AttributeActorID: triggerActor, AttributeProvenance: "human"},
				},
				WorkflowID: comment.WorkflowID,
				Target:     TargetPolicy{Kind: TargetEventSubject},
				MaxHop:     DefaultMaxHop, MaxOutstanding: DefaultMaxOutstanding, AdmissionsHour: DefaultAdmissionsHour,
			},
			{
				ID:       "linear-label",
				Revision: 1,
				Name:     "Linear label",
				Enabled:  label.Enabled,
				Filter: Filter{
					Source: "linear", Type: "Issue", Action: "update",
					Attributes: map[string]string{AttributeActorID: triggerActor, AttributeAddedLabel: CanonicalFold(label.Label)},
				},
				WorkflowID: label.WorkflowID,
				Target:     TargetPolicy{Kind: TargetEventSubject},
				MaxHop:     DefaultMaxHop, MaxOutstanding: DefaultMaxOutstanding, AdmissionsHour: DefaultAdmissionsHour,
			},
		},
		Schedules: []Schedule{},
	}
}
