package triggerregistry

import (
	"testing"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
)

func TestDefaultsPreserveLegacyAdmissionsAsGenericRules(t *testing.T) {
	t.Parallel()
	configuration := settings.Defaults(3)
	snapshot := Defaults(configuration, "actor-tom")
	if err := snapshot.Validate(configuration); err != nil {
		t.Fatalf("validate defaults: %v", err)
	}
	if len(snapshot.Rules) != 2 || len(snapshot.Schedules) != 0 {
		t.Fatalf("defaults = %#v", snapshot)
	}
	label, found := snapshot.Rule("linear-label")
	if !found || !label.Filter.Matches(eventwire.Event{
		Source: eventwire.SourceLinear, Type: "Issue", Action: "update", Subject: "ENG-40",
		Attributes: map[string][]string{AttributeActorID: {"actor-tom"}, AttributeAddedLabel: {CanonicalFold("Factory")}},
	}) {
		t.Fatalf("label rule = %#v, found=%t", label, found)
	}
	comment, found := snapshot.Rule("linear-comment")
	if !found || !comment.Filter.Matches(eventwire.Event{
		Source: eventwire.SourceLinear, Type: "Comment", Action: "create", Subject: "ENG-40",
		Attributes: map[string][]string{AttributeActorID: {"actor-tom"}, AttributeProvenance: {"human"}},
	}) {
		t.Fatalf("comment rule = %#v, found=%t", comment, found)
	}
}

func TestFilterDistinguishesWildcardAbsentAndExactSubject(t *testing.T) {
	t.Parallel()
	absent := ""
	exact := "ENG-40"
	events := []eventwire.Event{{}, {Subject: "ENG-40"}}
	tests := []struct {
		name   string
		filter Filter
		want   []bool
	}{
		{name: "wildcard", filter: Filter{}, want: []bool{true, true}},
		{name: "absent", filter: Filter{Subject: &absent}, want: []bool{true, false}},
		{name: "exact", filter: Filter{Subject: &exact}, want: []bool{false, true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for i, event := range events {
				if got := test.filter.Matches(event); got != test.want[i] {
					t.Fatalf("event %d match = %t, want %t", i, got, test.want[i])
				}
			}
		})
	}
}

func TestSnapshotRejectsInvalidBoundsAndReferences(t *testing.T) {
	t.Parallel()
	configuration := settings.Defaults(3)
	valid := Defaults(configuration, "actor-tom")
	tests := []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{name: "duplicate ID", mutate: func(snapshot *Snapshot) { snapshot.Rules[1].ID = snapshot.Rules[0].ID }},
		{name: "invalid source", mutate: func(snapshot *Snapshot) { snapshot.Rules[0].Filter.Source = "Linear" }},
		{name: "missing workflow", mutate: func(snapshot *Snapshot) { snapshot.Rules[0].WorkflowID = "missing" }},
		{name: "invalid target", mutate: func(snapshot *Snapshot) {
			snapshot.Rules[0].Target = TargetPolicy{Kind: TargetFixedIssue, Value: "eng-40"}
		}},
		{name: "excess hop", mutate: func(snapshot *Snapshot) { snapshot.Rules[0].MaxHop = MaximumMaxHop + 1 }},
		{name: "reserved schedule attribute", mutate: func(snapshot *Snapshot) {
			snapshot.Schedules = []Schedule{{
				ID: "daily", Revision: 1, Name: "Daily", Cron: "0 8 * * *", Timezone: "America/Los_Angeles",
				Attributes: map[string][]string{AttributeScheduleID: {"override"}},
			}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid.Clone()
			test.mutate(&candidate)
			if err := candidate.Validate(configuration); err == nil {
				t.Fatalf("invalid snapshot was accepted: %#v", candidate)
			}
		})
	}
}

func TestCanonicalFoldUsesStableUnicodeSimpleCase(t *testing.T) {
	t.Parallel()
	if left, right := CanonicalFold("  ΟΣ  "), CanonicalFold("ος"); left != right {
		t.Fatalf("fold mismatch: %q != %q", left, right)
	}
}
