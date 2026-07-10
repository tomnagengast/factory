package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	tmuxFieldSeparator = "\x1f"
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
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Output  string `json:"output"`
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

	exists, err := o.sessionExists(ctx, run.SessionName)
	if err != nil {
		return AgentView{}, err
	}
	if !exists {
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
		windows = append(windows, WindowView{
			ID:      fields[0],
			Name:    fields[1],
			Command: fields[2],
			Output:  o.cleanPane(pane),
		})
	}
	if len(windows) == 0 {
		return nil, errors.New("agent observer: tmux returned no parseable windows")
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
		name, found := strings.CutPrefix(entry.Name(), "attempt-")
		if !found {
			continue
		}
		value, found := strings.CutSuffix(name, "-events.jsonl")
		if !found {
			continue
		}
		attempt, err := strconv.Atoi(value)
		if err == nil {
			attempts = max(attempts, attempt)
		}
	}
	return attempts
}

func (o *Observer) cleanPane(output []byte) string {
	omitted := len(output) > maxPaneBytes
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
	cleaned = formatAgentStream(strings.TrimSpace(cleaned))
	cleaned = o.redact(cleaned)
	if omitted {
		return "[older output omitted]\n" + cleaned
	}
	return cleaned
}

func formatAgentStream(value string) string {
	var rendered []string
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type             string `json:"type"`
				Text             string `json:"text"`
				Command          string `json:"command"`
				AggregatedOutput string `json:"aggregated_output"`
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
