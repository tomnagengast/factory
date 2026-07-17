package repositories

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// CompiledSource is the caller-neutral shape of Factory's compiled repository
// configuration. It intentionally lives here so the canonical owner never
// imports the packages it will replace.
type CompiledSource struct {
	App            string
	Repository     string
	RepoURL        string
	RepoPath       string
	ManagedRoot    string
	ProjectPath    string
	BaseBranch     string
	Bootstrap      bool
	CloudURL       string
	ReceiptPath    string
	PendingReceipt string
	HealthURL      string
	SourcePath     string
}

// SetupSource is the caller-neutral persisted project-setup shape.
type SetupSource struct {
	ProjectID           string
	ProjectName         string
	Repository          string
	RepoURL             string
	LocalPath           string
	ManagedRoot         string
	CloudURL            string
	BaseBranch          string
	Bootstrap           bool
	Managed             bool
	State               SetupState
	Attempts            int
	LastError           string
	NextAttemptAt       *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ProvisionedAt       *time.Time
	ProviderCoordinated bool
}

type AwaitingProject struct {
	Project ProjectIdentity `json:"project"`
	Setup   Setup           `json:"setup"`
}

type SourceState struct {
	Records  []Record          `json:"records"`
	Awaiting []AwaitingProject `json:"awaiting,omitempty"`
}

func (s SourceState) Clone() SourceState {
	clone := SourceState{Records: slices.Clone(s.Records), Awaiting: slices.Clone(s.Awaiting)}
	for index := range clone.Records {
		clone.Records[index].Setup = cloneSetup(clone.Records[index].Setup)
	}
	for index := range clone.Awaiting {
		clone.Awaiting[index].Setup = cloneSetup(clone.Awaiting[index].Setup)
	}
	return clone
}

