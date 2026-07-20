package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Schema      json.RawMessage `json:"schema,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	Tokens      int64           `json:"tokens,omitempty"`
	Concurrency int             `json:"concurrency,omitempty"`
	Budget      *int64          `json:"budget,omitempty"`
}

const HumanReviewExitCode = 75

var ErrHumanReview = errors.New("workflow suspended for human review")

type RunRequest struct {
	Directory string
	Source    string
	Settings  state.Settings
	Arguments any
	Resume    []json.RawMessage
}

type Runner interface {
	List(context.Context) ([]Definition, error)
	Validate(context.Context, string) error
	Run(context.Context, RunRequest, func(Event) error) (string, error)
	LocalPath(int64) string
}

type CLI struct {
	Command        string
	Workspace      string
	CodexCommand   string
	ClaudeCommand  string
	FactoryCommand string
	FactoryURL     string
}

func (c CLI) Prepare() error {
	if c.Command == "" || c.Workspace == "" || c.CodexCommand == "" || c.ClaudeCommand == "" ||
		c.FactoryCommand == "" || c.FactoryURL == "" {
		return errors.New("workflow command, workspace, agent commands, Factory CLI, and URL are required")
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

func (c CLI) Validate(ctx context.Context, source string) error {
	if strings.TrimSpace(source) == "" {
		return errors.New("workflow source path is required")
	}
	output, err := exec.CommandContext(
		ctx, c.Command, "--cwd", c.Workspace, "validate", source,
	).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("validate workflow: %w: %s", err, message)
		}
		return fmt.Errorf("validate workflow: %w", err)
	}
	return nil
}

func (c CLI) Run(
	ctx context.Context,
	request RunRequest,
	emit func(Event) error,
) (string, error) {
	if emit == nil {
		return "", errors.New("workflow event sink is required")
	}
	if strings.TrimSpace(request.Source) == "" {
		return "", errors.New("workflow source path is required")
	}
	encoded, err := json.Marshal(request.Arguments)
	if err != nil {
		return "", fmt.Errorf("encode workflow arguments: %w", err)
	}
	journal, err := os.CreateTemp("", "factory-workflow-*.jsonl")
	if err != nil {
		return "", fmt.Errorf("prepare workflow journal: %w", err)
	}
	journalPath := journal.Name()
	offset, sequence, err := seedJournal(journal, request.Resume)
	if err != nil {
		journal.Close()
		os.Remove(journalPath)
		return "", err
	}
	if err := journal.Close(); err != nil {
		os.Remove(journalPath)
		return "", fmt.Errorf("close workflow journal: %w", err)
	}
	defer os.Remove(journalPath)
	if request.Directory == "" {
		request.Directory = c.Workspace
	}
	commandArgs := []string{
		"--cwd", request.Directory,
		"run", request.Source,
		"--args", string(encoded),
		"--backend", request.Settings.Harness,
		"--model", request.Settings.Model,
		"--allow-mutating",
		"--no-validate",
		"--journal", journalPath,
	}
	if len(request.Resume) > 0 {
		commandArgs = append(commandArgs, "--resume", journalPath)
	}
	switch request.Settings.Harness {
	case state.Codex:
		commandArgs = append(commandArgs,
			"--codex-bin", c.CodexCommand,
			"--codex-yolo",
			"--codex-arg", "-c",
			"--codex-arg", `model_reasoning_effort="`+request.Settings.Reasoning+`"`,
		)
	case state.Claude:
		commandArgs = append(commandArgs,
			"--claude-bin", c.ClaudeCommand,
			"--claude-yolo",
			"--claude-arg", "--effort",
			"--claude-arg", request.Settings.Reasoning,
		)
	default:
		return "", fmt.Errorf("unknown harness %q", request.Settings.Harness)
	}
	commandContext, cancelCommand := context.WithCancel(ctx)
	defer cancelCommand()
	command := exec.CommandContext(commandContext, c.Command, commandArgs...)
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return command.Process.Signal(os.Interrupt)
	}
	command.WaitDelay = 2 * time.Second
	factory, err := filepath.Abs(c.FactoryCommand)
	if err != nil {
		return "", fmt.Errorf("resolve Factory CLI: %w", err)
	}
	command.Env = append(os.Environ(), "FACTORY_CLI="+factory, "FACTORY_URL="+c.FactoryURL)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	followContext, stopFollowing := context.WithCancel(ctx)
	followed := make(chan error, 1)
	go func() {
		err := followJournal(followContext, journalPath, offset, sequence, emit)
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
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == HumanReviewExitCode {
			return output, ErrHumanReview
		}
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return output, fmt.Errorf("run workflow: %w: %s", err, message)
		}
		return output, fmt.Errorf("run workflow: %w", err)
	}
	return output, nil
}

func seedJournal(file *os.File, events []json.RawMessage) (int, int64, error) {
	var offset int
	var sequence int64
	for _, raw := range events {
		var event Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return 0, 0, fmt.Errorf("decode resumed workflow event after sequence %d: %w", sequence, err)
		}
		if event.Sequence != sequence+1 || event.Type == "" || event.Workflow == "" {
			return 0, 0, fmt.Errorf("invalid resumed workflow event after sequence %d", sequence)
		}
		line := append(append([]byte(nil), raw...), '\n')
		written, err := file.Write(line)
		if err != nil {
			return 0, 0, fmt.Errorf("seed workflow journal: %w", err)
		}
		offset += written
		sequence = event.Sequence
	}
	return offset, sequence, nil
}

func (c CLI) LocalPath(id int64) string {
	return filepath.Join(c.Workspace, ".claude", "workflows", "workflow-"+strconv.FormatInt(id, 10)+".js")
}

func followJournal(
	ctx context.Context,
	path string,
	offset int,
	sequence int64,
	emit func(Event) error,
) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(int64(offset), io.SeekStart); err != nil {
		return err
	}
	buffer := make([]byte, 32<<10)
	pending := make([]byte, 0)
	read := func() error {
		for {
			count, readErr := file.Read(buffer)
			if count > 0 {
				pending = append(pending, buffer[:count]...)
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			for {
				end := bytes.IndexByte(pending, '\n')
				if end < 0 {
					break
				}
				line := pending[:end]
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
				pending = pending[end+1:]
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
		}
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
