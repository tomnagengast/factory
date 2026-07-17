package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandRunnerInvokesUnrestrictedAgentAndStreamsOutput(t *testing.T) {
	directory := t.TempDir()
	command := filepath.Join(directory, "fake-codex")
	script := `#!/bin/sh
IFS= read -r prompt
printf 'prompt:%s\n' "$prompt"
printf 'cwd:%s args:%s\n' "$PWD" "$*" >&2
`
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var outputs []Output
	runner := CommandRunner{Command: command, Workspace: directory}
	err := runner.Run(context.Background(), "Build the smallest loop", func(output Output) error {
		outputs = append(outputs, output)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr string
	for _, output := range outputs {
		switch output.Stream {
		case "stdout":
			stdout += output.Text
		case "stderr":
			stderr += output.Text
		}
	}
	if stdout != "prompt:Build the smallest loop" {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, expected := range []string{
		"cwd:" + directory,
		"--dangerously-bypass-approvals-and-sandbox",
		"--dangerously-bypass-hook-trust",
		"--ignore-rules",
		"--skip-git-repo-check",
		"-C " + directory,
	} {
		if !strings.Contains(stderr, expected) {
			t.Errorf("stderr %q does not contain %q", stderr, expected)
		}
	}
}
