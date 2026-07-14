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

func TestTmuxLauncherBootstrapsGreenfieldRepositoryIdempotently(t *testing.T) {
	t.Parallel()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	managedRoot := filepath.Join(root, "managed")
	if err := os.MkdirAll(managedRoot, 0o700); err != nil {
		t.Fatalf("create managed root: %v", err)
	}
	remote := filepath.Join(root, "artifacts.git")
	state := filepath.Join(root, "github-created")
	createLog := filepath.Join(root, "create.log")
	repositoryURL := "git@github.com:tomnagengast/artifacts.git"
	gitWrapper := filepath.Join(root, "git-wrapper")
	gitScript := fmt.Sprintf("#!/bin/sh\nexport GIT_CONFIG_COUNT=1\nexport GIT_CONFIG_KEY_0=url.%s.insteadOf\nexport GIT_CONFIG_VALUE_0=%s\nif [ \"$1\" = clone ]; then\n  %s \"$@\" || exit $?\n  for destination do :; done\n  %s -C \"$destination\" remote set-url origin %s\n  exit 0\nfi\nexec %s \"$@\"\n", remote, repositoryURL, gitPath, gitPath, repositoryURL, gitPath)
	if err := os.WriteFile(gitWrapper, []byte(gitScript), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	ghPath := filepath.Join(root, "gh")
	ghScript := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$1 $2" = "repo view" ]; then
  if [ ! -f %q ]; then
    echo 'Could not resolve to a Repository' >&2
    exit 1
  fi
  branch='{"name":""}'
  if %s --git-dir=%q show-ref --verify --quiet refs/heads/main; then branch='{"name":"main"}'; fi
  printf '{"nameWithOwner":"tomnagengast/artifacts","isPrivate":true,"defaultBranchRef":%%s}\n' "$branch"
  exit 0
fi
if [ "$1 $2" = "repo create" ]; then
  %s init --bare --initial-branch=main %q >/dev/null
  touch %q
  printf 'create\n' >> %q
  exit 0
fi
if [ "$1 $2" = "repo edit" ]; then exit 0; fi
exit 2
`, state, gitPath, remote, gitPath, remote, state, createLog)
	if err := os.WriteFile(ghPath, []byte(ghScript), 0o700); err != nil {
		t.Fatalf("write gh fake: %v", err)
	}
	workspace := filepath.Join(managedRoot, "artifacts")
	launcher, err := NewTmuxLauncher(LauncherConfig{
		Repository: "tomnagengast/artifacts", RepoURL: repositoryURL, RepoPath: workspace,
		ManagedRoot: managedRoot, BaseBranch: "main", Bootstrap: true,
		StateRoot: root, BinaryPath: "unused", GitPath: gitWrapper, GitHubPath: ghPath,
		WorktrunkPath: "unused", TmuxPath: "unused", TmuxSocket: "unused",
	})
	if err != nil {
		t.Fatalf("new launcher: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := launcher.Prepare(context.Background()); err != nil {
			t.Fatalf("prepare attempt %d: %v", attempt, err)
		}
	}
	if got := gitOutput(t, gitWrapper, workspace, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("branch = %q, want main", got)
	}
	if got := gitOutput(t, gitWrapper, workspace, "rev-parse", "--abbrev-ref", "@{upstream}"); got != "origin/main" {
		t.Fatalf("upstream = %q, want origin/main", got)
	}
	data, err := os.ReadFile(createLog)
	if err != nil {
		t.Fatalf("read create log: %v", err)
	}
	if got := strings.Count(string(data), "create\n"); got != 1 {
		t.Fatalf("GitHub create calls = %d, want 1", got)
	}
}

func TestTmuxLauncherRejectsUnsafeWorkspaceStates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	managedRoot := filepath.Join(root, "managed")
	outside := filepath.Join(root, "outside")
	for _, directory := range []string{managedRoot, outside} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create %s: %v", directory, err)
		}
	}
	base := LauncherConfig{
		RepoURL: "unused", ManagedRoot: managedRoot, BaseBranch: "main",
		StateRoot: root, BinaryPath: "unused", GitPath: "git", WorktrunkPath: "wt",
		TmuxPath: "tmux", TmuxSocket: "factory-test",
	}
	t.Run("target symlink", func(t *testing.T) {
		target := filepath.Join(managedRoot, "target")
		if err := os.Symlink(outside, target); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		config := base
		config.RepoPath = target
		launcher, _ := NewTmuxLauncher(config)
		if err := launcher.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("error = %v, want symbolic link rejection", err)
		}
	})
	t.Run("ancestor symlink escape", func(t *testing.T) {
		link := filepath.Join(managedRoot, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		config := base
		config.RepoPath = filepath.Join(link, "repository")
		launcher, _ := NewTmuxLauncher(config)
		if err := launcher.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("error = %v, want escape rejection", err)
		}
	})
	t.Run("existing non-git directory", func(t *testing.T) {
		target := filepath.Join(managedRoot, "empty")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		config := base
		config.RepoPath = target
		launcher, _ := NewTmuxLauncher(config)
		if err := launcher.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "not a Git checkout") {
			t.Fatalf("error = %v, want non-Git rejection", err)
		}
	})
}

func TestTmuxLauncherRejectsMismatchedOriginAndDefaultBranch(t *testing.T) {
	t.Parallel()

	t.Run("origin", func(t *testing.T) {
		fixture := newLauncherFixture(t)
		runGit(t, fixture.gitPath, fixture.workspacePath, "remote", "set-url", "origin", filepath.Join(fixture.root, "unexpected.git"))
		if err := fixture.launcher.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("error = %v, want origin mismatch", err)
		}
	})

	t.Run("GitHub default branch", func(t *testing.T) {
		fixture := newLauncherFixture(t)
		ghPath := filepath.Join(fixture.root, "gh")
		if err := os.WriteFile(ghPath, []byte("#!/bin/sh\nprintf '{\"nameWithOwner\":\"tomnagengast/test\",\"isPrivate\":true,\"defaultBranchRef\":{\"name\":\"release\"}}\\n'\n"), 0o700); err != nil {
			t.Fatalf("write gh fake: %v", err)
		}
		remotePath := filepath.Join(fixture.root, "remote.git")
		repositoryURL := "git@github.com:tomnagengast/test.git"
		runGit(t, fixture.gitPath, fixture.workspacePath, "config", "url."+remotePath+".insteadOf", repositoryURL)
		runGit(t, fixture.gitPath, fixture.workspacePath, "remote", "set-url", "origin", repositoryURL)
		fixture.launcher.config.Repository = "tomnagengast/test"
		fixture.launcher.config.GitHubPath = ghPath
		if err := fixture.launcher.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "default branch does not match main") {
			t.Fatalf("error = %v, want default branch mismatch", err)
		}
	})
}

func TestTmuxLauncherPrepareAllowsRegisteredWorktreeOnly(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture(t)
	addTestWorktree(t, fixture, "integrated")

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

func TestTmuxLauncherStartPreservesWorktreesWhenCleanupIsDisabled(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture(t)
	integratedPath := addTestWorktree(t, fixture, "active")
	tmuxPath := filepath.Join(fixture.root, "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	fixture.launcher.config.TmuxPath = tmuxPath
	run := Run{
		ID:              "run-active",
		IssueIdentifier: "ENG-123",
		RepositoryURL:   fixture.launcher.config.RepoURL,
		RepositoryPath:  fixture.workspacePath,
		ManagedRoot:     fixture.launcher.config.ManagedRoot,
		BaseBranch:      "main",
	}
	if err := fixture.launcher.Start(context.Background(), run, "factory-eng-123", filepath.Join(fixture.root, "run"), StartOptions{}); err != nil {
		t.Fatalf("start without cleanup: %v", err)
	}
	if _, err := os.Stat(integratedPath); err != nil {
		t.Fatalf("active worktree was removed: %v", err)
	}
	if _, err := os.Stat(fixture.worktrunkLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktrunk cleanup unexpectedly ran: %v", err)
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
	if err := launcher.Start(context.Background(), run, "factory-eng-123", runDirectory, StartOptions{}); err != nil {
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
