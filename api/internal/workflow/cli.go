package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tomnagengast/factory/api/internal/state"
)

type Definition struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Scope       string   `json:"scope"`
	Description string   `json:"description"`
	Phases      []string `json:"phases"`
	Mutating    bool     `json:"mutating"`
}

type Runner interface {
	List(context.Context) ([]Definition, error)
	Run(context.Context, string, string, string, state.Settings, any) (string, error)
	LocalPath(int64) string
}

type CLI struct {
	Command       string
	Workspace     string
	CodexCommand  string
	ClaudeCommand string
}

func (c CLI) Prepare() error {
	if c.Command == "" || c.Workspace == "" || c.CodexCommand == "" || c.ClaudeCommand == "" {
		return errors.New("workflow command, workspace, and agent commands are required")
	}
	return os.MkdirAll(filepath.Join(c.Workspace, ".claude", "workflows"), 0o777)
}

func (c CLI) List(ctx context.Context) ([]Definition, error) {
	output, err := exec.CommandContext(ctx, c.Command, "--cwd", c.Workspace, "list", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}
	var definitions []Definition
	if err := json.Unmarshal(output, &definitions); err != nil {
		return nil, fmt.Errorf("decode workflows: %w", err)
	}
	return definitions, nil
}

func (c CLI) Run(
	ctx context.Context,
	directory, name, source string,
	settings state.Settings,
	args any,
) (string, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode workflow arguments: %w", err)
	}
	if directory == "" {
		directory = c.Workspace
	} else {
		workflowDirectory := filepath.Join(directory, ".claude", "workflows")
		if err := os.MkdirAll(workflowDirectory, 0o777); err != nil {
			return "", fmt.Errorf("prepare project workflows: %w", err)
		}
		link, err := os.CreateTemp(workflowDirectory, "~factory-*.js")
		if err != nil {
			return "", fmt.Errorf("prepare project workflow: %w", err)
		}
		link.Close()
		os.Remove(link.Name())
		if err := os.Symlink(source, link.Name()); err != nil {
			return "", fmt.Errorf("link project workflow: %w", err)
		}
		defer os.Remove(link.Name())
	}
	commandArgs := []string{
		"--cwd", directory,
		"run", name,
		"--args", string(encoded),
		"--backend", settings.Harness,
		"--model", settings.Model,
		"--allow-mutating",
		"--no-validate",
	}
	switch settings.Harness {
	case state.Codex:
		commandArgs = append(commandArgs,
			"--codex-bin", c.CodexCommand,
			"--codex-yolo",
			"--codex-arg", "-c",
			"--codex-arg", `model_reasoning_effort="`+settings.Reasoning+`"`,
		)
	case state.Claude:
		commandArgs = append(commandArgs,
			"--claude-bin", c.ClaudeCommand,
			"--claude-yolo",
			"--claude-arg", "--effort",
			"--claude-arg", settings.Reasoning,
		)
	default:
		return "", fmt.Errorf("unknown harness %q", settings.Harness)
	}
	command := exec.CommandContext(ctx, c.Command, commandArgs...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	output := strings.TrimSpace(stdout.String())
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return output, fmt.Errorf("run workflow: %w: %s", err, message)
		}
		return output, fmt.Errorf("run workflow: %w", err)
	}
	return output, nil
}

func (c CLI) LocalPath(id int64) string {
	return filepath.Join(c.Workspace, ".claude", "workflows", "workflow-"+strconv.FormatInt(id, 10)+".js")
}
