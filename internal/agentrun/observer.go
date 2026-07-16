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

	"github.com/tomnagengast/factory/internal/taskmodel"
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
	ID                string            `json:"id"`
	Task              taskmodel.TaskRef `json:"task"`
	IssueIdentifier   string            `json:"issueIdentifier"`
	State             State             `json:"state"`
	Attempts          int               `json:"attempts"`
	DuplicateTriggers uint64            `json:"duplicateTriggers"`
	Detail            string            `json:"detail,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	StartedAt         *time.Time        `json:"startedAt,omitempty"`
	FinishedAt        *time.Time        `json:"finishedAt,omitempty"`
	ObservedAt        time.Time         `json:"observedAt"`
	Live              bool              `json:"live"`
	AttachCommand     string            `json:"attachCommand,omitempty"`
	Windows           []WindowView      `json:"windows"`
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
	Action  string `json:"action"`
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
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
		Task:              run.Task,
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
	type observedStep struct {
		view    StepView
		sources []json.RawMessage
	}

	observed := make([]observedStep, 0)
	stepIndexes := make(map[string]int)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, update := range agentStepUpdates(line, redact) {
			if index, found := stepIndexes[update.view.ID]; found {
				if update.merge {
					observed[index].view = mergeStepView(observed[index].view, update.view)
					observed[index].sources = append(observed[index].sources, update.source)
					observed[index].view.Payload = payloadForSources(observed[index].sources, redact)
				} else {
					observed[index] = observedStep{view: update.view, sources: []json.RawMessage{update.source}}
				}
				continue
			}
			stepIndexes[update.view.ID] = len(observed)
			observed = append(observed, observedStep{view: update.view, sources: []json.RawMessage{update.source}})
		}
	}
	steps := make([]StepView, 0, len(observed))
	for _, step := range observed {
		steps = append(steps, step.view)
	}
	return steps
}

type stepUpdate struct {
	view   StepView
	source json.RawMessage
	merge  bool
}

func agentStepUpdates(line string, redact func(string) string) []stepUpdate {
	var event agentEvent
	if json.Unmarshal([]byte(line), &event) != nil || lifecycleEvent(event.Type) {
		return nil
	}
	source := append(json.RawMessage(nil), []byte(line)...)
	updates := normalizedStepUpdates(event, source)
	for index := range updates {
		updates[index].source = source
		updates[index].view = redactStepView(updates[index].view, redact)
		updates[index].view.Payload = payloadForSources([]json.RawMessage{source}, redact)
	}
	return updates
}

func normalizedStepUpdates(event agentEvent, source json.RawMessage) []stepUpdate {
	switch event.Type {
	case "item.started", "item.completed":
		return []stepUpdate{{view: codexStep(event, source)}}
	case "assistant":
		updates := make([]stepUpdate, 0, len(event.Message.Content))
		for index, content := range event.Message.Content {
			switch content.Type {
			case "thinking":
				continue
			case "text":
				if content.Text == "" {
					continue
				}
				updates = append(updates, stepUpdate{view: StepView{
					ID:      eventStepID(event, source, index),
					Type:    "text",
					Action:  "Responded",
					Summary: summarizeStep(content.Text),
					Detail:  content.Text,
				}})
			case "tool_use":
				updates = append(updates, stepUpdate{view: claudeToolStep(event, content, source, index)})
			default:
				updates = append(updates, stepUpdate{view: StepView{
					ID:      eventStepID(event, source, index),
					Type:    content.Type,
					Action:  "Observed",
					Summary: fallbackSummary(content.Type, "Assistant event"),
				}})
			}
		}
		if len(updates) == 0 && len(event.Message.Content) == 0 {
			updates = append(updates, stepUpdate{view: fallbackStep(event, source, 0)})
		}
		return updates
	case "user":
		updates := make([]stepUpdate, 0, len(event.Message.Content))
		for index, content := range event.Message.Content {
			if content.Type != "tool_result" {
				continue
			}
			updates = append(updates, claudeToolResultUpdate(event, content, source, index))
		}
		return updates
	case "result":
		if event.Result == "" {
			return []stepUpdate{{view: fallbackStep(event, source, 0)}}
		}
		return []stepUpdate{{view: StepView{
			ID:      eventStepID(event, source, 0),
			Type:    "result",
			Status:  "completed",
			Action:  "Finished",
			Summary: summarizeStep(event.Result),
			Detail:  event.Result,
		}}}
	default:
		return []stepUpdate{{view: fallbackStep(event, source, 0)}}
	}
}

type agentChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type agentContent struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type agentEvent struct {
	Type string `json:"type"`
	UUID string `json:"uuid"`
	Item struct {
		ID               string          `json:"id"`
		Type             string          `json:"type"`
		Status           string          `json:"status"`
		Text             string          `json:"text"`
		Message          string          `json:"message"`
		Command          string          `json:"command"`
		AggregatedOutput string          `json:"aggregated_output"`
		Server           string          `json:"server"`
		Tool             string          `json:"tool"`
		Query            string          `json:"query"`
		Arguments        json.RawMessage `json:"arguments"`
		Result           json.RawMessage `json:"result"`
		Error            json.RawMessage `json:"error"`
		Action           json.RawMessage `json:"action"`
		Changes          []agentChange   `json:"changes"`
	} `json:"item"`
	Message struct {
		ID      string         `json:"id"`
		Content []agentContent `json:"content"`
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

func codexStep(event agentEvent, source json.RawMessage) StepView {
	item := event.Item
	step := StepView{
		ID:     itemStepID(item.ID, source),
		Type:   item.Type,
		Status: item.Status,
	}
	switch item.Type {
	case "command_execution":
		step.Action = "Ran"
		step.Summary = summarizeStep(displayCommand(item.Command))
		step.Detail = item.Command
		step.Output = strings.TrimSpace(item.AggregatedOutput)
	case "agent_message":
		step.Action = "Reported"
		step.Summary = summarizeStep(item.Text)
		step.Detail = item.Text
	case "reasoning":
		step.Action = "Reasoned"
		step.Summary = summarizeStep(item.Text)
		step.Detail = item.Text
	case "mcp_tool_call":
		step.Action = "Used"
		step.Summary = mcpSummary(item.Server, item.Tool)
		step.Detail = rawValuePretty(item.Arguments)
		step.Output = rawValueText(item.Result)
		step.Error = rawValueText(item.Error)
	case "web_search":
		step.Action = "Searched"
		step.Summary = summarizeStep(firstNonEmpty(item.Query, rawObjectString(item.Action, "query"), rawValueText(item.Action)))
		step.Detail = rawValuePretty(item.Action)
	case "file_change":
		step.Action = "Updated"
		paths := make([]string, 0, len(item.Changes))
		for _, change := range item.Changes {
			if change.Path != "" {
				paths = append(paths, change.Path)
			}
		}
		step.Summary = summarizePathList(paths)
		step.Detail = strings.Join(paths, "\n")
	case "error":
		message := firstNonEmpty(item.Message, item.Text, rawValueText(item.Error))
		step.Action = "Failed"
		step.Status = "failed"
		step.Summary = summarizeStep(message)
		step.Error = message
	default:
		step.Action = "Observed"
		step.Summary = summarizeStep(firstNonEmpty(item.Command, item.Text, item.Message, firstChangePath(item.Changes), fallbackSummary(item.Type, event.Type)))
		step.Detail = firstNonEmpty(item.Command, item.Text, item.Message)
	}
	if step.Summary == "" {
		step.Summary = fallbackSummary(item.Type, event.Type)
	}
	if step.Error != "" {
		step.Status = "failed"
	}
	return step
}

func claudeToolStep(event agentEvent, content agentContent, source json.RawMessage, index int) StepView {
	action := claudeToolAction(content.Name)
	summary := firstNonEmpty(
		rawObjectString(content.Input, "description"),
		rawObjectString(content.Input, "command"),
		rawObjectString(content.Input, "file_path", "path"),
		rawObjectString(content.Input, "query", "pattern"),
		content.Name,
	)
	if strings.EqualFold(content.Name, "Bash") {
		summary = displayCommand(summary)
	}
	return StepView{
		ID:      toolStepID(content.ID, event, source, index),
		Type:    firstNonEmpty(content.Name, "tool_use"),
		Status:  "in_progress",
		Action:  action,
		Summary: summarizeStep(summary),
		Detail:  rawValuePretty(content.Input),
	}
}

func claudeToolResultUpdate(event agentEvent, content agentContent, source json.RawMessage, index int) stepUpdate {
	result := rawValueText(content.Content)
	step := StepView{
		ID:      toolStepID(content.ToolUseID, event, source, index),
		Type:    "tool_result",
		Status:  "completed",
		Action:  "Returned",
		Summary: fallbackSummary(content.ToolUseID, "Tool result"),
	}
	if content.IsError {
		step.Status = "failed"
		step.Error = result
	} else {
		step.Output = result
	}
	return stepUpdate{view: step, merge: content.ToolUseID != ""}
}

func fallbackStep(event agentEvent, source json.RawMessage, index int) StepView {
	return StepView{
		ID:      eventStepID(event, source, index),
		Type:    event.Type,
		Action:  "Observed",
		Summary: fallbackSummary(event.Type, "Agent event"),
	}
}

func mergeStepView(current, update StepView) StepView {
	if current.Type == "" {
		current.Type = update.Type
	}
	if current.Action == "" {
		current.Action = update.Action
	}
	if current.Summary == "" {
		current.Summary = update.Summary
	}
	if current.Detail == "" {
		current.Detail = update.Detail
	}
	if update.Status != "" {
		current.Status = update.Status
	}
	if update.Output != "" {
		current.Output = update.Output
	}
	if update.Error != "" {
		current.Error = update.Error
	}
	return current
}

func redactStepView(step StepView, redact func(string) string) StepView {
	step.Action = redact(step.Action)
	step.Summary = redact(step.Summary)
	step.Detail = redact(step.Detail)
	step.Output = redact(step.Output)
	step.Error = redact(step.Error)
	return step
}

func payloadForSources(sources []json.RawMessage, redact func(string) string) string {
	var value any
	if len(sources) == 1 {
		value = json.RawMessage(sources[0])
	} else {
		value = sources
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ""
	}
	return redact(string(encoded))
}

func itemStepID(id string, source json.RawMessage) string {
	if id != "" {
		return "item:" + id
	}
	return digestStepID("event", source, 0)
}

func toolStepID(id string, event agentEvent, source json.RawMessage, index int) string {
	if id != "" {
		return "tool:" + id
	}
	return eventStepID(event, source, index)
}

func eventStepID(event agentEvent, source json.RawMessage, index int) string {
	if id := firstNonEmpty(event.UUID, event.Message.ID); id != "" {
		return fmt.Sprintf("event:%s:%d", id, index)
	}
	return digestStepID("event", source, index)
}

func digestStepID(prefix string, source json.RawMessage, index int) string {
	digest := sha256.Sum256(source)
	return fmt.Sprintf("%s:%x:%d", prefix, digest[:8], index)
}

func claudeToolAction(name string) string {
	switch strings.ToLower(name) {
	case "bash", "shell", "execute":
		return "Ran"
	case "read":
		return "Read"
	case "write", "edit", "applypatch", "apply_patch":
		return "Updated"
	case "grep", "glob", "search", "websearch", "web_search":
		return "Searched"
	default:
		return "Used"
	}
}

func mcpSummary(server, tool string) string {
	if server != "" && tool != "" {
		return summarizeStep(server + " · " + tool)
	}
	return summarizeStep(firstNonEmpty(tool, server, "MCP tool"))
}

func displayCommand(command string) string {
	for _, prefix := range []string{
		"/bin/zsh -lc ", "zsh -lc ",
		"/bin/bash -lc ", "bash -lc ",
		"/bin/sh -lc ", "sh -lc ",
		"/bin/zsh -c ", "zsh -c ",
		"/bin/bash -c ", "bash -c ",
		"/bin/sh -c ", "sh -c ",
	} {
		argument, found := strings.CutPrefix(command, prefix)
		if !found || len(argument) < 2 {
			continue
		}
		switch argument[0] {
		case '\'':
			if argument[len(argument)-1] == '\'' && !strings.Contains(argument[1:len(argument)-1], "'") {
				return argument[1 : len(argument)-1]
			}
		case '"':
			if argument[len(argument)-1] == '"' && !strings.Contains(argument[1:len(argument)-1], "\\") {
				return argument[1 : len(argument)-1]
			}
		}
	}
	return command
}

func rawValuePretty(value json.RawMessage) string {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return ""
	}
	var formatted bytes.Buffer
	if json.Indent(&formatted, value, "", "  ") != nil {
		return strings.TrimSpace(string(value))
	}
	return formatted.String()
}

func rawValueText(value json.RawMessage) string {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return strings.TrimSpace(text)
	}
	var list []json.RawMessage
	if json.Unmarshal(value, &list) == nil {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			if part := rawValueText(item); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "\n")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(value, &object) == nil {
		for _, key := range []string{"text", "message", "content", "error", "output"} {
			if field, found := object[key]; found {
				if result := rawValueText(field); result != "" {
					return result
				}
			}
		}
	}
	return rawValuePretty(value)
}

func rawObjectString(value json.RawMessage, keys ...string) string {
	var object map[string]json.RawMessage
	if len(value) == 0 || json.Unmarshal(value, &object) != nil {
		return ""
	}
	for _, key := range keys {
		if field, found := object[key]; found {
			if result := rawValueText(field); result != "" {
				return result
			}
		}
	}
	return ""
}

func summarizePathList(paths []string) string {
	if len(paths) == 0 {
		return "File changes"
	}
	if len(paths) == 1 {
		return summarizeStep(paths[0])
	}
	return summarizeStep(fmt.Sprintf("%s and %d more", paths[0], len(paths)-1))
}

func firstChangePath(changes []agentChange) string {
	if len(changes) == 0 {
		return ""
	}
	return changes[0].Path
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func fallbackSummary(value, fallback string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "_", " "), "-", " "))
	if value == "" {
		return fallback
	}
	return value
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
