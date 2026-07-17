package repositories

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/tomnagengast/factory/internal/taskmodel"
)

type ProjectMetadata struct {
	ProjectID   string
	ProjectName string
	Repository  string
	LocalPath   string
}

type TaskLookup struct {
	Ref       taskmodel.TaskRef
	Project   *ProjectMetadata
	ProjectID string
	Route     *Route
}

type Choice struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Repository  string `json:"repository"`
}

type SetupSnapshot struct {
	Total            int `json:"total"`
	AwaitingMetadata int `json:"awaitingMetadata"`
	Pending          int `json:"pending"`
	Running          int `json:"running"`
	Succeeded        int `json:"succeeded"`
	Failed           int `json:"failed"`
}

type catalogIndex struct {
	state            SourceState
	byRepository     map[string]Record
	byProject        map[string]Record
	compiledBaseline map[string]Record
}

type Catalog struct {
	mu    sync.RWMutex
	index catalogIndex
}

func NewCatalog(state SourceState) (*Catalog, error) {
	index, err := buildIndex(state)
	if err != nil {
		return nil, err
	}
	return &Catalog{index: index}, nil
}

func (c *Catalog) Replace(state SourceState) error {
	index, err := buildIndex(state)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateReplacement(c.index, index); err != nil {
		return err
	}
	c.index = index
	return nil
}

func validateReplacement(current, candidate catalogIndex) error {
	for repository, baseline := range current.compiledBaseline {
		replacement, found := candidate.byRepository[repository]
		if !found {
			return permanent(fmt.Errorf("repository catalog: compiled repository %s cannot be removed", repository))
		}
		if !sameCompiledIdentity(baseline, replacement) {
			return permanent(fmt.Errorf("repository catalog: compiled repository %s identity is immutable", repository))
		}
		if baseline.Project.IsZero() && replacement.Project.IsZero() && baseline.CloudURL != replacement.CloudURL {
			return permanent(fmt.Errorf("repository catalog: compiled repository %s Cloud URL requires a project overlay", repository))
		}
	}
	for repository := range candidate.compiledBaseline {
		if _, found := current.compiledBaseline[repository]; !found {
			return permanent(fmt.Errorf("repository catalog: compiled repository %s is not in the immutable baseline", repository))
		}
	}
	for _, record := range current.state.Records {
		if record.Project.IsZero() {
			continue
		}
		replacement, found := candidate.byProject[record.Project.ID]
		if !found {
			return permanent(fmt.Errorf("repository catalog: admitted project %s cannot be removed", record.Project.ID))
		}
		if !sameAdmittedIdentity(record, replacement) {
			return permanent(fmt.Errorf("repository catalog: admitted project %s repository identity is immutable", record.Project.ID))
		}
		if record.CloudURL != replacement.CloudURL && replacement.Setup.ProviderCoordinated {
			return permanent(fmt.Errorf("repository catalog: project %s Cloud URL change must clear provider coordination", record.Project.ID))
		}
	}
	for _, awaiting := range current.state.Awaiting {
		if _, found := candidate.byProject[awaiting.Project.ID]; found {
			continue
		}
		found := false
		for _, replacement := range candidate.state.Awaiting {
			if replacement.Project.ID == awaiting.Project.ID {
				found = true
				break
			}
		}
		if !found {
			return permanent(fmt.Errorf("repository catalog: admitted project %s cannot be removed", awaiting.Project.ID))
		}
	}
	return nil
}

func sameCompiledIdentity(current, candidate Record) bool {
	return current.Provenance == ProvenanceCompiled && candidate.Provenance == ProvenanceCompiled &&
		current.App == candidate.App &&
		current.Repository == candidate.Repository &&
		current.Origin == candidate.Origin &&
		current.LocalPath == candidate.LocalPath &&
		current.ManagedPath == candidate.ManagedPath &&
		current.ManagedRoot == candidate.ManagedRoot &&
		current.DefaultBranch == candidate.DefaultBranch &&
		current.Bootstrap == candidate.Bootstrap &&
		current.Deployment == candidate.Deployment
}

