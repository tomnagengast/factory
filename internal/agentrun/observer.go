package agentrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	maxObservedWindows = 16
	maxPaneBytes       = 128 << 10
	paneHistoryLines   = "-300"
	tmuxFieldSeparator = "|||FACTORY|||"
	tmuxWindowFormat   = "#{window_id}" + tmuxFieldSeparator + "#{window_name}" + tmuxFieldSeparator + "#{pane_current_command}"
)

var (
	ErrRunNotFound = errors.New("agent run not found")
	ansiSequence   = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

type AgentView struct {
	ID                string       `json:"id"`
	IssueIdentifier   string       `json:"issueIdentifier"`
	State             State        `json:"state"`
	Attempts          int          `json:"attempts"`
	DuplicateTriggers uint64       `json:"duplicateTriggers"`
	Detail            string       `json:"detail,omitempty"`
	CreatedAt         time.Time    `json:"createdAt"`
	UpdatedAt         time.Time    `json:"updatedAt"`
	StartedAt         *time.Time   `json:"startedAt,omitempty"`
	FinishedAt        *time.Time   `json:"finishedAt,omitempty"`
	ObservedAt        time.Time    `json:"observedAt"`
	Live              bool         `json:"live"`
	AttachCommand     string       `json:"attachCommand,omitempty"`
	Windows           []WindowView `json:"windows"`
}

type WindowView struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Command string     `json:"command"`
	Output  string     `json:"output"`
	Steps   []StepView `json:"steps"`
}

type StepView struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Status  string `json:"status,omitempty"`
	Summary string `json:"summary"`
	Payload string `json:"payload"`
}

type Observer struct {
	store      *Store
	tmuxPath   string
	tmuxSocket string
	redactions []string
	now        func() time.Time
	run        func(context.Context, ...string) ([]byte, error)
}

func NewObserver(store *Store, tmuxPath, tmuxSocket string, redactions []string, now func() time.Time) (*Observer, error) {
	if store == nil {
		return nil, errors.New("agent observer: run store is required")
	}
	if tmuxPath == "" || tmuxSocket == "" {
		return nil, errors.New("agent observer: tmux path and socket are required")
	}
	if now == nil {
		return nil, errors.New("agent observer: clock is required")
	}
	filtered := make([]string, 0, len(redactions))
	for _, value := range redactions {
		if value != "" {
			filtered = append(filtered, value)
		}
	}
	slices.SortFunc(filtered, func(a, b string) int { return len(b) - len(a) })

	observer := &Observer{
		store:      store,
		tmuxPath:   tmuxPath,
		tmuxSocket: tmuxSocket,
		redactions: filtered,
		now:        now,
	}
	observer.run = func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, observer.tmuxPath, args...).CombinedOutput()
	}
	return observer, nil
}

func (o *Observer) Observe(ctx context.Context, id string) (AgentView, error) {
	run, found := o.store.Find(id)
	if !found {
		return AgentView{}, ErrRunNotFound
	}
	view := AgentView{
		ID:                run.ID,
		IssueIdentifier:   run.IssueIdentifier,
		State:             run.State,
		Attempts:          run.Attempts,
		DuplicateTriggers: run.DuplicateTriggers,
		Detail:            o.redact(run.Detail),
		CreatedAt:         run.CreatedAt,
		UpdatedAt:         run.UpdatedAt,
		StartedAt:         run.StartedAt,
		FinishedAt:        run.FinishedAt,
		ObservedAt:        o.now().UTC(),
		Windows:           []WindowView{},
	}
	view.Attempts = max(view.Attempts, currentAttempt(run.RunDirectory))
	if run.SessionName == "" {
		return view, nil
	}
	if !run.State.Active() {
		windows, err := o.historyWindows(run.RunDirectory)
		if err != nil {
			return AgentView{}, err
		}
		view.Windows = windows
		return view, nil
	}

	exists, err := o.sessionExists(ctx, run.SessionName)
	if err != nil {
		return AgentView{}, err
	}
	if !exists {
		windows, historyErr := o.historyWindows(run.RunDirectory)
		if historyErr != nil {
			return AgentView{}, historyErr
		}
		view.Windows = windows
		return view, nil
	}

	view.Live = true
	view.AttachCommand = fmt.Sprintf("tmux -L %s attach -t %s", o.tmuxSocket, run.SessionName)
	windows, err := o.windows(ctx, run.SessionName)
	if err != nil {
		return AgentView{}, err
	}
	view.Windows = windows
	return view, nil
}

func (o *Observer) sessionExists(ctx context.Context, sessionName string) (bool, error) {
	output, err := o.run(ctx, "-L", o.tmuxSocket, "has-session", "-t", sessionName)
	if err == nil {
		return true, nil
	}
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("agent observer: inspect session: %w: %s", err, strings.TrimSpace(string(output)))
}

