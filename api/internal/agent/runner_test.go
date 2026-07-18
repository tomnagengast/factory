package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandRunnerInvokesUnrestrictedCodex(t *testing.T) {
	directory := t.TempDir()
	argsPath := filepath.Join(directory, "args")
	envPath := filepath.Join(directory, "env")
	stdinPath := filepath.Join(directory, "stdin")
	command := filepath.Join(directory, "codex")
	factory := filepath.Join(directory, "factory")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsPath +
		"\nprintf '%s\\n%s\\n' \"$FACTORY_CLI\" \"$FACTORY_URL\" > " + envPath +
		"\ncat > " + stdinPath + "\nprintf 'Workflow updated.'\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := (CommandRunner{
		Command: command, Workspace: directory,
		FactoryCommand: factory, FactoryURL: "http://127.0.0.1:8092",
	}).Run(context.Background(), "Build a workflow")
	if err != nil {
		t.Fatal(err)
	}
	if output != "Workflow updated." {
		t.Fatalf("unexpected output: %q", output)
	}
	args, _ := os.ReadFile(argsPath)
	if !strings.Contains(string(args), "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("unrestricted flag missing: %s", args)
	}
	environment, _ := os.ReadFile(envPath)
	if string(environment) != factory+"\nhttp://127.0.0.1:8092\n" {
		t.Fatalf("unexpected Factory environment: %s", environment)
	}
	stdin, _ := os.ReadFile(stdinPath)
	if string(stdin) != "Build a workflow" {
		t.Fatalf("unexpected prompt: %q", stdin)
	}
}
