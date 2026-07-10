package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const maxChildPromptBytes = 1 << 20

var childNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

type SpawnChildConfig struct {
	Provider     string
	Name         string
	Session      string
	Socket       string
	RunID        string
	RunDirectory string
	RepoPath     string
	BinaryPath   string
	TmuxPath     string
}

type ChildLaunch struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	WindowID        string `json:"windowId"`
	OutputDirectory string `json:"outputDirectory"`
	EventsPath      string `json:"eventsPath"`
	DiagnosticsPath string `json:"diagnosticsPath"`
}

func SpawnChild(ctx context.Context, config SpawnChildConfig, prompt io.Reader) (ChildLaunch, error) {
	if config.Provider != "codex" && config.Provider != "claude" {
		return ChildLaunch{}, fmt.Errorf("agent child: provider must be codex or claude")
	}
	if !childNamePattern.MatchString(config.Name) {
		return ChildLaunch{}, fmt.Errorf("agent child: name must be a lowercase slug up to 31 characters")
	}
	if config.Session == "" || config.Socket == "" || config.RunID == "" || config.RunDirectory == "" {
		return ChildLaunch{}, errors.New("agent child: Factory session environment is incomplete")
	}
	if config.RepoPath == "" || config.BinaryPath == "" || config.TmuxPath == "" {
		return ChildLaunch{}, errors.New("agent child: repository, binary, and tmux paths are required")
	}

	promptData, err := io.ReadAll(io.LimitReader(prompt, maxChildPromptBytes+1))
	if err != nil {
		return ChildLaunch{}, fmt.Errorf("agent child: read prompt: %w", err)
	}
	if len(promptData) == 0 {
		return ChildLaunch{}, errors.New("agent child: prompt is required on stdin")
	}
	if len(promptData) > maxChildPromptBytes {
		return ChildLaunch{}, fmt.Errorf("agent child: prompt exceeds %d bytes", maxChildPromptBytes)
	}

	id, err := newID()
	if err != nil {
		return ChildLaunch{}, err
	}
	suffix := strings.TrimPrefix(id, "run-")[:8]
	windowName := config.Name + "-" + suffix
	outputDirectory := filepath.Join(config.RunDirectory, "children", windowName)
	if err := os.MkdirAll(outputDirectory, 0o700); err != nil {
		return ChildLaunch{}, fmt.Errorf("agent child: create output directory: %w", err)
	}
	promptPath := filepath.Join(outputDirectory, "prompt.txt")
	if err := os.WriteFile(promptPath, promptData, 0o600); err != nil {
		return ChildLaunch{}, fmt.Errorf("agent child: write prompt: %w", err)
	}

	args := []string{
		"-L", config.Socket,
		"new-window", "-d", "-P", "-F", "#{window_id}",
		"-t", config.Session,
		"-n", windowName,
		"-c", config.RepoPath,
		"-e", "FACTORY_TMUX_SOCKET=" + config.Socket,
		"-e", "FACTORY_TMUX_SESSION=" + config.Session,
		"-e", "FACTORY_RUN_ID=" + config.RunID,
		"-e", "FACTORY_RUN_DIR=" + config.RunDirectory,
		"-e", "FACTORY_REPO_PATH=" + config.RepoPath,
		"-e", "FACTORY_AGENT_HELPER=" + config.BinaryPath,
		config.BinaryPath,
		"child-exec",
		"--provider", config.Provider,
		"--repo", config.RepoPath,
		"--prompt", promptPath,
		"--output-dir", outputDirectory,
	}
	cmd := exec.CommandContext(ctx, config.TmuxPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ChildLaunch{}, fmt.Errorf("agent child: tmux new-window: %w: %s", err, strings.TrimSpace(string(output)))
	}
	launch := ChildLaunch{
		Name:            windowName,
		Provider:        config.Provider,
		WindowID:        strings.TrimSpace(string(output)),
		OutputDirectory: outputDirectory,
		EventsPath:      filepath.Join(outputDirectory, "events.jsonl"),
		DiagnosticsPath: filepath.Join(outputDirectory, "stderr.log"),
	}
	return launch, nil
}

func WriteChildLaunch(w io.Writer, launch ChildLaunch) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(launch)
}
