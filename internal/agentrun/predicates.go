package agentrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/tomnagengast/factory/internal/predicate"
)

var (
	deployCompletionProfile     = predicate.SDLCDeployProfile()
	repositoryCompletionProfile = predicate.SDLCRepoOnlyProfile()
	healthIdentityProfile       = predicate.Profile{
		Name: "health-identity",
		Mode: predicate.All,
		Requirements: []predicate.Requirement{
			{Atom: predicate.HealthStatus, Failure: "health status is not ok"},
			{Atom: predicate.HealthApp, Failure: "health app does not match the receipt"},
			{Atom: predicate.HealthCommit, Failure: "health commit does not match the receipt"},
			{Atom: predicate.HealthTree, Failure: "health tree does not match the receipt"},
			{Atom: predicate.HealthBuild, Failure: "health build does not match the receipt"},
			{Atom: predicate.HealthDeployment, Failure: "health deployment does not match the receipt"},
			{Atom: predicate.HealthContract, Failure: "health contract is not current"},
			{Atom: predicate.HealthStartedAt, Failure: "health process predates the deployment"},
		},
	}
	checkoutSourceProfile = predicate.Profile{
		Name: "checkout-source",
		Mode: predicate.All,
		Requirements: []predicate.Requirement{
			{Atom: predicate.CheckoutClean, Failure: "checkout is not clean"},
			{Atom: predicate.CheckoutBaseBranch, Failure: "checkout is not on the base branch"},
			{Atom: predicate.CheckoutUpstream, Failure: "checkout does not track the base branch"},
			{Atom: predicate.CheckoutHeadOnMain, Failure: "checkout head is not contained in upstream main"},
			{Atom: predicate.CheckoutOrigin, Failure: "checkout origin is not allowlisted"},
		},
	}
	deploySourceProfile = predicate.Profile{
		Name: "deploy-source",
		Mode: predicate.All,
		Requirements: appendRequirements(checkoutSourceProfile.Requirements, []predicate.Requirement{
			{Atom: predicate.ReceiptSourceOnMain, Failure: "receipt source is not contained in upstream main"},
			{Atom: predicate.ReceiptStatus, Failure: "receipt status is not successful"},
			{Atom: predicate.ReceiptApp, Failure: "receipt app is not configured"},
			{Atom: predicate.ReceiptBranch, Failure: "receipt branch is not the base branch"},
			{Atom: predicate.ReceiptTree, Failure: "receipt tree does not match source"},
			{Atom: predicate.ReceiptContract, Failure: "receipt contract is not current"},
			{Atom: predicate.ReceiptCommitFormat, Failure: "receipt commit is invalid"},
			{Atom: predicate.ReceiptTreeFormat, Failure: "receipt tree is invalid"},
			{Atom: predicate.ReceiptBinaryFormat, Failure: "receipt binary hash is invalid"},
			{Atom: predicate.ReceiptDeploymentID, Failure: "receipt deployment ID is missing"},
			{Atom: predicate.ReceiptBuildID, Failure: "receipt build ID is missing"},
			{Atom: predicate.ReceiptRepository, Failure: "receipt repository does not match"},
			{Atom: predicate.ReceiptCheckpointTime, Failure: "receipt predates the ready checkpoint"},
			{Atom: predicate.ReceiptFinishOrder, Failure: "receipt finishes before it starts"},
		}),
	}
	taskCompletionReadyProfile = predicate.Profile{
		Name: "task-completion-ready",
		Mode: predicate.All,
		Requirements: []predicate.Requirement{
			{Atom: predicate.SourceValid, Failure: "source is not ready"},
			{Atom: predicate.MergeContained, Failure: "merge is not contained"},
			{Atom: predicate.VerifiedHeadContained, Failure: "verified head is not contained"},
			{Atom: predicate.SafeguardsClear, Failure: "safeguards regressed"},
			{Atom: predicate.RemoteBranchAbsent, Failure: "remote branch remains"},
			{Atom: predicate.WorktreeAbsent, Failure: "worktree remains"},
			{Atom: predicate.ChildrenComplete, Failure: "children remain"},
		},
	}
)

func appendRequirements(prefix, suffix []predicate.Requirement) []predicate.Requirement {
	result := make([]predicate.Requirement, 0, len(prefix)+len(suffix))
	result = append(result, prefix...)
	return append(result, suffix...)
}

func evaluatePredicateProfile(profile predicate.Profile, values map[predicate.Atom]bool) (predicate.Evaluation, error) {
	facts := make([]predicate.Fact, 0, len(values))
	for atom, passed := range values {
		facts = append(facts, predicate.Fact{Atom: atom, Passed: passed})
	}
	source, err := predicate.NewStaticSource(facts)
	if err != nil {
		return predicate.Evaluation{}, err
	}
	return predicate.Evaluate(context.Background(), profile, source)
}

