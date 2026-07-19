package workflow

import (
	"context"
	"errors"
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
	envPath := filepath.Join(directory, "env")
	command := filepath.Join(directory, "workflow")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + logPath + "\n" +
		"if [ \"$3\" = \"list\" ]; then printf '[{\"name\":\"demo\",\"path\":\"/demo.js\",\"scope\":\"user\",\"description\":\"Demo\",\"phases\":[\"Run\"],\"mutating\":false}]'; " +
		"else while [ \"$#\" -gt 0 ]; do if [ \"$1\" = \"--journal\" ]; then shift; journal=\"$1\"; fi; shift; done; " +
		"printf '%s\\n%s\\n' \"$FACTORY_CLI\" \"$FACTORY_URL\" > " + envPath + "; " +
		"printf 'human presentation only\\n' >&2; " +
		"printf '%s\\n' " +
		"'{\"sequence\":1,\"at\":\"2026-07-17T12:00:00Z\",\"type\":\"runtime.started\",\"workflow\":\"demo\",\"backend\":\"codex\"}' " +
		"'{\"sequence\":2,\"at\":\"2026-07-17T12:00:01Z\",\"type\":\"phase.started\",\"workflow\":\"demo\",\"phase\":\"Review\"}' " +
		"'{\"sequence\":3,\"at\":\"2026-07-17T12:00:02Z\",\"type\":\"log\",\"workflow\":\"demo\",\"phase\":\"Review\",\"message\":\"Checking inputs\"}' " +
		"'{\"sequence\":4,\"at\":\"2026-07-17T12:00:03Z\",\"type\":\"step.started\",\"workflow\":\"demo\",\"phase\":\"Review\",\"stepId\":1,\"key\":\"one\",\"agentId\":\"reviewer\",\"backend\":\"codex\",\"kind\":\"agent\",\"message\":\"Review it\"}' " +
		"'{\"sequence\":5,\"at\":\"2026-07-17T12:00:04Z\",\"type\":\"step.completed\",\"workflow\":\"demo\",\"phase\":\"Review\",\"stepId\":1,\"key\":\"one\",\"agentId\":\"reviewer\",\"backend\":\"codex\",\"kind\":\"agent\",\"result\":\"done\"}' " +
		"'{\"sequence\":6,\"at\":\"2026-07-17T12:00:05Z\",\"type\":\"runtime.completed\",\"workflow\":\"demo\",\"phase\":\"Review\",\"result\":\"complete\",\"extension\":{\"kept\":true}}' > \"$journal\"; " +
		"printf 'complete'; fi\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("export const meta = { name: 'demo' }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	cli := CLI{
		Command: command, Workspace: directory,
		CodexCommand: "custom-codex", ClaudeCommand: "custom-claude",
		FactoryCommand: filepath.Join(directory, "factory"), FactoryURL: "http://127.0.0.1:8092",
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
		var events []Event
		output, err := cli.Run(context.Background(), project, source, settings, map[string]int{"id": 1},
			func(event Event) error {
				events = append(events, event)
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if output != "complete" {
			t.Fatalf("unexpected output: %q", output)
		}
		if len(events) != 6 || events[0].Type != "runtime.started" ||
			events[2].Message != "Checking inputs" || events[3].StepID != 1 ||
			string(events[4].Result) != `"done"` || events[5].Type != "runtime.completed" ||
			!strings.Contains(string(events[5].Raw), `"extension":{"kept":true}`) {
			t.Fatalf("unexpected workflow events: %#v", events)
		}
		for index, event := range events {
			if event.Sequence != int64(index+1) || len(event.Raw) == 0 {
				t.Fatalf("event %d was not forwarded losslessly: %#v", index, event)
			}
		}
		if _, err := os.Stat(filepath.Join(project, ".claude")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("workflow run created project discovery files: %v", err)
		}
		args, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(args), "--cwd\n"+project+"\nrun\n"+source+"\n") {
			t.Fatalf("workflow source and working directory are not independent: %s", args)
		}
		for _, expected := range []string{
			"--backend", settings.Harness, settings.Model, settings.Reasoning,
			"--allow-mutating", "--" + settings.Harness + "-yolo", project,
		} {
			if !strings.Contains(string(args), expected) {
				t.Fatalf("%q missing from args: %s", expected, args)
			}
		}
		environment, err := os.ReadFile(envPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(environment) != filepath.Join(directory, "factory")+"\nhttp://127.0.0.1:8092\n" {
			t.Fatalf("unexpected Factory environment: %s", environment)
		}
	}
	if got := cli.LocalPath(42); !strings.HasSuffix(got, "workflow-42.js") {
		t.Fatalf("unexpected local path: %s", got)
	}
}

func TestCLIValidatesWorkflow(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "demo.js")
	command := filepath.Join(directory, "workflow")
	script := "#!/bin/sh\n" +
		"if [ \"$3\" = \"validate\" ] && [ \"$4\" = \"" + source + "\" ]; then exit 0; fi\n" +
		"printf 'parse error near demo.js\\n' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cli := CLI{Command: command, Workspace: directory}
	if err := cli.Validate(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if err := cli.Validate(context.Background(), filepath.Join(directory, "bad.js")); err == nil ||
		!strings.Contains(err.Error(), "parse error near demo.js") {
		t.Fatalf("validation error = %v", err)
	}
	if err := cli.Validate(context.Background(), " "); err == nil ||
		!strings.Contains(err.Error(), "source path is required") {
		t.Fatalf("empty source error = %v", err)
	}
}

func TestCLICancelsWorkflowWhenEventCannotBeRecorded(t *testing.T) {
	directory := t.TempDir()
	command := filepath.Join(directory, "workflow")
	script := "#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do if [ \"$1\" = \"--journal\" ]; then shift; journal=\"$1\"; fi; shift; done\n" +
		"printf '%s\\n' '{\"sequence\":1,\"at\":\"2026-07-17T12:00:00Z\",\"type\":\"runtime.started\",\"workflow\":\"demo\"}' > \"$journal\"\n" +
		"while :; do :; done\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cli := CLI{
		Command: command, Workspace: directory,
		CodexCommand: "codex", ClaudeCommand: "claude",
		FactoryCommand: filepath.Join(directory, "factory"), FactoryURL: "http://127.0.0.1:8092",
	}
	if err := cli.Prepare(); err != nil {
		t.Fatal(err)
	}
	_, err := cli.Run(
		context.Background(), "", filepath.Join(directory, "demo.js"), state.DefaultSettings, nil,
		func(Event) error { return errors.New("wire unavailable") },
	)
	if err == nil || !strings.Contains(err.Error(), "wire unavailable") {
		t.Fatalf("run error = %v", err)
	}
}
