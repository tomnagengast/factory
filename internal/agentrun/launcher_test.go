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
)

type launcherFixture struct {
	gitPath       string
	root          string
	sourcePath    string
	workspacePath string
	worktrunkLog  string
	launcher      *TmuxLauncher
}

func TestTmuxLauncherPrepareFastForwardsExistingWorkspace(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture(t)

	writeWorkspaceVersion(t, fixture.sourcePath, "second\n")
	commitAndPush(t, fixture.gitPath, fixture.sourcePath, "second")
	if err := fixture.launcher.Prepare(context.Background()); err != nil {
		t.Fatalf("refresh workspace: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(fixture.workspacePath, "version.txt"))
	if err != nil {
		t.Fatalf("read refreshed workspace: %v", err)
	}
	if got := string(data); got != "second\n" {
		t.Fatalf("workspace version = %q, want second", got)
	}
	if got, want := gitOutput(t, fixture.gitPath, fixture.workspacePath, "rev-parse", "HEAD"), gitOutput(t, fixture.gitPath, fixture.sourcePath, "rev-parse", "HEAD"); got != want {
		t.Fatalf("workspace HEAD = %s, want %s", got, want)
	}

	writeWorkspaceVersion(t, fixture.workspacePath, "dirty\n")
	if err := fixture.launcher.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "local changes: M version.txt") {
		t.Fatalf("prepare dirty workspace error = %v, want local changes", err)
	}
}

func TestTmuxLauncherPrepareAllowsRegisteredWorktreeOnly(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture(t)
	addTestWorktree(t, fixture, "integrated")
	symlinkPath := filepath.Join(fixture.root, "workspace-link")
	if err := os.Symlink(fixture.workspacePath, symlinkPath); err != nil {
		t.Fatalf("symlink workspace: %v", err)
	}
	fixture.launcher.config.RepoPath = symlinkPath

	if err := fixture.launcher.Prepare(context.Background()); err != nil {
		t.Fatalf("prepare with registered worktree: %v", err)
	}
	strayPath := filepath.Join(fixture.workspacePath, ".worktrees", "stray.txt")
	if err := os.WriteFile(strayPath, []byte("stray\n"), 0o600); err != nil {
		t.Fatalf("write stray worktree file: %v", err)
	}
	if err := fixture.launcher.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "?? .worktrees/") {
		t.Fatalf("prepare stray worktree error = %v, want local changes", err)
	}
}

func TestTmuxLauncherCleanupSelectsCleanIntegratedWorktrees(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture(t)
	integratedPath := addTestWorktree(t, fixture, "integrated")
	dirtyPath := addTestWorktree(t, fixture, "dirty")
	if err := os.WriteFile(filepath.Join(dirtyPath, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("write dirty worktree file: %v", err)
	}
	unmergedPath := addTestWorktree(t, fixture, "unmerged")
	writeWorkspaceVersion(t, unmergedPath, "unmerged\n")
	runGit(t, fixture.gitPath, unmergedPath, "add", "version.txt")
	runGit(t, fixture.gitPath, unmergedPath, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "unmerged")
	lockedPath := addTestWorktree(t, fixture, "locked")
	runGit(t, fixture.gitPath, fixture.workspacePath, "worktree", "lock", lockedPath)

	if err := fixture.launcher.CleanupWorktrees(context.Background()); err != nil {
		t.Fatalf("cleanup worktrees: %v", err)
	}
	data, err := os.ReadFile(fixture.worktrunkLog)
	if err != nil {
		t.Fatalf("read worktrunk log: %v", err)
	}
	if got, want := strings.TrimSpace(string(data)), "-y remove --foreground --no-hooks integrated"; got != want {
		t.Fatalf("worktrunk calls = %q, want %q", got, want)
	}
	if _, err := os.Stat(integratedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("integrated worktree still exists: %v", err)
	}
	for _, path := range []string{dirtyPath, unmergedPath, lockedPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained worktree %s: %v", path, err)
		}
	}
}

func TestTmuxLauncherCleanupRemovesEmptyWorktreeDirectory(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture(t)
	addTestWorktree(t, fixture, "integrated")
	if err := fixture.launcher.CleanupWorktrees(context.Background()); err != nil {
		t.Fatalf("cleanup worktrees: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.workspacePath, ".worktrees")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty worktree directory still exists: %v", err)
	}
}

