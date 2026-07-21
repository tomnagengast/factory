package state

import (
	"slices"
	"testing"
)

func TestDefaultSettingsReturnsIndependentReactionPalettes(t *testing.T) {
	first := DefaultSettings()
	second := DefaultSettings()
	want := []string{"👍", "👎", "❤️", "🎉", "😂", "👀"}
	if !slices.Equal(first.ReactionEmojis, want) || !slices.Equal(second.ReactionEmojis, want) {
		t.Fatalf("default reactions = %v and %v", first.ReactionEmojis, second.ReactionEmojis)
	}
	first.ReactionEmojis[0] = "changed"
	if !slices.Equal(second.ReactionEmojis, want) {
		t.Fatalf("default reactions share backing storage: %v", second.ReactionEmojis)
	}
}

func TestValidSettings(t *testing.T) {
	selected := DefaultSettings()
	selected.Harness, selected.Model, selected.Reasoning = Claude, "sonnet", "high"
	selected.WorkflowCapacity = 4
	selected.ReactionEmojis = []string{"🧑🏽‍💻", "plain text", "🤔"}
	for _, capacity := range []int{MinWorkflowCapacity, MaxWorkflowCapacity} {
		valid := selected
		valid.WorkflowCapacity = capacity
		if !ValidSettings(valid) {
			t.Fatalf("workflow capacity %d was rejected", capacity)
		}
	}

	crossHarness := selected
	crossHarness.Model = "gpt-5.6-sol"
	if ValidSettings(crossHarness) {
		t.Fatal("cross-harness model was accepted")
	}
	for _, capacity := range []int{-1, MaxWorkflowCapacity + 1} {
		invalid := selected
		invalid.WorkflowCapacity = capacity
		if ValidSettings(invalid) {
			t.Fatalf("workflow capacity %d was accepted", capacity)
		}
	}

	for name, emojis := range map[string][]string{
		"nil":                 nil,
		"empty array":         {},
		"empty entry":         {"👍", ""},
		"leading whitespace":  {" 👍"},
		"trailing whitespace": {"👍 "},
		"carriage return":     {"👍\r"},
		"line feed":           {"👍\n"},
		"exact duplicate":     {"👍", "👍"},
		"invalid UTF-8":       {string([]byte{0xff})},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := selected
			invalid.ReactionEmojis = emojis
			if ValidSettings(invalid) {
				t.Fatalf("reaction emojis %q were accepted", emojis)
			}
		})
	}
}

func TestReactionEmojiConfiguredUsesExactMembership(t *testing.T) {
	emojis := []string{"👍🏻", "❤️", "plain text"}
	for _, emoji := range emojis {
		if !ReactionEmojiConfigured(emojis, emoji) {
			t.Fatalf("configured emoji %q was rejected", emoji)
		}
	}
	for _, emoji := range []string{"👍", "❤", " plain text"} {
		if ReactionEmojiConfigured(emojis, emoji) {
			t.Fatalf("unconfigured emoji %q was accepted", emoji)
		}
	}
}
