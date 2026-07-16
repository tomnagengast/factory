package agentrun

import (
	"os"
	"path/filepath"
	"testing"
)

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
		{name: "pending check", checks: []pullRequestCheck{{Status: "IN_PROGRESS"}}, want: true},
		{name: "failed status", checks: []pullRequestCheck{{State: "FAILURE"}}, want: true},
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
	path := filepath.Join(directory, "gh")
	script := `#!/bin/sh
printf '%s' '[{"number":1,"state":"OPEN","headRefName":"eng-46-fix"},{"number":2,"state":"OPEN","headRefName":"ENG-46-uppercase"},{"number":3,"state":"OPEN","headRefName":"factory-task-1-fix"}]'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
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
