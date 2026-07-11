package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/agentrun"
	"github.com/tomnagengast/network/apps/factory/internal/githubhook"
	"github.com/tomnagengast/network/apps/factory/internal/linearhook"
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
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: factory agent spawn|github-events|linear-comments")
		return 2
	}
	switch args[0] {
	case "spawn":
		return runSpawnHelper(ctx, args[1:])
	case "github-events":
		return runGitHubEventsHelper(ctx, args[1:], os.Stdout)
	case "linear-comments":
		return runLinearCommentsHelper(ctx, args[1:], os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown Factory agent command %q\n", args[0])
		return 2
	}
}

func runLinearCommentsHelper(ctx context.Context, args []string, output io.Writer) int {
	flags := flag.NewFlagSet("agent linear-comments", flag.ContinueOnError)
	issue := flags.String("issue", "", "Linear issue identifier")
	issueID := flags.String("issue-id", "", "Linear issue UUID")
	after := flags.Uint64("after", 0, "event cursor")
	wait := flags.Duration("wait", time.Minute, "maximum time to wait")
	if flags.Parse(args) != nil || *wait < 0 || *wait > 5*time.Minute {
		return 2
	}
	identifier := strings.ToUpper(strings.TrimSpace(*issue))
	if identifier != "" && !agentrun.ValidIssueIdentifier(identifier) {
		fmt.Fprintln(os.Stderr, "Linear comment journal: invalid issue identifier")
		return 2
	}
	filter := linearhook.Filter{IssueIdentifier: identifier, IssueID: strings.TrimSpace(*issueID)}
	if err := filter.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	runID := os.Getenv("FACTORY_RUN_ID")
	runDirectory := filepath.Clean(os.Getenv("FACTORY_RUN_DIR"))
	if runID == "" || runDirectory == "." || filepath.Base(runDirectory) != runID || filepath.Base(filepath.Dir(runDirectory)) != "runs" {
		fmt.Fprintln(os.Stderr, "agent Linear comments: Factory run environment is invalid")
		return 2
	}
	stateRoot := filepath.Dir(filepath.Dir(runDirectory))
	journalPath := filepath.Join(stateRoot, "data", "linear-comments.json")

	var batch linearhook.Batch
	var err error
	if *wait == 0 {
		batch, err = linearhook.Read(journalPath, filter, *after)
	} else {
		waitCtx, cancel := context.WithTimeout(ctx, *wait)
		defer cancel()
		batch, err = linearhook.Wait(waitCtx, journalPath, filter, *after, 250*time.Millisecond)
		if errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(output).Encode(batch); err != nil {
		fmt.Fprintf(os.Stderr, "agent Linear comments: encode response: %v\n", err)
		return 1
	}
	return 0
}

func runSpawnHelper(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("agent spawn", flag.ContinueOnError)
	provider := flags.String("provider", "", "codex or claude")
	name := flags.String("name", "", "child window slug")
	if flags.Parse(args) != nil {
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

func runGitHubEventsHelper(ctx context.Context, args []string, output io.Writer) int {
	flags := flag.NewFlagSet("agent github-events", flag.ContinueOnError)
	repository := flags.String("repo", "", "GitHub repository in owner/name form")
	pullRequest := flags.Int("pr", 0, "pull request number")
	headBranch := flags.String("branch", "", "pull request head branch")
	after := flags.Uint64("after", 0, "event cursor")
	wait := flags.Duration("wait", time.Minute, "maximum time to wait")
	if flags.Parse(args) != nil || *wait < 0 || *wait > 5*time.Minute {
		return 2
	}
	filter := githubhook.Filter{Repository: *repository, PullRequest: *pullRequest, HeadBranch: *headBranch}
	if err := filter.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	runID := os.Getenv("FACTORY_RUN_ID")
	runDirectory := filepath.Clean(os.Getenv("FACTORY_RUN_DIR"))
	if runID == "" || runDirectory == "." || filepath.Base(runDirectory) != runID || filepath.Base(filepath.Dir(runDirectory)) != "runs" {
		fmt.Fprintln(os.Stderr, "agent GitHub events: Factory run environment is invalid")
		return 2
	}
	stateRoot := filepath.Dir(filepath.Dir(runDirectory))
	journalPath := filepath.Join(stateRoot, "data", "github-events.json")

	var batch githubhook.Batch
	var err error
	if *wait == 0 {
		batch, err = githubhook.Read(journalPath, filter, *after)
	} else {
		waitCtx, cancel := context.WithTimeout(ctx, *wait)
		defer cancel()
		batch, err = githubhook.Wait(waitCtx, journalPath, filter, *after, 250*time.Millisecond)
		if errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(output).Encode(batch); err != nil {
		fmt.Fprintf(os.Stderr, "agent GitHub events: encode response: %v\n", err)
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
