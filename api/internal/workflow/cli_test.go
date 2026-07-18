package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomnagengast/factory/api/internal/state"
)

func TestCLIListsAndRunsWorkflows(t *testing.T) {
	directory := t.TempDir()
	project := filepath.Join(directory, "project")
	source := filepath.Join(directory, "demo.js")
	logPath := filepath.Join(directory, "args")
	command := filepath.Join(directory, "workflow")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + logPath + "\n" +
		"if [ \"$3\" = \"list\" ]; then printf '[{\"name\":\"demo\",\"path\":\"/demo.js\",\"scope\":\"user\",\"description\":\"Demo\",\"phases\":[\"Run\"],\"mutating\":false}]'; " +
		"else cwd=\"$2\"; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = \"--journal\" ]; then shift; journal=\"$1\"; fi; shift; done; " +
		"set -- \"$cwd\"/.claude/workflows/~factory-*.js; [ -L \"$1\" ] || exit 9; " +
		"printf '[workflow:demo] phase: Review\\n[workflow:demo] Checking inputs\\n' >&2; " +
		"printf '%s\\n' '{\"type\":\"started\",\"key\":\"one\",\"agentId\":\"reviewer\",\"backend\":\"codex\",\"kind\":\"agent\"}' " +
		"'{\"type\":\"started\",\"key\":\"one\",\"agentId\":\"reviewer\",\"backend\":\"codex\",\"kind\":\"agent\"}' " +
		"'{\"type\":\"result\",\"key\":\"one\",\"agentId\":\"reviewer\",\"backend\":\"codex\",\"kind\":\"agent\",\"result\":\"done\"}' " +
		"'{\"type\":\"result\",\"key\":\"one\",\"agentId\":\"reviewer\",\"backend\":\"codex\",\"kind\":\"agent\",\"result\":\"done\"}' > \"$journal\"; " +
		"printf 'complete'; fi\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("export const meta = { name: 'demo' }"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := CLI{
		Command: command, Workspace: directory,
		CodexCommand: "custom-codex", ClaudeCommand: "custom-claude",
	}
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
	for _, settings := range []state.Settings{
		{Harness: state.Codex, Model: "gpt-5.6-sol", Reasoning: "high"},
		{Harness: state.Claude, Model: "sonnet", Reasoning: "medium"},
	} {
		var logs []Log
		output, err := cli.Run(context.Background(), project, "demo", source, settings, map[string]int{"id": 1},
			func(log Log) { logs = append(logs, log) })
		if err != nil {
			t.Fatal(err)
		}
		if output != "complete" {
			t.Fatalf("unexpected output: %q", output)
		}
		if len(logs) != 5 || logs[0].Kind != "log" || logs[0].Phase != "Review" ||
			logs[1].Done || logs[2].Done || !logs[3].Done ||
			logs[1].Key == logs[2].Key || string(logs[3].Result) != `"done"` {
			t.Fatalf("unexpected workflow logs: %#v", logs)
		}
		if entries, err := os.ReadDir(filepath.Join(project, ".claude", "workflows")); err != nil || len(entries) != 0 {
			t.Fatalf("temporary project workflows remain: %#v, %v", entries, err)
		}
		args, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{
			"--backend", settings.Harness, settings.Model, settings.Reasoning,
			"--allow-mutating", "--" + settings.Harness + "-yolo", project,
		} {
			if !strings.Contains(string(args), expected) {
				t.Fatalf("%q missing from args: %s", expected, args)
			}
		}
	}
	if got := cli.LocalPath(42); !strings.HasSuffix(got, "workflow-42.js") {
		t.Fatalf("unexpected local path: %s", got)
	}
}