func ConvertSources(compiled []CompiledSource, setups []SetupSource) (SourceState, error) {
	state := SourceState{Records: make([]Record, 0, len(compiled)+len(setups))}
	compiledByRepository := make(map[string]int, len(compiled))
	for _, source := range compiled {
		record, err := convertCompiled(source)
		if err != nil {
			return SourceState{}, err
		}
		if _, found := compiledByRepository[record.Repository]; found {
			return SourceState{}, fmt.Errorf("repository sources: duplicate compiled repository %s", record.Repository)
		}
		compiledByRepository[record.Repository] = len(state.Records)
		state.Records = append(state.Records, record)
	}

	seenProjects := make(map[string]string, len(setups))
	seenRepositories := make(map[string]string, len(setups))
	for _, source := range setups {
		project, setup, err := convertSetupIdentity(source)
		if err != nil {
			return SourceState{}, err
		}
		if previous, found := seenProjects[project.ID]; found {
			return SourceState{}, fmt.Errorf("repository sources: project %s conflicts with %s", project.ID, previous)
		}
		seenProjects[project.ID] = project.Name
		if source.State == SetupStateAwaitingMetadata {
			if source.Repository != "" || source.RepoURL != "" || source.LocalPath != "" || source.ManagedRoot != "" ||
				source.CloudURL != "" || source.BaseBranch != "" || source.Bootstrap || source.Managed {
				return SourceState{}, fmt.Errorf("repository sources: awaiting project %s carries repository metadata", project.ID)
			}
			state.Awaiting = append(state.Awaiting, AwaitingProject{Project: project, Setup: setup})
			continue
		}

		repository, origin, err := normalizeOrigin(source.RepoURL)
		if err != nil {
			return SourceState{}, fmt.Errorf("repository sources: project %s origin: %w", project.ID, err)
		}
		declaredRepository, err := normalizeProjectRepository(source.Repository)
		if err != nil || declaredRepository != repository || source.Repository != repository || source.RepoURL != origin {
			return SourceState{}, fmt.Errorf("repository sources: project %s repository conflicts with origin", project.ID)
		}
		if previous, found := seenRepositories[repository]; found {
			return SourceState{}, fmt.Errorf("repository sources: repository %s is assigned to projects %s and %s", repository, previous, project.ID)
		}
		seenRepositories[repository] = project.ID

		localPath := filepath.Clean(source.LocalPath)
		managedRoot := filepath.Clean(source.ManagedRoot)
		if source.LocalPath != localPath || source.ManagedRoot != managedRoot || !canonicalAbsolutePath(localPath) ||
			!canonicalAbsolutePath(managedRoot) || source.Bootstrap != source.Managed {
			return SourceState{}, fmt.Errorf("repository sources: project %s has invalid path or management policy", project.ID)
		}
		cloudURL := ""
		if source.CloudURL != "" {
			cloudURL, err = normalizeCloudURL(source.CloudURL)
			if err != nil {
				return SourceState{}, fmt.Errorf("repository sources: project %s Cloud URL: %w", project.ID, err)
			}
			if source.CloudURL != cloudURL {
				return SourceState{}, fmt.Errorf("repository sources: project %s Cloud URL is not canonical", project.ID)
			}
		}

		if index, found := compiledByRepository[repository]; found {
			if source.Managed {
				return SourceState{}, fmt.Errorf("repository sources: compiled repository %s cannot be managed by project setup", repository)
			}
			record := state.Records[index]
			if localPath != record.LocalPath || managedRoot != filepath.Dir(record.LocalPath) || origin != record.Origin ||
				source.BaseBranch != record.DefaultBranch || source.Bootstrap {
				return SourceState{}, fmt.Errorf("repository sources: project %s conflicts with compiled repository %s", project.ID, repository)
			}
			if record.CloudURL != "" && cloudURL != "" && record.CloudURL != cloudURL {
				return SourceState{}, fmt.Errorf("repository sources: project %s Cloud URL conflicts with compiled repository %s", project.ID, repository)
			}
			if cloudURL != "" {
				record.CloudURL = cloudURL
			}
			record.Project = project
			record.Setup = setup
			if err := record.validate(); err != nil {
				return SourceState{}, fmt.Errorf("repository sources: project %s: %w", project.ID, err)
			}
			state.Records[index] = record
			continue
		}

		if !source.Managed {
			return SourceState{}, fmt.Errorf("repository sources: existing repository %s is no longer compiled", repository)
		}
		_, app, _ := strings.Cut(repository, "/")
		record := Record{
			App: strings.ToLower(app), Provenance: ProvenanceProject, Project: project, Repository: repository, Origin: origin,
			LocalPath: localPath, ManagedPath: localPath, ManagedRoot: managedRoot,
			DefaultBranch: source.BaseBranch, Bootstrap: source.Bootstrap, CloudURL: cloudURL,
			Setup: setup,
		}
		if err := record.validate(); err != nil {
			return SourceState{}, fmt.Errorf("repository sources: project %s: %w", project.ID, err)
		}
		state.Records = append(state.Records, record)
	}

	if err := validateSourceState(state); err != nil {
		return SourceState{}, err
	}
	return canonicalSourceOrder(state), nil
}

