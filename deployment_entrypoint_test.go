package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNetworkAppCompatibilityEntrypointTranslatesExactCommands(t *testing.T) {
	home := t.TempDir()
	provider := filepath.Join(home, ".local", "bin", "nags")
	if err := os.MkdirAll(filepath.Dir(provider), 0o700); err != nil {
		t.Fatal(err)
	}
	providerScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HOME/provider-args\"\n"
	if err := os.WriteFile(provider, []byte(providerScript), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "deploy removes compatibility app positional",
			args: []string{"deploy", "factory", "--expected-commit", "abc123"},
			want: []string{"deploy", "--expected-commit", "abc123"},
		},
		{
			name: "rollback retains provider app positional",
			args: []string{"rollback", "factory", "--to", "deployment-1"},
			want: []string{"rollback", "factory", "--to", "deployment-1"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("bin/network-app", test.args...)
			command.Env = append(os.Environ(), "HOME="+home)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("entrypoint failed: %v: %s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(home, "provider-args"))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Fields(string(data))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("provider args = %#v, want %#v", got, test.want)
			}
		})
	}

	command := exec.Command("bin/network-app", "restart", "factory")
	command.Env = append(os.Environ(), "HOME="+home)
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 2 || !strings.Contains(string(output), "usage:") {
		t.Fatalf("unsupported command = %v, output %q", err, output)
	}
}

func TestNetworkAppCompatibilityEntrypointFailsWithoutProvider(t *testing.T) {
	command := exec.Command("bin/network-app", "deploy", "factory", "--expected-commit", "abc123")
	command.Env = append(os.Environ(), "HOME="+t.TempDir())
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("entrypoint succeeded without the provider")
	}
	if !strings.Contains(string(output), "deployment provider is unavailable") {
		t.Fatalf("missing-provider output = %q", output)
	}
}
