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
	"sync"
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

type Log struct {
	Key     string
	Phase   string
	Kind    string
	Backend string
	Message string
	Result  json.RawMessage
	Error   string
	Done    bool
}

type Runner interface {
	List(context.Context) ([]Definition, error)
	Run(context.Context, string, string, string, state.Settings, any, func(Log)) (string, error)
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
	emit func(Log),
) (string, error) {
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
	command := exec.CommandContext(ctx, c.Command, commandArgs...)
	var stdout, stderr bytes.Buffer
	progress := &progressWriter{emit: emit}
	command.Stdout, command.Stderr = &stdout, io.MultiWriter(&stderr, progress)
	followContext, stopFollowing := context.WithCancel(ctx)
	followed := make(chan struct{})
	go func() {
		followJournal(followContext, journalPath, progress, emit)
		close(followed)
	}()
	err = command.Run()
	stopFollowing()
	<-followed
	progress.Flush()
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

type progressWriter struct {
	mu      sync.Mutex
	phase   string
	pending string
	emit    func(Log)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(data)
	for {
		index := strings.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		w.line(w.pending[:index])
		w.pending = w.pending[index+1:]
	}
	return len(data), nil
}

func (w *progressWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != "" {
		w.line(w.pending)
		w.pending = ""
	}
}

func (w *progressWriter) Phase() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.phase
}

func (w *progressWriter) line(line string) {
	index := strings.Index(line, "] ")
	if index < 0 {
		return
	}
	message := strings.TrimSpace(line[index+2:])
	if strings.HasPrefix(message, "phase: ") {
		w.phase = strings.TrimPrefix(message, "phase: ")
		return
	}
	if message == "" || strings.HasPrefix(message, "agent ") ||
		strings.HasPrefix(message, "gate ") || strings.HasPrefix(message, "note: ") {
		return
	}
	if w.emit != nil {
		w.emit(Log{Phase: w.phase, Kind: "log", Message: message, Done: true})
	}
}

func followJournal(ctx context.Context, path string, progress *progressWriter, emit func(Log)) {
	if emit == nil {
		return
	}
	var offset int
	sequence := 0
	pending := make(map[string][]Log)
	read := func() {
		data, err := os.ReadFile(path)
		if err != nil || len(data) <= offset {
			return
		}
		chunk := data[offset:]
		end := bytes.LastIndexByte(chunk, '\n')
		if end < 0 {
			return
		}
		offset += end + 1
		for _, line := range bytes.Split(chunk[:end], []byte{'\n'}) {
			var event struct {
				Type, Key, AgentID, Backend, Kind, Error string
				Result                                   json.RawMessage
			}
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			log := Log{
				Kind: event.Kind, Backend: event.Backend, Message: event.AgentID,
				Result: event.Result, Error: event.Error, Done: event.Type == "result",
			}
			if event.Type == "started" {
				sequence++
				log.Key = event.Key + ":" + strconv.Itoa(sequence)
				log.Phase = progress.Phase()
				pending[event.Key] = append(pending[event.Key], log)
			} else if queued := pending[event.Key]; len(queued) > 0 {
				log.Key, log.Phase = queued[0].Key, queued[0].Phase
				pending[event.Key] = queued[1:]
			} else {
				log.Key = event.Key
			}
			emit(log)
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			read()
			return
		case <-ticker.C:
			read()
		}
	}
}
