package agentrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var observerTestNow = time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

func TestObserverCapturesAndRedactsLiveWindows(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "runs.json"), 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	run, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, observerTestNow)
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	runDirectory := t.TempDir()
	if err := store.MarkStarting(run.ID, "factory-eng-123", runDirectory, observerTestNow); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "attempt-2-events.jsonl"), nil, 0o600); err != nil {
		t.Fatalf("write attempt file: %v", err)
	}

	observer, err := NewObserver(store, "tmux", "factory-agents", []string{"linear-secret"}, func() time.Time { return observerTestNow })
	if err != nil {
		t.Fatalf("new observer: %v", err)
	}
	observer.run = func(_ context.Context, args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, "has-session"):
			return nil, nil
		case strings.Contains(command, "list-windows"):
			return []byte("@1\\tprincipal\\tcodex\n@2\\tplan-review\\tclaude\n"), nil
		case strings.Contains(command, "-t @1"):
			return []byte("\x1b[32m{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"command\":\"printf working\",\"aggregated_output\":\"linear-secret\"}}\x1b[0m\n"), nil
		case strings.Contains(command, "-t @2"):
			return []byte("reviewing\x00 plan\n"), nil
		default:
			return nil, errors.New("unexpected tmux command")
		}
	}

	view, err := observer.Observe(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("observe run: %v", err)
	}
	if !view.Live || view.AttachCommand != "tmux -L factory-agents attach -t factory-eng-123" {
		t.Fatalf("live view = %#v", view)
	}
	if view.ObservedAt != observerTestNow || view.Attempts != 2 {
		t.Fatalf("snapshot metadata = %#v", view)
	}
	if len(view.Windows) != 2 {
		t.Fatalf("windows = %#v", view.Windows)
	}
	if got := view.Windows[0].Output; got != "$ printf working\n[REDACTED]" {
		t.Fatalf("principal output = %q", got)
	}
	if got := view.Windows[0].Steps; len(got) != 1 || got[0].Type != "command_execution" || got[0].Summary != "printf working" || !strings.Contains(got[0].Payload, "[REDACTED]") {
		t.Fatalf("principal steps = %#v", got)
	}
	if got := view.Windows[1].Output; got != "reviewing plan" {
		t.Fatalf("review output = %q", got)
	}
	if got := view.Windows[1].Steps; len(got) != 0 {
		t.Fatalf("plain output steps = %#v", got)
	}
}