func convertCompiled(source CompiledSource) (Record, error) {
	repository, origin, err := normalizeOrigin(source.RepoURL)
	if err != nil {
		return Record{}, fmt.Errorf("repository sources: compiled origin: %w", err)
	}
	declaredRepository, err := normalizeProjectRepository(source.Repository)
	if err != nil || declaredRepository != repository {
		return Record{}, errors.New("repository sources: compiled repository conflicts with origin")
	}
	cloudURL := ""
	if source.CloudURL != "" {
		cloudURL, err = normalizeCloudURL(source.CloudURL)
		if err != nil {
			return Record{}, fmt.Errorf("repository sources: compiled Cloud URL: %w", err)
		}
		if source.CloudURL != cloudURL {
			return Record{}, errors.New("repository sources: compiled Cloud URL is not canonical")
		}
	}
	if source.RepoPath != filepath.Clean(source.RepoPath) || source.ManagedRoot != filepath.Clean(source.ManagedRoot) ||
		source.ProjectPath != "" && source.ProjectPath != filepath.Clean(source.ProjectPath) {
		return Record{}, errors.New("repository sources: compiled paths must be canonical")
	}
	record := Record{
		App: strings.ToLower(strings.TrimSpace(source.App)), Provenance: ProvenanceCompiled,
		Repository: repository, Origin: origin,
		LocalPath: filepath.Clean(source.ProjectPath), ManagedPath: filepath.Clean(source.RepoPath),
		ManagedRoot: filepath.Clean(source.ManagedRoot), DefaultBranch: source.BaseBranch,
		Bootstrap: source.Bootstrap, CloudURL: cloudURL,
		Deployment: Deployment{
			ReceiptPath: source.ReceiptPath, PendingReceiptPath: source.PendingReceipt,
			HealthURL: source.HealthURL, SourcePath: source.SourcePath,
		},
		Setup: Setup{State: SetupStateCompiled},
	}
	if record.LocalPath == "." {
		record.LocalPath = record.ManagedPath
	}
	if err := record.validate(); err != nil {
		return Record{}, fmt.Errorf("repository sources: compiled %s: %w", repository, err)
	}
	return record, nil
}

func convertSetupIdentity(source SetupSource) (ProjectIdentity, Setup, error) {
	project := ProjectIdentity{ID: strings.TrimSpace(source.ProjectID), Name: strings.TrimSpace(source.ProjectName)}
	if source.ProjectID != project.ID || source.ProjectName != project.Name || source.LastError != strings.TrimSpace(source.LastError) {
		return ProjectIdentity{}, Setup{}, errors.New("repository sources: project identity and setup error must be canonical")
	}
	if err := project.validate(); err != nil {
		return ProjectIdentity{}, Setup{}, fmt.Errorf("repository sources: %w", err)
	}
	setup := Setup{
		State: source.State, Attempts: source.Attempts, LastError: strings.TrimSpace(source.LastError),
		NextAttemptAt: cloneTime(source.NextAttemptAt), CreatedAt: source.CreatedAt.UTC(),
		UpdatedAt: source.UpdatedAt.UTC(), ProvisionedAt: cloneTime(source.ProvisionedAt),
		ProviderCoordinated: source.ProviderCoordinated,
	}
	if source.State == SetupStateAwaitingMetadata {
		if err := setup.validateAwaiting(); err != nil {
			return ProjectIdentity{}, Setup{}, fmt.Errorf("repository sources: project %s has invalid awaiting lifecycle", project.ID)
		}
		return project, setup, nil
	}
	if err := setup.validate(true); err != nil {
		return ProjectIdentity{}, Setup{}, fmt.Errorf("repository sources: project %s: %w", project.ID, err)
	}
	return project, setup, nil
}

