package agentrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTmuxLauncherPrepareFastForwardsExistingWorkspace(t *testing.T) {
	t.Parallel()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	remotePath := filepath.Join(root, "remote.git")
	sourcePath := filepath.Join(root, "source")
	workspacePath := filepath.Join(root, "workspace")
	runGit(t, gitPath, "", "init", "--bare", "--initial-branch=main", remotePath)
	runGit(t, gitPath, "", "init", "--initial-branch=main", sourcePath)
	runGit(t, gitPath, sourcePath, "remote", "add", "origin", remotePath)
	writeWorkspaceVersion(t, sourcePath, "first\n")
	commitAndPush(t, gitPath, sourcePath, "first")

	launcher, err := NewTmuxLauncher(LauncherConfig{
		RepoURL:    remotePath,
		RepoPath:   workspacePath,
		StateRoot:  filepath.Join(root, "state"),
		BinaryPath: "unused",
		GitPath:    gitPath,
		TmuxPath:   "unused",
		TmuxSocket: "unused",
	})
	if err != nil {
		t.Fatalf("new launcher: %v", err)
	}
	if err := launcher.Prepare(context.Background()); err != nil {
		t.Fatalf("clone workspace: %v", err)
	}

	writeWorkspaceVersion(t, sourcePath, "second\n")
	commitAndPush(t, gitPath, sourcePath, "second")
	if err := launcher.Prepare(context.Background()); err != nil {
		t.Fatalf("refresh workspace: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspacePath, "version.txt"))
	if err != nil {
		t.Fatalf("read refreshed workspace: %v", err)
	}
	if got := string(data); got != "second\n" {
		t.Fatalf("workspace version = %q, want second", got)
	}
	if got, want := gitOutput(t, gitPath, workspacePath, "rev-parse", "HEAD"), gitOutput(t, gitPath, sourcePath, "rev-parse", "HEAD"); got != want {
		t.Fatalf("workspace HEAD = %s, want %s", got, want)
	}

	writeWorkspaceVersion(t, workspacePath, "dirty\n")
	if err := launcher.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("prepare dirty workspace error = %v, want local changes", err)
	}
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
