package agentrun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type staticCompletionEvidence struct {
	evidence CompletionEvidence
	err      error
}

func (r staticCompletionEvidence) ReadCompletionEvidence(context.Context, Run, PullRequestSnapshot) (CompletionEvidence, error) {
	return r.evidence, r.err
}

func TestCompletionValidatorRejectsOpenPullRequestTerminalIntents(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 22, 0, 0, 0, time.UTC)
	run := Run{ID: "run-1", Ready: readyPointer(testReadyCheckpoint("run-1", now))}
	reader := &fakePullRequestReader{snapshot: PullRequestSnapshot{State: "OPEN", BaseBranch: "main", HeadBranch: run.Ready.HeadBranch, HeadOID: run.Ready.VerifiedHeadOID}}
	validator := mustCompletionValidator(t, reader, completeEvidence(), now)
	for _, status := range []string{string(StateSucceeded), string(StateBlocked)} {
		decision := validator.Validate(context.Background(), run, ProcessResult{Status: status})
		if !decision.Repark || decision.Validation.Accepted || !strings.Contains(decision.Validation.Reason, "still open") {
			t.Fatalf("%s decision = %#v", status, decision)
		}
	}
}

func TestCompletionValidatorRequiresCheckpointOrTypedPrePRBlocker(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 22, 0, 0, 0, time.UTC)
	validator := mustCompletionValidator(t, &fakePullRequestReader{}, completeEvidence(), now)
	success := validator.Validate(context.Background(), Run{}, ProcessResult{Status: string(StateSucceeded)})
	if success.Validation.Accepted || success.State != StateFailed || !strings.Contains(success.Detail, "without a manager-validated") {
		t.Fatalf("checkpoint-less success = %#v", success)
	}
	blocked := validator.Validate(context.Background(), Run{}, ProcessResult{Status: string(StateBlocked), Blocker: BlockerAuthorityUnavailable})
	if !blocked.Validation.Accepted || blocked.State != StateBlocked {
		t.Fatalf("typed blocker = %#v", blocked)
	}
	untyped := validator.Validate(context.Background(), Run{}, ProcessResult{Status: string(StateBlocked)})
	if untyped.Validation.Accepted || untyped.State != StateFailed {
		t.Fatalf("untyped blocker = %#v", untyped)
	}
	withPullRequest := mustCompletionValidator(t, &fakePullRequestReader{matches: []PullRequestSnapshot{{Number: 8, State: "OPEN"}}}, completeEvidence(), now)
	matched := withPullRequest.Validate(context.Background(), Run{IssueIdentifier: "ENG-123"}, ProcessResult{Status: string(StateBlocked), Blocker: BlockerAuthorityUnavailable})
	if !matched.Validation.Accepted || matched.State != StateBlocked || !strings.Contains(matched.Detail, "typed pre-checkpoint blocker") {
		t.Fatalf("matched PR blocker = %#v", matched)
	}
}

func TestCompletionValidatorReparksPostCheckpointProcessFailure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 22, 0, 0, 0, time.UTC)
	checkpoint := testReadyCheckpoint("run-1", now)
	validator := mustCompletionValidator(t, &fakePullRequestReader{}, completeEvidence(), now)
	preCheckpoint := validator.Validate(context.Background(), Run{}, ProcessResult{Status: string(StateFailed)})
	if !preCheckpoint.Validation.Accepted || preCheckpoint.State != StateFailed || preCheckpoint.Repark {
		t.Fatalf("pre-checkpoint failure = %#v", preCheckpoint)
	}
	postCheckpoint := validator.Validate(context.Background(), Run{Ready: &checkpoint}, ProcessResult{Status: string(StateFailed)})
	if postCheckpoint.Validation.Accepted || postCheckpoint.State != StateFailed || !postCheckpoint.Repark {
		t.Fatalf("post-checkpoint failure = %#v", postCheckpoint)
	}
}

func TestCompletionValidatorRequiresEveryPostMergeCondition(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 22, 0, 0, 0, time.UTC)
	checkpoint := testReadyCheckpoint("run-1", now)
	run := Run{ID: "run-1", Ready: &checkpoint}
	reader := &fakePullRequestReader{snapshot: mergedSnapshot(checkpoint)}
	valid := completeEvidence()
	decision := mustCompletionValidator(t, reader, valid, now).Validate(context.Background(), run, ProcessResult{Status: string(StateSucceeded)})
	if !decision.Validation.Accepted || decision.State != StateSucceeded || decision.Validation.DeploymentID == "" {
		t.Fatalf("valid success = %#v", decision)
	}
	transient := mustCompletionValidator(t, reader, staticCompletionEvidence{err: errors.New("offline")}, now).Validate(context.Background(), run, ProcessResult{Status: string(StateSucceeded)})
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
		{name: "health", mutate: func(value *CompletionEvidence) { value.HealthMatches = false }, want: "health"},
		{name: "safeguards", mutate: func(value *CompletionEvidence) { value.SafeguardRegression = true }, want: "reviews"},
		{name: "remote", mutate: func(value *CompletionEvidence) { value.RemoteBranchAbsent = false }, want: "remote"},
		{name: "worktree", mutate: func(value *CompletionEvidence) { value.WorktreeAbsent = false }, want: "worktree"},
		{name: "Linear", mutate: func(value *CompletionEvidence) { value.LinearComplete = false }, want: "Linear"},
		{name: "children", mutate: func(value *CompletionEvidence) { value.ChildrenComplete = false }, want: "child"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evidence := completeEvidence().evidence
			test.mutate(&evidence)
			decision := mustCompletionValidator(t, reader, staticCompletionEvidence{evidence: evidence}, now).Validate(context.Background(), run, ProcessResult{Status: string(StateSucceeded)})
			if decision.Validation.Accepted || decision.State != StateFailed || !strings.Contains(decision.Detail, test.want) {
				t.Fatalf("decision = %#v, want %q", decision, test.want)
			}
		})
	}
}