func (o *Observer) windows(ctx context.Context, sessionName string) ([]WindowView, error) {
	output, err := o.run(
		ctx,
		"-L", o.tmuxSocket,
		"list-windows", "-t", sessionName,
		"-F", tmuxWindowFormat,
	)
	if err != nil {
		return nil, fmt.Errorf("agent observer: list windows: %w: %s", err, strings.TrimSpace(string(output)))
	}

	trimmed := strings.TrimRight(string(output), "\r\n")
	if trimmed == "" {
		return nil, errors.New("agent observer: live session returned no windows")
	}
	lines := strings.Split(trimmed, "\n")
	windows := make([]WindowView, 0, min(len(lines), maxObservedWindows))
	for _, line := range lines {
		if len(windows) >= maxObservedWindows || line == "" {
			break
		}
		fields := splitWindowFields(line)
		if len(fields) != 3 {
			continue
		}
		pane, captureErr := o.run(
			ctx,
			"-L", o.tmuxSocket,
			"capture-pane", "-p", "-J", "-S", paneHistoryLines,
			"-t", fields[0],
		)
		if captureErr != nil {
			pane = []byte(fmt.Sprintf("Unable to capture this window: %v", captureErr))
		}
		observedPane := o.cleanPane(pane)
		windows = append(windows, WindowView{
			ID:      fields[0],
			Name:    fields[1],
			Command: fields[2],
			Output:  observedPane.output,
			Steps:   observedPane.steps,
		})
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("agent observer: tmux returned no parseable windows: %q", trimmed)
	}
	return windows, nil
}

func splitWindowFields(line string) []string {
	for _, separator := range []string{tmuxFieldSeparator, "\t", `\t`} {
		if fields := strings.SplitN(line, separator, 3); len(fields) == 3 {
			return fields
		}
	}
	return nil
}

type eventFile struct {
	name    string
	path    string
	command string
}

func (o *Observer) historyWindows(runDirectory string) ([]WindowView, error) {
	files, err := historyEventFiles(runDirectory)
	if err != nil {
		return nil, err
	}
	windows := make([]WindowView, 0, len(files))
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if err != nil {
			return nil, fmt.Errorf("agent observer: read retained history %s: %w", file.name, err)
		}
		command := file.command
		if command == "" {
			command = retainedAgentCommand(data)
		}
		history := o.cleanOutput(data, false)
		windows = append(windows, WindowView{
			ID:      "history:" + file.name,
			Name:    file.name,
			Command: command,
			Output:  history.output,
			Steps:   history.steps,
		})
	}
	return windows, nil
}

func historyEventFiles(runDirectory string) ([]eventFile, error) {
	if runDirectory == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(runDirectory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("agent observer: read run directory: %w", err)
	}
	var attempts []int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if attempt, ok := attemptFromEventFile(entry.Name()); ok {
			attempts = append(attempts, attempt)
		}
	}
	slices.Sort(attempts)
	files := make([]eventFile, 0, len(attempts))
	for _, attempt := range attempts {
		name := "principal"
		if len(attempts) > 1 {
			name = fmt.Sprintf("principal-attempt-%d", attempt)
		}
		files = append(files, eventFile{
			name:    name,
			path:    filepath.Join(runDirectory, fmt.Sprintf("attempt-%d-events.jsonl", attempt)),
			command: "codex",
		})
	}
	children, err := os.ReadDir(filepath.Join(runDirectory, "children"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("agent observer: read retained children: %w", err)
	}
	for _, child := range children {
		if !child.IsDir() {
			continue
		}
		path := filepath.Join(runDirectory, "children", child.Name(), "events.jsonl")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("agent observer: inspect retained child %s: %w", child.Name(), err)
		}
		files = append(files, eventFile{name: child.Name(), path: path})
	}
	return files, nil
}

func retainedAgentCommand(events []byte) string {
	prefix := string(events[:min(len(events), 4096)])
	if strings.Contains(prefix, `"type":"thread.started"`) {
		return "codex"
	}
	if strings.Contains(prefix, `"type":"system"`) || strings.Contains(prefix, `"type":"assistant"`) {
		return "claude"
	}
	return "agent"
}

func currentAttempt(runDirectory string) int {
	if runDirectory == "" {
		return 0
	}
	entries, err := os.ReadDir(runDirectory)
	if err != nil {
		return 0
	}
	attempts := 0
	for _, entry := range entries {
		if attempt, ok := attemptFromEventFile(entry.Name()); ok {
			attempts = max(attempts, attempt)
		}
	}
	return attempts
}

func attemptFromEventFile(name string) (int, bool) {
	name, found := strings.CutPrefix(name, "attempt-")
	if !found {
		return 0, false
	}
	value, found := strings.CutSuffix(name, "-events.jsonl")
	if !found {
		return 0, false
	}
	attempt, err := strconv.Atoi(value)
	return attempt, err == nil && attempt > 0
}

type paneView struct {
	output string
	steps  []StepView
}

func (o *Observer) cleanPane(output []byte) paneView {
	return o.cleanOutput(output, true)
}

