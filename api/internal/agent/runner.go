package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Runner interface {
	Run(context.Context, string) (string, error)
}

// CommandRunner invokes one unrestricted, ephemeral Codex process and returns
// its final plain-text response for the workflow conversation.
type CommandRunner struct {
	Command   string
	Workspace string
}

func (r CommandRunner) Run(ctx context.Context, prompt string) (string, error) {
	if r.Command == "" || r.Workspace == "" {
		return "", errors.New("agent command and workspace are required")
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
	command.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
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
