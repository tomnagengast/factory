package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/api/internal/state"
)

func TestCommandRunnerNormalizesSelectedHarness(t *testing.T) {
	tests := []struct {
		settings state.Settings
		stream   string
		want     []AgentStep
		final    string
	}{
		{
			settings: state.Settings{Harness: state.Codex, Model: "gpt-5.6-sol", Reasoning: "high"},
			stream: strings.Join([]string{
				`{"type":"thread.started","thread_id":"thread-1"}`,
				`{"type":"item.completed","item":{"id":"reason-1","type":"reasoning","text":"I will inspect the workflow."}}`,
				`{"type":"item.started","item":{"id":"command-1","type":"command_execution","command":"workflow validate /workflows/review.js","status":"in_progress"}}`,
				`{"type":"item.completed","item":{"id":"command-1","type":"command_execution","command":"workflow validate /workflows/review.js","aggregated_output":"valid\n","exit_code":0,"status":"completed"}}`,
				`{"type":"item.completed","item":{"id":"message-1","type":"agent_message","text":"The file is written."}}`,
				`{"type":"future.semantic","detail":{"kept":true}}`,
				`{"type":"item.completed","item":{"id":"message-2","type":"agent_message","text":"Created and validated the workflow."}}`,
				`{"type":"turn.completed"}`,
			}, "\n") + "\n",
			want: []AgentStep{
				{Kind: "reasoning", Label: "codex", Content: "I will inspect the workflow."},
				{Kind: "tool-use", Label: "command", Content: "workflow validate /workflows/review.js"},
				{Kind: "tool-output", Label: "command", Content: "valid\n"},
				{Kind: "message", Content: "The file is written."},
				{Kind: "event", Label: "future.semantic", Content: `{"type":"future.semantic","detail":{"kept":true}}`},
			},
			final: "Created and validated the workflow.",
		},
		{
			settings: state.Settings{Harness: state.Claude, Model: "sonnet", Reasoning: "medium"},
			stream: strings.Join([]string{
				`{"type":"system","subtype":"init"}`,
				`{"type":"system","subtype":"api_retry","attempt":2}`,
				`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"I will inspect the source."},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"/workflows/review.js"}}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","content":"export const meta = {}"}]}}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"I found the workflow."}]}}`,
				`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool-2","name":"Bash","input":{"command":"workflow validate review.js"}}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-2","content":"validation failed","is_error":true}]}}`,
				`{"type":"hook_response","name":"after-tool","kept":true}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"Created and validated the workflow."}]}}`,
				`{"type":"result","subtype":"success","is_error":false,"result":"Created and validated the workflow."}`,
			}, "\n") + "\n",
			want: []AgentStep{
				{Kind: "event", Label: "system/api_retry", Content: `{"type":"system","subtype":"api_retry","attempt":2}`},
				{Kind: "reasoning", Label: "claude", Content: "I will inspect the source."},
				{Kind: "tool-use", Label: "Read", Content: "{\n  \"file_path\": \"/workflows/review.js\"\n}"},
				{Kind: "tool-output", Label: "Read", Content: "export const meta = {}"},
				{Kind: "message", Content: "I found the workflow."},
				{Kind: "tool-use", Label: "Bash", Content: "{\n  \"command\": \"workflow validate review.js\"\n}"},
				{Kind: "error", Label: "Bash", Content: "validation failed"},
				{Kind: "event", Label: "hook_response", Content: `{"type":"hook_response","name":"after-tool","kept":true}`},
			},
			final: "Created and validated the workflow.",
		},
	}
	for _, test := range tests {
		t.Run(test.settings.Harness, func(t *testing.T) {
			directory := t.TempDir()
			argsPath := filepath.Join(directory, "args")
			envPath := filepath.Join(directory, "env")
			stdinPath := filepath.Join(directory, "stdin")
			command := writeScript(t, directory, test.settings.Harness,
				"printf '%s\\n' \"$@\" > "+shellQuote(argsPath)+"\n"+
					"printf '%s\\n%s\\n' \"$FACTORY_CLI\" \"$FACTORY_URL\" > "+shellQuote(envPath)+"\n"+
					"cat > "+shellQuote(stdinPath)+"\n"+
					"printf '%s' "+shellQuote(test.stream)+"\n")
			factory := filepath.Join(directory, "factory")
			var steps []AgentStep
			output, err := testRunner(directory, command, factory).Run(
				context.Background(), test.settings, "Build a workflow",
				func(step AgentStep) error {
					steps = append(steps, step)
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if output != test.final {
				t.Fatalf("final output = %q, want %q", output, test.final)
			}
			if !equalSteps(steps, test.want) {
				t.Fatalf("steps = %#v, want %#v", steps, test.want)
			}
			args, _ := os.ReadFile(argsPath)
			for _, expected := range []string{test.settings.Model, test.settings.Reasoning, "dangerously"} {
				if !strings.Contains(string(args), expected) {
					t.Fatalf("%q missing from args: %s", expected, args)
				}
			}
			if test.settings.Harness == state.Codex && !strings.Contains(string(args), "--json") {
				t.Fatalf("Codex JSON mode missing: %s", args)
			}
			if test.settings.Harness == state.Claude &&
				(!strings.Contains(string(args), "stream-json") || !strings.Contains(string(args), "--verbose") ||
					!strings.Contains(string(args), "Build a workflow")) {
				t.Fatalf("Claude stream arguments missing: %s", args)
			}
			environment, _ := os.ReadFile(envPath)
			if string(environment) != factory+"\nhttp://127.0.0.1:8092\n" {
				t.Fatalf("unexpected Factory environment: %s", environment)
			}
			stdin, _ := os.ReadFile(stdinPath)
			if test.settings.Harness == state.Codex && string(stdin) != "Build a workflow" {
				t.Fatalf("unexpected Codex prompt: %q", stdin)
			}
		})
	}
}

func TestCommandRunnerEmitsBeforeProcessExits(t *testing.T) {
	directory := t.TempDir()
	release := filepath.Join(directory, "release")
	command := writeScript(t, directory, "codex",
		"printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"id\":\"reason-1\",\"type\":\"reasoning\",\"text\":\"Working\"}}'\n"+
			"while [ ! -f "+shellQuote(release)+" ]; do sleep 0.01; done\n"+
			"printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"id\":\"message-1\",\"type\":\"agent_message\",\"text\":\"Done\"}}'\n")
	stepReceived := make(chan AgentStep, 1)
	finished := make(chan error, 1)
	go func() {
		_, err := testRunner(directory, command, filepath.Join(directory, "factory")).Run(
			context.Background(), state.DefaultSettings, "Build it",
			func(step AgentStep) error {
				stepReceived <- step
				return nil
			},
		)
		finished <- err
	}()
	select {
	case step := <-stepReceived:
		if step.Kind != "reasoning" || step.Content != "Working" {
			t.Fatalf("early step = %#v", step)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("step was not emitted while the process was running")
	}
	select {
	case err := <-finished:
		t.Fatalf("process exited before release: %v", err)
	default:
	}
	if err := os.WriteFile(release, []byte("release"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit after release")
	}
}

func TestCommandRunnerPreservesLargeToolOutput(t *testing.T) {
	directory := t.TempDir()
	command := writeScript(t, directory, "codex",
		"printf '%s' '{\"type\":\"item.completed\",\"item\":{\"id\":\"command-1\",\"type\":\"command_execution\",\"command\":\"large\",\"aggregated_output\":\"'\n"+
			"head -c 200000 /dev/zero | tr '\\000' x\n"+
			"printf '%s\\n' '\",\"exit_code\":0}}'\n"+
			"printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"id\":\"message-1\",\"type\":\"agent_message\",\"text\":\"Done\"}}'\n")
	var steps []AgentStep
	_, err := testRunner(directory, command, filepath.Join(directory, "factory")).Run(
		context.Background(), state.DefaultSettings, "Build it",
		func(step AgentStep) error {
			steps = append(steps, step)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[1].Kind != "tool-output" || len(steps[1].Content) != 200000 {
		t.Fatalf("large output steps = %d, output bytes = %d", len(steps), len(steps[1].Content))
	}
}

func TestCommandRunnerReturnsSemanticHarnessFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		settings state.Settings
		stream   string
		want     AgentStep
	}{
		{
			name: "codex", settings: state.DefaultSettings,
			stream: `{"type":"item.completed","item":{"id":"error-1","type":"error","message":"usage limit reached"}}`,
			want:   AgentStep{Kind: "error", Label: "codex", Content: "usage limit reached"},
		},
		{
			name: "claude", settings: state.Settings{Harness: state.Claude, Model: "sonnet", Reasoning: "high"},
			stream: `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"usage limit reached"}`,
			want:   AgentStep{Kind: "error", Label: "error_during_execution", Content: "usage limit reached"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			command := writeScript(t, directory, test.name,
				"printf '%s\\n' "+shellQuote(test.stream)+"\n")
			var steps []AgentStep
			output, err := testRunner(directory, command, filepath.Join(directory, "factory")).Run(
				context.Background(), test.settings, "Build it",
				func(step AgentStep) error {
					steps = append(steps, step)
					return nil
				},
			)
			if err == nil || !strings.Contains(err.Error(), "usage limit reached") || output != "usage limit reached" {
				t.Fatalf("output = %q, error = %v", output, err)
			}
			if want := []AgentStep{test.want}; !equalSteps(steps, want) {
				t.Fatalf("steps = %#v, want %#v", steps, want)
			}
		})
	}
}

func TestCommandRunnerStopsOnStreamAndProcessFailures(t *testing.T) {
	tests := []struct {
		name, body, errorText string
		context               func() (context.Context, context.CancelFunc)
		emit                  func(AgentStep) error
	}{
		{name: "malformed", body: "printf '{bad json'\n", errorText: "decode agent event"},
		{name: "nonzero", body: "printf 'process failed' >&2\nexit 7\n", errorText: "process failed"},
		{
			name: "callback", body: "printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"id\":\"reason-1\",\"type\":\"reasoning\",\"text\":\"Working\"}}'\nwhile :; do sleep 1; done\n",
			errorText: "wire unavailable", emit: func(AgentStep) error { return errors.New("wire unavailable") },
		},
		{
			name: "canceled", body: "while :; do sleep 1; done\n", errorText: "context deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				return ctx, cancel
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			command := writeScript(t, directory, "codex", test.body)
			ctx, cancel := context.WithCancel(context.Background())
			if test.context != nil {
				cancel()
				ctx, cancel = test.context()
			}
			defer cancel()
			emit := test.emit
			if emit == nil {
				emit = func(AgentStep) error { return nil }
			}
			finished := make(chan error, 1)
			go func() {
				_, err := testRunner(directory, command, filepath.Join(directory, "factory")).Run(
					ctx, state.DefaultSettings, "Build it", emit,
				)
				finished <- err
			}()
			select {
			case err := <-finished:
				if err == nil || !strings.Contains(err.Error(), test.errorText) {
					t.Fatalf("error = %v, want %q", err, test.errorText)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("runner did not reap failed child process")
			}
		})
	}
}

func testRunner(directory, command, factory string) CommandRunner {
	return CommandRunner{
		CodexCommand: command, ClaudeCommand: command, Workspace: directory,
		FactoryCommand: factory, FactoryURL: "http://127.0.0.1:8092",
	}
}

func writeScript(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func equalSteps(left, right []AgentStep) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
