package policy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

func TestSnapshotIsImmutableAndCanonical(t *testing.T) {
	snapshot := mustConvertSources(t, populatedSources())
	originalDigest, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}

	model := snapshot.Model()
	model.Workflows[0].Markdown = "changed"
	model.Registry.Rules[0].Filter.Attributes["actorId"] = "changed"
	model.Registry.Schedules[0].Attributes["kind"][0] = "changed"
	model.TaskControl.EnabledProjectIDs[0] = "changed"

	registry := snapshot.Registry()
	registry.Rules[0].Filter.Attributes["actorId"] = "also-changed"
	registry.Schedules[0].Attributes["kind"][0] = "also-changed"
	projects := snapshot.TaskControl()
	projects.EnabledProjectIDs[0] = "also-changed"

	afterDigest, err := snapshot.Digest()
	if err != nil || afterDigest != originalDigest {
		t.Fatalf("immutable digest = %q, %v; want %q", afterDigest, err, originalDigest)
	}
	if got := snapshot.Workflows()[0].Markdown; got == "changed" {
		t.Fatal("workflow accessor exposed mutable snapshot state")
	}
}

func TestNewSnapshotRejectsCrossDomainAndShapeErrors(t *testing.T) {
	base := mustConvertSources(t, populatedSources()).Model()
	tests := []struct {
		name   string
		mutate func(*Model)
		want   string
	}{
		{name: "generation", mutate: func(model *Model) { model.Generation = 0 }, want: "generation"},
		{name: "duplicate workflow", mutate: func(model *Model) { model.Workflows = append(model.Workflows, model.Workflows[0]) }, want: "duplicated"},
		{name: "protected workflow", mutate: func(model *Model) { model.ProtectedWorkflows.LinearFeedback.WorkflowID = "missing" }, want: "protected"},
		{name: "enabled rule workflow", mutate: func(model *Model) { model.Registry.Rules[0].WorkflowID = "missing" }, want: "unavailable"},
		{name: "duplicate registry ID", mutate: func(model *Model) { model.Registry.Schedules[0].ID = model.Registry.Rules[0].ID }, want: "duplicated"},
		{name: "task project", mutate: func(model *Model) { model.TaskControl.EnabledProjectIDs = []string{"not valid"} }, want: "enabled projects"},
		{name: "provider", mutate: func(model *Model) { model.Registry.Rules[0].Target.Provider = taskmodel.SourceFactory }, want: "target provider"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneModel(base)
			test.mutate(&candidate)
			if _, err := NewSnapshot(candidate); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewSnapshot() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPolicyModelExcludesOperationalScheduleCursors(t *testing.T) {
	snapshot := mustConvertSources(t, populatedSources())
	data, err := json.Marshal(snapshot.Model())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"scheduleCursor":`, `"lastScheduledAt":`, `"skipped":`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("policy contains operational cursor field %q: %s", forbidden, data)
		}
	}
}
