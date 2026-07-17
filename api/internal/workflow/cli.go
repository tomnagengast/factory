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
	Run(context.Context, string, any) (string, error)
	LocalPath(int64) string
}

type CLI struct {
	Command   string
	Workspace string
}

func (c CLI) Prepare() error {
	if c.Command == "" || c.Workspace == "" {
		return errors.New("workflow command and workspace are required")
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

func (c CLI) Run(ctx context.Context, name string, args any) (string, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode workflow arguments: %w", err)
	}
	command := exec.CommandContext(
		ctx,
		c.Command,
		"--cwd", c.Workspace,
		"run", name,
		"--args", string(encoded),
		"--backend", "codex",
		"--allow-mutating",
		"--no-validate",
		"--codex-yolo",
	)
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
