package predicate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestRecordedEvaluations(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/evaluator.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name          string        `json:"name"`
		Mode          Mode          `json:"mode"`
		Requirements  []Requirement `json:"requirements"`
		Facts         []Fact        `json:"facts"`
		Passed        bool          `json:"passed"`
		Failures      []string      `json:"failures"`
		EvaluatedAtom []Atom        `json:"evaluatedAtoms"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			source, err := NewStaticSource(test.Facts)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Evaluate(context.Background(), Profile{Name: "recorded", Mode: test.Mode, Requirements: test.Requirements}, source)
			if err != nil {
				t.Fatal(err)
			}
			if got.Passed != test.Passed || !reflect.DeepEqual(got.Failures, test.Failures) {
				t.Fatalf("evaluation = passed %t, failures %v; want passed %t, failures %v", got.Passed, got.Failures, test.Passed, test.Failures)
			}
			atoms := make([]Atom, len(got.Facts))
			for index := range got.Facts {
				atoms[index] = got.Facts[index].Atom
				if got.Facts[index].Passed && got.Facts[index].Failure != "" {
					t.Fatalf("passing fact %s retained failure %q", got.Facts[index].Atom, got.Facts[index].Failure)
				}
			}
			if !reflect.DeepEqual(atoms, test.EvaluatedAtom) {
				t.Fatalf("evaluated atoms = %v, want %v", atoms, test.EvaluatedAtom)
			}
		})
	}
}

func TestEvaluateFailsClosed(t *testing.T) {
	t.Parallel()
	requirement := Requirement{Atom: CheckoutClean, Parameters: Parameters{"repository": "owner/repo"}, Failure: "checkout is dirty"}
	profile := Profile{Name: "test", Mode: All, Requirements: []Requirement{requirement}}

	t.Run("missing", func(t *testing.T) {
		source, err := NewStaticSource(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Evaluate(context.Background(), profile, source); err == nil {
			t.Fatal("expected missing fact error")
		}
	})

	t.Run("mismatched", func(t *testing.T) {
		source := SourceFunc(func(context.Context, Atom, Parameters) (Fact, error) {
			return Fact{Atom: CheckoutOrigin, Parameters: Parameters{"repository": "owner/repo"}, Passed: true}, nil
		})
		if _, err := Evaluate(context.Background(), profile, source); err == nil {
			t.Fatal("expected mismatched fact error")
		}
	})

	t.Run("source error", func(t *testing.T) {
		source := SourceFunc(func(context.Context, Atom, Parameters) (Fact, error) {
			return Fact{}, errors.New("unavailable")
		})
		if _, err := Evaluate(context.Background(), profile, source); err == nil {
			t.Fatal("expected source error")
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		source, err := NewStaticSource([]Fact{{Atom: CheckoutClean, Parameters: requirement.Parameters, Passed: true}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Evaluate(ctx, profile, source); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	})
}

func TestRejectsDuplicateRequirementsAndFacts(t *testing.T) {
	t.Parallel()
	requirement := Requirement{Atom: CheckoutClean, Failure: "checkout is dirty"}
	profile := Profile{Name: "duplicate", Mode: All, Requirements: []Requirement{requirement, requirement}}
	if err := profile.Validate(); err == nil {
		t.Fatal("expected duplicate requirement error")
	}
	if _, err := NewStaticSource([]Fact{{Atom: CheckoutClean}, {Atom: CheckoutClean}}); err == nil {
		t.Fatal("expected duplicate fact error")
	}
}

func TestSourceCannotMutateProfileParameters(t *testing.T) {
	t.Parallel()
	parameters := Parameters{"repository": "owner/repo"}
	profile := Profile{Name: "immutable", Mode: All, Requirements: []Requirement{{Atom: CheckoutClean, Parameters: parameters, Failure: "checkout is dirty"}}}
	source := SourceFunc(func(_ context.Context, atom Atom, got Parameters) (Fact, error) {
		got["repository"] = "other/repo"
		return Fact{Atom: atom, Parameters: Parameters{"repository": "owner/repo"}, Passed: true}, nil
	})
	if _, err := Evaluate(context.Background(), profile, source); err != nil {
		t.Fatal(err)
	}
	if parameters["repository"] != "owner/repo" {
		t.Fatalf("profile parameters were mutated: %v", parameters)
	}
}

func TestAtomVocabularyIsValidAndUnique(t *testing.T) {
	t.Parallel()
	atoms := []Atom{
		ReadyContractVersion, ReadyRunID, ReadyTaskIdentity, ReadyRepository, ReadyPullRequest, ReadyBaseBranch, ReadyHeadBranch, ReadyTaskPrefix, ReadyVerifiedHead, ReadyCreatedAt, ReadyRunDirectory,
		BindingTask, BindingRepository, BindingBaseBranch, BindingFreshness,
		PullRequestOpen, PullRequestNotDraft, PullRequestBase, PullRequestHeadBranch, PullRequestVerifiedHead, PullRequestMerged, PullRequestMergeCommit, PullRequestClosedUnmerged, PullRequestVerifiedHeadMismatch,
		SafeguardReviewClear, SafeguardChecksTerminal, SafeguardChecksAccepted, SafeguardRegression,
		HealthStatus, HealthApp, HealthCommit, HealthTree, HealthBuild, HealthDeployment, HealthContract, HealthStartedAt,
		CheckoutClean, CheckoutBaseBranch, CheckoutUpstream, CheckoutHeadOnMain, CheckoutOrigin,
		ReceiptSourceOnMain, ReceiptStatus, ReceiptApp, ReceiptBranch, ReceiptTree, ReceiptContract, ReceiptCommitFormat, ReceiptTreeFormat, ReceiptBinaryFormat, ReceiptDeploymentID, ReceiptBuildID, ReceiptRepository, ReceiptCheckpointTime, ReceiptFinishOrder,
		MergeContained, VerifiedHeadContained, DeploymentSuccessful, HealthMatches, SourceValid, SafeguardsClear, RemoteBranchAbsent, WorktreeAbsent, TaskComplete, ChildrenComplete,
		ExternalAuthenticationFailure, DeploymentFailed, SourceInvalid, RemoteBranchPresent, WorktreePresent, TaskIncomplete, ChildrenIncomplete,
	}
	seen := make(map[Atom]struct{}, len(atoms))
	for _, atom := range atoms {
		if err := atom.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, ok := seen[atom]; ok {
			t.Fatalf("duplicate atom %s", atom)
		}
		seen[atom] = struct{}{}
	}
}
