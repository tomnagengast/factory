package agentrun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

const (
	claudeChildSettings = `{"permissions":{"deny":["EnterPlanMode","ExitPlanMode","DesignSync","NotebookEdit","SendMessage","PushNotification","RemoteTrigger","ReportFindings","ScheduleWakeup","AskUserQuestion","CronCreate","CronDelete","CronList"]},"disableBundledSkills":true,"disableWorkflows":true,"disableRemoteControl":true,"disableClaudeAiConnectors":true,"disableArtifact":true}`
)

type PrincipalConfig struct {
	Task            taskmodel.TaskRef
	IssueIdentifier string
	TriggerKind     string
	RepoPath        string
	RunDirectory    string
	CodexPath       string
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
	AttemptOffset   int
	Provider        settings.PrincipalSettings
	Workflow        workflow.Pinned
}

type ChildConfig struct {
	Provider         string
	RepoPath         string
	PromptPath       string
	OutputDirectory  string
	CodexPath        string
	ClaudePath       string
	Now              func() time.Time
	ProviderSettings settings.ProviderSettings
}

func ExecutePrincipal(ctx context.Context, config PrincipalConfig) int {
	if err := os.MkdirAll(config.RunDirectory, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "create run directory: %v\n", err)
		return 1
	}
	task, err := taskmodel.ResolveCompatibilityIdentity(config.Task, config.IssueIdentifier)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve principal task: %v\n", err)
		return 1
	}
	prompt := taskPrincipalPrompt(task, config.TriggerKind, config.Workflow)
	if err := os.WriteFile(filepath.Join(config.RunDirectory, "prompt.txt"), []byte(prompt), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "write principal prompt: %v\n", err)
		return 1
	}

	var lastExit int
	var lastDetail string
	attemptsUsed := 0
	threadID := ""
	for attempt := 1; attempt <= config.Provider.MaxAttempts; attempt++ {
		attemptsUsed = attempt
		attemptNumber := config.AttemptOffset + attempt
		finalPath := filepath.Join(config.RunDirectory, fmt.Sprintf("attempt-%d-final.txt", attemptNumber))
		eventsPath := filepath.Join(config.RunDirectory, fmt.Sprintf("attempt-%d-events.jsonl", attemptNumber))
		errPath := filepath.Join(config.RunDirectory, fmt.Sprintf("attempt-%d-stderr.log", attemptNumber))
		continuation := prompt
		if attempt > 1 {
			continuation = "Resume the Factory workflow from durable repository, task-provider, pull-request, and Run state. Do not duplicate work or replace the pinned workflow revision."
		}
		exitCode, err := runCodex(ctx, config, threadID, continuation, finalPath, eventsPath, errPath)
		lastExit = exitCode
		if err == nil {
			finalMessage, readErr := os.ReadFile(finalPath)
			if readErr != nil {
				lastDetail = fmt.Sprintf("read final message: %v", readErr)
				break
			}
			status, blocker, detail := resultFromFinalMessage(string(finalMessage))
			result := ProcessResult{
				Status:     status,
				Blocker:    blocker,
				Attempts:   attemptNumber,
				ExitCode:   0,
				Detail:     detail,
				FinishedAt: config.Now().UTC(),
			}
			if writeErr := writeProcessResult(config.RunDirectory, result); writeErr != nil {
				fmt.Fprintf(os.Stderr, "write process result: %v\n", writeErr)
				return 1
			}
			if status == string(StateFailed) {
				return 1
			}
			return 0
		}

		lastDetail = err.Error()
		if found := readThreadID(eventsPath); found != "" {
			threadID = found
		}
		if attempt < config.Provider.MaxAttempts {
			if sleepErr := config.Sleep(ctx, time.Duration(attempt*5)*time.Second); sleepErr != nil {
				lastDetail = sleepErr.Error()
				break
			}
		}
	}

	result := ProcessResult{
		Status:     string(StateFailed),
		Attempts:   config.AttemptOffset + attemptsUsed,
		ExitCode:   lastExit,
		Detail:     lastDetail,
		FinishedAt: config.Now().UTC(),
	}
	if err := writeProcessResult(config.RunDirectory, result); err != nil {
		fmt.Fprintf(os.Stderr, "write process result: %v\n", err)
	}
	return 1
}

