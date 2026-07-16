package taskservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/taskmodel"
)

var ErrLinearTaskNotFound = errors.New("Linear task provider: issue not found")

type LinearActor struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind"`
}

type LinearMessage struct {
	ID        string      `json:"id"`
	Ordinal   uint64      `json:"ordinal"`
	ParentID  string      `json:"parentId,omitempty"`
	Body      string      `json:"body"`
	Author    LinearActor `json:"author"`
	CreatedAt time.Time   `json:"createdAt"`
}

type LinearIssue struct {
	Ref          taskmodel.TaskRef `json:"ref"`
	Title        string            `json:"title"`
	Description  string            `json:"description,omitempty"`
	ProjectID    string            `json:"projectId,omitempty"`
	ProjectName  string            `json:"projectName,omitempty"`
	State        string            `json:"state"`
	StateName    string            `json:"stateName"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	Revision     uint64            `json:"revision"`
	Messages     []LinearMessage   `json:"messages"`
	ExternalURL  string            `json:"externalUrl"`
	ProviderUUID string            `json:"-"`
	TeamUUID     string            `json:"-"`
}

type LinearProvider struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
	identities LinearIdentityBinder
}

type LinearIdentityBinder interface {
	Bind(identifier, uuid string) (bool, error)
}

type linearCommentNode struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	Parent    *struct {
		ID string `json:"id"`
	} `json:"parent"`
	User *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
}

type linearIssuePage struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	UpdatedAt   time.Time `json:"updatedAt"`
	State       struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Project *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	Team struct {
		ID string `json:"id"`
	} `json:"team"`
	Comments struct {
		Nodes    []linearCommentNode `json:"nodes"`
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"comments"`
}

func NewLinearProvider(endpoint, apiKey string, client *http.Client, identities LinearIdentityBinder) (*LinearProvider, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil {
		return nil, errors.New("Linear task provider: endpoint must be HTTPS")
	}
	host := net.ParseIP(parsed.Hostname())
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && host != nil && host.IsLoopback())) {
		return nil, errors.New("Linear task provider: endpoint must be HTTPS")
	}
	if apiKey == "" || client == nil || identities == nil {
		return nil, errors.New("Linear task provider: API key, HTTP client, and identity binder are required")
	}
	return &LinearProvider{endpoint: endpoint, apiKey: apiKey, httpClient: client, identities: identities}, nil
}

func (p *LinearProvider) Detail(ctx context.Context, identifier string) (LinearIssue, error) {
	ref, err := taskmodel.LegacyLinear(identifier)
	if err != nil {
		return LinearIssue{}, ErrLinearTaskNotFound
	}
	var first *linearIssuePage
	comments := make([]linearCommentNode, 0)
	commentIDs := make(map[string]struct{})
	after := ""
	for {
		var response struct {
			Issue *linearIssuePage `json:"issue"`
		}
		variables := map[string]any{"id": ref.Identifier}
		if after != "" {
			variables["after"] = after
		}
		err = p.graphql(ctx, `query FactoryTask($id: String!, $after: String) {
  issue(id: $id) {
    id identifier title description url updatedAt
    state { name type }
    project { id name }
    team { id }
    comments(first: 100, after: $after) {
      nodes { id body createdAt parent { id } user { id name } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`, variables, &response)
		if err != nil {
			return LinearIssue{}, err
		}
		if response.Issue == nil || !strings.EqualFold(response.Issue.Identifier, ref.Identifier) {
			return LinearIssue{}, ErrLinearTaskNotFound
		}
		if _, err := p.identities.Bind(response.Issue.Identifier, response.Issue.ID); err != nil {
			return LinearIssue{}, fmt.Errorf("Linear task provider: bind issue identity: %w", err)
		}
		if first == nil {
			first = response.Issue
		} else if response.Issue.ID != first.ID || !strings.EqualFold(response.Issue.Identifier, first.Identifier) {
			return LinearIssue{}, errors.New("Linear task provider: issue identity changed during pagination")
		}
		for _, comment := range response.Issue.Comments.Nodes {
			if comment.ID == "" {
				return LinearIssue{}, errors.New("Linear task provider: comment ID is missing")
			}
			if _, duplicate := commentIDs[comment.ID]; duplicate {
				return LinearIssue{}, fmt.Errorf("Linear task provider: duplicate comment ID %s", comment.ID)
			}
			commentIDs[comment.ID] = struct{}{}
			comments = append(comments, comment)
		}
		pageInfo := response.Issue.Comments.PageInfo
		if !pageInfo.HasNextPage {
			break
		}
		if pageInfo.EndCursor == "" || pageInfo.EndCursor == after {
			return LinearIssue{}, errors.New("Linear task provider: invalid comments pagination cursor")
		}
		after = pageInfo.EndCursor
	}
	if first == nil {
		return LinearIssue{}, ErrLinearTaskNotFound
	}
	issue := LinearIssue{
		Ref: ref, ProviderUUID: first.ID, TeamUUID: first.Team.ID,
		Title: first.Title, Description: first.Description, ExternalURL: first.URL,
		State: linearState(first.State.Type), StateName: first.State.Name,
		UpdatedAt: first.UpdatedAt.UTC(),
	}
	if issue.ExternalURL == "" {
		issue.ExternalURL = "https://linear.app/issue/" + strings.ToLower(ref.Identifier)
	}
	if first.Project != nil {
		issue.ProjectID, issue.ProjectName = first.Project.ID, first.Project.Name
	}
	sort.Slice(comments, func(i, j int) bool {
		if comments[i].CreatedAt.Equal(comments[j].CreatedAt) {
			return comments[i].ID < comments[j].ID
		}
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})
	for index, comment := range comments {
		message := LinearMessage{ID: comment.ID, Ordinal: uint64(index + 1), Body: comment.Body, CreatedAt: comment.CreatedAt.UTC(), Author: LinearActor{Kind: "human"}}
		if comment.Parent != nil {
			message.ParentID = comment.Parent.ID
		}
		if comment.User != nil {
			message.Author.ID, message.Author.Name = comment.User.ID, comment.User.Name
		} else {
			message.Author.ID, message.Author.Name, message.Author.Kind = "linear-integration", "Linear integration", "agent"
		}
		if linearhook.FactoryAuthored(comment.Body) {
			message.Author.Kind = "agent"
		}
		issue.Messages = append(issue.Messages, message)
	}
	if issue.UpdatedAt.IsZero() || issue.ProviderUUID == "" || issue.TeamUUID == "" || issue.Title == "" {
		return LinearIssue{}, errors.New("Linear task provider: issue response is incomplete")
	}
	issue.Revision = uint64(issue.UpdatedAt.UnixMilli())
	return issue, nil
}

