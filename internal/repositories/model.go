package repositories

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	appPattern        = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	projectIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	branchPattern     = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	cloudLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
)

type SetupState string

const (
	SetupStateCompiled         SetupState = "compiled"
	SetupStateAwaitingMetadata SetupState = "awaiting_metadata"
	SetupStatePending          SetupState = "pending"
	SetupStateRunning          SetupState = "running"
	SetupStateSucceeded        SetupState = "succeeded"
	SetupStateFailed           SetupState = "failed"
)

type Provenance string

const (
	ProvenanceCompiled Provenance = "compiled"
	ProvenanceProject  Provenance = "project"
)

type ProjectIdentity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (p ProjectIdentity) IsZero() bool {
	return p.ID == "" && p.Name == ""
}

func (p ProjectIdentity) validate() error {
	if !projectIDPattern.MatchString(p.ID) || !validText(p.Name, 256) {
		return errors.New("repository record: canonical project identity is required")
	}
	return nil
}

type Setup struct {
	State               SetupState `json:"state"`
	Attempts            int        `json:"attempts"`
	LastError           string     `json:"lastError,omitempty"`
	NextAttemptAt       *time.Time `json:"nextAttemptAt,omitempty"`
	CreatedAt           time.Time  `json:"createdAt,omitempty"`
	UpdatedAt           time.Time  `json:"updatedAt,omitempty"`
	ProvisionedAt       *time.Time `json:"provisionedAt,omitempty"`
	ProviderCoordinated bool       `json:"providerCoordinated"`
}

func (s Setup) validate(hasProject bool) error {
	if s.State == SetupStateCompiled {
		if hasProject || s.Attempts != 0 || s.LastError != "" || s.NextAttemptAt != nil ||
			!s.CreatedAt.IsZero() || !s.UpdatedAt.IsZero() || s.ProvisionedAt != nil || s.ProviderCoordinated {
			return errors.New("repository record: compiled setup state must not carry admitted project state")
		}
		return nil
	}
	if !hasProject {
		return errors.New("repository record: admitted setup state requires project identity")
	}
	switch s.State {
	case SetupStatePending, SetupStateRunning, SetupStateSucceeded, SetupStateFailed:
	default:
		return fmt.Errorf("repository record: invalid setup state %q", s.State)
	}
	if s.Attempts < 0 || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return errors.New("repository record: invalid setup lifecycle")
	}
	if !validOptionalText(s.LastError, 2048) {
		return errors.New("repository record: invalid setup error")
	}
	if s.NextAttemptAt != nil && s.NextAttemptAt.IsZero() || s.ProvisionedAt != nil && s.ProvisionedAt.IsZero() {
		return errors.New("repository record: invalid setup timestamp")
	}
	switch s.State {
	case SetupStatePending:
		if s.LastError != "" || s.NextAttemptAt != nil {
			return errors.New("repository record: pending setup cannot carry an error or retry")
		}
	case SetupStateRunning:
		if s.Attempts == 0 || s.LastError != "" || s.NextAttemptAt != nil {
			return errors.New("repository record: running setup requires an attempt without an error or retry")
		}
	case SetupStateSucceeded:
		if s.LastError != "" || s.NextAttemptAt != nil || s.ProvisionedAt == nil {
			return errors.New("repository record: succeeded setup requires provisioning without an error or retry")
		}
	case SetupStateFailed:
		if s.NextAttemptAt == nil {
			return errors.New("repository record: failed setup requires a retry")
		}
	}
	return nil
}

func (s Setup) validateAwaiting() error {
	if s.State != SetupStateAwaitingMetadata || s.Attempts != 0 || s.LastError != "" ||
		s.NextAttemptAt != nil || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() ||
		s.UpdatedAt.Before(s.CreatedAt) || s.ProvisionedAt != nil || s.ProviderCoordinated {
		return errors.New("repository record: invalid awaiting-metadata lifecycle")
	}
	return nil
}

func (s Setup) Routable() bool {
	return s.State == SetupStateSucceeded && s.ProviderCoordinated
}

type Deployment struct {
	ReceiptPath        string `json:"receiptPath,omitempty"`
	PendingReceiptPath string `json:"pendingReceiptPath,omitempty"`
	HealthURL          string `json:"healthUrl,omitempty"`
	SourcePath         string `json:"sourcePath,omitempty"`
}

func (d Deployment) Required() bool {
	return d.ReceiptPath != ""
}