func sameAdmittedIdentity(current, candidate Record) bool {
	return current.Project.ID == candidate.Project.ID &&
		current.Provenance == candidate.Provenance &&
		current.App == candidate.App &&
		current.Repository == candidate.Repository &&
		current.Origin == candidate.Origin &&
		current.LocalPath == candidate.LocalPath &&
		current.ManagedPath == candidate.ManagedPath &&
		current.ManagedRoot == candidate.ManagedRoot &&
		current.DefaultBranch == candidate.DefaultBranch &&
		current.Bootstrap == candidate.Bootstrap &&
		current.Deployment == candidate.Deployment
}

func buildIndex(state SourceState) (catalogIndex, error) {
	state = state.Clone()
	if err := validateSourceState(state); err != nil {
		return catalogIndex{}, err
	}
	state = canonicalSourceOrder(state)
	index := catalogIndex{
		state: state, byRepository: make(map[string]Record, len(state.Records)),
		byProject:        make(map[string]Record, len(state.Records)),
		compiledBaseline: make(map[string]Record, len(state.Records)),
	}
	for _, record := range state.Records {
		index.byRepository[record.Repository] = record
		if record.Provenance == ProvenanceCompiled {
			index.compiledBaseline[record.Repository] = record
		}
		if !record.Project.IsZero() {
			index.byProject[record.Project.ID] = record
		}
	}
	return index, nil
}

func (c *Catalog) Snapshot() SourceState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.index.state.Clone()
}

func (c *Catalog) ResolveProject(metadata ProjectMetadata) (Record, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return resolveProject(c.index, metadata)
}

// ResolveProjectID atomically resolves the current route-bearing record for an
// initial Factory task admission, before a pinned Route exists.
func (c *Catalog) ResolveProjectID(projectID string) (Record, error) {
	canonical := strings.TrimSpace(projectID)
	if projectID != canonical || !projectIDPattern.MatchString(canonical) {
		return Record{}, permanent(errors.New("repository catalog: canonical project identity is required"))
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	record, found := c.index.byProject[canonical]
	if !found || !record.Routable() {
		return Record{}, permanent(fmt.Errorf("repository catalog: project %s is not successfully admitted", canonical))
	}
	return cloneRecord(record), nil
}

func resolveProject(index catalogIndex, metadata ProjectMetadata) (Record, error) {
	projectID := strings.TrimSpace(metadata.ProjectID)
	projectName := strings.TrimSpace(metadata.ProjectName)
	repository, err := normalizeProjectRepository(metadata.Repository)
	if err != nil || !projectIDPattern.MatchString(projectID) || !validText(projectName, 256) || !filepath.IsAbs(metadata.LocalPath) {
		return Record{}, permanent(errors.New("repository catalog: complete canonical project metadata is required"))
	}
	localPath := filepath.Clean(metadata.LocalPath)
	record, found := index.byProject[projectID]
	if !found {
		return Record{}, permanent(fmt.Errorf("repository catalog: project %s is not admitted", projectID))
	}
	if !record.Routable() {
		return Record{}, permanent(fmt.Errorf("repository catalog: project %s is not successfully coordinated", projectID))
	}
	if record.Project.Name != projectName || record.Repository != repository || record.LocalPath != localPath {
		return Record{}, permanent(fmt.Errorf("repository catalog: project %s metadata conflicts with its admitted repository", projectID))
	}
	return cloneRecord(record), nil
}

func (c *Catalog) ResolveTask(lookup TaskLookup) (Record, error) {
	if err := lookup.Ref.Validate(); err != nil {
		return Record{}, permanent(fmt.Errorf("repository catalog: task reference is not canonical: %w", err))
	}
	switch lookup.Ref.Source {
	case taskmodel.SourceLinear:
		if lookup.Project == nil || lookup.ProjectID != "" || lookup.Route != nil {
			return Record{}, permanent(errors.New("repository catalog: Linear task lookup requires exact project metadata"))
		}
		c.mu.RLock()
		record, err := resolveProject(c.index, *lookup.Project)
		c.mu.RUnlock()
		return record, err
	case taskmodel.SourceFactory:
		projectID := strings.TrimSpace(lookup.ProjectID)
		if lookup.Project != nil || !projectIDPattern.MatchString(projectID) {
			return Record{}, permanent(errors.New("repository catalog: Factory task project identity is required"))
		}
		c.mu.RLock()
		record, found := c.index.byProject[projectID]
		c.mu.RUnlock()
		if !found || !record.Routable() {
			return Record{}, permanent(fmt.Errorf("repository catalog: task project %s is not successfully admitted", projectID))
		}
		if lookup.Route == nil || *lookup.Route != record.Route() {
			return Record{}, permanent(errors.New("repository catalog: Factory task route no longer matches admitted repository metadata"))
		}
		return cloneRecord(record), nil
	default:
		return Record{}, permanent(errors.New("repository catalog: task provider is unsupported"))
	}
}

func (c *Catalog) Record(repository string) (Record, bool) {
	repository, err := normalizeProjectRepository(repository)
	if err != nil {
		return Record{}, false
	}
	c.mu.RLock()
	record, found := c.index.byRepository[repository]
	c.mu.RUnlock()
	return cloneRecord(record), found
}

func (c *Catalog) Choices() []Choice {
	c.mu.RLock()
	defer c.mu.RUnlock()
	choices := make([]Choice, 0, len(c.index.byProject))
	for _, record := range c.index.state.Records {
		if !record.Routable() {
			continue
		}
		choices = append(choices, Choice{
			ProjectID: record.Project.ID, ProjectName: record.Project.Name, Repository: record.Repository,
		})
	}
	sort.Slice(choices, func(left, right int) bool {
		if choices[left].ProjectName != choices[right].ProjectName {
			return choices[left].ProjectName < choices[right].ProjectName
		}
		return choices[left].ProjectID < choices[right].ProjectID
	})
	return choices
}

func (c *Catalog) LaunchConfigs() []LaunchConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	configs := make([]LaunchConfig, 0, len(c.index.state.Records))
	for _, record := range c.index.state.Records {
		configs = append(configs, record.LaunchConfig())
	}
	return configs
}

