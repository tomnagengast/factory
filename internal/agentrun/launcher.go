package agentrun

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tomnagengast/factory/internal/workflow"
)

const resultFileName = "result.json"

const WorkflowSnapshotFileName = "workflow.json"

const ResultReadyForMerge = "ready_for_human_merge"

var repositoryPrepareLocks sync.Map

type LauncherConfig struct {
	Repository    string
	RepoURL       string
	RepoPath      string
	ManagedRoot   string
	BaseBranch    string
	Bootstrap     bool
	StateRoot     string
	BinaryPath    string
	GitPath       string
	GitHubPath    string
	WorktrunkPath string
	TmuxPath      string
	TmuxSocket    string
	TaskEndpoint  string
	// Repositories returns the current admitted repository catalog, exposed to
	// every run as FACTORY_REPOSITORIES so agents can work across repositories.
	Repositories func() []RepositoryConfig
}

type TmuxLauncher struct {
	config LauncherConfig
}

type linkedWorktree struct {
	path         string
	relativePath string
	branch       string
	branchRef    string
	locked       bool
}

type ProcessResult struct {
	Status     string    `json:"status"`
	Blocker    string    `json:"blocker,omitempty"`
	Attempts   int       `json:"attempts"`
	ExitCode   int       `json:"exitCode"`
	Detail     string    `json:"detail,omitempty"`
	FinishedAt time.Time `json:"finishedAt"`
}

func NewTmuxLauncher(config LauncherConfig) (*TmuxLauncher, error) {
	if config.RepoURL == "" || config.RepoPath == "" || config.StateRoot == "" {
		return nil, errors.New("agent launcher: repository and state paths are required")
	}
	if config.BinaryPath == "" || config.GitPath == "" || config.WorktrunkPath == "" || config.TmuxPath == "" || config.TmuxSocket == "" {
		return nil, errors.New("agent launcher: binary, git, worktrunk, tmux, and socket are required")
	}
	if config.ManagedRoot == "" {
		config.ManagedRoot = filepath.Dir(config.RepoPath)
	}
	if config.BaseBranch == "" {
		config.BaseBranch = "main"
	}
	if config.Bootstrap && (config.Repository == "" || config.GitHubPath == "") {
		return nil, errors.New("agent launcher: bootstrap requires repository identity and GitHub CLI")
	}
	return &TmuxLauncher{config: config}, nil
}

func (l *TmuxLauncher) Prepare(ctx context.Context) error {
	lockValue, _ := repositoryPrepareLocks.LoadOrStore(filepath.Clean(l.config.RepoPath), &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	if err := validateManagedTarget(l.config.ManagedRoot, l.config.RepoPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.config.RepoPath), 0o700); err != nil {
		return fmt.Errorf("create workspace parent: %w", err)
	}
	info, err := os.Lstat(l.config.RepoPath)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return fmt.Errorf("inspect agent workspace: %w", err)
	}
	if !missing {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("agent workspace path must not be a symbolic link")
		}
		if !info.IsDir() {
			return errors.New("agent workspace path is not a directory")
		}
	}
	gitDirectory := filepath.Join(l.config.RepoPath, ".git")
	if _, err := os.Stat(gitDirectory); errors.Is(err, os.ErrNotExist) {
		if !missing {
			return errors.New("agent workspace exists but is not a Git checkout")
		}
		if err := l.prepareRemote(ctx); err != nil {
			return err
		}
		if err := l.cloneWorkspace(ctx); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("inspect agent workspace: %w", err)
	}
	if err := validateManagedTarget(l.config.ManagedRoot, l.config.RepoPath); err != nil {
		return err
	}
	if err := l.validateOrigin(ctx); err != nil {
		return err
	}
	if err := l.ensureBaseBranch(ctx); err != nil {
		return err
	}
	if err := l.validateGitHubDefaultBranch(ctx); err != nil {
		return err
	}
	if err := l.reconcileGitHubRepositoryPolicy(ctx); err != nil {
		return err
	}
	return l.synchronizeWorkspace(ctx)
}

