package runs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type canonicalStaticEvidence struct {
	evidence CompletionEvidence
	err      error
}

func (r canonicalStaticEvidence) ReadCompletionEvidence(context.Context, Run, PullRequestSnapshot) (CompletionEvidence, error) {
	return r.evidence, r.err
}

type completionPullRequests struct {
	snapshot PullRequestSnapshot
	err      error
	matches  []PullRequestSnapshot
	repo     string
	prefix   string
}

type snapshotOnlyPullRequests struct{}

func (snapshotOnlyPullRequests) Snapshot(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error) {
	return PullRequestSnapshot{}, nil
}

func TestNewMechanicalCompletionValidatorRequiresAllAuthorities(t *testing.T) {
	t.Parallel()
	clock := func() time.Time { return completionNow() }
	evidence := canonicalCompleteEvidence()
	if _, err := NewMechanicalCompletionValidator(nil, evidence, clock); err == nil {
		t.Fatal("nil pull request authority accepted")
	}
	if _, err := NewMechanicalCompletionValidator(&completionPullRequests{}, nil, clock); err == nil {
		t.Fatal("nil completion evidence accepted")
	}
	if _, err := NewMechanicalCompletionValidator(&completionPullRequests{}, evidence, nil); err == nil {
		t.Fatal("nil clock accepted")
	}
	if _, err := NewMechanicalCompletionValidator(snapshotOnlyPullRequests{}, evidence, clock); err == nil {
		t.Fatal("pull request authority without issue discovery accepted")
	}
}

func (r *completionPullRequests) Snapshot(context.Context, ReadyCheckpoint) (PullRequestSnapshot, error) {
	return r.snapshot, r.err
}

func (r *completionPullRequests) MatchingIssuePullRequests(_ context.Context, repository, branchPrefix string) ([]PullRequestSnapshot, error) {
	r.repo = repository
	r.prefix = branchPrefix
	return r.matches, r.err
}

func TestCompletionValidatorRejectsOpenPullRequestTerminalIntents(t *testing.T) {
	t.Parallel()
	now := completionNow()
	run := completionTestRun(now)
	reader := &completionPullRequests{snapshot: PullRequestSnapshot{
		State: "OPEN", BaseBranch: "main", HeadBranch: run.Ready.HeadBranch, HeadOID: run.Ready.VerifiedHeadOID,
	}}
	validator := mustCanonicalCompletionValidator(t, reader, canonicalCompleteEvidence(), now)
	for _, status := range []string{string(StateSucceeded), string(StateBlocked)} {
		decision := validator.ValidateTerminal(context.Background(), run, ProcessResult{Status: status})
		if !decision.Repark || decision.Validation.Accepted || !strings.Contains(decision.Validation.Reason, "still open") {
			t.Fatalf("%s decision = %#v", status, decision)
		}
	}
}

func TestCompletionValidatorRequiresCheckpointOrTypedPrePRBlocker(t *testing.T) {
	t.Parallel()
	now := completionNow()
	reader := &completionPullRequests{}
	validator := mustCanonicalCompletionValidator(t, reader, canonicalCompleteEvidence(), now)
	run := completionTestRun(now)
	run.Ready = nil
	success := validator.ValidateTerminal(context.Background(), run, ProcessResult{Status: string(StateSucceeded)})
	if success.Validation.Accepted || success.State != StateFailed || !strings.Contains(success.Detail, "manager-validated") {
		t.Fatalf("checkpoint-less success = %#v", success)
	}
	if reader.repo != run.Repository.Repository || reader.prefix != "eng-123-" {
		t.Fatalf("discovery route = %q %q", reader.repo, reader.prefix)
	}
	for _, blocker := range []string{BlockerMissingRoutingMetadata, BlockerApprovalDenied, BlockerAuthorityUnavailable, BlockerDecisionRequired} {
		decision := validator.ValidateTerminal(context.Background(), Run{}, ProcessResult{Status: string(StateBlocked), Blocker: blocker})
		if !decision.Validation.Accepted || decision.State != StateBlocked {
			t.Fatalf("typed blocker %q = %#v", blocker, decision)
		}
	}
	for _, blocker := range []string{"", BlockerSafeguardRegression, BlockerDeploymentFailed} {
		decision := validator.ValidateTerminal(context.Background(), Run{}, ProcessResult{Status: string(StateBlocked), Blocker: blocker})
		if decision.Validation.Accepted || decision.State != StateFailed {
			t.Fatalf("unsupported pre-checkpoint blocker %q = %#v", blocker, decision)
		}
	}
	run.Repository = nil
	missingRoute := validator.ValidateTerminal(context.Background(), run, ProcessResult{Status: string(StateSucceeded)})
	if missingRoute.Validation.Accepted || !strings.Contains(missingRoute.Detail, "repository route") {
		t.Fatalf("missing route = %#v", missingRoute)
	}
}

