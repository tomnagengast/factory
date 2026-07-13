package projectsetup

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	githubRepositoryLine = regexp.MustCompile(`(?mi)^\s*GitHub Repo\s*:\s*(\S+)\s*$`)
	localPathLine        = regexp.MustCompile(`(?mi)^\s*Local Path\s*:\s*(.+?)\s*$`)
	cloudURLLine         = regexp.MustCompile(`(?mi)^\s*(?:Cloud URL|nags\.cloud URL)\s*:\s*(\S+)\s*$`)
	cloudHostnameLabel   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	repositoryName       = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

type Request struct {
	ProjectID   string
	ProjectName string
	Description string
}

type Spec struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Repository  string `json:"repository"`
	RepoURL     string `json:"repoUrl"`
	LocalPath   string `json:"localPath"`
	ManagedRoot string `json:"managedRoot"`
	CloudURL    string `json:"cloudUrl,omitempty"`
	BaseBranch  string `json:"baseBranch"`
	Bootstrap   bool   `json:"bootstrap"`
	Managed     bool   `json:"managed"`
}

type ExistingRepository struct {
	Repository  string
	ProjectPath string
}

type Parser struct {
	owner       string
	managedRoot string
	existing    map[string]ExistingRepository
}

func NewParser(owner, managedRoot string, existing []ExistingRepository) (*Parser, error) {
	if !repositoryName.MatchString(owner) {
		return nil, errors.New("project setup parser: GitHub owner is required")
	}
	if !filepath.IsAbs(managedRoot) {
		return nil, errors.New("project setup parser: managed root must be absolute")
	}
	parser := &Parser{
		owner:       owner,
		managedRoot: filepath.Clean(managedRoot),
		existing:    make(map[string]ExistingRepository, len(existing)),
	}
	for _, repository := range existing {
		if !validRepository(repository.Repository) || !filepath.IsAbs(repository.ProjectPath) {
			return nil, errors.New("project setup parser: existing repositories require canonical identities and absolute project paths")
		}
		key := strings.ToLower(repository.Repository)
		if _, found := parser.existing[key]; found {
			return nil, fmt.Errorf("project setup parser: duplicate existing repository %s", repository.Repository)
		}
		repository.ProjectPath = filepath.Clean(repository.ProjectPath)
		parser.existing[key] = repository
	}
	return parser, nil
}

func (p *Parser) Parse(request Request) (Spec, bool, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ProjectName = strings.TrimSpace(request.ProjectName)
	if request.ProjectID == "" || len(request.ProjectID) > 128 {
		return Spec{}, false, permanent(errors.New("project setup: Linear project ID is required"))
	}
	if request.ProjectName == "" || len(request.ProjectName) > 256 {
		return Spec{}, false, permanent(errors.New("project setup: Linear project name is required"))
	}

	repositoryValue, repositoryFound, err := uniqueLine(githubRepositoryLine, request.Description, "GitHub Repo")
	if err != nil {
		return Spec{}, false, permanent(err)
	}
	pathValue, pathFound, err := uniqueLine(localPathLine, request.Description, "Local Path")
	if err != nil {
		return Spec{}, false, permanent(err)
	}
	cloudValue, cloudFound, err := uniqueLine(cloudURLLine, request.Description, "Cloud URL")
	if err != nil {
		return Spec{}, false, permanent(err)
	}
	if !repositoryFound || !pathFound {
		return Spec{ProjectID: request.ProjectID, ProjectName: request.ProjectName}, false, nil
	}

	repository, ok := normalizeRepository(repositoryValue)
	if !ok {
		return Spec{}, false, permanent(errors.New("project setup: GitHub Repo must be an owner/name slug or canonical github.com URL"))
	}
	owner, name, _ := strings.Cut(repository, "/")
	if !strings.EqualFold(owner, p.owner) {
		return Spec{}, false, permanent(fmt.Errorf("project setup: GitHub repository owner must be %s", p.owner))
	}
	repository = p.owner + "/" + name

	localPath := filepath.Clean(strings.TrimSpace(pathValue))
	if !filepath.IsAbs(localPath) {
		return Spec{}, false, permanent(errors.New("project setup: Local Path must be absolute"))
	}
	cloudURL := ""
	if cloudFound {
		cloudURL, err = normalizeCloudURL(cloudValue)
		if err != nil {
			return Spec{}, false, permanent(err)
		}
	}

	managed := true
	managedRoot := p.managedRoot
	if existing, found := p.existing[strings.ToLower(repository)]; found {
		if localPath != existing.ProjectPath {
			return Spec{}, false, permanent(fmt.Errorf("project setup: Local Path %s does not match the existing Factory path %s", localPath, existing.ProjectPath))
		}
		repository = existing.Repository
		managed = false
		managedRoot = filepath.Dir(existing.ProjectPath)
	} else if filepath.Dir(localPath) != p.managedRoot || filepath.Base(localPath) != name {
		return Spec{}, false, permanent(fmt.Errorf("project setup: new repository path must be %s", filepath.Join(p.managedRoot, name)))
	}

	return Spec{
		ProjectID:   request.ProjectID,
		ProjectName: request.ProjectName,
		Repository:  repository,
		RepoURL:     "git@github.com:" + repository + ".git",
		LocalPath:   localPath,
		ManagedRoot: managedRoot,
		CloudURL:    cloudURL,
		BaseBranch:  "main",
		Bootstrap:   managed,
		Managed:     managed,
	}, true, nil
}

func (p *Parser) Validate(spec Spec) error {
	description := "GitHub Repo: " + spec.Repository + "\nLocal Path: " + spec.LocalPath
	if spec.CloudURL != "" {
		description += "\nCloud URL: " + spec.CloudURL
	}
	parsed, complete, err := p.Parse(Request{
		ProjectID: spec.ProjectID, ProjectName: spec.ProjectName, Description: description,
	})
	if err != nil {
		return err
	}
	if !complete || parsed != spec {
		return permanent(errors.New("project setup: persisted repository metadata does not match current Factory policy"))
	}
	return nil
}

func uniqueLine(pattern *regexp.Regexp, description, label string) (string, bool, error) {
	matches := pattern.FindAllStringSubmatch(description, -1)
	if len(matches) == 0 {
		return "", false, nil
	}
	if len(matches) != 1 {
		return "", false, fmt.Errorf("project setup: Linear project description must declare %s at most once", label)
	}
	return strings.TrimSpace(matches[0][1]), true, nil
}

func normalizeRepository(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if validRepository(value) {
		return value, true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	repository := strings.TrimSuffix(strings.Trim(strings.TrimSpace(parsed.Path), "/"), ".git")
	return repository, validRepository(repository)
}

func validRepository(value string) bool {
	owner, name, found := strings.Cut(value, "/")
	return found && !strings.Contains(name, "/") && repositoryName.MatchString(owner) && repositoryName.MatchString(name)
}

func normalizeCloudURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("project setup: Cloud URL must be an HTTPS nags.cloud URL")
	}
	host := strings.ToLower(parsed.Hostname())
	label := strings.TrimSuffix(host, ".nags.cloud")
	if label == host || !cloudHostnameLabel.MatchString(label) {
		return "", errors.New("project setup: Cloud URL must use one <app>.nags.cloud hostname")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("project setup: Cloud URL must not contain a path")
	}
	return "https://" + host, nil
}

type permanentError struct{ error }

func (permanentError) Permanent() bool { return true }

func (e permanentError) Unwrap() error { return e.error }

func permanent(err error) error { return permanentError{error: err} }
