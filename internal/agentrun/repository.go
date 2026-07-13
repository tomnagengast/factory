package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	projectRepositoryPattern = regexp.MustCompile("(?m)^GitHub Repo:\\s*https://github\\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)\\s*$")
	projectLocalPathPattern  = regexp.MustCompile("(?m)^Local Path:\\s*(/[^\\r\\n]+)\\s*$")
)

type RepositoryConfig struct {
	App            string
	Repository     string
	RepoURL        string
	RepoPath       string
	ProjectPath    string
	BaseBranch     string
	ReceiptPath    string
	PendingReceipt string
	HealthURL      string
	SourcePath     string
}

func (c RepositoryConfig) validate() error {
	if !repositoryPattern.MatchString(c.Repository) || c.App == "" {
		return errors.New("repository catalog: app and canonical repository are required")
	}
	if c.RepoURL == "" || !filepath.IsAbs(c.RepoPath) || !validBranch(c.BaseBranch) {
		return errors.New("repository catalog: URL, absolute path, and base branch are required")
	}
	if c.ProjectPath == "" {
		c.ProjectPath = c.RepoPath
	}
	if !filepath.IsAbs(c.ProjectPath) {
		return errors.New("repository catalog: Linear project path must be absolute")
	}
	if c.ReceiptPath == "" || c.PendingReceipt == "" || c.HealthURL == "" {
		return errors.New("repository catalog: receipt and health locations are required")
	}
	if c.SourcePath != "" && (filepath.IsAbs(c.SourcePath) || strings.HasPrefix(filepath.Clean(c.SourcePath), "..")) {
		return errors.New("repository catalog: source path must stay within the repository")
	}
	return nil
}

type RepositoryCatalog struct {
	byRepository map[string]RepositoryConfig
}

func NewRepositoryCatalog(configs []RepositoryConfig) (*RepositoryCatalog, error) {
	if len(configs) == 0 {
		return nil, errors.New("repository catalog: at least one repository is required")
	}
	catalog := &RepositoryCatalog{byRepository: make(map[string]RepositoryConfig, len(configs))}
	paths := make(map[string]string, len(configs))
	for _, config := range configs {
		if err := config.validate(); err != nil {
			return nil, err
		}
		if _, exists := catalog.byRepository[config.Repository]; exists {
			return nil, fmt.Errorf("repository catalog: duplicate repository %s", config.Repository)
		}
		cleanPath := filepath.Clean(config.RepoPath)
		projectPath := config.ProjectPath
		if projectPath == "" {
			projectPath = cleanPath
		}
		projectPath = filepath.Clean(projectPath)
		if repository, exists := paths[projectPath]; exists {
			return nil, fmt.Errorf("repository catalog: %s and %s share %s", repository, config.Repository, projectPath)
		}
		paths[projectPath] = config.Repository
		config.RepoPath = cleanPath
		config.ProjectPath = projectPath
		catalog.byRepository[config.Repository] = config
	}
	return catalog, nil
}

func (c *RepositoryCatalog) ResolveProject(description string) (RepositoryConfig, error) {
	repositoryMatch := projectRepositoryPattern.FindStringSubmatch(description)
	pathMatch := projectLocalPathPattern.FindStringSubmatch(description)
	if len(repositoryMatch) != 2 || len(pathMatch) != 2 {
		return RepositoryConfig{}, errors.New("repository catalog: Linear project must declare GitHub Repo and Local Path")
	}
	config, ok := c.byRepository[repositoryMatch[1]]
	if !ok {
		return RepositoryConfig{}, fmt.Errorf("repository catalog: %s is not allowlisted", repositoryMatch[1])
	}
	declaredPath := filepath.Clean(strings.TrimSpace(pathMatch[1]))
	if declaredPath != config.ProjectPath {
		return RepositoryConfig{}, fmt.Errorf("repository catalog: path %s does not match allowlisted %s", declaredPath, config.ProjectPath)
	}
	return config, nil
}

type RepositoryResolver interface {
	Resolve(context.Context, string) (RepositoryConfig, error)
}

type LinearRepositoryResolver struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
	catalog    *RepositoryCatalog
}

func NewLinearRepositoryResolver(endpoint, apiKey string, client *http.Client, catalog *RepositoryCatalog) (*LinearRepositoryResolver, error) {
	if endpoint == "" || apiKey == "" || client == nil || catalog == nil {
		return nil, errors.New("Linear repository resolver: endpoint, API key, client, and catalog are required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("Linear repository resolver: endpoint must be HTTPS")
	}
	return &LinearRepositoryResolver{endpoint: endpoint, apiKey: apiKey, httpClient: client, catalog: catalog}, nil
}

func (r *LinearRepositoryResolver) Resolve(ctx context.Context, issueIdentifier string) (RepositoryConfig, error) {
	if !ValidIssueIdentifier(issueIdentifier) {
		return RepositoryConfig{}, fmt.Errorf("resolve repository: invalid issue identifier %q", issueIdentifier)
	}
	payload, err := json.Marshal(map[string]any{
		"query":     "query FactoryRepository($id: String!) { issue(id: $id) { project { description } } }",
		"variables": map[string]string{"id": issueIdentifier},
	})
	if err != nil {
		return RepositoryConfig{}, fmt.Errorf("resolve repository: encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(payload))
	if err != nil {
		return RepositoryConfig{}, fmt.Errorf("resolve repository: create request: %w", err)
	}
	request.Header.Set("Authorization", r.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.httpClient.Do(request)
	if err != nil {
		return RepositoryConfig{}, fmt.Errorf("resolve repository: Linear request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return RepositoryConfig{}, fmt.Errorf("resolve repository: Linear HTTP %d", response.StatusCode)
	}
	var value struct {
		Data struct {
			Issue *struct {
				Project *struct {
					Description string
				}
			}
		}
		Errors []struct {
			Message string
		}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value); err != nil {
		return RepositoryConfig{}, fmt.Errorf("resolve repository: decode response: %w", err)
	}
	if len(value.Errors) > 0 {
		return RepositoryConfig{}, fmt.Errorf("resolve repository: Linear: %s", value.Errors[0].Message)
	}
	if value.Data.Issue == nil || value.Data.Issue.Project == nil {
		return RepositoryConfig{}, errors.New("resolve repository: issue has no Linear project")
	}
	return r.catalog.ResolveProject(value.Data.Issue.Project.Description)
}