func TestCompletionValidatorPreservesProcessFailures(t *testing.T) {
	t.Parallel()
	now := completionNow()
	validator := mustCanonicalCompletionValidator(t, &completionPullRequests{}, canonicalCompleteEvidence(), now)
	preCheckpoint := validator.ValidateTerminal(context.Background(), Run{}, ProcessResult{Status: string(StateFailed)})
	if !preCheckpoint.Validation.Accepted || preCheckpoint.State != StateFailed || preCheckpoint.Repark {
		t.Fatalf("pre-checkpoint failure = %#v", preCheckpoint)
	}
	postCheckpoint := validator.ValidateTerminal(context.Background(), completionTestRun(now), ProcessResult{Status: string(StateFailed)})
	if postCheckpoint.Validation.Accepted || postCheckpoint.State != StateFailed || !postCheckpoint.Repark {
		t.Fatalf("post-checkpoint failure = %#v", postCheckpoint)
	}
}

func TestCompletionValidatorRequiresEveryPostMergeCondition(t *testing.T) {
	t.Parallel()
	now := completionNow()
	run := completionTestRun(now)
	reader := &completionPullRequests{snapshot: canonicalMergedSnapshot(*run.Ready)}
	decision := mustCanonicalCompletionValidator(t, reader, canonicalCompleteEvidence(), now).ValidateTerminal(
		context.Background(), run, ProcessResult{Status: string(StateSucceeded)},
	)
	if !decision.Validation.Accepted || decision.State != StateSucceeded || decision.Validation.DeploymentID == "" {
		t.Fatalf("valid success = %#v", decision)
	}
	transient := mustCanonicalCompletionValidator(t, reader, canonicalStaticEvidence{err: errors.New("offline")}, now).ValidateTerminal(
		context.Background(), run, ProcessResult{Status: string(StateSucceeded)},
	)
	if !transient.Repark || transient.Validation.Accepted {
		t.Fatalf("transient evidence failure = %#v", transient)
	}

	tests := []struct {
		name   string
		mutate func(*CompletionEvidence)
		want   string
	}{
		{name: "receipt", mutate: func(value *CompletionEvidence) { value.Deployment.Status = "failed" }, want: "receipt"},
		{name: "source", mutate: func(value *CompletionEvidence) { value.SourceValid = false }, want: "source"},
		{name: "merge", mutate: func(value *CompletionEvidence) { value.MergeContained = false }, want: "contain"},
		{name: "verified head", mutate: func(value *CompletionEvidence) { value.VerifiedHeadContained = false }, want: "verified head"},
		{name: "health", mutate: func(value *CompletionEvidence) { value.HealthMatches = false }, want: "health"},
		{name: "safeguards", mutate: func(value *CompletionEvidence) { value.SafeguardRegression = true }, want: "reviews"},
		{name: "remote", mutate: func(value *CompletionEvidence) { value.RemoteBranchAbsent = false }, want: "remote"},
		{name: "worktree", mutate: func(value *CompletionEvidence) { value.WorktreeAbsent = false }, want: "worktree"},
		{name: "task", mutate: func(value *CompletionEvidence) { value.TaskComplete = false }, want: "task"},
		{name: "children", mutate: func(value *CompletionEvidence) { value.ChildrenComplete = false }, want: "child"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evidence := canonicalCompleteEvidence().evidence
			test.mutate(&evidence)
			decision := mustCanonicalCompletionValidator(t, reader, canonicalStaticEvidence{evidence: evidence}, now).ValidateTerminal(
				context.Background(), run, ProcessResult{Status: string(StateSucceeded)},
			)
			if decision.Validation.Accepted || decision.State != StateFailed || !strings.Contains(decision.Detail, test.want) {
				t.Fatalf("decision = %#v, want %q", decision, test.want)
			}
		})
	}
}

