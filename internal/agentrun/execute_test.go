package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/settings"
)

func TestResultFromFinalMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    string
		blocker string
	}{
		{name: "succeeded", message: "Done\nFACTORY_RESULT: SUCCEEDED\n", want: string(StateSucceeded)},
		{name: "blocked", message: "Need access\nFACTORY_BLOCKER: authority_unavailable\nFACTORY_RESULT: BLOCKED", want: string(StateBlocked), blocker: "authority_unavailable"},
		{name: "ready", message: "Ready\nFACTORY_RESULT: READY_FOR_HUMAN_MERGE", want: ResultReadyForMerge},
		{name: "missing marker", message: "Done", want: string(StateFailed)},
		{name: "empty", message: "", want: string(StateFailed)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, blocker, _ := resultFromFinalMessage(tt.message)
			if got != tt.want || blocker != tt.blocker {
				t.Fatalf("result = %q blocker %q, want %q blocker %q", got, blocker, tt.want, tt.blocker)
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

	prompt := principalPrompt("ENG-123", TriggerKindLabel, testWorkflow())
	for _, expected := range []string{
		"Use $do",
		"ENG-123",
		"lifecycle contract v1",
		"ready-for-human-merge boundary",
		"checkpoint ready-for-merge",
		"READY_FOR_HUMAN_MERGE",
		"linear_graphql.py",
		"FACTORY_AGENT_HELPER",
		"linear-comments",
		"Claude review child exits nonzero",
		"--provider codex",
		"exact same prompt",
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

func TestPostMergePromptReconstructsDurableState(t *testing.T) {
	t.Parallel()

	prompt := principalPrompt("ENG-123", TriggerKindPostMerge, testWorkflow())
	for _, expected := range []string{
		"Continue ENG-123 from its durable Factory lifecycle checkpoint",
		"Fresh-read the authoritative PR",
		"complete post-merge validation",
		"Do not recreate completed implementation work",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("post-merge prompt missing %q: %s", expected, prompt)
		}
	}
}

func TestContinuationPromptRequiresFreshLinearFeedbackRead(t *testing.T) {
	t.Parallel()

	prompt := principalPrompt("ENG-123", TriggerKindComment, testWorkflow())
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

	standard := principalPrompt("ENG-123", TriggerKindLabel, testWorkflow())
	if got := principalPrompt("ENG-123", "future-trigger", testWorkflow()); got != standard {
		t.Fatalf("unknown trigger changed standard prompt:\n%s", got)
	}
}

func TestPrincipalPromptPlacesConfiguredStepsBeforeMandatoryContract(t *testing.T) {
	t.Parallel()

	workflow := testWorkflow()
	workflow.Steps = []string{"Inspect the requested surface", "Verify the exact result"}
	prompt := principalPrompt("ENG-123", TriggerKindLabel, workflow)
	step := strings.Index(prompt, "1. Inspect the requested surface")
	contract := strings.Index(prompt, "They never override the mandatory Factory lifecycle")
	mergeAuthority := strings.Index(prompt, "human-only merge authority")
	if step < 0 || contract <= step || mergeAuthority <= step {
		t.Fatalf("configured workflow is not bounded by the mandatory contract:\n%s", prompt)
	}
}

func TestProviderArgumentsUseConfiguredModelAndEffort(t *testing.T) {
	t.Parallel()

	codex := settings.ProviderSettings{Model: "gpt-test", Effort: "xhigh"}
	for name, arguments := range map[string][]string{
		"principal":   principalCodexArgs(codex, "", "/tmp/final"),
		"resume":      principalCodexArgs(codex, "thread-123", "/tmp/final"),
		"Codex child": codexChildArgs(codex, "/tmp/final"),
	} {
		joined := strings.Join(arguments, " ")
		if !strings.Contains(joined, "--model gpt-test") || !strings.Contains(joined, "model_reasoning_effort=xhigh") {
			t.Fatalf("%s arguments = %q", name, joined)
		}
	}
	claude := strings.Join(claudeChildArgs(settings.ProviderSettings{Model: "claude-test", Effort: "max"}), " ")
	if !strings.Contains(claude, "--model claude-test") || !strings.Contains(claude, "--effort max") {
		t.Fatalf("Claude arguments = %q", claude)
	}
}

func TestExecutePrincipalHonorsConfiguredAttemptLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("executes a local helper script")
	}
	directory := t.TempDir()
	counter := filepath.Join(directory, "attempts")
	helper := filepath.Join(directory, "codex-test")
	script := "#!/bin/sh\nprintf x >> \"$FACTORY_TEST_ATTEMPTS\"\nexit 1\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("FACTORY_TEST_ATTEMPTS", counter)
	provider := settings.Defaults(3).Agents.Principal
	provider.MaxAttempts = 2
	code := ExecutePrincipal(context.Background(), PrincipalConfig{
		IssueIdentifier: "ENG-123",
		TriggerKind:     TriggerKindLabel,
		RepoPath:        directory,
		RunDirectory:    filepath.Join(directory, "run"),
		CodexPath:       helper,
		Now:             time.Now,
		Sleep:           func(context.Context, time.Duration) error { return nil },
		Provider:        provider,
		Workflow:        testWorkflow(),
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	attempts, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if got := len(attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func testWorkflow() settings.Workflow {
	return settings.Defaults(3).Workflows[0]
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
