package agent

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
	"strings"
	"sync"

	"github.com/tomnagengast/factory/api/internal/state"
)

type AgentStep struct {
	Kind    string
	Label   string
	Content string
}

type Runner interface {
	Run(context.Context, state.Settings, string, func(AgentStep) error) (string, error)
}

// CommandRunner invokes one unrestricted, ephemeral agent process, emits each
// completed semantic step, and returns the final conversational response.
type CommandRunner struct {
	CodexCommand   string
	ClaudeCommand  string
	Workspace      string
	FactoryCommand string
	FactoryURL     string
}

func (r CommandRunner) Run(
	ctx context.Context,
	settings state.Settings,
	prompt string,
	emit func(AgentStep) error,
) (string, error) {
	if r.CodexCommand == "" || r.ClaudeCommand == "" || r.Workspace == "" ||
		r.FactoryCommand == "" || r.FactoryURL == "" || emit == nil {
		return "", errors.New("agent commands, workspace, Factory CLI, URL, and step emitter are required")
	}
	factory, err := filepath.Abs(r.FactoryCommand)
	if err != nil {
		return "", fmt.Errorf("resolve Factory CLI: %w", err)
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	var command *exec.Cmd
	switch settings.Harness {
	case state.Codex:
		command = exec.CommandContext(runContext, r.CodexCommand,
			"exec",
			"--json",
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
		command = exec.CommandContext(runContext, r.ClaudeCommand,
			"--print", prompt,
			"--verbose",
			"--output-format", "stream-json",
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
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("agent stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("agent stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("start agent process: %w", err)
	}

	var stderrBuffer bytes.Buffer
	var stderrErr error
	var stderrWait sync.WaitGroup
	stderrWait.Add(1)
	go func() {
		defer stderrWait.Done()
		_, stderrErr = io.Copy(&stderrBuffer, stderr)
	}()

	normalizer := newAgentStream(settings.Harness, emit)
	decodeErr := decodeAgentStream(stdout, normalizer.handle)
	if decodeErr != nil {
		cancel()
	}
	stderrWait.Wait()
	waitErr := command.Wait()
	if decodeErr != nil {
		return normalizer.final(), decodeErr
	}
	if stderrErr != nil {
		return normalizer.final(), fmt.Errorf("read agent stderr: %w", stderrErr)
	}
	if ctx.Err() != nil {
		return normalizer.final(), ctx.Err()
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderrBuffer.String())
		if message != "" {
			return normalizer.final(), fmt.Errorf("agent process: %w: %s", waitErr, message)
		}
		return normalizer.final(), fmt.Errorf("agent process: %w", waitErr)
	}
	if normalizer.semanticErr != nil {
		return normalizer.final(), normalizer.semanticErr
	}
	return normalizer.final(), nil
}

func decodeAgentStream(reader io.Reader, handle func(json.RawMessage) error) error {
	decoder := json.NewDecoder(reader)
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode agent event: %w", err)
		}
		if err := handle(raw); err != nil {
			return fmt.Errorf("record agent step: %w", err)
		}
	}
}

type agentStream struct {
	harness     string
	emit        func(AgentStep) error
	candidate   string
	fallback    string
	semanticErr error
	toolStarted map[string]bool
	toolLabels  map[string]string
}

func newAgentStream(harness string, emit func(AgentStep) error) *agentStream {
	return &agentStream{
		harness: harness, emit: emit,
		toolStarted: make(map[string]bool), toolLabels: make(map[string]string),
	}
}

func (s *agentStream) handle(raw json.RawMessage) error {
	switch s.harness {
	case state.Codex:
		return s.handleCodex(raw)
	case state.Claude:
		return s.handleClaude(raw)
	default:
		return fmt.Errorf("unknown harness %q", s.harness)
	}
}

func (s *agentStream) final() string {
	if strings.TrimSpace(s.candidate) != "" {
		return s.candidate
	}
	return s.fallback
}

func (s *agentStream) setCandidate(content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if err := s.flushCandidate(); err != nil {
		return err
	}
	s.candidate = content
	return nil
}

func (s *agentStream) flushCandidate() error {
	if strings.TrimSpace(s.candidate) == "" {
		s.candidate = ""
		return nil
	}
	step := AgentStep{Kind: "message", Content: s.candidate}
	s.candidate = ""
	return s.emit(step)
}

func (s *agentStream) emitStep(step AgentStep) error {
	if strings.TrimSpace(step.Content) == "" {
		return nil
	}
	if err := s.flushCandidate(); err != nil {
		return err
	}
	return s.emit(step)
}

type codexEvent struct {
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Error   json.RawMessage `json:"error"`
	Item    codexItem       `json:"item"`
}

type codexItem struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Message          string          `json:"message"`
	Command          string          `json:"command"`
	AggregatedOutput string          `json:"aggregated_output"`
	ExitCode         *int            `json:"exit_code"`
	Status           string          `json:"status"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           json.RawMessage `json:"result"`
	Error            json.RawMessage `json:"error"`
	Query            string          `json:"query"`
	Changes          json.RawMessage `json:"changes"`
}

func (s *agentStream) handleCodex(raw json.RawMessage) error {
	var event codexEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	switch event.Type {
	case "thread.started", "thread.closed", "turn.started", "turn.completed":
		return nil
	case "item.started":
		return s.codexToolUse(event.Item)
	case "item.updated":
		return nil
	case "item.completed":
		return s.codexCompleted(event.Item, raw)
	case "error", "turn.failed":
		content := event.Message
		if content == "" {
			content = rawText(event.Error)
		}
		if content == "" {
			content = string(raw)
		}
		if err := s.emitStep(AgentStep{Kind: "error", Label: "codex", Content: content}); err != nil {
			return err
		}
		s.semanticErr = errors.New(content)
		if s.fallback == "" {
			s.fallback = content
		}
		return nil
	default:
		return s.emitStep(AgentStep{Kind: "event", Label: event.Type, Content: string(raw)})
	}
}

func (s *agentStream) codexToolUse(item codexItem) error {
	if s.toolStarted[item.ID] {
		return nil
	}
	var label, content string
	switch item.Type {
	case "command_execution":
		label, content = "command", item.Command
	case "mcp_tool_call":
		label = strings.Trim(strings.Join([]string{item.Server, firstNonempty(item.Tool, item.Name)}, "."), ".")
		content = rawText(item.Arguments)
	case "web_search":
		label, content = "web search", item.Query
	case "file_change":
		label, content = "file change", rawText(item.Changes)
	default:
		return nil
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	s.toolStarted[item.ID] = true
	return s.emitStep(AgentStep{Kind: "tool-use", Label: label, Content: content})
}

func (s *agentStream) codexCompleted(item codexItem, raw json.RawMessage) error {
	switch item.Type {
	case "agent_message":
		return s.setCandidate(item.Text)
	case "reasoning":
		return s.emitStep(AgentStep{Kind: "reasoning", Label: "codex", Content: item.Text})
	case "command_execution":
		if err := s.codexToolUse(item); err != nil {
			return err
		}
		content := item.AggregatedOutput
		if content == "" && item.ExitCode != nil {
			content = fmt.Sprintf("Exit code: %d", *item.ExitCode)
		}
		return s.emitStep(AgentStep{Kind: "tool-output", Label: "command", Content: content})
	case "mcp_tool_call":
		if err := s.codexToolUse(item); err != nil {
			return err
		}
		label := strings.Trim(strings.Join([]string{item.Server, firstNonempty(item.Tool, item.Name)}, "."), ".")
		if len(item.Error) > 0 && string(item.Error) != "null" {
			return s.emitStep(AgentStep{Kind: "error", Label: label, Content: rawText(item.Error)})
		}
		return s.emitStep(AgentStep{Kind: "tool-output", Label: label, Content: rawText(item.Result)})
	case "web_search", "file_change":
		return s.codexToolUse(item)
	case "error":
		content := firstNonempty(item.Message, item.Text, rawText(item.Error))
		if content == "" {
			content = string(raw)
		}
		if err := s.emitStep(AgentStep{Kind: "error", Label: "codex", Content: content}); err != nil {
			return err
		}
		s.semanticErr = errors.New(content)
		if s.fallback == "" {
			s.fallback = content
		}
		return nil
	default:
		return s.emitStep(AgentStep{Kind: "event", Label: item.Type, Content: string(raw)})
	}
}

type claudeEvent struct {
	Type    string        `json:"type"`
	Subtype string        `json:"subtype"`
	IsError bool          `json:"is_error"`
	Result  string        `json:"result"`
	Message claudeMessage `json:"message"`
}

type claudeMessage struct {
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (s *agentStream) handleClaude(raw json.RawMessage) error {
	var event claudeEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	switch event.Type {
	case "system":
		if event.Subtype == "init" {
			return nil
		}
		label := "system"
		if event.Subtype != "" {
			label += "/" + event.Subtype
		}
		return s.emitStep(AgentStep{Kind: "event", Label: label, Content: string(raw)})
	case "stream_event":
		return nil
	case "assistant":
		for _, block := range event.Message.Content {
			if err := s.claudeBlock(block, raw); err != nil {
				return err
			}
		}
		return nil
	case "user":
		for _, block := range event.Message.Content {
			if block.Type != "tool_result" {
				continue
			}
			kind := "tool-output"
			if block.IsError {
				kind = "error"
			}
			if err := s.emitStep(AgentStep{
				Kind: kind, Label: firstNonempty(s.toolLabels[block.ToolUseID], block.ToolUseID),
				Content: rawText(block.Content),
			}); err != nil {
				return err
			}
		}
		return nil
	case "result":
		if event.Result != "" {
			s.fallback = event.Result
		}
		if event.IsError || (event.Subtype != "" && event.Subtype != "success") {
			content := event.Result
			if content == "" {
				content = string(raw)
			}
			if err := s.emitStep(AgentStep{Kind: "error", Label: event.Subtype, Content: content}); err != nil {
				return err
			}
			s.semanticErr = errors.New(content)
			if s.fallback == "" {
				s.fallback = content
			}
		}
		return nil
	default:
		return s.emitStep(AgentStep{Kind: "event", Label: event.Type, Content: string(raw)})
	}
}

func (s *agentStream) claudeBlock(block claudeBlock, raw json.RawMessage) error {
	switch block.Type {
	case "text":
		return s.setCandidate(block.Text)
	case "thinking":
		return s.emitStep(AgentStep{Kind: "reasoning", Label: "claude", Content: block.Thinking})
	case "tool_use":
		s.toolStarted[block.ID] = true
		s.toolLabels[block.ID] = block.Name
		return s.emitStep(AgentStep{Kind: "tool-use", Label: block.Name, Content: rawText(block.Input)})
	case "tool_result":
		kind := "tool-output"
		if block.IsError {
			kind = "error"
		}
		return s.emitStep(AgentStep{
			Kind: kind, Label: firstNonempty(s.toolLabels[block.ToolUseID], block.ToolUseID),
			Content: rawText(block.Content),
		})
	default:
		return s.emitStep(AgentStep{Kind: "event", Label: block.Type, Content: string(raw)})
	}
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	var formatted bytes.Buffer
	encoder := json.NewEncoder(&formatted)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return string(raw)
	}
	return strings.TrimSuffix(formatted.String(), "\n")
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
