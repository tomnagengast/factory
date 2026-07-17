package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIListsAndRunsWorkflows(t *testing.T) {
	directory := t.TempDir()
	project := filepath.Join(directory, "project")
	source := filepath.Join(directory, "demo.js")
	logPath := filepath.Join(directory, "args")
	command := filepath.Join(directory, "workflow")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + logPath + "\n" +
		"if [ \"$3\" = \"list\" ]; then printf '[{\"name\":\"demo\",\"path\":\"/demo.js\",\"scope\":\"user\",\"description\":\"Demo\",\"phases\":[\"Run\"],\"mutating\":false}]'; " +
		"else set -- \"$2\"/.claude/workflows/~factory-*.js; [ -L \"$1\" ] || exit 9; printf 'complete'; fi\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("export const meta = { name: 'demo' }"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := CLI{Command: command, Workspace: directory}
	if err := cli.Prepare(); err != nil {
		t.Fatal(err)
	}
	definitions, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || definitions[0].Name != "demo" {
		t.Fatalf("unexpected definitions: %#v", definitions)
	}
	output, err := cli.Run(context.Background(), project, "demo", source, map[string]int{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if output != "complete" {
		t.Fatalf("unexpected output: %q", output)
	}
	if entries, err := os.ReadDir(filepath.Join(project, ".claude", "workflows")); err != nil || len(entries) != 0 {
		t.Fatalf("temporary project workflows remain: %#v, %v", entries, err)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--backend") ||
		!strings.Contains(string(args), "codex") ||
		!strings.Contains(string(args), "--allow-mutating") ||
		!strings.Contains(string(args), "--codex-yolo") ||
		!strings.Contains(string(args), project) {
		t.Fatalf("unrestricted flags missing: %s", args)
	}
	if got := cli.LocalPath(42); !strings.HasSuffix(got, "workflow-42.js") {
		t.Fatalf("unexpected local path: %s", got)
	}
}
