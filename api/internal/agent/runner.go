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
)

type Runner interface {
	Run(context.Context, string) (string, error)
}

// CommandRunner invokes one unrestricted, ephemeral Codex process and returns
// its final plain-text response for the workflow conversation.
type CommandRunner struct {
	Command        string
	Workspace      string
	FactoryCommand string
	FactoryURL     string
}

func (r CommandRunner) Run(ctx context.Context, prompt string) (string, error) {
	if r.Command == "" || r.Workspace == "" || r.FactoryCommand == "" || r.FactoryURL == "" {
		return "", errors.New("agent command, workspace, Factory CLI, and URL are required")
	}
	factory, err := filepath.Abs(r.FactoryCommand)
	if err != nil {
		return "", fmt.Errorf("resolve Factory CLI: %w", err)
	}
	command := exec.CommandContext(
		ctx,
		r.Command,
		"exec",
		"--ephemeral",
		"--dangerously-bypass-approvals-and-sandbox",
		"--dangerously-bypass-hook-trust",
		"--ignore-rules",
		"--skip-git-repo-check",
		"-C", r.Workspace,
		"-",
	)
	command.Dir = r.Workspace
	command.Env = append(os.Environ(), "FACTORY_CLI="+factory, "FACTORY_URL="+r.FactoryURL)
	command.Stdin = strings.NewReader(prompt)
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
