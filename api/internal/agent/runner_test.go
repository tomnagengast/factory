package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomnagengast/factory/api/internal/state"
)

func TestCommandRunnerInvokesSelectedHarness(t *testing.T) {
	for _, settings := range []state.Settings{
		{Harness: state.Codex, Model: "gpt-5.6-sol", Reasoning: "high"},
		{Harness: state.Claude, Model: "sonnet", Reasoning: "medium"},
	} {
		t.Run(settings.Harness, func(t *testing.T) {
			directory := t.TempDir()
			argsPath := filepath.Join(directory, "args")
			envPath := filepath.Join(directory, "env")
			stdinPath := filepath.Join(directory, "stdin")
			command := filepath.Join(directory, settings.Harness)
			factory := filepath.Join(directory, "factory")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsPath +
				"\nprintf '%s\\n%s\\n' \"$FACTORY_CLI\" \"$FACTORY_URL\" > " + envPath +
				"\ncat > " + stdinPath + "\nprintf 'Workflow updated.'\n"
			if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			output, err := (CommandRunner{
				CodexCommand: command, ClaudeCommand: command, Workspace: directory,
				FactoryCommand: factory, FactoryURL: "http://127.0.0.1:8092",
			}).Run(context.Background(), settings, "Build a workflow")
			if err != nil {
				t.Fatal(err)
			}
			if output != "Workflow updated." {
				t.Fatalf("unexpected output: %q", output)
			}
			args, _ := os.ReadFile(argsPath)
			for _, expected := range []string{settings.Model, settings.Reasoning, "dangerously"} {
				if !strings.Contains(string(args), expected) {
					t.Fatalf("%q missing from args: %s", expected, args)
				}
			}
			if settings.Harness == state.Claude && !strings.Contains(string(args), "Build a workflow") {
				t.Fatalf("Claude prompt missing: %s", args)
			}
			environment, _ := os.ReadFile(envPath)
			if string(environment) != factory+"\nhttp://127.0.0.1:8092\n" {
				t.Fatalf("unexpected Factory environment: %s", environment)
			}
			stdin, _ := os.ReadFile(stdinPath)
			if settings.Harness == state.Codex && string(stdin) != "Build a workflow" {
				t.Fatalf("unexpected Codex prompt: %q", stdin)
			}
		})
	}
}
