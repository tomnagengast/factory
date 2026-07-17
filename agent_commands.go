package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

func runAgentCommand(ctx context.Context, args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "serve":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "usage: factory serve")
			return 2, true
		}
		return 0, false
	case "--help", "-h":
		return runManagementHelp(args[1:], os.Stdout, os.Stderr), true
	case "--version", "-v":
		return runManagementVersion(args[1:], os.Stdout, os.Stderr), true
	case "start":
		return runManagementStart(ctx, args[1:], os.Stdout, os.Stderr), true
	case "status":
		return runManagementStatus(ctx, args[1:], os.Stdout, os.Stderr), true
	case "stop":
		return runManagementStop(ctx, args[1:], os.Stdout, os.Stderr), true
	case "doctor":
		return runManagementDoctor(ctx, args[1:], os.Stdout, os.Stderr), true
	case "agent-exec":
		return runPrincipal(ctx, args[1:]), true
	case "child-exec":
		return runChild(ctx, args[1:]), true
	case "agent":
		return runAgentHelper(ctx, args[1:]), true
	case "state-rollback-preflight":
		return runStateRollbackPreflight(args[1:], os.Stdout, os.Stderr), true
	case "state-rollback":
		return runStateRollback(ctx, args[1:], os.Stdout, os.Stderr), true
	case "state-restore":
		return runStateRestore(ctx, args[1:], os.Stdout, os.Stderr), true
	default:
		fmt.Fprintf(os.Stderr, "unknown Factory command %q\n", args[0])
		return 2, true
	}
}