func TestCompletionValidatorVerifiesTypedPostReadyBlockers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 22, 0, 0, 0, time.UTC)
	checkpoint := testReadyCheckpoint("run-1", now)
	run := Run{ID: "run-1", Ready: &checkpoint}
	closed := &fakePullRequestReader{snapshot: PullRequestSnapshot{State: "CLOSED", BaseBranch: "main", HeadBranch: checkpoint.HeadBranch, HeadOID: checkpoint.VerifiedHeadOID}}
	decision := mustCompletionValidator(t, closed, completeEvidence(), now).Validate(context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerClosedUnmerged})
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("closed blocker = %#v", decision)
	}

	failedEvidence := completeEvidence().evidence
	failedEvidence.DeploymentFailed = true
	merged := &fakePullRequestReader{snapshot: mergedSnapshot(checkpoint)}
	decision = mustCompletionValidator(t, merged, staticCompletionEvidence{evidence: failedEvidence}, now).Validate(context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerDeploymentFailed})
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("deployment blocker = %#v", decision)
	}

	regressed := mergedSnapshot(checkpoint)
	regressed.SafeguardRegression = true
	decision = mustCompletionValidator(t, &fakePullRequestReader{snapshot: regressed}, staticCompletionEvidence{err: errors.New("deployment has not started")}, now).Validate(context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerSafeguardRegression})
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("safeguard blocker = %#v", decision)
	}

	decision = mustCompletionValidator(t, merged, staticCompletionEvidence{err: errors.New("offline")}, now).Validate(context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerDeploymentFailed})
	if decision.Validation.Accepted || decision.State != StateFailed || !decision.Repark {
		t.Fatalf("unverified blocker = %#v", decision)
	}

	authFailure := externalAuthenticationError{operation: "GitHub CLI", detail: "HTTP 401"}
	decision = mustCompletionValidator(t, &fakePullRequestReader{err: authFailure}, completeEvidence(), now).Validate(context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerExternalAuthentication})
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("pull request authentication blocker = %#v", decision)
	}
	decision = mustCompletionValidator(t, merged, staticCompletionEvidence{err: authFailure}, now).Validate(context.Background(), run, ProcessResult{Status: string(StateBlocked), Blocker: BlockerExternalAuthentication})
	if !decision.Validation.Accepted || decision.State != StateBlocked {
		t.Fatalf("evidence authentication blocker = %#v", decision)
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

func completeEvidence() staticCompletionEvidence {
	return staticCompletionEvidence{evidence: CompletionEvidence{
		DeploymentRequired: true,
		Deployment:         DeploymentReceipt{Status: "success", DeploymentID: "deploy-1", SourceCommit: "378bfbbc26c0951a91bfc2db1e30c167b87bfa7b"},
		SourceValid:        true,
		MergeContained:     true,
		HealthMatches:      true,
		RemoteBranchAbsent: true,
		WorktreeAbsent:     true,
		LinearComplete:     true,
		ChildrenComplete:   true,
	}}
}

func TestCompletionValidatorAcceptsRepositoryOnlyEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 11, 22, 0, 0, 0, time.UTC)
	checkpoint := testReadyCheckpoint("run-1", now)
	evidence := CompletionEvidence{
		SourceValid: true, MergeContained: true, RemoteBranchAbsent: true,
		WorktreeAbsent: true, LinearComplete: true, ChildrenComplete: true,
	}
	decision := mustCompletionValidator(
		t, &fakePullRequestReader{snapshot: mergedSnapshot(checkpoint)},
		staticCompletionEvidence{evidence: evidence}, now,
	).Validate(context.Background(), Run{IssueIdentifier: "ENG-123", Ready: &checkpoint}, ProcessResult{Status: string(StateSucceeded)})
	if !decision.Validation.Accepted || decision.State != StateSucceeded {
		t.Fatalf("decision = %#v", decision)
	}
}

func mergedSnapshot(checkpoint ReadyCheckpoint) PullRequestSnapshot {
	return PullRequestSnapshot{
		State:          "MERGED",
		BaseBranch:     checkpoint.BaseBranch,
		HeadBranch:     checkpoint.HeadBranch,
		HeadOID:        checkpoint.VerifiedHeadOID,
		MergeCommitOID: "378bfbbc26c0951a91bfc2db1e30c167b87bfa7b",
	}
}

func mustCompletionValidator(t *testing.T, reader PullRequestReader, evidence CompletionEvidenceReader, now time.Time) *MechanicalCompletionValidator {
	t.Helper()
	validator, err := NewMechanicalCompletionValidator(reader, evidence, "tomnagengast/network", func() time.Time { return now })
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	return validator
}

func readyPointer(value ReadyCheckpoint) *ReadyCheckpoint {
	return &value
}
