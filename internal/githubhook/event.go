package githubhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Event struct {
	DeliveryID   string    `json:"deliveryId"`
	Type         string    `json:"type"`
	Action       string    `json:"action"`
	Repository   string    `json:"repository"`
	PullRequests []int     `json:"pullRequests,omitempty"`
	HeadBranch   string    `json:"headBranch,omitempty"`
	HeadSHA      string    `json:"headSha,omitempty"`
	Status       string    `json:"status,omitempty"`
	Conclusion   string    `json:"conclusion,omitempty"`
	URL          string    `json:"url,omitempty"`
	ReceivedAt   time.Time `json:"receivedAt"`
}

type payload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest *struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Issue *struct {
		Number      int             `json:"number"`
		HTMLURL     string          `json:"html_url"`
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Comment *struct {
		HTMLURL string `json:"html_url"`
	} `json:"comment"`
	Review *struct {
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
	} `json:"review"`
	CheckRun *struct {
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		HTMLURL      string `json:"html_url"`
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
		CheckSuite struct {
			HeadBranch string `json:"head_branch"`
		} `json:"check_suite"`
	} `json:"check_run"`
	CheckSuite *struct {
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		HeadBranch   string `json:"head_branch"`
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	WorkflowRun *struct {
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		HTMLURL      string `json:"html_url"`
		HeadBranch   string `json:"head_branch"`
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"workflow_run"`
	State     string `json:"state"`
	SHA       string `json:"sha"`
	TargetURL string `json:"target_url"`
	Branches  []struct {
		Name string `json:"name"`
	} `json:"branches"`
}

func Parse(deliveryID, eventType string, body []byte, receivedAt time.Time) (Event, error) {
	if deliveryID == "" {
		return Event{}, errors.New("github event: delivery ID is required")
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" || len(eventType) > 64 {
		return Event{}, errors.New("github event: event type is invalid")
	}

	var value payload
	if err := json.Unmarshal(body, &value); err != nil {
		return Event{}, fmt.Errorf("github event: decode: %w", err)
	}
	if value.Repository.FullName == "" || len(value.Repository.FullName) > 200 {
		return Event{}, errors.New("github event: repository is required")
	}

	event := Event{
		DeliveryID: deliveryID,
		Type:       eventType,
		Action:     value.Action,
		Repository: value.Repository.FullName,
		ReceivedAt: receivedAt.UTC(),
	}
	if event.Action == "" {
		event.Action = "received"
	}
	if eventType == "ping" {
		event.Action = "ping"
	}

	switch {
	case value.PullRequest != nil:
		event.PullRequests = appendPullRequest(event.PullRequests, value.PullRequest.Number)
		event.HeadBranch = value.PullRequest.Head.Ref
		event.HeadSHA = value.PullRequest.Head.SHA
		event.URL = value.PullRequest.HTMLURL
		if value.Comment != nil && value.Comment.HTMLURL != "" {
			event.URL = value.Comment.HTMLURL
		}
		if value.Review != nil {
			event.Status = value.Review.State
			if value.Review.HTMLURL != "" {
				event.URL = value.Review.HTMLURL
			}
		}
	case value.Issue != nil && len(value.Issue.PullRequest) > 0 && string(value.Issue.PullRequest) != "null":
		event.PullRequests = appendPullRequest(event.PullRequests, value.Issue.Number)
		event.URL = value.Issue.HTMLURL
		if value.Comment != nil && value.Comment.HTMLURL != "" {
			event.URL = value.Comment.HTMLURL
		}
	case value.CheckRun != nil:
		event.Status = value.CheckRun.Status
		event.Conclusion = value.CheckRun.Conclusion
		event.HeadBranch = value.CheckRun.CheckSuite.HeadBranch
		event.HeadSHA = value.CheckRun.HeadSHA
		event.URL = value.CheckRun.HTMLURL
		for _, pr := range value.CheckRun.PullRequests {
			event.PullRequests = appendPullRequest(event.PullRequests, pr.Number)
		}
	case value.CheckSuite != nil:
		event.Status = value.CheckSuite.Status
		event.Conclusion = value.CheckSuite.Conclusion
		event.HeadBranch = value.CheckSuite.HeadBranch
		event.HeadSHA = value.CheckSuite.HeadSHA
		for _, pr := range value.CheckSuite.PullRequests {
			event.PullRequests = appendPullRequest(event.PullRequests, pr.Number)
		}
	case value.WorkflowRun != nil:
		event.Status = value.WorkflowRun.Status
		event.Conclusion = value.WorkflowRun.Conclusion
		event.HeadBranch = value.WorkflowRun.HeadBranch
		event.HeadSHA = value.WorkflowRun.HeadSHA
		event.URL = value.WorkflowRun.HTMLURL
		for _, pr := range value.WorkflowRun.PullRequests {
			event.PullRequests = appendPullRequest(event.PullRequests, pr.Number)
		}
	default:
		event.Status = value.State
		event.HeadSHA = value.SHA
		event.URL = value.TargetURL
		if len(value.Branches) > 0 {
			event.HeadBranch = value.Branches[0].Name
		}
	}
	return event, nil
}

func appendPullRequest(values []int, number int) []int {
	if number > 0 && !slices.Contains(values, number) {
		return append(values, number)
	}
	return values
}
