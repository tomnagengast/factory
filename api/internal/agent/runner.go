package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tomnagengast/factory/api/internal/state"
)

type Runner interface {
	Run(context.Context, state.Settings, string) (string, error)
}

// CommandRunner invokes one unrestricted, ephemeral agent process and returns
// its final plain-text response for the workflow conversation.
type CommandRunner struct {
	CodexCommand   string
	ClaudeCommand  string
	Workspace      string
	FactoryCommand string
	FactoryURL     string
}

func (r CommandRunner) Run(ctx context.Context, settings state.Settings, prompt string) (string, error) {
	if r.CodexCommand == "" || r.ClaudeCommand == "" || r.Workspace == "" ||
		r.FactoryCommand == "" || r.FactoryURL == "" {
		return "", errors.New("agent commands, workspace, Factory CLI, and URL are required")
	}
	factory, err := filepath.Abs(r.FactoryCommand)
	if err != nil {
		return "", fmt.Errorf("resolve Factory CLI: %w", err)
	}
	var command *exec.Cmd
	switch settings.Harness {
	case state.Codex:
		command = exec.CommandContext(ctx, r.CodexCommand,
			"exec",
			"--ephemeral",
			"--dangerously-bypass-approvals-and-sandbox",
			"--dangerously-bypass-hook-trust",
			"--ignore-rules",
			"--skip-git-repo-check",
			"--model", settings.Model,
			"--config", `model_reasoning_effort="`+settings.Reasoning+`"`,
			"-C", r.Workspace,
			"-",
		)
		command.Stdin = strings.NewReader(prompt)
	case state.Claude:
		command = exec.CommandContext(ctx, r.ClaudeCommand,
			"--print", prompt,
			"--output-format", "text",
			"--model", settings.Model,
			"--effort", settings.Reasoning,
			"--dangerously-skip-permissions",
			"--no-session-persistence",
		)
	default:
		return "", fmt.Errorf("unknown harness %q", settings.Harness)
	}
	command.Dir = r.Workspace
	command.Env = append(os.Environ(), "FACTORY_CLI="+factory, "FACTORY_URL="+r.FactoryURL)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	output := strings.TrimSpace(stdout.String())
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return output, fmt.Errorf("agent process: %w: %s", err, message)
		}
		return output, fmt.Errorf("agent process: %w", err)
	}
	return output, nil
}
