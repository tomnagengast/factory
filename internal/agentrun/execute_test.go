package agentrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResultFromFinalMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    State
	}{
		{name: "succeeded", message: "Done\nFACTORY_RESULT: SUCCEEDED\n", want: StateSucceeded},
		{name: "blocked", message: "Need access\nFACTORY_RESULT: BLOCKED", want: StateBlocked},
		{name: "missing marker", message: "Done", want: StateFailed},
		{name: "empty", message: "", want: StateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _ := resultFromFinalMessage(tt.message)
			if got != tt.want {
				t.Fatalf("state = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadThreadID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "events.jsonl")
	data := "{\"type\":\"item.completed\"}\n{\"type\":\"thread.started\",\"thread_id\":\"thread-123\"}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}
	if got := readThreadID(path); got != "thread-123" {
		t.Fatalf("thread ID = %q, want thread-123", got)
	}
}

func TestPrincipalPromptGroupsChildAgentsInTmux(t *testing.T) {
	t.Parallel()

	prompt := principalPrompt("ENG-123", TriggerKindLabel)
	for _, expected := range []string{
		"Use $do",
		"ENG-123",
		"human merge",
		"wait for the human merge event",
		"deployment from updated main",
		"branch/worktree cleanup",
		"linear_graphql.py",
		"FACTORY_AGENT_HELPER",
		"linear-comments",
		"FACTORY_RESULT: SUCCEEDED",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q: %s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "GitHub approval") {
		t.Fatalf("prompt still requires GitHub approval: %s", prompt)
	}
}

func TestContinuationPromptRequiresFreshLinearFeedbackRead(t *testing.T) {
	t.Parallel()

	prompt := principalPrompt("ENG-123", TriggerKindComment)
	for _, expected := range []string{
		"continue ENG-123 in response to new human Linear feedback",
		"fresh-read the complete Linear issue and conversation",
		"not yet addressed",
		"focused follow-up",
		"Do not redo completed work",
		"FACTORY_RESULT: SUCCEEDED",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("continuation prompt missing %q: %s", expected, prompt)
		}
	}
}

func TestUnknownTriggerKindUsesStandardPrompt(t *testing.T) {
	t.Parallel()

	standard := principalPrompt("ENG-123", TriggerKindLabel)
	if got := principalPrompt("ENG-123", "future-trigger"); got != standard {
		t.Fatalf("unknown trigger changed standard prompt:\n%s", got)
	}
}

func TestAgentEnvironmentExcludesUnrelatedServiceSecrets(t *testing.T) {
	t.Parallel()

	got := agentEnvironment([]string{
		"HOME=/Users/test",
		"PATH=/usr/bin",
		"LINEAR_API_KEY=linear-secret",
		"LINEAR_WEBHOOK_SECRET=webhook-secret",
		"CF_API_TOKEN=cloudflare-secret",
		"OP_SERVICE_ACCOUNT_TOKEN=one-password-secret",
	})
	joined := strings.Join(got, "\n")
	for _, expected := range []string{"HOME=/Users/test", "PATH=/usr/bin", "LINEAR_API_KEY=linear-secret"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("environment missing %q: %v", expected, got)
		}
	}
	for _, secret := range []string{"LINEAR_WEBHOOK_SECRET", "CF_API_TOKEN", "OP_SERVICE_ACCOUNT_TOKEN"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("environment leaked %s: %v", secret, got)
		}
	}
}
