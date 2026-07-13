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
	ManagedRoot    string
	ProjectPath    string
	BaseBranch     string
	Bootstrap      bool
	ReceiptPath    string
	PendingReceipt string
	HealthURL      string
	SourcePath     string
}

func (c RepositoryConfig) DeploymentRequired() bool {
	return c.ReceiptPath != "" && c.PendingReceipt != "" && c.HealthURL != ""
}

func (c RepositoryConfig) validate() error {
	if !repositoryPattern.MatchString(c.Repository) || c.App == "" {
		return errors.New("repository catalog: app and canonical repository are required")
	}
	remoteRepository, ok := normalizeGitHubRepository(c.RepoURL)
	if !ok || !strings.EqualFold(remoteRepository, c.Repository) {
		return errors.New("repository catalog: URL must match the canonical GitHub repository")
	}
	if !filepath.IsAbs(c.RepoPath) || !filepath.IsAbs(c.ManagedRoot) || !validBranch(c.BaseBranch) {
		return errors.New("repository catalog: absolute managed paths and base branch are required")
	}
	if !pathWithin(filepath.Clean(c.ManagedRoot), filepath.Clean(c.RepoPath)) {
		return errors.New("repository catalog: repository path must stay within its managed root")
	}
	if c.ProjectPath == "" {
		c.ProjectPath = c.RepoPath
	}
	if !filepath.IsAbs(c.ProjectPath) {
		return errors.New("repository catalog: Linear project path must be absolute")
	}
	deploymentFields := 0
	for _, value := range []string{c.ReceiptPath, c.PendingReceipt, c.HealthURL} {
		if value != "" {
			deploymentFields++
		}
	}
	if deploymentFields != 0 && deploymentFields != 3 {
		return errors.New("repository catalog: deployment receipt and health locations must be configured together")
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
		config.ManagedRoot = filepath.Clean(config.ManagedRoot)
		config.ProjectPath = projectPath
		catalog.byRepository[config.Repository] = config
	}
	return catalog, nil
}

func (c *RepositoryCatalog) ResolveProject(description string) (RepositoryConfig, error) {
	repositoryMatch := projectRepositoryPattern.FindStringSubmatch(description)
	pathMatch := projectLocalPathPattern.FindStringSubmatch(description)
	if len(repositoryMatch) != 2 || len(pathMatch) != 2 {
		return RepositoryConfig{}, permanentRouting(errors.New("repository catalog: Linear project must declare GitHub Repo and Local Path"))
	}
	config, ok := c.byRepository[repositoryMatch[1]]
	if !ok {
		return RepositoryConfig{}, permanentRouting(fmt.Errorf("repository catalog: %s is not allowlisted", repositoryMatch[1]))
	}
	declaredPath := filepath.Clean(strings.TrimSpace(pathMatch[1]))
	if declaredPath != config.ProjectPath {
		return RepositoryConfig{}, permanentRouting(fmt.Errorf("repository catalog: path %s does not match allowlisted %s", declaredPath, config.ProjectPath))
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
		return RepositoryConfig{}, permanentRouting(fmt.Errorf("resolve repository: invalid issue identifier %q", issueIdentifier))
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
		return RepositoryConfig{}, permanentRouting(errors.New("resolve repository: issue has no Linear project"))
	}
	return r.catalog.ResolveProject(value.Data.Issue.Project.Description)
}

type permanentRoutingError struct{ error }

func (permanentRoutingError) Permanent() bool { return true }

func (e permanentRoutingError) Unwrap() error { return e.error }

func permanentRouting(err error) error { return permanentRoutingError{error: err} }

func normalizeGitHubRepository(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	if value, found := strings.CutPrefix(remote, "git@github.com:"); found {
		value = strings.TrimSuffix(value, ".git")
		return value, repositoryPattern.MatchString(value)
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/"), ".git")
	return value, repositoryPattern.MatchString(value)
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
