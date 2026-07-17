package agentrun

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/tomnagengast/factory/internal/predicate"
)

func TestCompletionPredicateGoldenParity(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/predicate_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name               string   `json:"name"`
		DeploymentRequired bool     `json:"deploymentRequired"`
		Fail               []string `json:"fail"`
		Problems           []string `json:"problems"`
		Atoms              []string `json:"atoms"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			evidence := passingCompletionEvidence(test.DeploymentRequired)
			for _, field := range test.Fail {
				failCompletionField(t, &evidence, field)
			}
			if got := completionProblems(evidence); !reflect.DeepEqual(got, test.Problems) {
				t.Fatalf("completionProblems() = %v, want %v", got, test.Problems)
			}
			evaluation, err := evaluatePredicateProfile(completionProfile(evidence), completionPredicateValues(evidence))
			if err != nil {
				t.Fatal(err)
			}
			atoms := make([]string, len(evaluation.Facts))
			for index := range evaluation.Facts {
				atoms[index] = string(evaluation.Facts[index].Atom)
			}
			if !reflect.DeepEqual(atoms, test.Atoms) {
				t.Fatalf("atoms = %v, want %v", atoms, test.Atoms)
			}
		})
	}
}

func TestCompletionPredicatesMatchFrozenLegacyMatrix(t *testing.T) {
	t.Parallel()
	for _, deploymentRequired := range []bool{false, true} {
		for mask := 0; mask < 1<<10; mask++ {
			evidence := passingCompletionEvidence(deploymentRequired)
			fields := []string{"deployment", "health", "source", "merge", "verified", "safeguards", "remote", "worktree", "task", "children"}
			for bit, field := range fields {
				if mask&(1<<bit) != 0 {
					failCompletionField(t, &evidence, field)
				}
			}
			got := completionProblems(evidence)
			want := frozenLegacyCompletionProblems(evidence)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("deployment=%t mask=%010b: got %v, want %v", deploymentRequired, mask, got, want)
			}
		}
	}
}

func TestCompletionBlockerProfilesMatchLegacyMatrix(t *testing.T) {
	t.Parallel()
	blockers := []string{
		BlockerSafeguardRegression,
		BlockerVerifiedHeadMismatch,
		BlockerDeploymentSource,
		BlockerExternalAuthentication,
		BlockerDeploymentFailed,
		BlockerCleanupFailed,
		"unsupported",
	}
	for mask := 0; mask < 1<<9; mask++ {
		evidence := CompletionEvidence{
			SafeguardRegression:           mask&1 != 0,
			VerifiedHeadContained:         mask&2 == 0,
			SourceValid:                   mask&4 == 0,
			ExternalAuthenticationFailure: mask&8 != 0,
			DeploymentFailed:              mask&16 != 0,
			RemoteBranchAbsent:            mask&32 == 0,
			WorktreeAbsent:                mask&64 == 0,
			TaskComplete:                  mask&128 == 0,
			ChildrenComplete:              mask&256 == 0,
		}
		for _, blocker := range blockers {
			got := completionBlockerSupported(blocker, evidence)
			want := frozenLegacyBlockerSupported(blocker, evidence)
			if got != want {
				t.Fatalf("mask=%09b blocker=%s: got %t, want %t", mask, blocker, got, want)
			}
		}
	}
}

func TestInternalPredicateProfilesCoverEverySourceAtom(t *testing.T) {
	t.Parallel()
	profiles := []predicate.Profile{healthIdentityProfile, checkoutSourceProfile, deploySourceProfile, taskCompletionReadyProfile, deployCompletionProfile, repositoryCompletionProfile}
	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			t.Fatalf("profile %s: %v", profile.Name, err)
		}
	}
	if deployCompletionProfile.Name != predicate.ProfileSDLCDeploy || repositoryCompletionProfile.Name != predicate.ProfileSDLCRepoOnly {
		t.Fatalf("public profiles = %q, %q", deployCompletionProfile.Name, repositoryCompletionProfile.Name)
	}
}

func passingCompletionEvidence(deploymentRequired bool) CompletionEvidence {
	return CompletionEvidence{
		DeploymentRequired:    deploymentRequired,
		Deployment:            DeploymentReceipt{Status: "success"},
		HealthMatches:         true,
		SourceValid:           true,
		MergeContained:        true,
		VerifiedHeadContained: true,
		RemoteBranchAbsent:    true,
		WorktreeAbsent:        true,
		TaskComplete:          true,
		ChildrenComplete:      true,
	}
}

func failCompletionField(t *testing.T, evidence *CompletionEvidence, field string) {
	t.Helper()
	switch field {
	case "deployment":
		evidence.Deployment.Status = "failed"
	case "health":
		evidence.HealthMatches = false
	case "source":
		evidence.SourceValid = false
	case "merge":
		evidence.MergeContained = false
	case "verified":
		evidence.VerifiedHeadContained = false
	case "safeguards":
		evidence.SafeguardRegression = true
	case "remote":
		evidence.RemoteBranchAbsent = false
	case "worktree":
		evidence.WorktreeAbsent = false
	case "task":
		evidence.TaskComplete = false
	case "children":
		evidence.ChildrenComplete = false
	default:
		t.Fatalf("unknown completion field %q", field)
	}
}

func frozenLegacyCompletionProblems(evidence CompletionEvidence) []string {
	type check struct {
		ok      bool
		message string
	}
	var checks []check
	if evidence.DeploymentRequired {
		checks = append(checks,
			check{evidence.Deployment.Status == "success", "deployment receipt is not successful"},
			check{evidence.HealthMatches, "running health identity does not match the deployment"},
		)
	}
	checks = append(checks,
		check{evidence.SourceValid, "completion source is not clean updated main"},
		check{evidence.MergeContained, "updated main does not contain the merge"},
		check{evidence.VerifiedHeadContained, "merged result does not contain the verified head"},
		check{!evidence.SafeguardRegression, "pull request checks or reviews regressed"},
		check{evidence.RemoteBranchAbsent, "remote issue branch still exists"},
		check{evidence.WorktreeAbsent, "issue worktree still exists"},
		check{evidence.TaskComplete, "task is not complete"},
		check{evidence.ChildrenComplete, "child work remains incomplete"},
	)
	var problems []string
	for _, value := range checks {
		if !value.ok {
			problems = append(problems, value.message)
		}
	}
	return problems
}

func frozenLegacyBlockerSupported(blocker string, evidence CompletionEvidence) bool {
	return blocker == BlockerSafeguardRegression && evidence.SafeguardRegression ||
		blocker == BlockerVerifiedHeadMismatch && !evidence.VerifiedHeadContained ||
		blocker == BlockerDeploymentSource && !evidence.SourceValid ||
		blocker == BlockerExternalAuthentication && evidence.ExternalAuthenticationFailure ||
		blocker == BlockerDeploymentFailed && evidence.DeploymentFailed ||
		blocker == BlockerCleanupFailed && (!evidence.RemoteBranchAbsent || !evidence.WorktreeAbsent || !evidence.TaskComplete || !evidence.ChildrenComplete)
}