func validateSourceState(state SourceState) error {
	if len(state.Records) == 0 {
		return errors.New("repository catalog: at least one repository is required")
	}
	projects := make(map[string]string, len(state.Records)+len(state.Awaiting))
	for index, record := range state.Records {
		if err := record.validate(); err != nil {
			return err
		}
		if !record.Project.IsZero() {
			if previous, found := projects[record.Project.ID]; found {
				return fmt.Errorf("repository catalog: project %s is assigned to %s and %s", record.Project.ID, previous, record.Repository)
			}
			projects[record.Project.ID] = record.Repository
		}
		for otherIndex := 0; otherIndex < index; otherIndex++ {
			other := state.Records[otherIndex]
			if record.Repository == other.Repository {
				return fmt.Errorf("repository catalog: duplicate repository %s", record.Repository)
			}
			if record.App == other.App {
				return fmt.Errorf("repository catalog: repositories %s and %s share app %s", other.Repository, record.Repository, record.App)
			}
			if pathsOverlap(record.ManagedPath, other.ManagedPath) {
				return fmt.Errorf("repository catalog: managed paths for %s and %s overlap", other.Repository, record.Repository)
			}
			if pathsOverlap(record.LocalPath, other.LocalPath) {
				return fmt.Errorf("repository catalog: local paths for %s and %s overlap", other.Repository, record.Repository)
			}
		}
	}
	for _, awaiting := range state.Awaiting {
		if err := awaiting.Project.validate(); err != nil {
			return err
		}
		if awaiting.Setup.State != SetupStateAwaitingMetadata {
			return errors.New("repository catalog: incomplete project must await metadata")
		}
		if err := awaiting.Setup.validateAwaiting(); err != nil {
			return err
		}
		if previous, found := projects[awaiting.Project.ID]; found {
			return fmt.Errorf("repository catalog: awaiting project %s conflicts with %s", awaiting.Project.ID, previous)
		}
		projects[awaiting.Project.ID] = "awaiting metadata"
	}
	return nil
}

func canonicalSourceOrder(state SourceState) SourceState {
	slices.SortFunc(state.Records, func(left, right Record) int {
		return strings.Compare(left.Repository, right.Repository)
	})
	slices.SortFunc(state.Awaiting, func(left, right AwaitingProject) int {
		return strings.Compare(left.Project.ID, right.Project.ID)
	})
	return state
}

func cloneSetup(setup Setup) Setup {
	setup.NextAttemptAt = cloneTime(setup.NextAttemptAt)
	setup.ProvisionedAt = cloneTime(setup.ProvisionedAt)
	return setup
}

func cloneRecord(record Record) Record {
	record.Setup = cloneSetup(record.Setup)
	return record
}

type RouteSource struct {
	ProjectID      string
	Repository     string
	RepositoryURL  string
	RepositoryPath string
	ManagedRoot    string
	BaseBranch     string
	Bootstrap      bool
	CloudURL       string
}

func ConvertRouteSource(source RouteSource) (Route, error) {
	repository, origin, err := normalizeOrigin(source.RepositoryURL)
	if err != nil {
		return Route{}, fmt.Errorf("repository route: origin: %w", err)
	}
	declaredRepository, err := normalizeProjectRepository(source.Repository)
	if err != nil || declaredRepository != repository || source.Repository != repository || source.RepositoryURL != origin {
		return Route{}, errors.New("repository route: repository conflicts with origin")
	}
	cloudURL := ""
	if source.CloudURL != "" {
		cloudURL, err = normalizeCloudURL(source.CloudURL)
		if err != nil {
			return Route{}, fmt.Errorf("repository route: Cloud URL: %w", err)
		}
		if source.CloudURL != cloudURL {
			return Route{}, errors.New("repository route: Cloud URL is not canonical")
		}
	}
	if source.RepositoryPath != filepath.Clean(source.RepositoryPath) || source.ManagedRoot != filepath.Clean(source.ManagedRoot) {
		return Route{}, errors.New("repository route: paths must already be canonical")
	}
	route := Route{
		ProjectID: strings.TrimSpace(source.ProjectID), Repository: repository, Origin: origin,
		ManagedPath: filepath.Clean(source.RepositoryPath), ManagedRoot: filepath.Clean(source.ManagedRoot),
		DefaultBranch: source.BaseBranch, Bootstrap: source.Bootstrap, CloudURL: cloudURL,
	}
	if source.ProjectID != route.ProjectID || !projectIDPattern.MatchString(route.ProjectID) || !canonicalAbsolutePath(route.ManagedPath) ||
		!canonicalAbsolutePath(route.ManagedRoot) || route.ManagedPath == route.ManagedRoot ||
		!pathWithin(route.ManagedRoot, route.ManagedPath) || !validBranch(route.DefaultBranch) {
		return Route{}, errors.New("repository route: identity, paths, or branch are invalid")
	}
	return route, nil
}