func (p *LinearProvider) Comment(ctx context.Context, identifier, parentID, body, operation, idempotencyKey string) (LinearIssue, error) {
	if !validLinearText(body, 32<<10, false) || idempotencyKey == "" ||
		(operation != "comment" && operation != "reply" && operation != "gate") || operation == "reply" && parentID == "" {
		return LinearIssue{}, errors.New("Linear task provider: comment is invalid")
	}
	issue, err := p.Detail(ctx, identifier)
	if err != nil {
		return LinearIssue{}, err
	}
	if parentID != "" {
		found := false
		for _, message := range issue.Messages {
			if message.ID == parentID {
				found = true
				break
			}
		}
		if !found {
			return LinearIssue{}, errors.New("Linear task provider: reply parent is unknown")
		}
	}
	body = linearSignedBody(issue.Ref.Identifier, operation, idempotencyKey, body)
	if !validLinearText(body, 32<<10, false) {
		return LinearIssue{}, errors.New("Linear task provider: signed comment is too large")
	}
	for _, message := range issue.Messages {
		if message.Body == body {
			return issue, nil
		}
	}
	input := map[string]any{"issueId": issue.ProviderUUID, "body": body}
	if parentID != "" {
		input["parentId"] = parentID
	}
	var response struct {
		CommentCreate struct {
			Success bool `json:"success"`
			Comment *struct {
				ID string `json:"id"`
			} `json:"comment"`
		} `json:"commentCreate"`
	}
	if err := p.graphql(ctx, `mutation FactoryComment($input: CommentCreateInput!) {
  commentCreate(input: $input) { success comment { id } }
}`, map[string]any{"input": input}, &response); err != nil {
		return LinearIssue{}, err
	}
	if !response.CommentCreate.Success || response.CommentCreate.Comment == nil {
		return LinearIssue{}, errors.New("Linear task provider: comment mutation was not accepted")
	}
	return p.Detail(ctx, identifier)
}

