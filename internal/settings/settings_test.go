package settings

import (
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/workflow"
)

func TestDefaultsCloneAndValidate(t *testing.T) {
	value := Defaults(3)
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	clone := value.Clone()
	clone.Workflows[0].Markdown = "changed"
	if value.Workflows[0].Markdown == "changed" {
		t.Fatal("clone changed original workflow")
	}
}

func TestSnapshotValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   string
	}{
		{name: "schema", mutate: func(value *Snapshot) { value.Schema = 1 }, want: "schema"},
		{name: "duplicate workflow", mutate: func(value *Snapshot) { value.Workflows = append(value.Workflows, value.Workflows[0]) }, want: "duplicated"},
		{name: "missing revision", mutate: func(value *Snapshot) { value.Workflows[0].Revision = 0 }, want: "revision"},
		{name: "blank Markdown", mutate: func(value *Snapshot) { value.Workflows[0].Markdown = " " }, want: "blank"},
		{name: "long Markdown", mutate: func(value *Snapshot) { value.Workflows[0].Markdown = strings.Repeat("a", workflow.MaxMarkdownBytes+1) }, want: "exceeds"},
		{name: "disabled protected binding", mutate: func(value *Snapshot) { value.Workflows[0].Enabled = false }, want: "protected"},
		{name: "principal attempts", mutate: func(value *Snapshot) { value.Agents.Principal.MaxAttempts = 0 }, want: "attempts"},
		{name: "runtime", mutate: func(value *Snapshot) { value.Runtime.MaxConcurrentRuns = 11 }, want: "concurrent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := Defaults(3)
			test.mutate(&value)
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestWorkflowForTriggerUsesProtectedFeedbackBinding(t *testing.T) {
	value := Defaults(3)
	value.Workflows = append(value.Workflows, workflow.Definition{
		ID: "feedback", Revision: 1, Name: "Feedback", Enabled: true,
		Markdown: "# Feedback", UpdatedAt: time.Now().UTC(),
	})
	value.ProtectedWorkflows.LinearFeedback.WorkflowID = "feedback"
	definition, err := value.WorkflowForTrigger("linear-comment")
	if err != nil || definition.ID != "feedback" {
		t.Fatalf("comment workflow = %#v, %v", definition, err)
	}
}
