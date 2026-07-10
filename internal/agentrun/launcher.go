package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const resultFileName = "result.json"

type LauncherConfig struct {
	RepoURL    string
	RepoPath   string
	StateRoot  string
	BinaryPath string
	GitPath    string
	TmuxPath   string
	TmuxSocket string
}

type TmuxLauncher struct {
	config LauncherConfig
}

type ProcessResult struct {
	Status     string    `json:"status"`
	Attempts   int       `json:"attempts"`
	ExitCode   int       `json:"exitCode"`
	Detail     string    `json:"detail,omitempty"`
	FinishedAt time.Time `json:"finishedAt"`
}

func NewTmuxLauncher(config LauncherConfig) (*TmuxLauncher, error) {
	if config.RepoURL == "" || config.RepoPath == "" || config.StateRoot == "" {
		return nil, errors.New("agent launcher: repository and state paths are required")
	}
	if config.BinaryPath == "" || config.GitPath == "" || config.TmuxPath == "" || config.TmuxSocket == "" {
		return nil, errors.New("agent launcher: binary, git, tmux, and socket are required")
	}
	return &TmuxLauncher{config: config}, nil
}

func (l *TmuxLauncher) Prepare(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(l.config.RepoPath), 0o700); err != nil {
		return fmt.Errorf("create workspace parent: %w", err)
	}
	gitDirectory := filepath.Join(l.config.RepoPath, ".git")
	if _, err := os.Stat(gitDirectory); errors.Is(err, os.ErrNotExist) {
		return runCommand(ctx, "clone agent workspace", "", l.config.GitPath, "clone", l.config.RepoURL, l.config.RepoPath)
	} else if err != nil {
		return fmt.Errorf("inspect agent workspace: %w", err)
	}
	return runCommand(ctx, "fetch agent workspace", l.config.RepoPath, l.config.GitPath, "fetch", "--prune", "origin")
}

func (l *TmuxLauncher) Start(ctx context.Context, run Run, sessionName, runDirectory string) error {
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	args := []string{
		"-L", l.config.TmuxSocket,
		"new-session", "-d",
		"-s", sessionName,
		"-n", "principal",
		"-c", l.config.RepoPath,
		"-e", "FACTORY_TMUX_SOCKET=" + l.config.TmuxSocket,
		"-e", "FACTORY_TMUX_SESSION=" + sessionName,
		"-e", "FACTORY_RUN_ID=" + run.ID,
		"-e", "FACTORY_RUN_DIR=" + runDirectory,
		"-e", "FACTORY_REPO_PATH=" + l.config.RepoPath,
		"-e", "FACTORY_AGENT_HELPER=" + l.config.BinaryPath,
		l.config.BinaryPath,
		"agent-exec",
		"--issue", run.IssueIdentifier,
		"--repo", l.config.RepoPath,
		"--run-dir", runDirectory,
	}
	cmd := exec.CommandContext(ctx, l.config.TmuxPath, args...)
	cmd.Env = agentEnvironment(os.Environ())
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func agentEnvironment(environ []string) []string {
	allowed := map[string]bool{
		"CODEX_HOME":     true,
		"GITHUB_TOKEN":   true,
		"GH_HOST":        true,
		"HOME":           true,
		"LANG":           true,
		"LC_ALL":         true,
		"LINEAR_API_KEY": true,
		"PATH":           true,
		"SHELL":          true,
		"SSH_AUTH_SOCK":  true,
		"TERM":           true,
		"TMPDIR":         true,
		"USER":           true,
	}
	filtered := make([]string, 0, len(allowed))
	for _, entry := range environ {
		name, _, found := strings.Cut(entry, "=")
		if found && allowed[name] {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (l *TmuxLauncher) SessionExists(ctx context.Context, sessionName string) (bool, error) {
	if sessionName == "" {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, l.config.TmuxPath, "-L", l.config.TmuxSocket, "has-session", "-t", sessionName)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("tmux has-session: %w", err)
}

func (l *TmuxLauncher) ReadResult(runDirectory string) (ProcessResult, error) {
	data, err := os.ReadFile(filepath.Join(runDirectory, resultFileName))
	if err != nil {
		return ProcessResult{}, fmt.Errorf("read agent result: %w", err)
	}
	var result ProcessResult
	if err := json.Unmarshal(data, &result); err != nil {
		return ProcessResult{}, fmt.Errorf("decode agent result: %w", err)
	}
	if result.Status != string(StateSucceeded) && result.Status != string(StateBlocked) && result.Status != string(StateFailed) {
		return ProcessResult{}, fmt.Errorf("invalid agent result status %q", result.Status)
	}
	return result, nil
}

func sessionName(issueIdentifier string) string {
	return "factory-" + strings.ToLower(issueIdentifier)
}

func runPath(stateRoot, runID string) string {
	return filepath.Join(stateRoot, "runs", runID)
}

func runCommand(ctx context.Context, operation, directory, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", operation, err, strings.TrimSpace(string(output)))
	}
	return nil
}
