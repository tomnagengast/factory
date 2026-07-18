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
	"time"

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

type Event struct {
	Raw         json.RawMessage `json:"-"`
	Sequence    int64           `json:"sequence"`
	At          time.Time       `json:"at"`
	Type        string          `json:"type"`
	Workflow    string          `json:"workflow"`
	Phase       string          `json:"phase,omitempty"`
	StepID      int64           `json:"stepId,omitempty"`
	Key         string          `json:"key,omitempty"`
	AgentID     string          `json:"agentId,omitempty"`
	Backend     string          `json:"backend,omitempty"`
	Kind        string          `json:"kind,omitempty"`
	Message     string          `json:"message,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	Tokens      int64           `json:"tokens,omitempty"`
	Concurrency int             `json:"concurrency,omitempty"`
	Budget      *int64          `json:"budget,omitempty"`
}

type Runner interface {
	List(context.Context) ([]Definition, error)
	Run(context.Context, string, string, string, state.Settings, any, func(Event) error) (string, error)
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
	emit func(Event) error,
) (string, error) {
	if emit == nil {
		return "", errors.New("workflow event sink is required")
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode workflow arguments: %w", err)
	}
	journal, err := os.CreateTemp("", "factory-workflow-*.jsonl")
	if err != nil {
		return "", fmt.Errorf("prepare workflow journal: %w", err)
	}
	journalPath := journal.Name()
	journal.Close()
	defer os.Remove(journalPath)
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
		"--journal", journalPath,
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
	commandContext, cancelCommand := context.WithCancel(ctx)
	defer cancelCommand()
	command := exec.CommandContext(commandContext, c.Command, commandArgs...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	followContext, stopFollowing := context.WithCancel(ctx)
	followed := make(chan error, 1)
	go func() {
		err := followJournal(followContext, journalPath, emit)
		if err != nil {
			cancelCommand()
		}
		followed <- err
	}()
	err = command.Run()
	stopFollowing()
	followErr := <-followed
	output := strings.TrimSpace(stdout.String())
	if followErr != nil {
		return output, fmt.Errorf("record workflow event: %w", followErr)
	}
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

func followJournal(ctx context.Context, path string, emit func(Event) error) error {
	var offset int
	var sequence int64
	read := func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(data) <= offset {
			return nil
		}
		chunk := data[offset:]
		end := bytes.LastIndexByte(chunk, '\n')
		if end < 0 {
			return nil
		}
		offset += end + 1
		for _, line := range bytes.Split(chunk[:end], []byte{'\n'}) {
			var event Event
			if err := json.Unmarshal(line, &event); err != nil {
				return fmt.Errorf("decode sequence %d: %w", sequence+1, err)
			}
			if event.Sequence != sequence+1 || event.Type == "" || event.Workflow == "" {
				return fmt.Errorf("invalid workflow event after sequence %d", sequence)
			}
			event.Raw = append(event.Raw, line...)
			if err := emit(event); err != nil {
				return err
			}
			sequence = event.Sequence
		}
		return nil
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return read()
		case <-ticker.C:
			if err := read(); err != nil {
				return err
			}
		}
	}
}
