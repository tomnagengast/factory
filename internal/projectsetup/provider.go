package projectsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

const maxLinearResponseBytes = 1 << 20

type ProviderIssue struct {
	ID         string
	Identifier string
}

type ProviderCoordinator interface {
	Ensure(context.Context, Spec) (ProviderIssue, error)
}

type LinearProviderCoordinator struct {
	url         string
	apiKey      string
	projectName string
	labelNames  []string
	client      *http.Client
}

type providerWorkspace struct {
	projectID string
	teamID    string
	labelIDs  []string
}

type providerLinearIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Description string `json:"description"`
	Labels      struct {
		Nodes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

func NewLinearProviderCoordinator(linearURL, apiKey, projectName string, labelNames []string, client *http.Client) (*LinearProviderCoordinator, error) {
	parsed, err := url.Parse(strings.TrimSpace(linearURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("provider coordinator: valid Linear URL is required")
	}
	apiKey = strings.TrimSpace(apiKey)
	projectName = strings.TrimSpace(projectName)
	if apiKey == "" || projectName == "" || client == nil {
		return nil, errors.New("provider coordinator: API key, project, and HTTP client are required")
	}
	labels := make([]string, 0, len(labelNames))
	for _, name := range labelNames {
		name = strings.TrimSpace(name)
		if name == "" || slices.ContainsFunc(labels, func(value string) bool { return strings.EqualFold(value, name) }) {
			return nil, errors.New("provider coordinator: unique label names are required")
		}
		labels = append(labels, name)
	}
	if len(labels) == 0 {
		return nil, errors.New("provider coordinator: at least one label is required")
	}
	return &LinearProviderCoordinator{
		url: strings.TrimSpace(linearURL), apiKey: apiKey, projectName: projectName,
		labelNames: labels, client: client,
	}, nil
}

func (c *LinearProviderCoordinator) Ensure(ctx context.Context, spec Spec) (ProviderIssue, error) {
	if spec.ProjectID == "" || spec.ProjectName == "" || !validRepository(spec.Repository) || spec.CloudURL == "" {
		return ProviderIssue{}, errors.New("provider coordinator: complete Cloud project metadata is required")
	}
	cloudURL, err := normalizeCloudURL(spec.CloudURL)
	if err != nil || cloudURL != spec.CloudURL {
		return ProviderIssue{}, errors.New("provider coordinator: canonical Cloud URL is required")
	}

	workspace, err := c.workspace(ctx)
	if err != nil {
		return ProviderIssue{}, err
	}
	marker := providerIssueMarker(spec.ProjectID, spec.CloudURL)
	issue, found, err := c.findIssue(ctx, workspace.projectID, marker)
	if err != nil {
		return ProviderIssue{}, err
	}
	if !found {
		issue, err = c.createIssue(ctx, workspace, spec, marker)
		if err != nil {
			return ProviderIssue{}, err
		}
	} else if err := c.ensureLabels(ctx, &issue, workspace.labelIDs); err != nil {
		return ProviderIssue{}, err
	}
	if issue.ID == "" || issue.Identifier == "" {
		return ProviderIssue{}, errors.New("provider coordinator: Linear returned an incomplete issue")
	}
	return ProviderIssue{ID: issue.ID, Identifier: issue.Identifier}, nil
}

func (c *LinearProviderCoordinator) workspace(ctx context.Context) (providerWorkspace, error) {
	const query = `query ProviderWorkspace {
  projects(first: 100) { nodes { id name teams { nodes { id } } } }
  issueLabels(first: 100) { nodes { id name } }
}`
	var response struct {
		Projects struct {
			Nodes []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Teams struct {
					Nodes []struct {
						ID string `json:"id"`
					} `json:"nodes"`
				} `json:"teams"`
			} `json:"nodes"`
		} `json:"projects"`
		Labels struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"issueLabels"`
	}
	if err := c.graphQL(ctx, query, nil, &response); err != nil {
		return providerWorkspace{}, fmt.Errorf("provider coordinator: resolve Linear workspace: %w", err)
	}

	var workspace providerWorkspace
	for _, project := range response.Projects.Nodes {
		if !strings.EqualFold(project.Name, c.projectName) {
			continue
		}
		if workspace.projectID != "" {
			return providerWorkspace{}, fmt.Errorf("provider coordinator: Linear project %q is ambiguous", c.projectName)
		}
		if len(project.Teams.Nodes) != 1 || project.Teams.Nodes[0].ID == "" {
			return providerWorkspace{}, fmt.Errorf("provider coordinator: Linear project %q must belong to exactly one team", c.projectName)
		}
		workspace.projectID = project.ID
		workspace.teamID = project.Teams.Nodes[0].ID
	}
	if workspace.projectID == "" {
		return providerWorkspace{}, fmt.Errorf("provider coordinator: Linear project %q was not found", c.projectName)
	}
	for _, required := range c.labelNames {
		labelID := ""
		for _, label := range response.Labels.Nodes {
			if strings.EqualFold(label.Name, required) {
				if labelID != "" {
					return providerWorkspace{}, fmt.Errorf("provider coordinator: Linear label %q is ambiguous", required)
				}
				labelID = label.ID
			}
		}
		if labelID == "" {
			return providerWorkspace{}, fmt.Errorf("provider coordinator: Linear label %q was not found", required)
		}
		workspace.labelIDs = append(workspace.labelIDs, labelID)
	}
	return workspace, nil
}

func (c *LinearProviderCoordinator) findIssue(ctx context.Context, projectID, marker string) (providerLinearIssue, bool, error) {
	const query = `query ProviderIssues($project: String!, $after: String) {
  project(id: $project) {
    issues(first: 100, after: $after) {
      nodes { id identifier description labels { nodes { id name } } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	var found providerLinearIssue
	matchCount := 0
	after := ""
	for {
		variables := map[string]any{"project": projectID}
		if after != "" {
			variables["after"] = after
		}
		var response struct {
			Project *struct {
				Issues struct {
					Nodes    []providerLinearIssue `json:"nodes"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"issues"`
			} `json:"project"`
		}
		if err := c.graphQL(ctx, query, variables, &response); err != nil {
			return providerLinearIssue{}, false, fmt.Errorf("provider coordinator: find provider issue: %w", err)
		}
		if response.Project == nil {
			return providerLinearIssue{}, false, errors.New("provider coordinator: provider project disappeared")
		}
		for _, issue := range response.Project.Issues.Nodes {
			if strings.Contains(issue.Description, marker) {
				found = issue
				matchCount++
			}
		}
		page := response.Project.Issues.PageInfo
		if !page.HasNextPage {
			break
		}
		if page.EndCursor == "" || page.EndCursor == after {
			return providerLinearIssue{}, false, errors.New("provider coordinator: invalid Linear issue pagination")
		}
		after = page.EndCursor
	}
	if matchCount > 1 {
		return providerLinearIssue{}, false, errors.New("provider coordinator: multiple provider issues carry the onboarding marker")
	}
	return found, matchCount == 1, nil
}

func (c *LinearProviderCoordinator) createIssue(ctx context.Context, workspace providerWorkspace, spec Spec, marker string) (providerLinearIssue, error) {
	const mutation = `mutation CreateProviderIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    success
    issue { id identifier description labels { nodes { id name } } }
  }
}`
	input := map[string]any{
		"teamId":      workspace.teamID,
		"projectId":   workspace.projectID,
		"title":       providerIssueTitle(spec),
		"description": providerIssueDescription(spec, marker),
		"labelIds":    workspace.labelIDs,
	}
	var response struct {
		Create struct {
			Success bool                `json:"success"`
			Issue   providerLinearIssue `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := c.graphQL(ctx, mutation, map[string]any{"input": input}, &response); err != nil {
		return providerLinearIssue{}, fmt.Errorf("provider coordinator: create provider issue: %w", err)
	}
	if !response.Create.Success || response.Create.Issue.ID == "" || response.Create.Issue.Identifier == "" {
		return providerLinearIssue{}, errors.New("provider coordinator: Linear did not create the provider issue")
	}
	return response.Create.Issue, nil
}

func (c *LinearProviderCoordinator) ensureLabels(ctx context.Context, issue *providerLinearIssue, required []string) error {
	labelIDs := make([]string, 0, len(issue.Labels.Nodes)+len(required))
	for _, label := range issue.Labels.Nodes {
		if label.ID != "" && !slices.Contains(labelIDs, label.ID) {
			labelIDs = append(labelIDs, label.ID)
		}
	}
	missing := false
	for _, labelID := range required {
		if !slices.Contains(labelIDs, labelID) {
			labelIDs = append(labelIDs, labelID)
			missing = true
		}
	}
	if !missing {
		return nil
	}
	const mutation = `mutation LabelProviderIssue($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) {
    success
    issue { id identifier description labels { nodes { id name } } }
  }
}`
	var response struct {
		Update struct {
			Success bool                `json:"success"`
			Issue   providerLinearIssue `json:"issue"`
		} `json:"issueUpdate"`
	}
	variables := map[string]any{"id": issue.ID, "input": map[string]any{"labelIds": labelIDs}}
	if err := c.graphQL(ctx, mutation, variables, &response); err != nil {
		return fmt.Errorf("provider coordinator: label provider issue: %w", err)
	}
	if !response.Update.Success {
		return errors.New("provider coordinator: Linear did not label the provider issue")
	}
	*issue = response.Update.Issue
	return nil
}

func (c *LinearProviderCoordinator) graphQL(ctx context.Context, query string, variables map[string]any, target any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Authorization", c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "nags-factory/1")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Linear returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxLinearResponseBytes))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return errors.New(envelope.Errors[0].Message)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("Linear returned no data")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	return nil
}

func providerIssueMarker(projectID, cloudURL string) string {
	digest := sha256.Sum256([]byte(projectID + "\x00" + cloudURL))
	return "<!-- factory-provider-setup:" + hex.EncodeToString(digest[:12]) + " -->"
}

func providerIssueDescription(spec Spec, marker string) string {
	_, app, _ := strings.Cut(spec.Repository, "/")
	host := strings.TrimPrefix(spec.CloudURL, "https://")
	return fmt.Sprintf(`Factory project onboarding created this separately routed provider task for **%s**.

- Tenant repository: %s
- Canonical tenant path: %s
- Tenant Linear project ID: %s
- Requested Cloud URL: %s
- Provider repository: tomnagengast/network

Own the reviewed provider desired state in the Network repository. Read the tenant project's current issues and comments before choosing access. Allocate the next available provider port, register tenant app %q, and add the %q route. Default access to private unless an explicit owner decision on the tenant project requires public access. The tenant root nags.toml remains authoritative for build, run, health, processes, and secrets.

Provider validation may require the tenant manifest on canonical tenant main. Prepare the provider change in its own Worktrunk branch and draft PR, but do not merge, reconcile, deploy, or bypass the exact-head Factory checkpoint. Coordinate sequencing through Linear when the tenant manifest is not ready.

%s`, spec.ProjectName, spec.Repository, spec.LocalPath, spec.ProjectID, spec.CloudURL, app, host, marker)
}

func providerIssueTitle(spec Spec) string {
	return "Register " + spec.Repository + " with NAGs Cloud"
}

var _ ProviderCoordinator = (*LinearProviderCoordinator)(nil)
