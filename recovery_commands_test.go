package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStateRollbackHoldsLeaseAcrossExactProviderInvocation(t *testing.T) {
	stateRoot := t.TempDir()
	dataRoot := filepath.Join(stateRoot, "data")
	history := filepath.Join(stateRoot, "deployments", "history")
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(history, 0o700); err != nil {
		t.Fatal(err)
	}
	target := "deployment-1"
	receipt := map[string]any{
		"contractVersion": 1, "commandVersion": 1, "deploymentId": target, "buildId": "build-1",
		"status": "success", "app": "factory", "sourceRepository": "tomnagengast/factory", "sourceBranch": "main",
		"sourceCommit": strings.Repeat("a", 40), "sourceTree": strings.Repeat("b", 40),
		"manifestSha256": strings.Repeat("c", 64), "binarySha256": strings.Repeat("d", 64),
		"startedAt":  time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC),
		"finishedAt": time.Date(2026, time.July, 16, 18, 1, 0, 0, time.UTC),
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, target+".json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	providerArgsPath := filepath.Join(stateRoot, "provider-args")
	provider := filepath.Join(stateRoot, "nags")
	providerScript := "#!/bin/sh\n" +
		"[ -n \"$NAGS_FACTORY_STATE_LEASE_FD\" ] || exit 91\n" +
		"[ -n \"$NAGS_FACTORY_STATE_LEASE_TOKEN\" ] || exit 92\n" +
		"[ -r \"/dev/fd/$NAGS_FACTORY_STATE_LEASE_FD\" ] || exit 93\n" +
		"printf '%s\\n' \"$@\" > \"" + providerArgsPath + "\"\n"
	if err := os.WriteFile(provider, []byte(providerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	code := runStateRollback(context.Background(), []string{
		"--data-root", dataRoot, "--provider", provider, "--", "--to", target, "--json",
	}, &output, &errorOutput)
	if code != 0 {
		t.Fatalf("state rollback exit = %d, stderr = %q", code, errorOutput.String())
	}
	providerData, err := os.ReadFile(providerArgsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"rollback", "factory", "--to", target, "--json"}
	if got := strings.Fields(string(providerData)); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider args = %#v, want %#v", got, want)
	}
}

func TestRollbackTargetRequiresOneExactTarget(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--json"}, {"--to"}, {"--to", "one", "--to=two"}, {"--to="},
	} {
		if _, err := rollbackTarget(args); err == nil {
			t.Fatalf("rollback target accepted %#v", args)
		}
	}
	for _, args := range [][]string{{"--to", "one"}, {"--json", "--to=one"}} {
		if target, err := rollbackTarget(args); err != nil || target != "one" {
			t.Fatalf("rollback target %#v = %q, %v", args, target, err)
		}
	}
}

func TestLiveTmuxSessionsDistinguishesEmptyAndLiveServers(t *testing.T) {
	directory := t.TempDir()
	live := filepath.Join(directory, "tmux-live")
	if err := os.WriteFile(live, []byte("#!/bin/sh\nprintf 'factory-eng-47\\nfactory-eng-48\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sessions, err := liveTmuxSessions(context.Background(), live, "factory-agents")
	if err != nil || !reflect.DeepEqual(sessions, []string{"factory-eng-47", "factory-eng-48"}) {
		t.Fatalf("live sessions = %#v, %v", sessions, err)
	}
	empty := filepath.Join(directory, "tmux-empty")
	if err := os.WriteFile(empty, []byte("#!/bin/sh\nprintf 'no server running on /tmp/tmux' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sessions, err = liveTmuxSessions(context.Background(), empty, "factory-agents")
	if err != nil || len(sessions) != 0 {
		t.Fatalf("empty sessions = %#v, %v", sessions, err)
	}
}