type githubRepository struct {
	NameWithOwner       string `json:"nameWithOwner"`
	IsPrivate           bool   `json:"isPrivate"`
	MergeCommitAllowed  bool   `json:"mergeCommitAllowed"`
	SquashMergeAllowed  bool   `json:"squashMergeAllowed"`
	RebaseMergeAllowed  bool   `json:"rebaseMergeAllowed"`
	DeleteBranchOnMerge bool   `json:"deleteBranchOnMerge"`
	DefaultBranchRef    *struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
}

func (l *TmuxLauncher) prepareRemote(ctx context.Context) error {
	if !l.config.Bootstrap {
		return nil
	}
	repository, found, err := l.readGitHubRepository(ctx)
	if err != nil {
		return err
	}
	if !found {
		if err := runCommand(ctx, "create GitHub repository", "", l.config.GitHubPath, "repo", "create", l.config.Repository, "--private"); err != nil {
			if _, exists, readErr := l.readGitHubRepository(ctx); readErr != nil || !exists {
				return err
			}
		}
		repository, found, err = l.readGitHubRepository(ctx)
		if err != nil {
			return fmt.Errorf("verify created GitHub repository: %w", err)
		}
		if !found {
			return errors.New("verify created GitHub repository: repository is still missing")
		}
	}
	if !strings.EqualFold(repository.NameWithOwner, l.config.Repository) {
		return fmt.Errorf("GitHub repository is %q, want %q", repository.NameWithOwner, l.config.Repository)
	}
	if !repository.IsPrivate {
		return errors.New("bootstrap GitHub repository is not private")
	}
	if repository.DefaultBranchRef != nil && repository.DefaultBranchRef.Name != "" && repository.DefaultBranchRef.Name != l.config.BaseBranch {
		return fmt.Errorf("GitHub default branch is %q, want %q", repository.DefaultBranchRef.Name, l.config.BaseBranch)
	}
	return nil
}

func (l *TmuxLauncher) validateGitHubDefaultBranch(ctx context.Context) error {
	if l.config.Repository == "" || l.config.GitHubPath == "" {
		return nil
	}
	repository, found, err := l.readGitHubRepository(ctx)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("configured GitHub repository does not exist")
	}
	if repository.DefaultBranchRef == nil || repository.DefaultBranchRef.Name != l.config.BaseBranch {
		return fmt.Errorf("GitHub default branch does not match %s", l.config.BaseBranch)
	}
	return nil
}

func (l *TmuxLauncher) reconcileGitHubRepositoryPolicy(ctx context.Context) error {
	if l.config.Repository == "" || l.config.GitHubPath == "" {
		return nil
	}
	repository, found, err := l.readGitHubRepository(ctx)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("configured GitHub repository does not exist")
	}
	if githubRepositoryPolicyMatches(repository) {
		return nil
	}
	if err := runCommand(
		ctx,
		"reconcile GitHub repository merge policy",
		"",
		l.config.GitHubPath,
		"repo", "edit", l.config.Repository,
		"--enable-merge-commit=true",
		"--enable-squash-merge=false",
		"--enable-rebase-merge=false",
		"--delete-branch-on-merge=true",
	); err != nil {
		return err
	}
	repository, found, err = l.readGitHubRepository(ctx)
	if err != nil {
		return fmt.Errorf("verify GitHub repository merge policy: %w", err)
	}
	if !found || !githubRepositoryPolicyMatches(repository) {
		return errors.New("verify GitHub repository merge policy: desired policy did not converge")
	}
	return nil
}

func githubRepositoryPolicyMatches(repository githubRepository) bool {
	return repository.MergeCommitAllowed &&
		!repository.SquashMergeAllowed &&
		!repository.RebaseMergeAllowed &&
		repository.DeleteBranchOnMerge
}

func (l *TmuxLauncher) readGitHubRepository(ctx context.Context) (githubRepository, bool, error) {
	cmd := exec.CommandContext(ctx, l.config.GitHubPath, "repo", "view", l.config.Repository, "--json", "nameWithOwner,isPrivate,defaultBranchRef,mergeCommitAllowed,squashMergeAllowed,rebaseMergeAllowed,deleteBranchOnMerge")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if strings.Contains(strings.ToLower(detail), "could not resolve to a repository") {
			return githubRepository{}, false, nil
		}
		return githubRepository{}, false, fmt.Errorf("inspect GitHub repository: %w: %s", err, detail)
	}
	var repository githubRepository
	if err := json.Unmarshal(output, &repository); err != nil {
		return githubRepository{}, false, fmt.Errorf("decode GitHub repository: %w", err)
	}
	return repository, true, nil
}

