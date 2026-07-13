package settings

import (
	"strings"
	"testing"
)

func TestDefaultsAreValidAndIndependent(t *testing.T) {
	t.Parallel()

	first := Defaults(3)
	if err := first.Validate(); err != nil {
		t.Fatalf("validate defaults: %v", err)
	}
	first.Workflows[0].Steps[0] = "changed"
	second := Defaults(3)
	if second.Workflows[0].Steps[0] == "changed" {
		t.Fatal("defaults share workflow step storage")
	}
	if second.Triggers.LinearLabel.Label != "Factory" || second.Agents.Principal.Model != "gpt-5.6-sol" || second.Agents.ClaudeChild.Model != "fable" {
		t.Fatalf("unexpected defaults: %#v", second)
	}
}

func TestValidateRejectsUnsafeOrInconsistentSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   string
	}{
		{name: "schema", mutate: func(value *Snapshot) { value.Schema = 2 }, want: "schema"},
		{name: "label control", mutate: func(value *Snapshot) { value.Triggers.LinearLabel.Label = "Factory\nOther" }, want: "Linear label"},
		{name: "duplicate workflow", mutate: func(value *Snapshot) { value.Workflows = append(value.Workflows, value.Workflows[0]) }, want: "duplicated"},
		{name: "runner", mutate: func(value *Snapshot) { value.Workflows[0].Runner = "shell" }, want: "runner"},
		{name: "missing steps", mutate: func(value *Snapshot) { value.Workflows[0].Steps = nil }, want: "steps"},
		{name: "long step", mutate: func(value *Snapshot) { value.Workflows[0].Steps[0] = strings.Repeat("a", maxWorkflowStepBytes+1) }, want: "step"},
		{name: "disabled reference", mutate: func(value *Snapshot) { value.Workflows[0].Enabled = false }, want: "trigger"},
		{name: "model", mutate: func(value *Snapshot) { value.Agents.Principal.Model = "bad model" }, want: "model"},
		{name: "Codex effort", mutate: func(value *Snapshot) { value.Agents.CodexChild.Effort = "max" }, want: "effort"},
		{name: "Claude effort", mutate: func(value *Snapshot) { value.Agents.ClaudeChild.Effort = "xhigh" }, want: "effort"},
		{name: "attempts", mutate: func(value *Snapshot) { value.Agents.Principal.MaxAttempts = 0 }, want: "attempts"},
		{name: "concurrency", mutate: func(value *Snapshot) { value.Runtime.MaxConcurrentRuns = 0 }, want: "concurrent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := Defaults(3)
			test.mutate(&value)
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestWorkflowForTriggerUsesCommentMappingAndLabelFallback(t *testing.T) {
	t.Parallel()

	value := Defaults(3)
	value.Workflows = append(value.Workflows, Workflow{
		ID: "comment-review", Name: "Comment review", Enabled: true, Runner: "do", Steps: []string{"Read feedback"},
	})
	value.Triggers.LinearComment.WorkflowID = "comment-review"
	comment, err := value.WorkflowForTrigger("linear-comment")
	if err != nil || comment.ID != "comment-review" {
		t.Fatalf("comment workflow = %#v, %v", comment, err)
	}
	continuation, err := value.WorkflowForTrigger("post-merge")
	if err != nil || continuation.ID != DefaultWorkflowID {
		t.Fatalf("continuation workflow = %#v, %v", continuation, err)
	}
}