func (c *Catalog) LaunchConfig(repository string) (LaunchConfig, error) {
	record, found := c.Record(repository)
	if !found {
		return LaunchConfig{}, permanent(fmt.Errorf("repository catalog: %s is not allowlisted", strings.TrimSpace(repository)))
	}
	return record.LaunchConfig(), nil
}

func (c *Catalog) CompletionIdentities() []CompletionIdentity {
	c.mu.RLock()
	defer c.mu.RUnlock()
	identities := make([]CompletionIdentity, 0, len(c.index.state.Records))
	for _, record := range c.index.state.Records {
		identities = append(identities, record.CompletionIdentity())
	}
	return identities
}

func (c *Catalog) CompletionIdentity(repository string) (CompletionIdentity, error) {
	record, found := c.Record(repository)
	if !found {
		return CompletionIdentity{}, permanent(fmt.Errorf("repository catalog: %s has no completion identity", strings.TrimSpace(repository)))
	}
	return record.CompletionIdentity(), nil
}

func (c *Catalog) SetupSnapshot() SetupSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snapshot := SetupSnapshot{AwaitingMetadata: len(c.index.state.Awaiting)}
	for _, record := range c.index.state.Records {
		switch record.Setup.State {
		case SetupStatePending:
			snapshot.Pending++
		case SetupStateRunning:
			snapshot.Running++
		case SetupStateSucceeded:
			snapshot.Succeeded++
		case SetupStateFailed:
			snapshot.Failed++
		}
	}
	snapshot.Total = snapshot.AwaitingMetadata + snapshot.Pending + snapshot.Running + snapshot.Succeeded + snapshot.Failed
	return snapshot
}

type permanentError struct{ error }

func (permanentError) Permanent() bool { return true }

func (e permanentError) Unwrap() error { return e.error }

func permanent(err error) error { return permanentError{error: err} }