func (l *TmuxLauncher) cloneWorkspace(ctx context.Context) error {
	parent := filepath.Dir(l.config.RepoPath)
	staging, err := os.MkdirTemp(parent, ".factory-clone-")
	if err != nil {
		return fmt.Errorf("create clone staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := runCommand(ctx, "clone agent workspace", "", l.config.GitPath, "clone", l.config.RepoURL, staging); err != nil {
		return err
	}
	if err := os.Rename(staging, l.config.RepoPath); err != nil {
		return fmt.Errorf("install cloned agent workspace: %w", err)
	}
	return nil
}

func (l *TmuxLauncher) validateOrigin(ctx context.Context) error {
	top, err := commandOutput(ctx, "inspect agent workspace root", l.config.RepoPath, l.config.GitPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	resolvedTop, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil {
		return fmt.Errorf("resolve agent workspace root: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(l.config.RepoPath)
	if err != nil {
		return fmt.Errorf("resolve agent workspace target: %w", err)
	}
	if resolvedTop != resolvedTarget {
		return fmt.Errorf("agent workspace Git top-level is %s, want %s", resolvedTop, resolvedTarget)
	}
	remote, err := commandOutput(ctx, "inspect agent workspace origin", l.config.RepoPath, l.config.GitPath, "config", "--get", "remote.origin.url")
	if err != nil {
		return err
	}
	actual := strings.TrimSpace(string(remote))
	if l.config.Repository != "" {
		repository, ok := normalizeGitHubRepository(actual)
		if !ok || !strings.EqualFold(repository, l.config.Repository) {
			return fmt.Errorf("agent workspace origin %q does not match %s", actual, l.config.Repository)
		}
	} else if filepath.Clean(actual) != filepath.Clean(l.config.RepoURL) {
		return fmt.Errorf("agent workspace origin %q does not match %q", actual, l.config.RepoURL)
	}
	return nil
}

func (l *TmuxLauncher) ensureBaseBranch(ctx context.Context) error {
	remoteRef := "refs/remotes/origin/" + l.config.BaseBranch
	if commandSucceeds(ctx, l.config.RepoPath, l.config.GitPath, "rev-parse", "--verify", "--quiet", remoteRef) {
		if !commandSucceeds(ctx, l.config.RepoPath, l.config.GitPath, "rev-parse", "--verify", "--quiet", "HEAD") {
			return runCommand(ctx, "check out agent workspace base", l.config.RepoPath, l.config.GitPath, "switch", "--track", "-c", l.config.BaseBranch, "origin/"+l.config.BaseBranch)
		}
		return nil
	}
	refs, err := commandOutput(ctx, "inspect agent workspace remote branches", l.config.RepoPath, l.config.GitPath, "for-each-ref", "--format=%(refname)", "refs/remotes/origin")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(refs)) != "" {
		return fmt.Errorf("agent workspace remote does not contain configured base branch %s", l.config.BaseBranch)
	}
	if !l.config.Bootstrap {
		return fmt.Errorf("agent workspace remote base branch %s does not exist", l.config.BaseBranch)
	}
	status, err := commandOutput(ctx, "inspect empty agent workspace", l.config.RepoPath, l.config.GitPath, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(status)) != "" {
		return errors.New("empty bootstrap checkout has local changes")
	}
	if err := runCommand(ctx, "select bootstrap base branch", l.config.RepoPath, l.config.GitPath, "symbolic-ref", "HEAD", "refs/heads/"+l.config.BaseBranch); err != nil {
		return err
	}
	if err := runCommand(ctx, "initialize bootstrap repository", l.config.RepoPath, l.config.GitPath, "-c", "user.name=Factory", "-c", "user.email=factory@nags.cloud", "commit", "--allow-empty", "-m", "Initialize repository"); err != nil {
		return err
	}
	if err := runCommand(ctx, "publish bootstrap base branch", l.config.RepoPath, l.config.GitPath, "push", "-u", "origin", l.config.BaseBranch); err != nil {
		return err
	}
	if err := runCommand(ctx, "set GitHub default branch", "", l.config.GitHubPath, "repo", "edit", l.config.Repository, "--default-branch", l.config.BaseBranch); err != nil {
		return err
	}
	return runCommand(ctx, "fetch initialized agent workspace", l.config.RepoPath, l.config.GitPath, "fetch", "--prune", "origin")
}

func (l *TmuxLauncher) synchronizeWorkspace(ctx context.Context) error {
	worktrees, err := l.linkedWorktrees(ctx)
	if err != nil {
		return err
	}
	statusArgs := []string{"status", "--porcelain", "--", "."}
	for _, worktree := range worktrees {
		statusArgs = append(statusArgs, ":(exclude,literal)"+worktree.relativePath)
	}
	output, err := commandOutput(ctx, "inspect agent workspace status", l.config.RepoPath, l.config.GitPath, statusArgs...)
	if err != nil {
		return err
	}
	if detail := strings.TrimSpace(string(output)); detail != "" {
		return fmt.Errorf("agent workspace has local changes: %s", detail)
	}
	if err := runCommand(ctx, "fetch agent workspace", l.config.RepoPath, l.config.GitPath, "fetch", "--prune", "origin"); err != nil {
		return err
	}
	branch, err := commandOutput(ctx, "inspect agent workspace branch", l.config.RepoPath, l.config.GitPath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(branch)) != l.config.BaseBranch {
		return fmt.Errorf("agent workspace branch is %q, want %q", strings.TrimSpace(string(branch)), l.config.BaseBranch)
	}
	upstream, err := commandOutput(ctx, "inspect agent workspace upstream", l.config.RepoPath, l.config.GitPath, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(upstream)) != "origin/"+l.config.BaseBranch {
		return fmt.Errorf("agent workspace upstream is %q, want origin/%s", strings.TrimSpace(string(upstream)), l.config.BaseBranch)
	}
	return runCommand(ctx, "fast-forward agent workspace", l.config.RepoPath, l.config.GitPath, "merge", "--ff-only", "origin/"+l.config.BaseBranch)
}

func validateManagedTarget(root, target string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(target) {
		return errors.New("agent workspace managed root and path must be absolute")
	}
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if !pathWithin(root, target) || root == target {
		return errors.New("agent workspace path must stay below its managed root")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve agent workspace managed root: %w", err)
	}
	ancestor := target
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect agent workspace ancestor: %w", err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return errors.New("agent workspace has no existing ancestor")
		}
		ancestor = parent
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return fmt.Errorf("resolve agent workspace ancestor: %w", err)
	}
	if !pathWithin(resolvedRoot, resolvedAncestor) {
		return errors.New("agent workspace path escapes its managed root through a symbolic link")
	}
	return nil
}

func commandSucceeds(ctx context.Context, directory, name string, args ...string) bool {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	return cmd.Run() == nil
}

func (l *TmuxLauncher) CleanupWorktrees(ctx context.Context) error {
	worktrees, err := l.linkedWorktrees(ctx)
	if err != nil {
		return err
	}
	var cleanupErrors []error
	for _, worktree := range worktrees {
		if worktree.branch == "" || worktree.locked {
			continue
		}
		clean, err := l.worktreeClean(ctx, worktree)
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if !clean {
			continue
		}
		integrated, err := l.worktreeIntegrated(ctx, worktree)
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if !integrated {
			continue
		}
		if err := runCommand(ctx, "remove integrated worktree", l.config.RepoPath, l.config.WorktrunkPath, "-y", "remove", "--foreground", "--no-hooks", worktree.branch); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		_ = os.Remove(filepath.Dir(worktree.path))
	}
	return errors.Join(cleanupErrors...)
}

func (l *TmuxLauncher) linkedWorktrees(ctx context.Context) ([]linkedWorktree, error) {
	output, err := commandOutput(ctx, "list agent worktrees", l.config.RepoPath, l.config.GitPath, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	repoPath, err := filepath.EvalSymlinks(l.config.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("resolve agent workspace: %w", err)
	}

	var worktrees []linkedWorktree
	var current linkedWorktree
	for _, field := range bytes.Split(output, []byte{0}) {
		if len(field) == 0 {
			if worktree, ok := resolveLinkedWorktree(repoPath, current); ok {
				worktrees = append(worktrees, worktree)
			}
			current = linkedWorktree{}
			continue
		}
		key, value, _ := bytes.Cut(field, []byte(" "))
		switch string(key) {
		case "worktree":
			current.path = string(value)
		case "branch":
			current.branchRef = string(value)
			current.branch = strings.TrimPrefix(current.branchRef, "refs/heads/")
			if current.branch == current.branchRef {
				current.branch = ""
			}
		case "locked":
			current.locked = true
		}
	}
	return worktrees, nil
}

func resolveLinkedWorktree(repoPath string, worktree linkedWorktree) (linkedWorktree, bool) {
	if worktree.path == "" {
		return linkedWorktree{}, false
	}
	path, err := filepath.EvalSymlinks(worktree.path)
	if err != nil || path == repoPath {
		return linkedWorktree{}, false
	}
	relativePath, err := filepath.Rel(repoPath, path)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return linkedWorktree{}, false
	}
	worktree.path = path
	worktree.relativePath = filepath.ToSlash(relativePath)
	return worktree, true
}

func (l *TmuxLauncher) worktreeClean(ctx context.Context, worktree linkedWorktree) (bool, error) {
	output, err := commandOutput(ctx, "inspect worktree "+worktree.branch, worktree.path, l.config.GitPath, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(output)) == "", nil
}

func (l *TmuxLauncher) worktreeIntegrated(ctx context.Context, worktree linkedWorktree) (bool, error) {
	cmd := exec.CommandContext(ctx, l.config.GitPath, "merge-base", "--is-ancestor", worktree.branchRef, "@{upstream}")
	cmd.Dir = l.config.RepoPath
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect worktree integration %s: %w", worktree.branch, err)
}

func (l *TmuxLauncher) Start(ctx context.Context, run Run, sessionName, runDirectory string, options StartOptions) error {
	launcher := l.forRun(run)
	if launcher != l {
		if err := launcher.Prepare(ctx); err != nil {
			return err
		}
		if options.CleanupWorktrees {
			if err := launcher.CleanupWorktrees(ctx); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	if err := removeLifecycleArtifacts(runDirectory); err != nil {
		return err
	}
	workflowPath := ""
	if run.PinnedWorkflow != nil {
		context := "start Run"
		if run.InvocationID != "" {
			context = "start invocation Run"
		}
		if err := validatePinnedWorkflow(*run.PinnedWorkflow, run.PinnedWorkflowDigest, run.PinnedPolicyRevision); err != nil {
			return fmt.Errorf("%s: invalid pinned workflow: %w", context, err)
		}
		workflowPath = filepath.Join(runDirectory, WorkflowSnapshotFileName)
		snapshot := workflow.EncodePinnedSnapshot(*run.PinnedWorkflow, run.PinnedWorkflowDigest)
		if err := writeJSONFile(workflowPath, snapshot); err != nil {
			return fmt.Errorf("write pinned workflow: %w", err)
		}
	} else if run.InvocationID != "" {
		return errors.New("start invocation Run: pinned workflow is missing")
	}
	args := []string{
		"-L", launcher.config.TmuxSocket,
		"new-session", "-d",
		"-s", sessionName,
		"-n", "principal",
		"-c", launcher.config.RepoPath,
		"-e", "FACTORY_TMUX_SOCKET=" + launcher.config.TmuxSocket,
		"-e", "FACTORY_TMUX_SESSION=" + sessionName,
		"-e", "FACTORY_RUN_ID=" + run.ID,
		"-e", "FACTORY_RUN_DIR=" + runDirectory,
		"-e", "FACTORY_TASK_SOURCE=" + string(run.Task.Source),
		"-e", "FACTORY_TASK_PROVIDER_ID=" + run.Task.ProviderID,
		"-e", "FACTORY_TASK_IDENTIFIER=" + run.Task.Identifier,
		"-e", "FACTORY_TRIGGER_KIND=" + run.TriggerKind,
		"-e", "FACTORY_REPOSITORY=" + run.Repository,
		"-e", "FACTORY_REPO_PATH=" + launcher.config.RepoPath,
		"-e", "FACTORY_REPOSITORIES=" + launcher.repositoriesJSON(),
		"-e", "FACTORY_CLOUD_URL=" + run.CloudURL,
		"-e", "FACTORY_AGENT_HELPER=" + launcher.config.BinaryPath,
		launcher.config.BinaryPath,
		"agent-exec",
		"--task-source", string(run.Task.Source),
		"--task-provider-id", run.Task.ProviderID,
		"--task-identifier", run.Task.Identifier,
		"--issue", run.IssueIdentifier,
		"--trigger-kind", run.TriggerKind,
		"--repo", launcher.config.RepoPath,
		"--run-dir", runDirectory,
		"--attempt-offset", fmt.Sprintf("%d", run.Attempts),
	}
	providerNeutral := run.PinnedWorkflow != nil && run.PinnedWorkflow.ID == workflow.ProviderNeutralID
	if providerNeutral {
		if launcher.config.TaskEndpoint == "" {
			return errors.New("start provider-neutral Run: task helper endpoint is missing")
		}
		_, err := WriteTaskCapability(runDirectory, run, rand.Reader, time.Now())
		if err != nil {
			return fmt.Errorf("write task capability: %w", err)
		}
		commandIndex := slices.Index(args, launcher.config.BinaryPath)
		if commandIndex < 0 {
			return errors.New("start provider-neutral Run: principal command is missing")
		}
		args = slices.Insert(args, commandIndex,
			"-e", "FACTORY_TASK_ENDPOINT="+launcher.config.TaskEndpoint,
			"-e", "FACTORY_TASK_CAPABILITY_FILE="+filepath.Join(runDirectory, TaskCapabilityTokenFileName),
		)
	}
	if workflowPath != "" {
		args = append(args, "--workflow-file", workflowPath)
	}
	cmd := exec.CommandContext(ctx, launcher.config.TmuxPath, args...)
	cmd.Env = agentEnvironment(os.Environ(), !providerNeutral)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (l *TmuxLauncher) repositoriesJSON() string {
	if l.config.Repositories == nil {
		return ""
	}
	type catalogEntry struct {
		Repository string `json:"repository"`
		Path       string `json:"path"`
		BaseBranch string `json:"baseBranch"`
	}
	configs := l.config.Repositories()
	entries := make([]catalogEntry, 0, len(configs))
	for _, config := range configs {
		entries = append(entries, catalogEntry{
			Repository: config.Repository,
			Path:       config.RepoPath,
			BaseBranch: config.BaseBranch,
		})
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	return string(data)
}

func (l *TmuxLauncher) forRun(run Run) *TmuxLauncher {
	if run.RepositoryURL == "" || run.RepositoryPath == "" {
		return l
	}
	config := l.config
	config.RepoURL = run.RepositoryURL
	config.RepoPath = run.RepositoryPath
	config.Repository = run.Repository
	config.ManagedRoot = run.ManagedRoot
	if config.ManagedRoot == "" {
		config.ManagedRoot = filepath.Dir(run.RepositoryPath)
	}
	if run.BaseBranch != "" {
		config.BaseBranch = run.BaseBranch
	}
	config.Bootstrap = run.Bootstrap
	return &TmuxLauncher{config: config}
}

func removeLifecycleArtifacts(runDirectory string) error {
	for _, name := range []string{resultFileName, readyCheckpointFileName, TaskCapabilityFileName, TaskCapabilityTokenFileName} {
		path := filepath.Join(runDirectory, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale lifecycle artifact %s: %w", name, err)
		}
	}
	return nil
}

func agentEnvironment(environ []string, allowLinear bool) []string {
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
		if found && allowed[name] && (name != "LINEAR_API_KEY" || allowLinear) {
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
	if result.Status != string(StateSucceeded) && result.Status != string(StateBlocked) && result.Status != string(StateFailed) && result.Status != ResultReadyForMerge {
		return ProcessResult{}, fmt.Errorf("invalid agent result status %q", result.Status)
	}
	return result, nil
}

func (l *TmuxLauncher) ReadReadyCheckpoint(runDirectory string) (ReadyCheckpoint, error) {
	return ReadReadyCheckpoint(runDirectory)
}

func sessionName(issueIdentifier string) string {
	return "factory-" + strings.ToLower(issueIdentifier)
}

func taskSessionName(run Run) string {
	source := string(run.Task.Source)
	if source == "" {
		source = "linear"
	}
	return "factory-" + source + "-" + strings.TrimPrefix(run.ID, "run-")
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

func commandOutput(ctx context.Context, operation, directory, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", operation, err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}
