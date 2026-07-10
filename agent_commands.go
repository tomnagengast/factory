package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/agentrun"
)

func runAgentCommand(ctx context.Context, args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "serve":
		return 0, false
	case "agent-exec":
		return runPrincipal(ctx, args[1:]), true
	case "child-exec":
		return runChild(ctx, args[1:]), true
	case "agent":
		return runAgentHelper(ctx, args[1:]), true
	default:
		fmt.Fprintf(os.Stderr, "unknown Factory command %q\n", args[0])
		return 2, true
	}
}

func runPrincipal(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("agent-exec", flag.ContinueOnError)
	issue := flags.String("issue", "", "Linear issue identifier")
	repo := flags.String("repo", "", "repository path")
	runDirectory := flags.String("run-dir", "", "run output directory")
	if flags.Parse(args) != nil || *issue == "" || *repo == "" || *runDirectory == "" {
		return 2
	}
	return agentrun.ExecutePrincipal(ctx, agentrun.PrincipalConfig{
		IssueIdentifier: *issue,
		RepoPath:        *repo,
		RunDirectory:    *runDirectory,
		CodexPath:       requiredCommand("codex"),
		Now:             time.Now,
		Sleep:           sleepContext,
	})
}

func runChild(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("child-exec", flag.ContinueOnError)
	provider := flags.String("provider", "", "codex or claude")
	repo := flags.String("repo", "", "repository path")
	prompt := flags.String("prompt", "", "prompt file")
	outputDirectory := flags.String("output-dir", "", "child output directory")
	if flags.Parse(args) != nil || *provider == "" || *repo == "" || *prompt == "" || *outputDirectory == "" {
		return 2
	}
	return agentrun.ExecuteChild(ctx, agentrun.ChildConfig{
		Provider:        *provider,
		RepoPath:        *repo,
		PromptPath:      *prompt,
		OutputDirectory: *outputDirectory,
		CodexPath:       requiredCommand("codex"),
		ClaudePath:      requiredCommand("claude"),
		Now:             time.Now,
	})
}

func runAgentHelper(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "spawn" {
		fmt.Fprintln(os.Stderr, "usage: factory agent spawn --provider codex|claude --name <slug> < prompt.txt")
		return 2
	}
	flags := flag.NewFlagSet("agent spawn", flag.ContinueOnError)
	provider := flags.String("provider", "", "codex or claude")
	name := flags.String("name", "", "child window slug")
	if flags.Parse(args[1:]) != nil {
		return 2
	}
	binaryPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve Factory binary: %v\n", err)
		return 1
	}
	launch, err := agentrun.SpawnChild(ctx, agentrun.SpawnChildConfig{
		Provider:     *provider,
		Name:         *name,
		Session:      os.Getenv("FACTORY_TMUX_SESSION"),
		Socket:       os.Getenv("FACTORY_TMUX_SOCKET"),
		RunID:        os.Getenv("FACTORY_RUN_ID"),
		RunDirectory: os.Getenv("FACTORY_RUN_DIR"),
		RepoPath:     os.Getenv("FACTORY_REPO_PATH"),
		BinaryPath:   binaryPath,
		TmuxPath:     requiredCommand("tmux"),
	}, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := agentrun.WriteChildLaunch(os.Stdout, launch); err != nil {
		fmt.Fprintf(os.Stderr, "write child launch: %v\n", err)
		return 1
	}
	return 0
}

func requiredCommand(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	return path
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