func predicateProfilePassed(profile predicate.Profile, values map[predicate.Atom]bool) bool {
	evaluation, err := evaluatePredicateProfile(profile, values)
	return err == nil && evaluation.Passed
}

func predicateProfileFailure(profile predicate.Profile, values map[predicate.Atom]bool) error {
	evaluation, err := evaluatePredicateProfile(profile, values)
	if err != nil {
		return err
	}
	if evaluation.Passed {
		return nil
	}
	if len(evaluation.Failures) == 0 {
		return fmt.Errorf("predicate profile %s failed without a reason", profile.Name)
	}
	return errors.New(evaluation.Failures[0])
}

func singlePredicateProfile(name string, atom predicate.Atom) predicate.Profile {
	return predicate.Profile{Name: name, Mode: predicate.All, Requirements: []predicate.Requirement{{
		Atom: atom, Failure: "blocker evidence is absent",
	}}}
}

func completionProfile(evidence CompletionEvidence) predicate.Profile {
	if evidence.DeploymentRequired {
		return deployCompletionProfile
	}
	return repositoryCompletionProfile
}

func completionPredicateValues(evidence CompletionEvidence) map[predicate.Atom]bool {
	return map[predicate.Atom]bool{
		predicate.DeploymentSuccessful:  evidence.Deployment.Status == "success",
		predicate.HealthMatches:         evidence.HealthMatches,
		predicate.SourceValid:           evidence.SourceValid,
		predicate.MergeContained:        evidence.MergeContained,
		predicate.VerifiedHeadContained: evidence.VerifiedHeadContained,
		predicate.SafeguardsClear:       !evidence.SafeguardRegression,
		predicate.RemoteBranchAbsent:    evidence.RemoteBranchAbsent,
		predicate.WorktreeAbsent:        evidence.WorktreeAbsent,
		predicate.TaskComplete:          evidence.TaskComplete,
		predicate.ChildrenComplete:      evidence.ChildrenComplete,
	}
}

func blockerPredicateProfile(blocker string) (predicate.Profile, map[predicate.Atom]bool, bool) {
	single := func(name string, atom predicate.Atom) (predicate.Profile, map[predicate.Atom]bool, bool) {
		return singlePredicateProfile(name, atom), make(map[predicate.Atom]bool, 1), true
	}
	switch blocker {
	case BlockerSafeguardRegression:
		return single("blocker-safeguard-regression", predicate.SafeguardRegression)
	case BlockerVerifiedHeadMismatch:
		return single("blocker-verified-head-mismatch", predicate.PullRequestVerifiedHeadMismatch)
	case BlockerDeploymentSource:
		return single("blocker-source-invalid", predicate.SourceInvalid)
	case BlockerExternalAuthentication:
		return single("blocker-external-authentication", predicate.ExternalAuthenticationFailure)
	case BlockerDeploymentFailed:
		return single("blocker-deployment-failed", predicate.DeploymentFailed)
	case BlockerCleanupFailed:
		profile := predicate.Profile{Name: "blocker-cleanup-failed", Mode: predicate.Any, Requirements: []predicate.Requirement{
			{Atom: predicate.RemoteBranchPresent, Failure: "remote branch is absent"},
			{Atom: predicate.WorktreePresent, Failure: "worktree is absent"},
			{Atom: predicate.TaskIncomplete, Failure: "task is complete"},
			{Atom: predicate.ChildrenIncomplete, Failure: "children are complete"},
		}}
		return profile, map[predicate.Atom]bool{}, true
	default:
		return predicate.Profile{}, nil, false
	}
}

func completionBlockerSupported(blocker string, evidence CompletionEvidence) bool {
	profile, values, ok := blockerPredicateProfile(blocker)
	if !ok {
		return false
	}
	switch blocker {
	case BlockerSafeguardRegression:
		values[predicate.SafeguardRegression] = evidence.SafeguardRegression
	case BlockerVerifiedHeadMismatch:
		values[predicate.PullRequestVerifiedHeadMismatch] = !evidence.VerifiedHeadContained
	case BlockerDeploymentSource:
		values[predicate.SourceInvalid] = !evidence.SourceValid
	case BlockerExternalAuthentication:
		values[predicate.ExternalAuthenticationFailure] = evidence.ExternalAuthenticationFailure
	case BlockerDeploymentFailed:
		values[predicate.DeploymentFailed] = evidence.DeploymentFailed
	case BlockerCleanupFailed:
		values[predicate.RemoteBranchPresent] = !evidence.RemoteBranchAbsent
		values[predicate.WorktreePresent] = !evidence.WorktreeAbsent
		values[predicate.TaskIncomplete] = !evidence.TaskComplete
		values[predicate.ChildrenIncomplete] = !evidence.ChildrenComplete
	}
	return predicateProfilePassed(profile, values)
}