func ReadWorkflowSnapshot(runDirectory, path string) (workflow.Pinned, string, error) {
	expected := filepath.Join(filepath.Clean(runDirectory), WorkflowSnapshotFileName)
	if filepath.Clean(path) != expected || !filepath.IsAbs(path) {
		return workflow.Pinned{}, "", errors.New("pinned workflow path is invalid")
	}
	info, err := os.Stat(path)
	if err != nil {
		return workflow.Pinned{}, "", fmt.Errorf("read pinned workflow: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return workflow.Pinned{}, "", errors.New("pinned workflow permissions are invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return workflow.Pinned{}, "", fmt.Errorf("read pinned workflow: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, workflow.MaxAuthoringBodyBytes+1))
	if err != nil {
		return workflow.Pinned{}, "", fmt.Errorf("read pinned workflow: %w", err)
	}
	if len(data) > workflow.MaxAuthoringBodyBytes {
		return workflow.Pinned{}, "", errors.New("pinned workflow is too large")
	}
	snapshot, err := workflow.DecodePinnedSnapshot(data)
	if err != nil {
		return workflow.Pinned{}, "", err
	}
	return snapshot.Workflow, snapshot.Digest, nil
}

func ExecuteChild(ctx context.Context, config ChildConfig) int {
	if err := os.MkdirAll(config.OutputDirectory, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "create child output directory: %v\n", err)
		return 1
	}
	prompt, err := os.Open(config.PromptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open child prompt: %v\n", err)
		return 1
	}
	defer prompt.Close()

	eventsPath := filepath.Join(config.OutputDirectory, "events.jsonl")
	errPath := filepath.Join(config.OutputDirectory, "stderr.log")
	finalPath := filepath.Join(config.OutputDirectory, "final.txt")
	stdout, stderr, closeFiles, err := outputWriters(eventsPath, errPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open child output: %v\n", err)
		return 1
	}
	defer closeFiles()

	var cmd *exec.Cmd
	switch config.Provider {
	case "codex":
		cmd = exec.CommandContext(ctx, config.CodexPath, codexChildArgs(config.ProviderSettings, finalPath)...)
	case "claude":
		cmd = exec.CommandContext(ctx, config.ClaudePath, claudeChildArgs(config.ProviderSettings)...)
	default:
		fmt.Fprintf(os.Stderr, "unsupported child provider %q\n", config.Provider)
		return 2
	}
	cmd.Dir = config.RepoPath
	cmd.Stdin = prompt
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	exitCode := exitCode(err)
	result := ProcessResult{
		Status:     string(StateSucceeded),
		Attempts:   1,
		ExitCode:   exitCode,
		FinishedAt: config.Now().UTC(),
	}
	if err != nil {
		result.Status = string(StateFailed)
		result.Detail = err.Error()
	}
	if writeErr := writeJSONFile(filepath.Join(config.OutputDirectory, resultFileName), result); writeErr != nil {
		fmt.Fprintf(os.Stderr, "write child result: %v\n", writeErr)
		return 1
	}
	return exitCode
}

func runCodex(
	ctx context.Context,
	config PrincipalConfig,
	threadID,
	prompt,
	finalPath,
	eventsPath,
	errPath string,
) (int, error) {
	stdout, stderr, closeFiles, err := outputWriters(eventsPath, errPath)
	if err != nil {
		return 1, err
	}
	defer closeFiles()

	args := principalCodexArgs(config.Provider.ProviderSettings, threadID, finalPath)
	cmd := exec.CommandContext(ctx, config.CodexPath, args...)
	cmd.Dir = config.RepoPath
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if err != nil {
		return exitCode(err), fmt.Errorf("codex attempt: %w", err)
	}
	return 0, nil
}

func codexChildArgs(provider settings.ProviderSettings, finalPath string) []string {
	return []string{
		"exec",
		"--dangerously-bypass-approvals-and-sandbox",
		"--json",
		"--color", "never",
		"--config", "model_reasoning_effort=" + provider.Effort,
		"--model", provider.Model,
		"--output-last-message", finalPath,
		"-",
	}
}

func claudeChildArgs(provider settings.ProviderSettings) []string {
	return []string{
		"--model", provider.Model,
		"--effort", provider.Effort,
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--settings", claudeChildSettings,
		"--print",
	}
}

func principalCodexArgs(provider settings.ProviderSettings, threadID, finalPath string) []string {
	if threadID == "" {
		return []string{
			"exec",
			"--dangerously-bypass-approvals-and-sandbox",
			"--json",
			"--color", "never",
			"--config", "model_reasoning_effort=" + provider.Effort,
			"--model", provider.Model,
			"--output-last-message", finalPath,
			"-",
		}
	}
	return []string{
		"exec", "resume",
		"--dangerously-bypass-approvals-and-sandbox",
		"--json",
		"--config", "model_reasoning_effort=" + provider.Effort,
		"--model", provider.Model,
		"--output-last-message", finalPath,
		threadID,
		"-",
	}
}

func principalPrompt(issueIdentifier, triggerKind string, pin workflow.Pinned) string {
	task, err := taskmodel.LegacyLinear(issueIdentifier)
	if err != nil {
		return "invalid Factory task identity"
	}
	return taskPrincipalPrompt(task, triggerKind, pin)
}

func taskPrincipalPrompt(task taskmodel.TaskRef, triggerKind string, pin workflow.Pinned) string {
	if pin.IsLegacy() {
		return legacyPrincipalPrompt(task.Identifier, triggerKind, pin.LegacyDefinition())
	}
	switch triggerKind {
	case TriggerKindLabel, TriggerKindComment, TriggerKindGitHub, TriggerKindPostMerge, TriggerKindRule:
	default:
		triggerKind = TriggerKindLabel
	}
	digest, err := pin.Digest()
	if err != nil {
		digest = "invalid"
	}
	segment := "initial"
	context := "Complete the issue from its current durable state."
	if triggerKind == TriggerKindComment {
		segment = "feedback"
		if task.Source == taskmodel.SourceFactory {
			context = "Fresh-read the complete durable task conversation first. Treat every later human message or gate decision not already addressed by Factory evidence as current scope. Resume active work when it exists; otherwise create a focused continuation from fetched default branch state."
		} else {
			context = "Fresh-read the complete Linear conversation first. Treat every later human comment not already addressed by Factory evidence as current scope. Resume active work when it exists; otherwise create a focused continuation from fetched default branch state."
		}
	}
	if triggerKind == TriggerKindPostMerge || triggerKind == TriggerKindGitHub {
		segment = "remediation"
		provider := "Linear"
		if task.Source == taskmodel.SourceFactory {
			provider = "task-provider"
		}
		context = "Fresh-read authoritative pull-request, " + provider + ", repository, approved-plan, deployment, and cleanup state. Address an open pull request or complete post-merge work without recreating finished implementation."
		if triggerKind == TriggerKindPostMerge {
			segment = "post-merge"
		}
	}
	identityLabel := "Issue"
	if task.Source != taskmodel.SourceLinear {
		identityLabel = "Task"
	}
	providerInstructions := `LINEAR_API_KEY is available in the inherited environment. Send GraphQL request JSON on stdin to "$FACTORY_AGENT_HELPER" agent linear-graphql. Never pass the key in arguments or print it.`
	if pin.ID == workflow.ProviderNeutralID && digest == workflow.ProviderNeutralDigest() {
		providerInstructions = `Use "$FACTORY_AGENT_HELPER" agent task commands for the exact task scoped to this Run. The helper capability derives task identity and never accepts a different task ID.`
	}
	return fmt.Sprintf(`FACTORY WORKFLOW SEGMENT
%s: %s
Task source: %s
Task provider ID: %s
Trigger: %s
Segment: %s
Workflow: %s revision %d
Workflow digest: %s

%s

----- BEGIN PINNED WORKFLOW MARKDOWN -----
%s
----- END PINNED WORKFLOW MARKDOWN -----

FACTORY RUNTIME PROTOCOL

You are the principal agent in a Factory-managed tmux session. The pinned Markdown is the procedural workflow for this Run. Continue until it reaches the applicable Factory terminal boundary.

%s

When another agent can independently research, review, or verify a bounded subtask, launch it as a window in this same tmux session instead of using an invisible in-process subagent. Pass its prompt as data with a quoted heredoc:

"$FACTORY_AGENT_HELPER" agent spawn --provider claude --name short-name <<'PROMPT'
Put the complete child prompt here.
PROMPT

The helper returns the tmux window and durable output paths. Child windows inherit the same helper and may spawn their own bounded children. Keep all work for this issue inside this session. Wait for every child window and consume its result before you finish. If a child must be stopped, kill only that window. Never use tmux kill-server.

GitHub and Linear journal events are durable wake signals only. Use "$FACTORY_AGENT_HELPER" agent github-events and "$FACTORY_AGENT_HELPER" agent linear-comments, then refresh authoritative state after every event or timeout.

At the ready-for-human-merge boundary, write the validated checkpoint with "$FACTORY_AGENT_HELPER" agent checkpoint ready-for-merge, then end with exactly FACTORY_RESULT: READY_FOR_HUMAN_MERGE. Do not keep an LLM turn alive while waiting for the human.

Before a ready checkpoint exists, the only valid blockers are missing_routing_metadata, approval_denied, authority_unavailable, and decision_required. Use decision_required when planning or review cannot safely continue without new human direction. safeguard_regression is not a pre-checkpoint blocker.

After merge, prove the reported merge commit contains the exact checkpointed head with git merge-base --is-ancestor. A rebase or squash merge that replayed the changes without containing that commit is verified_head_mismatch even when GitHub still reports the original pull-request head. Do not deploy it. safeguard_regression is reserved for authoritative pull-request checks or reviews that regressed after the checkpoint.

If the complete post-merge workflow succeeds, end with exactly FACTORY_RESULT: SUCCEEDED. If it reaches a genuine typed blocker, put FACTORY_BLOCKER: <type> on the preceding line and end with exactly FACTORY_RESULT: BLOCKED. Allowed types are missing_routing_metadata, approval_denied, authority_unavailable, decision_required, closed_unmerged, verified_head_mismatch, safeguard_regression, deployment_source_invalid, external_authentication, deployment_failed, and cleanup_failed.

Factory's mechanical repository routing, one-Run ownership, checkpoint, human-merge, verified-head, deployment-source, completion, and cleanup validators are authoritative and cannot be waived by workflow text.`, identityLabel, task.Identifier, task.Source, task.ProviderID, triggerKind, segment, pin.Name, pin.Revision, digest, context, pin.Markdown, providerInstructions)
}

func legacyPrincipalPrompt(issueIdentifier, triggerKind string, definition workflow.LegacyDefinition) string {
	return fmt.Sprintf(`Use $do to continue %s using the retained legacy workflow %s.

This Run was admitted before Markdown workflow migration. Preserve its original semantics and do not reinterpret it as a new workflow revision.

Legacy trigger kind: %s
Legacy operator guidance:
%s`, issueIdentifier, definition.Name, triggerKind, legacyWorkflowSteps(definition.Steps))
}

func legacyWorkflowSteps(steps []string) string {
	var rendered strings.Builder
	for index, step := range steps {
		fmt.Fprintf(&rendered, "%d. %s\n", index+1, step)
	}
	return strings.TrimSpace(rendered.String())
}

func resultFromFinalMessage(message string) (string, string, string) {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return string(StateFailed), "", "principal returned an empty final message"
	}
	lines := strings.Split(trimmed, "\n")
	switch strings.TrimSpace(lines[len(lines)-1]) {
	case "FACTORY_RESULT: SUCCEEDED":
		return string(StateSucceeded), "", ""
	case "FACTORY_RESULT: BLOCKED":
		blocker := ""
		if len(lines) > 1 {
			blocker = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[len(lines)-2]), "FACTORY_BLOCKER:"))
			if !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-2]), "FACTORY_BLOCKER:") {
				blocker = ""
			}
		}
		return string(StateBlocked), blocker, "principal reported blocker " + blocker
	case "FACTORY_RESULT: READY_FOR_HUMAN_MERGE":
		return ResultReadyForMerge, "", "waiting for human merge"
	default:
		return string(StateFailed), "", "principal final message is missing a Factory result marker"
	}
}

func readThreadID(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "thread.started" {
			return event.ThreadID
		}
	}
	return ""
}

func outputWriters(eventsPath, errPath string) (io.Writer, io.Writer, func(), error) {
	events, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("open events output: %w", err)
	}
	diagnostics, err := os.OpenFile(errPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		events.Close()
		return nil, nil, func() {}, fmt.Errorf("open diagnostics output: %w", err)
	}
	closeFiles := func() {
		events.Close()
		diagnostics.Close()
	}
	return io.MultiWriter(os.Stdout, events), io.MultiWriter(os.Stderr, diagnostics), closeFiles, nil
}

func writeProcessResult(runDirectory string, result ProcessResult) error {
	return writeJSONFile(filepath.Join(runDirectory, resultFileName), result)
}

func writeJSONFile(path string, value any) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".result-*")
	if err != nil {
		return fmt.Errorf("create result file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("set result permissions: %w", err)
	}
	if err := json.NewEncoder(temp).Encode(value); err != nil {
		temp.Close()
		return fmt.Errorf("encode result: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close result: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace result: %w", err)
	}
	return nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}