func (o *Observer) cleanOutput(output []byte, truncate bool) paneView {
	omitted := truncate && len(output) > maxPaneBytes
	if omitted {
		output = output[len(output)-maxPaneBytes:]
	}
	cleaned := strings.ToValidUTF8(string(output), "�")
	cleaned = ansiSequence.ReplaceAllString(cleaned, "")
	cleaned = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, cleaned)
	cleaned = strings.TrimSpace(cleaned)
	steps := agentSteps(cleaned, o.redact)
	formatted := o.redact(formatAgentStream(cleaned))
	if omitted {
		formatted = "[older output omitted]\n" + formatted
	}
	return paneView{output: formatted, steps: steps}
}

func agentSteps(value string, redact func(string) string) []StepView {
	steps := make([]StepView, 0)
	stepIndexes := make(map[string]int)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		step, ok := agentStep(line, redact)
		if !ok {
			continue
		}
		if index, found := stepIndexes[step.ID]; found {
			steps[index] = step
			continue
		}
		stepIndexes[step.ID] = len(steps)
		steps = append(steps, step)
	}
	return steps
}

func agentStep(line string, redact func(string) string) (StepView, bool) {
	var event agentEvent
	if json.Unmarshal([]byte(line), &event) != nil || lifecycleEvent(event.Type) {
		return StepView{}, false
	}
	var payload bytes.Buffer
	if json.Indent(&payload, []byte(line), "", "  ") != nil {
		return StepView{}, false
	}
	redacted := redact(payload.String())
	return StepView{
		ID:      stepID(event, redacted),
		Type:    stepType(event),
		Status:  event.Item.Status,
		Summary: stepSummary(event),
		Payload: redacted,
	}, true
}

func stepID(event agentEvent, payload string) string {
	if event.Item.ID != "" {
		return "item:" + event.Item.ID
	}
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("event:%x", digest[:8])
}

type agentEvent struct {
	Type string `json:"type"`
	Item struct {
		ID               string `json:"id"`
		Type             string `json:"type"`
		Status           string `json:"status"`
		Text             string `json:"text"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
		Changes          []struct {
			Path string `json:"path"`
		} `json:"changes"`
	} `json:"item"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
	Result string `json:"result"`
}

func lifecycleEvent(eventType string) bool {
	switch eventType {
	case "thread.started", "turn.started", "turn.completed", "system":
		return true
	default:
		return false
	}
}

func stepType(event agentEvent) string {
	if event.Item.Type != "" {
		return event.Item.Type
	}
	return event.Type
}

func stepSummary(event agentEvent) string {
	switch event.Type {
	case "item.started", "item.completed":
		if event.Item.Command != "" {
			return summarizeStep(event.Item.Command)
		}
		if event.Item.Text != "" {
			return summarizeStep(event.Item.Text)
		}
		if len(event.Item.Changes) > 0 {
			return summarizeStep(event.Item.Changes[0].Path)
		}
		return strings.ReplaceAll(event.Item.Type, "_", " ")
	case "assistant":
		for _, content := range event.Message.Content {
			if content.Text != "" {
				return summarizeStep(content.Text)
			}
			if content.Name != "" {
				return "Tool: " + content.Name
			}
		}
		return "Assistant response"
	case "user":
		return "Tool result"
	case "result":
		if event.Result != "" {
			return summarizeStep(event.Result)
		}
		return "Agent result"
	default:
		return strings.ReplaceAll(event.Type, "_", " ")
	}
}

func summarizeStep(value string) string {
	const limit = 160
	summary := strings.Join(strings.Fields(value), " ")
	runes := []rune(summary)
	if len(runes) <= limit {
		return summary
	}
	return string(runes[:limit-1]) + "…"
}

func formatAgentStream(value string) string {
	var rendered []string
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event agentEvent
		if json.Unmarshal([]byte(line), &event) != nil {
			rendered = append(rendered, line)
			continue
		}

		switch event.Type {
		case "item.completed":
			switch event.Item.Type {
			case "agent_message", "reasoning":
				if event.Item.Text != "" {
					rendered = append(rendered, event.Item.Text)
				}
			case "command_execution":
				command := "$ " + event.Item.Command
				if event.Item.AggregatedOutput != "" {
					command += "\n" + strings.TrimSpace(event.Item.AggregatedOutput)
				}
				rendered = append(rendered, command)
			}
		case "assistant":
			for _, content := range event.Message.Content {
				switch content.Type {
				case "text":
					if content.Text != "" {
						rendered = append(rendered, content.Text)
					}
				case "tool_use":
					if content.Name != "" {
						rendered = append(rendered, "Tool: "+content.Name)
					}
				}
			}
		case "result":
			if event.Result != "" {
				rendered = append(rendered, event.Result)
			}
		case "thread.started", "turn.started", "turn.completed", "system":
			// Lifecycle events do not add useful pane content.
		default:
			rendered = append(rendered, line)
		}
	}
	return strings.Join(rendered, "\n\n")
}

func (o *Observer) redact(value string) string {
	for _, secret := range o.redactions {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}