func TestTmuxLauncherPropagatesTriggerKind(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	argumentsPath := filepath.Join(root, "tmux-arguments")
	tmuxPath := filepath.Join(root, "tmux")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", argumentsPath)
	if err := os.WriteFile(tmuxPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	launcher, err := NewTmuxLauncher(LauncherConfig{
		RepoURL:       "git@example.invalid:repo.git",
		RepoPath:      root,
		StateRoot:     root,
		BinaryPath:    "/tmp/factory",
		GitPath:       "git",
		WorktrunkPath: "wt",
		TmuxPath:      tmuxPath,
		TmuxSocket:    "factory-agents",
	})
	if err != nil {
		t.Fatalf("new launcher: %v", err)
	}
	run := Run{ID: "run-123", IssueIdentifier: "ENG-123", TriggerKind: TriggerKindComment}
	runDirectory := filepath.Join(root, "runs", run.ID)
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatalf("create run directory: %v", err)
	}
	for _, name := range []string{resultFileName, readyCheckpointFileName} {
		if err := os.WriteFile(filepath.Join(runDirectory, name), []byte("stale"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if err := launcher.Start(context.Background(), run, "factory-eng-123", runDirectory); err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, name := range []string{resultFileName, readyCheckpointFileName} {
		if _, err := os.Stat(filepath.Join(runDirectory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale %s still exists: %v", name, err)
		}
	}
	data, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatalf("read tmux arguments: %v", err)
	}
	arguments := string(data)
	for _, expected := range []string{"FACTORY_TRIGGER_KIND=" + TriggerKindComment, "--trigger-kind\n" + TriggerKindComment} {
		if !strings.Contains(arguments, expected) {
			t.Fatalf("arguments missing %q:\n%s", expected, arguments)
		}
	}
}

func newLauncherFixture(t *testing.T) launcherFixture {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	remotePath := filepath.Join(root, "remote.git")
	sourcePath := filepath.Join(root, "source")
	workspacePath := filepath.Join(root, "workspace")
	worktrunkLog := filepath.Join(root, "worktrunk.log")
	worktrunkPath := filepath.Join(root, "wt")
	runGit(t, gitPath, "", "init", "--bare", "--initial-branch=main", remotePath)
	runGit(t, gitPath, "", "init", "--initial-branch=main", sourcePath)
	runGit(t, gitPath, sourcePath, "remote", "add", "origin", remotePath)
	writeWorkspaceVersion(t, sourcePath, "first\n")
	commitAndPush(t, gitPath, sourcePath, "first")
	script := fmt.Sprintf("#!/bin/sh\nset -e\nbranch=$5\n%s -C %q worktree remove %q/\"$branch\"\n%s -C %q branch -d \"$branch\"\nprintf '%%s\\n' \"$*\" >> %q\n", gitPath, workspacePath, filepath.Join(workspacePath, ".worktrees"), gitPath, workspacePath, worktrunkLog)
	if err := os.WriteFile(worktrunkPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake worktrunk: %v", err)
	}

	launcher, err := NewTmuxLauncher(LauncherConfig{
		RepoURL:       remotePath,
		RepoPath:      workspacePath,
		StateRoot:     filepath.Join(root, "state"),
		BinaryPath:    "unused",
		GitPath:       gitPath,
		WorktrunkPath: worktrunkPath,
		TmuxPath:      "unused",
		TmuxSocket:    "unused",
	})
	if err != nil {
		t.Fatalf("new launcher: %v", err)
	}
	if err := launcher.Prepare(context.Background()); err != nil {
		t.Fatalf("clone workspace: %v", err)
	}
	return launcherFixture{
		gitPath:       gitPath,
		root:          root,
		sourcePath:    sourcePath,
		workspacePath: workspacePath,
		worktrunkLog:  worktrunkLog,
		launcher:      launcher,
	}
}

func TestCommandOutputSeparatesSuccessfulStderr(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	commandPath := filepath.Join(root, "command")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\nprintf 'data'\nprintf 'warning' >&2\n"), 0o700); err != nil {
		t.Fatalf("write command: %v", err)
	}
	output, err := commandOutput(context.Background(), "test command", root, commandPath)
	if err != nil {
		t.Fatalf("command output: %v", err)
	}
	if got, want := string(output), "data"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func addTestWorktree(t *testing.T, fixture launcherFixture, branch string) string {
	t.Helper()
	path := filepath.Join(fixture.workspacePath, ".worktrees", branch)
	runGit(t, fixture.gitPath, fixture.workspacePath, "worktree", "add", "-b", branch, path)
	return path
}

func writeWorkspaceVersion(t *testing.T, directory, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "version.txt"), []byte(value), 0o600); err != nil {
		t.Fatalf("write workspace version: %v", err)
	}
}

func commitAndPush(t *testing.T, gitPath, directory, message string) {
	t.Helper()
	runGit(t, gitPath, directory, "add", "version.txt")
	runGit(t, gitPath, directory, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", message)
	runGit(t, gitPath, directory, "push", "-u", "origin", "main")
}

func runGit(t *testing.T, gitPath, directory string, args ...string) {
	t.Helper()
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = directory
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}

func gitOutput(t *testing.T, gitPath, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}
