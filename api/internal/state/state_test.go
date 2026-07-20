package state

import "testing"

func TestValidSettings(t *testing.T) {
	selected := Settings{
		Harness: Claude, Model: "sonnet", Reasoning: "high", WorkflowCapacity: 4,
	}
	for _, capacity := range []int{MinWorkflowCapacity, MaxWorkflowCapacity} {
		valid := selected
		valid.WorkflowCapacity = capacity
		if !ValidSettings(valid) {
			t.Fatalf("workflow capacity %d was rejected", capacity)
		}
	}
	if ValidSettings(Settings{
		Harness: Claude, Model: "gpt-5.6-sol", Reasoning: "high", WorkflowCapacity: 4,
	}) {
		t.Fatal("cross-harness model was accepted")
	}
	for _, capacity := range []int{-1, MaxWorkflowCapacity + 1} {
		invalid := selected
		invalid.WorkflowCapacity = capacity
		if ValidSettings(invalid) {
			t.Fatalf("workflow capacity %d was accepted", capacity)
		}
	}
}