func (d Deployment) validate() error {
	configured := 0
	for _, value := range []string{d.ReceiptPath, d.PendingReceiptPath, d.HealthURL} {
		if value != "" {
			configured++
		}
	}
	if configured != 0 && configured != 3 {
		return errors.New("repository record: deployment receipt and health locations must be configured together")
	}
	if configured == 3 {
		if !canonicalAbsolutePath(d.ReceiptPath) || !canonicalAbsolutePath(d.PendingReceiptPath) || d.ReceiptPath == d.PendingReceiptPath {
			return errors.New("repository record: deployment receipt paths must be distinct canonical absolute paths")
		}
		if err := validateHTTPURL(d.HealthURL); err != nil {
			return fmt.Errorf("repository record: invalid deployment health URL: %w", err)
		}
	}
	if d.SourcePath != "" {
		clean := filepath.Clean(d.SourcePath)
		if !utf8.ValidString(d.SourcePath) || filepath.IsAbs(d.SourcePath) || clean == "." || clean != d.SourcePath || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return errors.New("repository record: source path must be canonical and stay within the repository")
		}
	}
	return nil
}

type Record struct {
	App           string          `json:"app"`
	Provenance    Provenance      `json:"provenance"`
	Project       ProjectIdentity `json:"project,omitempty"`
	Repository    string          `json:"repository"`
	Origin        string          `json:"origin"`
	LocalPath     string          `json:"localPath"`
	ManagedPath   string          `json:"managedPath"`
	ManagedRoot   string          `json:"managedRoot"`
	DefaultBranch string          `json:"defaultBranch"`
	Bootstrap     bool            `json:"bootstrap"`
	CloudURL      string          `json:"cloudUrl,omitempty"`
	Deployment    Deployment      `json:"deployment"`
	Setup         Setup           `json:"setup"`
}

func (r Record) validate() error {
	if !appPattern.MatchString(r.App) {
		return errors.New("repository record: canonical app identity is required")
	}
	if r.Provenance != ProvenanceCompiled && r.Provenance != ProvenanceProject {
		return errors.New("repository record: source provenance is required")
	}
	repository, origin, err := normalizeOrigin(r.Origin)
	if err != nil || repository != r.Repository || origin != r.Origin {
		return errors.New("repository record: normalized GitHub origin must match repository identity")
	}
	if !canonicalAbsolutePath(r.LocalPath) || !canonicalAbsolutePath(r.ManagedPath) || !canonicalAbsolutePath(r.ManagedRoot) {
		return errors.New("repository record: local and managed paths must be canonical absolute paths")
	}
	if r.ManagedPath == r.ManagedRoot || !pathWithin(r.ManagedRoot, r.ManagedPath) {
		return errors.New("repository record: managed path must stay below its managed root")
	}
	if !validBranch(r.DefaultBranch) {
		return errors.New("repository record: default branch is invalid")
	}
	if r.CloudURL != "" {
		cloudURL, err := normalizeCloudURL(r.CloudURL)
		if err != nil || cloudURL != r.CloudURL {
			return errors.New("repository record: Cloud URL must be a canonical HTTPS <app>.nags.cloud URL")
		}
	}
	if err := r.Deployment.validate(); err != nil {
		return err
	}
	hasProject := !r.Project.IsZero()
	if hasProject {
		if err := r.Project.validate(); err != nil {
			return err
		}
	}
	if r.Provenance == ProvenanceProject && !hasProject {
		return errors.New("repository record: project source provenance requires project identity")
	}
	return r.Setup.validate(hasProject)
}

func (r Record) Routable() bool {
	return !r.Project.IsZero() && r.Setup.Routable()
}

type Route struct {
	ProjectID     string `json:"projectId"`
	Repository    string `json:"repository"`
	Origin        string `json:"origin"`
	ManagedPath   string `json:"managedPath"`
	ManagedRoot   string `json:"managedRoot"`
	DefaultBranch string `json:"defaultBranch"`
	Bootstrap     bool   `json:"bootstrap"`
	CloudURL      string `json:"cloudUrl,omitempty"`
}

func (r Record) Route() Route {
	return Route{
		ProjectID: r.Project.ID, Repository: r.Repository, Origin: r.Origin,
		ManagedPath: r.ManagedPath, ManagedRoot: r.ManagedRoot,
		DefaultBranch: r.DefaultBranch, Bootstrap: r.Bootstrap, CloudURL: r.CloudURL,
	}
}

