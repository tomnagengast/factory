//go:build workflow_cli_integration

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/workflow"
)

func TestLoopRetriesFailedRunThroughSupportedWorkflowCLI(t *testing.T) {
	workflowCommand := strings.TrimSpace(os.Getenv("FACTORY_TEST_WORKFLOW_CLI"))
	if workflowCommand == "" {
		t.Fatal("FACTORY_TEST_WORKFLOW_CLI must point to the official workflow v0.0.6 binary")
	}
	version, err := exec.Command(workflowCommand, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("workflow --version: %v: %s", err, version)
	}
	if got := strings.TrimSpace(string(version)); got != "0.0.6" {
		t.Fatalf("workflow version = %q, want exactly 0.0.6", got)
	}

	directory := t.TempDir()
	source := filepath.Join(directory, "retry-integration.js")
	calls := filepath.Join(directory, "calls.txt")
	failedOnce := filepath.Join(directory, "unfinished-failed")
	fakeCodex := filepath.Join(directory, "fake-codex")
	sourceText := `export const meta = {
  name: "retry-integration",
  description: "Prove failed-journal retry caching.",
  phases: ["Work"],
  mutating: false,
};
phase("Work");
const completed = await agent("completed step");
const unfinished = await agent("unfinished step");
if (unfinished == null) throw new Error("unfinished step failed");
return { completed, unfinished };
`
	if err := os.WriteFile(source, []byte(sourceText), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeScript := fmt.Sprintf(`#!/bin/sh
output=""
prompt=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    output="$1"
  fi
  prompt="$1"
  shift
done
case "$prompt" in
  *"completed step"*) label="completed" ;;
  *"unfinished step"*) label="unfinished" ;;
  *) label="unexpected" ;;
esac
printf '%%s\n' "$label" >> %q
if [ "$label" = "unfinished" ] && [ ! -e %q ]; then
  : > %q
  printf 'usage limit exhausted\n' >&2
  exit 9
fi
printf 'result-%%s' "$label" > "$output"
`, calls, failedOnce, failedOnce)
	if err := os.WriteFile(fakeCodex, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	wire := openWire(t)
	defer wire.Close()
	workflowEvent, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{
		Name: "retry-integration", Path: &source, Phases: []string{"Work"},
	})
	wire.Publish(state.TriggerCreated, state.TriggerData{
		EventType: "retry.integration", WorkflowID: workflowEvent.ID, Enabled: true,
	})
	wire.Publish("retry.integration", map[string]bool{"ready": true})
	runner := workflow.CLI{
		Command: workflowCommand, Workspace: directory,
		CodexCommand: fakeCodex, ClaudeCommand: "claude",
		FactoryCommand: filepath.Join(directory, "factory"), FactoryURL: "http://127.0.0.1:1",
	}
	if err := runner.Prepare(); err != nil {
		t.Fatal(err)
	}
	loop, err := newTestLoop(wire, &fakeAgent{}, runner)
	if err != nil {
		t.Fatal(err)
	}

	worked, _, err := loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("initial dispatch = %v, %v", worked, err)
	}
	waitForActiveCount(t, loop, 0)
	view, _, err := wire.Snapshot()
	if err != nil || len(view.Runs) != 1 || view.Runs[0].Status != "failed" {
		t.Fatalf("first attempt = %#v, %v", view.Runs, err)
	}
	runID := view.Runs[0].ID
	if _, err := wire.Publish(state.WorkflowRunRetryRequested, state.WorkflowRunStateData{RunID: runID}); err != nil {
		t.Fatal(err)
	}
	worked, _, err = loop.step(context.Background())
	if err != nil || !worked {
		t.Fatalf("retry dispatch = %v, %v", worked, err)
	}
	waitForActiveCount(t, loop, 0)

	view, _, err = wire.Snapshot()
	if err != nil || len(view.Runs) != 1 || view.Runs[0].ID != runID ||
		view.Runs[0].Status != "completed" || view.Workflows[0].RunCount != 1 {
		t.Fatalf("retried run = %#v, workflows=%#v err=%v", view.Runs, view.Workflows, err)
	}
	callBytes, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(callBytes)); !slices.Equal(got, []string{"completed", "unfinished", "unfinished"}) {
		t.Fatalf("backend calls = %v", got)
	}
	events, err := wire.RunEvents(runID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, len(events))
	for index, event := range events {
		types[index] = event.Type
		if event.Sequence != int64(index+1) {
			t.Fatalf("journal sequence %d = %d", index, event.Sequence)
		}
	}
	for _, required := range []string{
		"step.completed", "step.failed", "runtime.failed", "runtime.resumed", "step.cached", "runtime.completed",
	} {
		if !slices.Contains(types, required) {
			t.Fatalf("journal types %v omit %s", types, required)
		}
	}
	if eventTypeCount(wire.Events(0), state.WorkflowRunStarted) != 1 {
		t.Fatalf("retry created another run: %#v", wire.Events(0))
	}
}