func TestCompletionValidatorVerifiesTypedPostReadyBlockers(t *testing.T) {
	t.Parallel()
	now := completionNow()
	run := completionTestRun(now)
	closed := &completionPullRequests{snapshot: PullRequestSnapshot{
		State: "CLOSED", BaseBranch: "main", HeadBranch: run.Ready.HeadBranch, HeadOID: run.Ready.VerifiedHeadOID,
	}}
	decision := mustCanonicalCompletionValidator(t, closed, canonicalCompleteEvidence(), now).ValidateTerminal(
		context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerClosedUnmerged},
	)
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("closed blocker = %#v", decision)
	}

	merged := &completionPullRequests{snapshot: canonicalMergedSnapshot(*run.Ready)}
	failed := canonicalCompleteEvidence().evidence
	failed.DeploymentFailed = true
	decision = mustCanonicalCompletionValidator(t, merged, canonicalStaticEvidence{evidence: failed}, now).ValidateTerminal(
		context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerDeploymentFailed},
	)
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("deployment blocker = %#v", decision)
	}

	rebased := canonicalCompleteEvidence().evidence
	rebased.VerifiedHeadContained = false
	decision = mustCanonicalCompletionValidator(t, merged, canonicalStaticEvidence{evidence: rebased}, now).ValidateTerminal(
		context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerVerifiedHeadMismatch},
	)
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("verified-head blocker = %#v", decision)
	}

	regressed := canonicalMergedSnapshot(*run.Ready)
	regressed.SafeguardRegression = true
	decision = mustCanonicalCompletionValidator(t, &completionPullRequests{snapshot: regressed}, canonicalStaticEvidence{err: errors.New("deployment not started")}, now).ValidateTerminal(
		context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerSafeguardRegression},
	)
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("safeguard blocker = %#v", decision)
	}

	decision = mustCanonicalCompletionValidator(t, merged, canonicalStaticEvidence{err: errors.New("offline")}, now).ValidateTerminal(
		context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerDeploymentFailed},
	)
	if decision.Validation.Accepted || decision.State != StateFailed || !decision.Repark {
		t.Fatalf("unverified blocker = %#v", decision)
	}

	authFailure := externalAuthenticationError{operation: "GitHub CLI", detail: "HTTP 401"}
	decision = mustCanonicalCompletionValidator(t, &completionPullRequests{err: authFailure}, canonicalCompleteEvidence(), now).ValidateTerminal(
		context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerExternalAuthentication},
	)
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("pull request authentication blocker = %#v", decision)
	}
	decision = mustCanonicalCompletionValidator(t, merged, canonicalStaticEvidence{err: authFailure}, now).ValidateTerminal(
		context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerExternalAuthentication},
	)
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("evidence authentication blocker = %#v", decision)
	}
}

