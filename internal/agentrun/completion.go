package agentrun

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	BlockerMissingRoutingMetadata = "missing_routing_metadata"
	BlockerApprovalDenied         = "approval_denied"
	BlockerAuthorityUnavailable   = "authority_unavailable"
	BlockerDecisionRequired       = "decision_required"
	BlockerClosedUnmerged         = "closed_unmerged"
	BlockerVerifiedHeadMismatch   = "verified_head_mismatch"
	BlockerSafeguardRegression    = "safeguard_regression"
	BlockerDeploymentSource       = "deployment_source_invalid"
	BlockerExternalAuthentication = "external_authentication"
	BlockerDeploymentFailed       = "deployment_failed"
	BlockerCleanupFailed          = "cleanup_failed"
)

var prePullRequestBlockers = map[string]struct{}{
	BlockerMissingRoutingMetadata: {},
	BlockerApprovalDenied:         {},
	BlockerAuthorityUnavailable:   {},
	BlockerDecisionRequired:       {},
}

type DeploymentReceipt struct {
	ContractVersion  int       `json:"contractVersion"`
	DeploymentID     string    `json:"deploymentId"`
	BuildID          string    `json:"buildId"`
	Status           string    `json:"status"`
	App              string    `json:"app"`
	SourceRepository string    `json:"sourceRepository"`
	SourceBranch     string    `json:"sourceBranch"`
	SourceCommit     string    `json:"sourceCommit"`
	SourceTree       string    `json:"sourceTree"`
	BinarySHA256     string    `json:"binarySha256"`
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt"`
	Message          string    `json:"message,omitempty"`
}

type HealthIdentity struct {
	Status          string    `json:"status"`
	App             string    `json:"app"`
	Commit          string    `json:"commit"`
	Tree            string    `json:"tree"`
	BuildID         string    `json:"buildId"`
	DeploymentID    string    `json:"deploymentId"`
	ContractVersion string    `json:"contractVersion"`
	StartedAt       time.Time `json:"startedAt"`
}

type CompletionEvidence struct {
	Deployment                    DeploymentReceipt
	Health                        HealthIdentity
	SourceValid                   bool
	MergeContained                bool
	HealthMatches                 bool
	RemoteBranchAbsent            bool
	WorktreeAbsent                bool
	LinearComplete                bool
	ChildrenComplete              bool
	SafeguardRegression           bool
	ExternalAuthenticationFailure bool
	DeploymentFailed              bool
}

type CompletionEvidenceReader interface {
	ReadCompletionEvidence(context.Context, Run, PullRequestSnapshot) (CompletionEvidence, error)
}

type CompletionValidation struct {
	Accepted         bool      `json:"accepted"`
	Intent           string    `json:"intent"`
	Blocker          string    `json:"blocker,omitempty"`
	State            State     `json:"state"`
	Reason           string    `json:"reason"`
	ValidatedAt      time.Time `json:"validatedAt"`
	PullRequestState string    `json:"pullRequestState,omitempty"`
	PullRequestHead  string    `json:"pullRequestHead,omitempty"`
	MergeCommitOID   string    `json:"mergeCommitOid,omitempty"`
	DeploymentID     string    `json:"deploymentId,omitempty"`
	DeploymentCommit string    `json:"deploymentCommit,omitempty"`
}

type CompletionDecision struct {
	State      State
	Detail     string
	Repark     bool
	Validation CompletionValidation
}

type TerminalValidator interface {
	Validate(context.Context, Run, ProcessResult) CompletionDecision
}

type MechanicalCompletionValidator struct {
	pullRequests PullRequestReader
	discoverer   PullRequestDiscoverer
	evidence     CompletionEvidenceReader
	repository   string
	now          func() time.Time
}

func NewMechanicalCompletionValidator(pullRequests PullRequestReader, evidence CompletionEvidenceReader, repository string, now func() time.Time) (*MechanicalCompletionValidator, error) {
	if pullRequests == nil || evidence == nil || !repositoryPattern.MatchString(repository) || now == nil {
		return nil, fmt.Errorf("completion validator: pull request, evidence, and clock are required")
	}
	discoverer, ok := pullRequests.(PullRequestDiscoverer)
	if !ok {
		return nil, fmt.Errorf("completion validator: pull request reader must support issue discovery")
	}
	return &MechanicalCompletionValidator{pullRequests: pullRequests, discoverer: discoverer, evidence: evidence, repository: repository, now: now}, nil
}

