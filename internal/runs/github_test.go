package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewGitHubCLIRequiresExecutableAndDirectory(t *testing.T) {
	t.Parallel()
	if _, err := NewGitHubCLI("", t.TempDir()); err == nil {
		t.Fatal("empty executable accepted")
	}
	if _, err := NewGitHubCLI("gh", ""); err == nil {
		t.Fatal("empty directory accepted")
	}
}

func TestPullRequestSafeguardRegression(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		review string
		checks []pullRequestCheck
		want   bool
	}{
		{name: "no checks"},
		{name: "changes requested", review: "CHANGES_REQUESTED", want: true},
		{name: "successful check", checks: []pullRequestCheck{{Conclusion: "SUCCESS", Status: "COMPLETED"}}},
		{name: "neutral check", checks: []pullRequestCheck{{Conclusion: "NEUTRAL", Status: "COMPLETED"}}},
		{name: "skipped check", checks: []pullRequestCheck{{Conclusion: "SKIPPED", Status: "COMPLETED"}}},
		{name: "pending check", checks: []pullRequestCheck{{Status: "IN_PROGRESS"}}, want: true},
		{name: "failed state", checks: []pullRequestCheck{{State: "FAILURE"}}, want: true},
		{name: "cancelled conclusion", checks: []pullRequestCheck{{Conclusion: "CANCELLED", Status: "COMPLETED"}}, want: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := pullRequestSafeguardRegression(test.review, test.checks); got != test.want {
				t.Fatalf("pullRequestSafeguardRegression() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestGitHubCLIMatchesExactProviderBranchPrefix(t *testing.T) {
	directory := t.TempDir()
	path := writeGitHubScript(t, directory, `#!/bin/sh
printf '%s' '[{"number":1,"state":"OPEN","headRefName":"eng-46-fix"},{"number":2,"state":"OPEN","headRefName":"ENG-46-uppercase"},{"number":3,"state":"OPEN","headRefName":"factory-task-1-fix"}]'
`)
	client, err := NewGitHubCLI(path, directory)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := client.MatchingIssuePullRequests(t.Context(), "tomnagengast/factory", "eng-46-")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Number != 1 {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestGitHubCLISnapshotPreservesMechanicalFields(t *testing.T) {
	directory := t.TempDir()
	updated := "2026-07-16T20:00:00Z"
	path := writeGitHubScript(t, directory, `#!/bin/sh
printf '%s' '{"state":"MERGED","isDraft":false,"baseRefName":"main","headRefName":"eng-47-fix","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mergeCommit":{"oid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"reviewDecision":"APPROVED","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}],"updatedAt":"`+updated+`"}'
`)
	client, err := NewGitHubCLI(path, directory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Snapshot(t.Context(), ReadyCheckpoint{Repository: "tomnagengast/factory", PullRequest: 18})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "MERGED" || snapshot.BaseBranch != "main" || snapshot.HeadBranch != "eng-47-fix" ||
		snapshot.HeadOID != strings.Repeat("a", 40) || snapshot.MergeCommitOID != strings.Repeat("b", 40) || snapshot.SafeguardRegression {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	wantUpdated, _ := time.Parse(time.RFC3339, updated)
	if !snapshot.UpdatedAt.Equal(wantUpdated) {
		t.Fatalf("updatedAt = %s, want %s", snapshot.UpdatedAt, wantUpdated)
	}
}

func TestGitHubCLIClassifiesAuthenticationFailure(t *testing.T) {
	directory := t.TempDir()
	path := writeGitHubScript(t, directory, "#!/bin/sh\nprintf '%s' 'HTTP 401: run gh auth login' >&2\nexit 1\n")
	client, err := NewGitHubCLI(path, directory)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.MatchingIssuePullRequests(t.Context(), "tomnagengast/factory", "eng-47-")
	if !isExternalAuthenticationError(err) {
		t.Fatalf("error = %v, want external authentication classification", err)
	}
}

func writeGitHubScript(t *testing.T, directory, script string) string {
	t.Helper()
	path := filepath.Join(directory, "gh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