func (p *LinearProvider) Link(ctx context.Context, identifier, label, linkURL string) (LinearIssue, error) {
	parsed, err := url.Parse(linkURL)
	if !validLinearText(label, 160, false) || err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return LinearIssue{}, errors.New("Linear task provider: attachment is invalid")
	}
	issue, err := p.Detail(ctx, identifier)
	if err != nil {
		return LinearIssue{}, err
	}
	var response struct {
		AttachmentCreate struct {
			Success    bool `json:"success"`
			Attachment *struct {
				ID string `json:"id"`
			} `json:"attachment"`
		} `json:"attachmentCreate"`
	}
	if err := p.graphql(ctx, `mutation FactoryAttachment($input: AttachmentCreateInput!) {
  attachmentCreate(input: $input) { success attachment { id } }
}`, map[string]any{"input": map[string]any{"issueId": issue.ProviderUUID, "title": label, "url": linkURL}}, &response); err != nil {
		return LinearIssue{}, err
	}
	if !response.AttachmentCreate.Success || response.AttachmentCreate.Attachment == nil {
		return LinearIssue{}, errors.New("Linear task provider: attachment mutation was not accepted")
	}
	return p.Detail(ctx, identifier)
}

func (p *LinearProvider) State(ctx context.Context, identifier, state string) (LinearIssue, error) {
	desired, ok := map[string][]string{
		"open": {"unstarted", "backlog"}, "in_progress": {"started"}, "completed": {"completed"}, "canceled": {"canceled"},
	}[state]
	if !ok {
		return LinearIssue{}, errors.New("Linear task provider: state is invalid")
	}
	issue, err := p.Detail(ctx, identifier)
	if err != nil || issue.State == state {
		return issue, err
	}
	var states struct {
		WorkflowStates struct {
			Nodes []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
				Team struct {
					ID string `json:"id"`
				} `json:"team"`
			} `json:"nodes"`
		} `json:"workflowStates"`
	}
	if err := p.graphql(ctx, `query FactoryWorkflowStates { workflowStates(first: 250) { nodes { id type team { id } } } }`, nil, &states); err != nil {
		return LinearIssue{}, err
	}
	stateID := ""
	for _, candidateType := range desired {
		for _, candidate := range states.WorkflowStates.Nodes {
			if candidate.Team.ID == issue.TeamUUID && strings.EqualFold(candidate.Type, candidateType) {
				stateID = candidate.ID
				break
			}
		}
		if stateID != "" {
			break
		}
	}
	if stateID == "" {
		return LinearIssue{}, errors.New("Linear task provider: matching workflow state is unavailable")
	}
	var update struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := p.graphql(ctx, `mutation FactoryIssueState($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) { success }
}`, map[string]any{"id": issue.ProviderUUID, "input": map[string]string{"stateId": stateID}}, &update); err != nil {
		return LinearIssue{}, err
	}
	if !update.IssueUpdate.Success {
		return LinearIssue{}, errors.New("Linear task provider: state mutation was not accepted")
	}
	return p.Detail(ctx, identifier)
}

func (p *LinearProvider) Gate(ctx context.Context, identifier, kind, mode, artifactURL, idempotencyKey string) (LinearIssue, error) {
	if !validLinearText(kind, 80, false) || mode != "gated" && mode != "automatic" {
		return LinearIssue{}, errors.New("Linear task provider: gate is invalid")
	}
	if artifactURL != "" {
		parsed, err := url.Parse(artifactURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return LinearIssue{}, errors.New("Linear task provider: gate artifact is invalid")
		}
	}
	body := "Gate: " + kind + "\nMode: " + mode
	if artifactURL != "" {
		body += "\nArtifact: " + artifactURL
	}
	if mode == "gated" {
		body += "\n\nPlease approve with an affirmative reaction or request revisions in a contextual reply."
	}
	return p.Comment(ctx, identifier, "", body, "gate", idempotencyKey)
}

func (p *LinearProvider) graphql(ctx context.Context, query string, variables map[string]any, target any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", p.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := p.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("Linear task provider: request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return fmt.Errorf("Linear task provider: HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("Linear task provider: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("Linear task provider: GraphQL: %s", envelope.Errors[0].Message)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("Linear task provider: response data is missing")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("Linear task provider: decode data: %w", err)
	}
	return nil
}

func linearState(value string) string {
	switch strings.ToLower(value) {
	case "completed":
		return "completed"
	case "canceled":
		return "canceled"
	case "started":
		return "in_progress"
	default:
		return "open"
	}
}

func linearSignedBody(identifier, operation, key, body string) string {
	digest := sha256.Sum256([]byte(key))
	marker := "🐘 `codex-do:" + identifier + ":" + operation + "-" + hex.EncodeToString(digest[:4]) + ":r1`"
	return strings.TrimSpace(body) + "\n\n" + marker
}

func validLinearText(value string, maximum int, empty bool) bool {
	return (empty || value != "") && value == strings.TrimSpace(value) && len(value) <= maximum && utf8.ValidString(value)
}
