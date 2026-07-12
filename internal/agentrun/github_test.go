package agentrun

import "testing"

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