func (v *MechanicalCompletionValidator) Validate(ctx context.Context, run Run, result ProcessResult) CompletionDecision {
	decision := CompletionDecision{
		State:  State(result.Status),
		Detail: result.Detail,
		Validation: CompletionValidation{
			Intent:      result.Status,
			Blocker:     result.Blocker,
			State:       State(result.Status),
			ValidatedAt: v.now().UTC(),
		},
	}
	if result.Status == string(StateFailed) {
		decision.Validation.Accepted = true
		decision.Validation.Reason = "process failure preserved"
		return decision
	}
	if run.Ready == nil {
		return v.validateBeforePullRequest(ctx, run, decision, result)
	}

	snapshot, err := v.pullRequests.Snapshot(ctx, *run.Ready)
	if err != nil {
		return rejectCompletion(decision, "authoritative pull request refresh failed: "+err.Error(), true)
	}
	decision.Validation.PullRequestState = snapshot.State
	decision.Validation.PullRequestHead = snapshot.HeadOID
	decision.Validation.MergeCommitOID = snapshot.MergeCommitOID
	if snapshot.State == "OPEN" {
		return rejectCompletion(decision, "pull request is still open", true)
	}
	if result.Status == string(StateBlocked) {
		return v.validatePostReadyBlocker(ctx, run, result, snapshot, decision)
	}
	if result.Status != string(StateSucceeded) {
		return rejectCompletion(decision, "unsupported terminal intent", false)
	}
	if err := validateMergedSnapshot(*run.Ready, snapshot); err != nil {
		return rejectCompletion(decision, err.Error(), false)
	}
	evidence, err := v.evidence.ReadCompletionEvidence(ctx, run, snapshot)
	if err != nil {
		return rejectCompletion(decision, "read completion evidence: "+err.Error(), true)
	}
	decision.Validation.DeploymentID = evidence.Deployment.DeploymentID
	decision.Validation.DeploymentCommit = evidence.Deployment.SourceCommit
	if problems := completionProblems(evidence); len(problems) > 0 {
		return rejectCompletion(decision, strings.Join(problems, "; "), false)
	}
	decision.Validation.Accepted = true
	decision.Validation.Reason = "all mechanical post-merge conditions verified"
	decision.Detail = decision.Validation.Reason
	return decision
}

func (v *MechanicalCompletionValidator) validateBeforePullRequest(ctx context.Context, run Run, decision CompletionDecision, result ProcessResult) CompletionDecision {
	matches, err := v.discoverer.MatchingIssuePullRequests(ctx, v.repository, run.IssueIdentifier)
	if err != nil {
		return rejectCompletion(decision, "discover issue pull requests: "+err.Error(), false)
	}
	if len(matches) > 0 {
		return rejectCompletion(decision, fmt.Sprintf("found %d matching issue pull request(s) without a validated checkpoint", len(matches)), false)
	}
	if result.Status == string(StateBlocked) {
		if _, ok := prePullRequestBlockers[result.Blocker]; ok {
			decision.Validation.Accepted = true
			decision.Validation.Reason = "typed pre-pull-request blocker accepted"
			decision.Detail = decision.Validation.Reason + ": " + result.Blocker
			return decision
		}
		return rejectCompletion(decision, "blocked intent lacks an allowed pre-pull-request blocker", false)
	}
	return rejectCompletion(decision, "success without a manager-validated ready checkpoint is not permitted for this repository", false)
}

func (v *MechanicalCompletionValidator) validatePostReadyBlocker(
	ctx context.Context,
	run Run,
	result ProcessResult,
	snapshot PullRequestSnapshot,
	decision CompletionDecision,
) CompletionDecision {
	if snapshot.State == "CLOSED" && result.Blocker == BlockerClosedUnmerged {
		decision.Validation.Accepted = true
		decision.Validation.Reason = "pull request closed without merge"
		decision.Detail = decision.Validation.Reason
		return decision
	}
	if snapshot.State == "MERGED" && snapshot.HeadOID != run.Ready.VerifiedHeadOID && result.Blocker == BlockerVerifiedHeadMismatch {
		decision.Validation.Accepted = true
		decision.Validation.Reason = "merged head differs from verified checkpoint"
		decision.Detail = decision.Validation.Reason
		return decision
	}
	if snapshot.State != "MERGED" {
		return rejectCompletion(decision, "blocker does not match authoritative pull request state", false)
	}
	evidence, err := v.evidence.ReadCompletionEvidence(ctx, run, snapshot)
	if err != nil {
		return rejectCompletion(decision, "read blocker evidence: "+err.Error(), true)
	}
	matched := result.Blocker == BlockerSafeguardRegression && evidence.SafeguardRegression ||
		result.Blocker == BlockerDeploymentSource && !evidence.SourceValid ||
		result.Blocker == BlockerExternalAuthentication && evidence.ExternalAuthenticationFailure ||
		result.Blocker == BlockerDeploymentFailed && evidence.DeploymentFailed ||
		result.Blocker == BlockerCleanupFailed && (!evidence.RemoteBranchAbsent || !evidence.WorktreeAbsent || !evidence.LinearComplete || !evidence.ChildrenComplete)
	if !matched {
		return rejectCompletion(decision, "typed blocker is not supported by mechanical evidence", false)
	}
	decision.Validation.Accepted = true
	decision.Validation.Reason = "typed post-merge blocker verified"
	decision.Detail = decision.Validation.Reason + ": " + result.Blocker
	return decision
}

func rejectCompletion(decision CompletionDecision, reason string, repark bool) CompletionDecision {
	decision.State = StateFailed
	decision.Detail = "terminal intent rejected: " + reason
	decision.Repark = repark
	decision.Validation.Accepted = false
	decision.Validation.State = StateFailed
	decision.Validation.Reason = reason
	return decision
}

func completionProblems(evidence CompletionEvidence) []string {
	var problems []string
	checks := []struct {
		ok      bool
		message string
	}{
		{evidence.Deployment.Status == "success", "deployment receipt is not successful"},
		{evidence.SourceValid, "deployment source is not clean updated main"},
		{evidence.MergeContained, "deployed commit does not contain the merge"},
		{evidence.HealthMatches, "running health identity does not match the deployment"},
		{evidence.RemoteBranchAbsent, "remote issue branch still exists"},
		{evidence.WorktreeAbsent, "issue worktree still exists"},
		{evidence.LinearComplete, "Linear issue is not complete"},
		{evidence.ChildrenComplete, "child work remains incomplete"},
	}
	for _, check := range checks {
		if !check.ok {
			problems = append(problems, check.message)
		}
	}
	return problems
}