type LaunchConfig struct {
	Repository    string `json:"repository"`
	Origin        string `json:"origin"`
	Path          string `json:"path"`
	ManagedRoot   string `json:"managedRoot"`
	DefaultBranch string `json:"defaultBranch"`
	Bootstrap     bool   `json:"bootstrap"`
	CloudURL      string `json:"cloudUrl,omitempty"`
}

func (r Record) LaunchConfig() LaunchConfig {
	return LaunchConfig{
		Repository: r.Repository, Origin: r.Origin, Path: r.ManagedPath,
		ManagedRoot: r.ManagedRoot, DefaultBranch: r.DefaultBranch,
		Bootstrap: r.Bootstrap, CloudURL: r.CloudURL,
	}
}

type CompletionIdentity struct {
	App           string     `json:"app"`
	Repository    string     `json:"repository"`
	RemoteURLs    []string   `json:"remoteUrls"`
	Path          string     `json:"path"`
	DefaultBranch string     `json:"defaultBranch"`
	Deployment    Deployment `json:"deployment"`
}

func (r Record) CompletionIdentity() CompletionIdentity {
	return CompletionIdentity{
		App: r.App, Repository: r.Repository, RemoteURLs: remoteURLs(r.Repository),
		Path: r.ManagedPath, DefaultBranch: r.DefaultBranch, Deployment: r.Deployment,
	}
}

func (i CompletionIdentity) Clone() CompletionIdentity {
	i.RemoteURLs = slices.Clone(i.RemoteURLs)
	return i
}

// Validate proves the identity has the exact normalized shape derived from an
// allowlisted repository Record. Consumers can therefore reject reconstructed
// or broadened remote authority before reading completion evidence.
func (i CompletionIdentity) Validate() error {
	if !appPattern.MatchString(i.App) || !repositoryPattern.MatchString(i.Repository) || i.Repository != strings.ToLower(i.Repository) ||
		!canonicalAbsolutePath(i.Path) || !validBranch(i.DefaultBranch) || !slices.Equal(i.RemoteURLs, remoteURLs(i.Repository)) {
		return errors.New("repository completion identity is not canonical")
	}
	if err := i.Deployment.validate(); err != nil {
		return fmt.Errorf("repository completion identity: %w", err)
	}
	return nil
}

func normalizeOrigin(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	repository := ""
	if slug, found := strings.CutPrefix(value, "git@github.com:"); found {
		repository = strings.TrimSuffix(slug, ".git")
	} else {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", errors.New("GitHub origin must be canonical SSH or HTTPS")
		}
		repository = strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	}
	if !repositoryPattern.MatchString(repository) {
		return "", "", errors.New("GitHub origin must identify owner/name")
	}
	repository = strings.ToLower(repository)
	return repository, "git@github.com:" + repository + ".git", nil
}

func normalizeProjectRepository(value string) (string, error) {
	value = strings.TrimSpace(value)
	if repositoryPattern.MatchString(value) {
		return strings.ToLower(value), nil
	}
	repository, _, err := normalizeOrigin(value)
	return repository, err
}

func normalizeCloudURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Cloud URL must be HTTPS")
	}
	host := strings.ToLower(parsed.Hostname())
	label := strings.TrimSuffix(host, ".nags.cloud")
	if label == host || !cloudLabelPattern.MatchString(label) || parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("Cloud URL must use one <app>.nags.cloud hostname")
	}
	return "https://" + host, nil
}

func validateHTTPURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	return nil
}

func remoteURLs(repository string) []string {
	return []string{
		"git@github.com:" + repository + ".git",
		"https://github.com/" + repository,
		"https://github.com/" + repository + ".git",
	}
}

func validBranch(value string) bool {
	return value != "" && len(value) <= 255 && branchPattern.MatchString(value) &&
		!strings.HasPrefix(value, "/") && !strings.HasSuffix(value, "/") &&
		!strings.Contains(value, "..") && !strings.Contains(value, "//")
}

func canonicalAbsolutePath(value string) bool {
	return utf8.ValidString(value) && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathsOverlap(left, right string) bool {
	return pathWithin(left, right) || pathWithin(right, left)
}

func validText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && value == strings.TrimSpace(value) && len(value) <= maximum
}

func validOptionalText(value string, maximum int) bool {
	return value == "" || validText(value, maximum)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}
