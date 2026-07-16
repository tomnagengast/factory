package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
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

func TestPrincipalPromptExecutesPinnedMarkdownDirectly(t *testing.T) {
	t.Parallel()

	prompt := principalPrompt("ENG-123", TriggerKindLabel, testWorkflow())
	for _, expected := range []string{
		"ENG-123",
		"Workflow: Full SDLC revision 1",
		"----- BEGIN PINNED WORKFLOW MARKDOWN -----",
		workflow.DefaultMarkdown(),
		"FACTORY RUNTIME PROTOCOL",
		"checkpoint ready-for-merge",
		"READY_FOR_HUMAN_MERGE",
		"agent linear-graphql",
		"FACTORY_AGENT_HELPER",
		"linear-comments",
		"--provider claude",
		"authority_unavailable",
		"only valid blockers are missing_routing_metadata",
		"safeguard_regression is not a pre-checkpoint blocker",
		"FACTORY_RESULT: SUCCEEDED",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q: %s", expected, prompt)
		}
	}
	for _, forbidden := range []string{"Use $do", "The /do skill owns", ".agents/skills/do", "linear_graphql.py"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt contains legacy dependency %q: %s", forbidden, prompt)
		}
	}
}

func TestPostMergePromptReconstructsDurableState(t *testing.T) {
	t.Parallel()

	prompt := principalPrompt("ENG-123", TriggerKindPostMerge, testWorkflow())
	for _, expected := range []string{
		"Segment: post-merge",
		"Fresh-read authoritative pull-request, Linear, repository, approved-plan, deployment, and cleanup state.",
		"git merge-base --is-ancestor",
		"rebase or squash merge that replayed the changes",
		"verified_head_mismatch",
		"without recreating finished implementation",
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
		"Segment: feedback",
		"Fresh-read the complete Linear conversation first.",
		"not already addressed by Factory evidence",
		"focused continuation",
		"FACTORY_RESULT: SUCCEEDED",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("continuation prompt missing %q: %s", expected, prompt)
		}
	}
}

func TestNativeContinuationPromptRequiresFreshDurableTaskRead(t *testing.T) {
	prompt := taskPrincipalPrompt(
		taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"},
		TriggerKindComment,
		workflow.Pin(workflow.ProviderNeutralDefault(time.Now())),
	)
	for _, expected := range []string{"Fresh-read the complete durable task conversation first.", "later human message or gate decision", "focused continuation"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("native continuation prompt missing %q: %s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "Fresh-read the complete Linear conversation first.") {
		t.Fatalf("native continuation prompt instructed a Linear read: %s", prompt)
	}
}

func TestProviderNeutralLinearPromptUsesScopedTaskHelper(t *testing.T) {
	t.Parallel()

	prompt := taskPrincipalPrompt(
		taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-123", Identifier: "ENG-123"},
		TriggerKindLabel,
		workflow.Pin(workflow.ProviderNeutralDefault(time.Now())),
	)
	if !strings.Contains(prompt, "agent task commands") || strings.Contains(prompt, "LINEAR_API_KEY") || strings.Contains(prompt, "agent linear-graphql") {
		t.Fatalf("provider-neutral Linear prompt did not use scoped helper:\n%s", prompt)
	}
}

func TestUnknownTriggerKindUsesStandardPrompt(t *testing.T) {
	t.Parallel()

	standard := principalPrompt("ENG-123", TriggerKindLabel, testWorkflow())
	if got := principalPrompt("ENG-123", "future-trigger", testWorkflow()); got != standard {
		t.Fatalf("unknown trigger changed standard prompt:\n%s", got)
	}
}

func TestPrincipalPromptPreservesConfiguredMarkdownBeforeRuntimeProtocol(t *testing.T) {
	t.Parallel()

	pinned := workflow.Pin(workflow.Definition{
		ID: "custom", Revision: 9, Name: "Custom", Enabled: true,
		Markdown: "# Exact\n\n- preserve `code`\n\n```text\nraw\n```\n",
	})
	prompt := principalPrompt("ENG-123", TriggerKindLabel, pinned)
	body := strings.Index(prompt, pinned.Markdown)
	protocol := strings.Index(prompt, "FACTORY RUNTIME PROTOCOL")
	if body < 0 || protocol <= body || strings.Count(prompt, pinned.Markdown) != 1 {
		t.Fatalf("configured Markdown was not preserved before the protocol:\n%s", prompt)
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

func testWorkflow() workflow.Pinned {
	return workflow.Pin(settings.Defaults(3).Workflows[0])
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
	}, true)
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

func TestProviderNeutralAgentEnvironmentExcludesLinearKey(t *testing.T) {
	got := agentEnvironment([]string{"HOME=/Users/test", "LINEAR_API_KEY=linear-secret"}, false)
	if joined := strings.Join(got, "\n"); strings.Contains(joined, "LINEAR_API_KEY") || !strings.Contains(joined, "HOME=/Users/test") {
		t.Fatalf("provider-neutral environment = %v", got)
	}
}