func runPrincipal(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("agent-exec", flag.ContinueOnError)
	issue := flags.String("issue", "", "Linear issue identifier")
	taskSource := flags.String("task-source", "", "task provider source")
	taskProviderID := flags.String("task-provider-id", "", "provider-owned task ID")
	taskIdentifier := flags.String("task-identifier", "", "task display identifier")
	triggerKind := flags.String("trigger-kind", "", "Factory trigger kind")
	repo := flags.String("repo", "", "repository path")
	runDirectory := flags.String("run-dir", "", "run output directory")
	attemptOffset := flags.Int("attempt-offset", 0, "completed attempts before this lifecycle segment")
	workflowFile := flags.String("workflow-file", "", "pinned workflow snapshot")
	if flags.Parse(args) != nil || *repo == "" || *runDirectory == "" || *attemptOffset < 0 {
		return 2
	}
	task, err := taskmodel.ResolveCompatibilityIdentity(taskmodel.TaskRef{
		Source: taskmodel.Source(*taskSource), ProviderID: *taskProviderID, Identifier: *taskIdentifier,
	}, *issue)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	configuration, err := loadRunSettings(*runDirectory)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var pinned workflow.Pinned
	if *workflowFile != "" {
		pinned, _, err = agentrun.ReadWorkflowSnapshot(*runDirectory, *workflowFile)
	} else {
		var definition workflow.Definition
		definition, err = configuration.WorkflowForTrigger(*triggerKind)
		pinned = workflow.Pin(definition)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return agentrun.ExecutePrincipal(ctx, agentrun.PrincipalConfig{
		Task:            task,
		IssueIdentifier: *issue,
		TriggerKind:     *triggerKind,
		RepoPath:        *repo,
		RunDirectory:    *runDirectory,
		CodexPath:       requiredCommand("codex"),
		Now:             time.Now,
		Sleep:           sleepContext,
		AttemptOffset:   *attemptOffset,
		Provider:        configuration.Agents.Principal,
		Workflow:        pinned,
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
	configuration, err := loadRunSettings(os.Getenv("FACTORY_RUN_DIR"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	providerSettings := configuration.Agents.CodexChild
	if *provider == "claude" {
		providerSettings = configuration.Agents.ClaudeChild
	}
	return agentrun.ExecuteChild(ctx, agentrun.ChildConfig{
		Provider:         *provider,
		RepoPath:         *repo,
		PromptPath:       *prompt,
		OutputDirectory:  *outputDirectory,
		CodexPath:        requiredCommand("codex"),
		ClaudePath:       requiredCommand("claude"),
		Now:              time.Now,
		ProviderSettings: providerSettings,
	})
}

func loadRunSettings(runDirectory string) (settings.Snapshot, error) {
	runID := os.Getenv("FACTORY_RUN_ID")
	runDirectory = filepath.Clean(runDirectory)
	if runID == "" || runDirectory == "." || filepath.Base(runDirectory) != runID || filepath.Base(filepath.Dir(runDirectory)) != "runs" {
		return settings.Snapshot{}, errors.New("settings: Factory run environment is invalid")
	}
	stateRoot := filepath.Dir(filepath.Dir(runDirectory))
	store, err := settings.Open(filepath.Join(stateRoot, "data", "settings.json"), settings.Defaults(defaultMaxConcurrentRuns))
	if err != nil {
		return settings.Snapshot{}, err
	}
	return store.Snapshot(), nil
}

func runAgentHelper(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: factory agent spawn|checkpoint|events|github-events|linear-comments|linear-graphql")
		return 2
	}
	switch args[0] {
	case "spawn":
		return runSpawnHelper(ctx, args[1:])
	case "checkpoint":
		return runCheckpointHelper(args[1:])
	case "events":
		return runEventsHelper(ctx, args[1:], os.Stdout)
	case "github-events":
		return runGitHubEventsHelper(ctx, args[1:], os.Stdout)
	case "linear-comments":
		return runLinearCommentsHelper(ctx, args[1:], os.Stdout)
	case "linear-graphql":
		return runLinearGraphQLHelper(ctx, os.Stdin, os.Stdout)
	case "task":
		return runTaskHelper(ctx, args[1:], os.Stdin, os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown Factory agent command %q\n", args[0])
		return 2
	}
}

type taskHelperRequest struct {
	Operation      string `json:"operation"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	After          uint64 `json:"after,omitempty"`
	Revision       uint64 `json:"revision,omitempty"`
	Body           string `json:"body,omitempty"`
	ParentID       string `json:"parentId,omitempty"`
	Label          string `json:"label,omitempty"`
	URL            string `json:"url,omitempty"`
	State          string `json:"state,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Mode           string `json:"mode,omitempty"`
	ArtifactURL    string `json:"artifactUrl,omitempty"`
}

func runTaskHelper(ctx context.Context, args []string, input io.Reader, output io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: factory agent task show|messages|activity|comment|reply|link|state|gate open")
		return 2
	}
	operation := args[0]
	if operation == "gate" {
		if len(args) < 2 || args[1] != "open" {
			fmt.Fprintln(os.Stderr, "usage: factory agent task gate open")
			return 2
		}
		operation, args = "gate-open", args[2:]
	} else {
		args = args[1:]
	}
	flags := flag.NewFlagSet("agent task "+operation, flag.ContinueOnError)
	after := flags.Uint64("after", 0, "message cursor")
	revision := flags.Uint64("revision", 0, "task revision cursor")
	wait := flags.Duration("wait", time.Minute, "maximum activity wait")
	body := flags.String("body", "", "message body (defaults to stdin)")
	parent := flags.String("parent", "", "parent message ID")
	label := flags.String("label", "", "link label")
	linkURL := flags.String("url", "", "HTTPS link URL")
	state := flags.String("state", "", "task state")
	kind := flags.String("kind", "", "gate kind")
	mode := flags.String("mode", "", "gate mode")
	artifactURL := flags.String("artifact-url", "", "gate artifact URL")
	idempotency := flags.String("idempotency-key", "", "stable retry key")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *wait < 0 || *wait > 5*time.Minute {
		return 2
	}
	request := taskHelperRequest{Operation: operation, After: *after, Revision: *revision, Body: *body, ParentID: *parent, Label: *label, URL: *linkURL, State: *state, Kind: *kind, Mode: *mode, ArtifactURL: *artifactURL, IdempotencyKey: *idempotency}
	if (operation == "comment" || operation == "reply") && request.Body == "" {
		data, err := io.ReadAll(io.LimitReader(input, 32<<10+1))
		if err != nil || len(data) > 32<<10 {
			fmt.Fprintln(os.Stderr, "task helper: message body is invalid or too large")
			return 2
		}
		request.Body = strings.TrimSpace(string(data))
	}
	if request.IdempotencyKey == "" && operation != "show" && operation != "messages" && operation != "activity" {
		data, _ := json.Marshal(request)
		digest := sha256.Sum256(data)
		request.IdempotencyKey = fmt.Sprintf("%x", digest[:])
	}
	deadline := time.Now().Add(*wait)
	for {
		status, err := callTaskHelper(ctx, request, output)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if operation != "activity" || status != http.StatusNoContent || *wait == 0 || !time.Now().Before(deadline) {
			return 0
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 1
		case <-timer.C:
		}
	}
}

func callTaskHelper(ctx context.Context, payload taskHelperRequest, output io.Writer) (int, error) {
	endpoint := strings.TrimSpace(os.Getenv("FACTORY_TASK_ENDPOINT"))
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "/api/agent/task" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Hostname() != "127.0.0.1" {
		return 0, errors.New("task helper: scoped endpoint is invalid")
	}
	capabilityFile := filepath.Clean(os.Getenv("FACTORY_TASK_CAPABILITY_FILE"))
	runDirectory := filepath.Clean(os.Getenv("FACTORY_RUN_DIR"))
	if capabilityFile == "." || runDirectory == "." || filepath.Dir(capabilityFile) != runDirectory || filepath.Base(capabilityFile) != agentrun.TaskCapabilityTokenFileName {
		return 0, errors.New("task helper: scoped capability file is invalid")
	}
	data, err := os.ReadFile(capabilityFile)
	if err != nil {
		return 0, errors.New("task helper: scoped capability is unreadable")
	}
	capability := strings.TrimSpace(string(data))
	runID := os.Getenv("FACTORY_RUN_ID")
	if capability == "" || runID == "" {
		return 0, errors.New("task helper: scoped Run capability is missing")
	}
	data, err = json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+capability)
	request.Header.Set("X-Factory-Run-ID", runID)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return 0, fmt.Errorf("task helper: request failed: %w", err)
	}
	defer response.Body.Close()
	responseData, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil || len(responseData) > 2<<20 {
		return response.StatusCode, errors.New("task helper: response is invalid or too large")
	}
	if response.StatusCode == http.StatusNoContent {
		return response.StatusCode, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("task helper: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(responseData)))
	}
	if _, err := output.Write(responseData); err != nil {
		return response.StatusCode, err
	}
	return response.StatusCode, nil
}

func runLinearGraphQLHelper(ctx context.Context, input io.Reader, output io.Writer) int {
	key := os.Getenv("LINEAR_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "linear GraphQL: LINEAR_API_KEY is required")
		return 1
	}
	data, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		fmt.Fprintln(os.Stderr, "linear GraphQL: request is invalid or too large")
		return 2
	}
	var envelope struct {
		Query         string          `json:"query"`
		Variables     json.RawMessage `json:"variables,omitempty"`
		OperationName string          `json:"operationName,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.Query == "" || len(envelope.Query) > 512<<10 {
		fmt.Fprintln(os.Stderr, "linear GraphQL: request is invalid")
		return 2
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, linearGraphQLURL, bytes.NewReader(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, "linear GraphQL: create request failed")
		return 1
	}
	request.Header.Set("Authorization", key)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "linear GraphQL: request failed: %v\n", err)
		return 1
	}
	defer response.Body.Close()
	responseData, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil || len(responseData) > 4<<20 {
		fmt.Fprintln(os.Stderr, "linear GraphQL: response is invalid or too large")
		return 1
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "linear GraphQL: HTTP %d\n", response.StatusCode)
		return 1
	}
	if _, err := output.Write(responseData); err != nil {
		fmt.Fprintln(os.Stderr, "linear GraphQL: write response failed")
		return 1
	}
	return 0
}

func runCheckpointHelper(args []string) int {
	if len(args) == 0 || args[0] != "ready-for-merge" {
		fmt.Fprintln(os.Stderr, "usage: factory agent checkpoint ready-for-merge --repo owner/name --pr N --base branch --head branch --verified-head OID")
		return 2
	}
	flags := flag.NewFlagSet("agent checkpoint ready-for-merge", flag.ContinueOnError)
	repository := flags.String("repo", "", "GitHub repository in owner/name form")
	pullRequest := flags.Int("pr", 0, "pull request number")
	baseBranch := flags.String("base", "", "pull request base branch")
	headBranch := flags.String("head", "", "pull request head branch")
	verifiedHead := flags.String("verified-head", "", "locally verified head OID")
	if flags.Parse(args[1:]) != nil {
		return 2
	}
	runID := os.Getenv("FACTORY_RUN_ID")
	runDirectory := filepath.Clean(os.Getenv("FACTORY_RUN_DIR"))
	if runID == "" || runDirectory == "." || filepath.Base(runDirectory) != runID || filepath.Base(filepath.Dir(runDirectory)) != "runs" {
		fmt.Fprintln(os.Stderr, "ready checkpoint: Factory run environment is invalid")
		return 2
	}
	checkpoint := agentrun.ReadyCheckpoint{
		ContractVersion: agentrun.LifecycleContractVersion,
		RunID:           runID,
		Repository:      strings.TrimSpace(*repository),
		PullRequest:     *pullRequest,
		BaseBranch:      strings.TrimSpace(*baseBranch),
		HeadBranch:      strings.TrimSpace(*headBranch),
		VerifiedHeadOID: strings.TrimSpace(*verifiedHead),
		CreatedAt:       time.Now().UTC(),
	}
	if source := os.Getenv("FACTORY_TASK_SOURCE"); source != "" {
		task, err := (taskmodel.TaskRef{
			Source: taskmodel.Source(source), ProviderID: os.Getenv("FACTORY_TASK_PROVIDER_ID"), Identifier: os.Getenv("FACTORY_TASK_IDENTIFIER"),
		}).Normalize()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		checkpoint.Task = task
	}
	if err := agentrun.WriteReadyCheckpoint(runDirectory, checkpoint); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(checkpoint); err != nil {
		fmt.Fprintf(os.Stderr, "ready checkpoint: encode response: %v\n", err)
		return 1
	}
	return 0
}

type matchFlags []string

func (m *matchFlags) String() string {
	return strings.Join(*m, ",")
}

func (m *matchFlags) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func runEventsHelper(ctx context.Context, args []string, output io.Writer) int {
	flags := flag.NewFlagSet("agent events", flag.ContinueOnError)
	source := flags.String("source", "", "event source")
	eventType := flags.String("type", "", "event type")
	action := flags.String("action", "", "event action")
	subject := flags.String("subject", "", "event subject")
	after := flags.Uint64("after", 0, "event cursor")
	wait := flags.Duration("wait", time.Minute, "maximum time to wait")
	var matches matchFlags
	flags.Var(&matches, "match", "attribute match in key=value form")
	if flags.Parse(args) != nil || *wait < 0 || *wait > 5*time.Minute {
		return 2
	}
	filter := eventwire.Filter{
		Source:     eventwire.Source(strings.TrimSpace(*source)),
		Type:       strings.TrimSpace(*eventType),
		Action:     strings.TrimSpace(*action),
		Subject:    strings.TrimSpace(*subject),
		Attributes: make(map[string]string, len(matches)),
	}
	if filter.Source != "" && !eventwire.ValidSource(filter.Source) {
		fmt.Fprintln(os.Stderr, "event wire: invalid source")
		return 2
	}
	for _, match := range matches {
		key, value, found := strings.Cut(match, "=")
		if !found || strings.TrimSpace(key) == "" {
			fmt.Fprintln(os.Stderr, "event wire: --match must be key=value")
			return 2
		}
		filter.Attributes[strings.TrimSpace(key)] = value
	}
	journalPath, err := factoryJournalPath("system-events.jsonl")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	read := func(cursor uint64) (eventwire.Batch, error) {
		return eventwire.Read(journalPath, filter, cursor)
	}
	var batch eventwire.Batch
	if *wait == 0 {
		batch, err = read(*after)
	} else {
		waitCtx, cancel := context.WithTimeout(ctx, *wait)
		defer cancel()
		batch, err = eventwire.Wait(waitCtx, read, *after, 250*time.Millisecond)
		if errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(output).Encode(batch); err != nil {
		fmt.Fprintf(os.Stderr, "agent events: encode response: %v\n", err)
		return 1
	}
	return 0
}

func factoryJournalPath(name string) (string, error) {
	runID := os.Getenv("FACTORY_RUN_ID")
	runDirectory := filepath.Clean(os.Getenv("FACTORY_RUN_DIR"))
	if runID == "" || runDirectory == "." || filepath.Base(runDirectory) != runID || filepath.Base(filepath.Dir(runDirectory)) != "runs" {
		return "", errors.New("agent events: Factory run environment is invalid")
	}
	stateRoot := filepath.Dir(filepath.Dir(runDirectory))
	return filepath.Join(stateRoot, "data", name), nil
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
	journalPath := filepath.Join(stateRoot, "data", "system-events.jsonl")

	var batch linearhook.Batch
	var err error
	if *wait == 0 {
		batch, err = linearhook.ReadWire(journalPath, filter, *after)
	} else {
		waitCtx, cancel := context.WithTimeout(ctx, *wait)
		defer cancel()
		batch, err = linearhook.WaitWire(waitCtx, journalPath, filter, *after, 250*time.Millisecond)
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
	journalPath := filepath.Join(stateRoot, "data", "system-events.jsonl")

	var batch githubhook.Batch
	var err error
	if *wait == 0 {
		batch, err = githubhook.ReadWire(journalPath, filter, *after)
	} else {
		waitCtx, cancel := context.WithTimeout(ctx, *wait)
		defer cancel()
		batch, err = githubhook.WaitWire(waitCtx, journalPath, filter, *after, 250*time.Millisecond)
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