func TestAgentStepsSkipLifecycleEventsAndKeepStableIDs(t *testing.T) {
	t.Parallel()

	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"item.started","item":{"id":"item-1","type":"file_change","status":"in_progress","changes":[{"path":"main.go"}]}}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"file_change","status":"completed","changes":[{"path":"main.go"}]}}`,
		`{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"A concise update for the operator."}}`,
	}, "\n")
	redact := func(value string) string { return value }
	first := agentSteps(stream, redact)
	second := agentSteps(stream, redact)

	if len(first) != 2 {
		t.Fatalf("steps = %#v", first)
	}
	if first[0].Summary != "main.go" || first[0].Status != "completed" || !strings.Contains(first[0].Payload, `"type": "item.completed"`) {
		t.Fatalf("file step = %#v", first[0])
	}
	if first[1].Summary != "A concise update for the operator." || first[1].Type != "agent_message" {
		t.Fatalf("message step = %#v", first[1])
	}
	if first[0].ID == "" || first[0].ID != second[0].ID {
		t.Fatalf("step IDs are not stable: %#v %#v", first, second)
	}
}

func TestObserverReturnsTerminalRunWithoutCallingTmux(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := Open(filepath.Join(root, "runs.json"), 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	run, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, observerTestNow)
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	runDirectory := filepath.Join(root, "run")
	if err := os.MkdirAll(filepath.Join(runDirectory, "children", "review-agent"), 0o700); err != nil {
		t.Fatalf("create history directories: %v", err)
	}
	principalEvent := `{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"principal retained"}}` + "\n"
	childEvent := `{"type":"assistant","message":{"content":[{"type":"text","text":"child retained secret-value"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(runDirectory, "attempt-1-events.jsonl"), []byte(principalEvent), 0o600); err != nil {
		t.Fatalf("write principal history: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, "children", "review-agent", "events.jsonl"), []byte(childEvent), 0o600); err != nil {
		t.Fatalf("write child history: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", runDirectory, observerTestNow); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := store.MarkRunning(run.ID, 1, observerTestNow); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := store.Finish(run.ID, StateFailed, 1, "failed safely", observerTestNow); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	observer, err := NewObserver(store, "tmux", "factory-agents", []string{"secret-value"}, func() time.Time { return observerTestNow })
	if err != nil {
		t.Fatalf("new observer: %v", err)
	}
	observer.run = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("tmux should not be called for a run without a session")
		return nil, nil
	}
	view, err := observer.Observe(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("observe run: %v", err)
	}
	if view.Live || len(view.Windows) != 2 || view.State != StateFailed {
		t.Fatalf("terminal view = %#v", view)
	}
	if got := view.Windows[0]; got.Name != "principal" || got.Command != "codex" || len(got.Steps) != 1 || got.Steps[0].Summary != "principal retained" {
		t.Fatalf("principal history = %#v", got)
	}
	if got := view.Windows[1]; got.Name != "review-agent" || got.Command != "claude" || len(got.Steps) != 1 || !strings.Contains(got.Steps[0].Payload, "[REDACTED]") {
		t.Fatalf("child history = %#v", got)
	}
}

func TestObserverRejectsUnknownRun(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "runs.json"), 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	observer, err := NewObserver(store, "tmux", "factory-agents", nil, func() time.Time { return observerTestNow })
	if err != nil {
		t.Fatalf("new observer: %v", err)
	}
	if _, err := observer.Observe(context.Background(), "run-missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("error = %v, want ErrRunNotFound", err)
	}
}

func TestObserverTreatsMissingTmuxSessionAsNotLive(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "runs.json"), 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	run, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, observerTestNow)
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", "/tmp/run", observerTestNow); err != nil {
		t.Fatalf("mark starting: %v", err)
	}

	observer, err := NewObserver(store, "tmux", "factory-agents", nil, func() time.Time { return observerTestNow })
	if err != nil {
		t.Fatalf("new observer: %v", err)
	}
	observer.run = func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "sh", "-c", "exit 1").CombinedOutput()
	}
	view, err := observer.Observe(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("observe run: %v", err)
	}
	if view.Live {
		t.Fatalf("view = %#v, want not live", view)
	}
}

func TestObserverRejectsUnparseableLiveWindows(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "runs.json"), 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	run, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, observerTestNow)
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if err := store.MarkStarting(run.ID, "factory-eng-123", t.TempDir(), observerTestNow); err != nil {
		t.Fatalf("mark starting: %v", err)
	}

	observer, err := NewObserver(store, "tmux", "factory-agents", nil, func() time.Time { return observerTestNow })
	if err != nil {
		t.Fatalf("new observer: %v", err)
	}
	observer.run = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "has-session") {
			return nil, nil
		}
		return []byte("unparseable-window-row\n"), nil
	}

	if _, err := observer.Observe(context.Background(), run.ID); err == nil {
		t.Fatal("observe run succeeded, want an observer error")
	}
}

func TestObserverReadsRealTmuxSession(t *testing.T) {
	t.Parallel()

	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	socket := fmt.Sprintf("factory-observer-test-%d", os.Getpid())
	session := "factory-observer-smoke"
	start := exec.Command(
		tmuxPath,
		"-L", socket,
		"new-session", "-d",
		"-s", session,
		"-n", "principal",
		"printf 'observer-smoke\\n'; exec sleep 30",
	)
	if output, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start tmux session: %v: %s", err, output)
	}
	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, "-L", socket, "kill-session", "-t", session).Run()
	})

	store, err := Open(filepath.Join(t.TempDir(), "runs.json"), 10)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	run, _, err := store.Claim(Trigger{DeliveryID: "delivery-1", IssueIdentifier: "ENG-123", Kind: "test"}, observerTestNow)
	if err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if err := store.MarkStarting(run.ID, session, "/tmp/run", observerTestNow); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	observer, err := NewObserver(store, tmuxPath, socket, nil, func() time.Time { return observerTestNow })
	if err != nil {
		t.Fatalf("new observer: %v", err)
	}
	view, err := observer.Observe(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("observe run: %v", err)
	}
	if !view.Live || len(view.Windows) != 1 || !strings.Contains(view.Windows[0].Output, "observer-smoke") {
		t.Fatalf("view = %#v", view)
	}
}