func TestCompletionValidatorRejectsMergedSnapshotIdentityDrift(t *testing.T) {
	t.Parallel()
	now := completionNow()
	run := completionTestRun(now)
	tests := []struct {
		name   string
		mutate func(*PullRequestSnapshot)
	}{
		{name: "base", mutate: func(value *PullRequestSnapshot) { value.BaseBranch = "develop" }},
		{name: "head branch", mutate: func(value *PullRequestSnapshot) { value.HeadBranch = "eng-123-other" }},
		{name: "head oid", mutate: func(value *PullRequestSnapshot) { value.HeadOID = strings.Repeat("c", 40) }},
		{name: "merge oid", mutate: func(value *PullRequestSnapshot) { value.MergeCommitOID = "missing" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := canonicalMergedSnapshot(*run.Ready)
			test.mutate(&snapshot)
			decision := mustCanonicalCompletionValidator(t, &completionPullRequests{snapshot: snapshot}, canonicalCompleteEvidence(), now).ValidateTerminal(
				context.Background(), run, ProcessResult{Status: string(StateSucceeded)},
			)
			if decision.Validation.Accepted || decision.State != StateFailed {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

type repositoryEvidenceRecorder struct {
	called bool
}

func (r *repositoryEvidenceRecorder) ReadCompletionEvidence(context.Context, Run, PullRequestSnapshot) (CompletionEvidence, error) {
	r.called = true
	return CompletionEvidence{SourceValid: true}, nil
}

func TestRepositoryCompletionEvidenceRoutesOnlyByImmutableRunRoute(t *testing.T) {
	t.Parallel()
	factory := &repositoryEvidenceRecorder{}
	network := &repositoryEvidenceRecorder{}
	router, err := NewRepositoryCompletionEvidence(map[string]CompletionEvidenceReader{
		"tomnagengast/factory": factory,
		"tomnagengast/network": network,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := completionTestRun(completionNow())
	if _, err := router.ReadCompletionEvidence(context.Background(), run, PullRequestSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if !factory.called || network.called {
		t.Fatalf("factory called = %t, network called = %t", factory.called, network.called)
	}
	run.Repository = nil
	if _, err := router.ReadCompletionEvidence(context.Background(), run, PullRequestSnapshot{}); err == nil {
		t.Fatal("missing immutable route unexpectedly used a fallback reader")
	}
}

func TestAuthenticationFailureClassification(t *testing.T) {
	t.Parallel()
	for _, detail := range []string{"HTTP 401", "HTTP 403", "run gh auth login", "not logged into github.com"} {
		if !looksLikeAuthenticationFailure(detail) {
			t.Fatalf("authentication failure %q was not classified", detail)
		}
	}
	if looksLikeAuthenticationFailure("connection timed out") {
		t.Fatal("network timeout was classified as authentication failure")
	}
}

func completionNow() time.Time {
	return time.Date(2026, time.July, 16, 22, 0, 0, 0, time.UTC)
}

func completionTestRun(now time.Time) Run {
	route := managerRoute("/tmp/factory-completion-test")
	run := Run{ID: "run-completion", Causation: Causation{Task: linearTask("ENG-123")}, Repository: &route}
	run.Ready = &ReadyCheckpoint{
		ContractVersion: readyContractVersion, RunID: run.ID, Task: run.Causation.Task,
		Repository: route.Repository, PullRequest: 18, BaseBranch: route.DefaultBranch,
		HeadBranch: "eng-123-fix", VerifiedHeadOID: strings.Repeat("a", 40), CreatedAt: now,
	}
	return run
}

func canonicalCompleteEvidence() canonicalStaticEvidence {
	return canonicalStaticEvidence{evidence: CompletionEvidence{
		DeploymentRequired: true,
		Deployment: DeploymentReceipt{
			Status: "success", DeploymentID: "deploy-1", SourceCommit: strings.Repeat("b", 40),
		},
		SourceValid: true, MergeContained: true, VerifiedHeadContained: true, HealthMatches: true,
		RemoteBranchAbsent: true, WorktreeAbsent: true, TaskComplete: true, ChildrenComplete: true,
	}}
}

func canonicalMergedSnapshot(checkpoint ReadyCheckpoint) PullRequestSnapshot {
	return PullRequestSnapshot{
		State: "MERGED", BaseBranch: checkpoint.BaseBranch, HeadBranch: checkpoint.HeadBranch,
		HeadOID: checkpoint.VerifiedHeadOID, MergeCommitOID: strings.Repeat("b", 40),
	}
}

func mustCanonicalCompletionValidator(t *testing.T, reader PullRequestReader, evidence CompletionEvidenceReader, now time.Time) *MechanicalCompletionValidator {
	t.Helper()
	validator, err := NewMechanicalCompletionValidator(reader, evidence, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewMechanicalCompletionValidator: %v", err)
	}
	return validator
}
