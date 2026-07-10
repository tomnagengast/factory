package githubhook

import (
	"testing"
	"time"
)

func TestParseExtractsPullRequestAndCIMetadata(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 10, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		eventType  string
		body       string
		wantAction string
		wantPR     int
		wantBranch string
		wantStatus string
		wantResult string
	}{
		{
			name:       "pull request",
			eventType:  "pull_request",
			body:       `{"action":"synchronize","repository":{"full_name":"tom/repo"},"pull_request":{"number":42,"html_url":"https://github.com/tom/repo/pull/42","head":{"ref":"eng-42-fix","sha":"abc"}}}`,
			wantAction: "synchronize",
			wantPR:     42,
			wantBranch: "eng-42-fix",
		},
		{
			name:       "check run",
			eventType:  "check_run",
			body:       `{"action":"completed","repository":{"full_name":"tom/repo"},"check_run":{"status":"completed","conclusion":"failure","head_sha":"abc","html_url":"https://github.com/check/1","pull_requests":[{"number":42}],"check_suite":{"head_branch":"eng-42-fix"}}}`,
			wantAction: "completed",
			wantPR:     42,
			wantBranch: "eng-42-fix",
			wantStatus: "completed",
			wantResult: "failure",
		},
		{
			name:       "issue comment on pull request",
			eventType:  "issue_comment",
			body:       `{"action":"created","repository":{"full_name":"tom/repo"},"issue":{"number":42,"html_url":"https://github.com/tom/repo/pull/42","pull_request":{"url":"https://api.github.com/pulls/42"}},"comment":{"html_url":"https://github.com/comment/1"}}`,
			wantAction: "created",
			wantPR:     42,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			event, err := Parse("delivery-1", test.eventType, []byte(test.body), now)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if event.Action != test.wantAction || event.HeadBranch != test.wantBranch || event.Status != test.wantStatus || event.Conclusion != test.wantResult {
				t.Fatalf("event = %#v", event)
			}
			if test.wantPR > 0 && (len(event.PullRequests) != 1 || event.PullRequests[0] != test.wantPR) {
				t.Fatalf("pull requests = %v, want %d", event.PullRequests, test.wantPR)
			}
			if event.Repository != "tom/repo" || event.ReceivedAt != now {
				t.Fatalf("event = %#v", event)
			}
		})
	}
}

func TestParseRejectsMissingRepository(t *testing.T) {
	t.Parallel()
	if _, err := Parse("delivery-1", "ping", []byte(`{"zen":"hi"}`), time.Now()); err == nil {
		t.Fatal("expected error")
	}
}
