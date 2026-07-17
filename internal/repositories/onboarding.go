package repositories

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// ProjectAdmission is complete, parser-neutral project metadata accepted by
// the canonical repository owner. Managed distinguishes a repository created
// by onboarding from an immutable compiled repository overlay.
type ProjectAdmission struct {
	Project       ProjectIdentity
	Repository    string
	Origin        string
	LocalPath     string
	ManagedRoot   string
	CloudURL      string
	DefaultBranch string
	Bootstrap     bool
	Managed       bool
}

type AdmissionResult struct {
	Record         Record
	NeedsProvision bool
}

// RecordAwaitingMetadata durably records a valid project identity before its
// repository metadata exists. Once repository metadata is admitted, returning
// to this state is permanently rejected.
func (s *Store) RecordAwaitingMetadata(project ProjectIdentity, now time.Time) (AwaitingProject, error) {
	if err := project.validate(); err != nil {
		return AwaitingProject{}, permanent(fmt.Errorf("repositories: awaiting metadata: %w", err))
	}
	now, err := setupTime(now)
	if err != nil {
		return AwaitingProject{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.snapshotLocked()
	if recordIndexByProject(current, project.ID) >= 0 {
		return AwaitingProject{}, permanent(errors.New("repositories: admitted repository metadata cannot be removed"))
	}
	if index := awaitingIndexByProject(current, project.ID); index >= 0 {
		awaiting := current.Awaiting[index]
		if awaiting.Project.Name == project.Name {
			return cloneAwaitingProject(awaiting), nil
		}
		awaiting.Project = project
		awaiting.Setup.UpdatedAt = now
		current.Awaiting[index] = awaiting
		updated, persistErr := s.persistLocked(current)
		return awaitingProjectByID(updated, project.ID), persistErr
	}

	awaiting := AwaitingProject{
		Project: project,
		Setup: Setup{
			State: SetupStateAwaitingMetadata, CreatedAt: now, UpdatedAt: now,
		},
	}
	current.Awaiting = append(current.Awaiting, awaiting)
	updated, persistErr := s.persistLocked(current)
	return awaitingProjectByID(updated, project.ID), persistErr
}

// AdmitProject creates a managed repository record or overlays project state
// onto an immutable compiled repository. An exact no-op does not advance the
// repository generation.
func (s *Store) AdmitProject(admission ProjectAdmission, now time.Time) (AdmissionResult, error) {
	if err := validateProjectAdmission(admission); err != nil {
		return AdmissionResult{}, permanent(err)
	}
	now, err := setupTime(now)
	if err != nil {
		return AdmissionResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.snapshotLocked()
	projectIndex := recordIndexByProject(current, admission.Project.ID)
	repositoryIndex := recordIndexByRepository(current, admission.Repository)
	awaitingIndex := awaitingIndexByProject(current, admission.Project.ID)
	newlyAdmitted := projectIndex < 0

	if projectIndex >= 0 && projectIndex != repositoryIndex {
		return AdmissionResult{}, permanent(fmt.Errorf("repositories: project %s repository identity is immutable", admission.Project.ID))
	}
	if projectIndex < 0 && repositoryIndex >= 0 && !current.Records[repositoryIndex].Project.IsZero() {
		return AdmissionResult{}, permanent(fmt.Errorf("repositories: repository %s is already admitted", admission.Repository))
	}
	if projectIndex < 0 && repositoryIndex < 0 && !admission.Managed {
		return AdmissionResult{}, permanent(fmt.Errorf("repositories: existing repository %s is not in the compiled baseline", admission.Repository))
	}

	var record Record
	var previous Record
	if repositoryIndex >= 0 {
		previous = current.Records[repositoryIndex]
		if err := admissionMatchesRecord(admission, previous, projectIndex >= 0); err != nil {
			return AdmissionResult{}, permanent(err)
		}
		record = cloneRecord(previous)
		if projectIndex >= 0 || admission.CloudURL != "" {
			record.CloudURL = admission.CloudURL
		}
	} else {
		_, app, _ := strings.Cut(admission.Repository, "/")
		record = Record{
			App: strings.ToLower(app), Provenance: ProvenanceProject,
			Repository: admission.Repository, Origin: admission.Origin,
			LocalPath: admission.LocalPath, ManagedPath: admission.LocalPath,
			ManagedRoot: admission.ManagedRoot, DefaultBranch: admission.DefaultBranch,
			Bootstrap: admission.Bootstrap, CloudURL: admission.CloudURL,
		}
	}
	record.Project = admission.Project

	createdAt := now
	if projectIndex >= 0 {
		createdAt = previous.Setup.CreatedAt
	} else if awaitingIndex >= 0 {
		createdAt = current.Awaiting[awaitingIndex].Setup.CreatedAt
	}
	providerChanged := projectIndex >= 0 && previous.CloudURL != record.CloudURL
	repositoryNeedsProvision := admission.Managed && (newlyAdmitted || previous.Setup.State == SetupStateFailed)
	providerNeedsProvision := admission.CloudURL != "" &&
		(newlyAdmitted || previous.Setup.State == SetupStateFailed || providerChanged || !previous.Setup.ProviderCoordinated)
	needsProvision := repositoryNeedsProvision || providerNeedsProvision || providerChanged
	changed := newlyAdmitted || previous.Project.Name != admission.Project.Name || providerChanged

	if newlyAdmitted {
		record.Setup = Setup{CreatedAt: createdAt, UpdatedAt: now}
		if needsProvision {
			record.Setup.State = SetupStatePending
		} else {
			record.Setup.State = SetupStateSucceeded
			record.Setup.ProvisionedAt = cloneTime(&now)
			record.Setup.ProviderCoordinated = true
		}
	} else if changed || needsProvision {
		record.Setup.UpdatedAt = now
		if providerChanged {
			record.Setup.ProviderCoordinated = false
		}
		if needsProvision {
			record.Setup.State = SetupStatePending
			record.Setup.LastError = ""
			record.Setup.NextAttemptAt = nil
		} else if !admission.Managed {
			record.Setup.State = SetupStateSucceeded
			record.Setup.LastError = ""
			record.Setup.NextAttemptAt = nil
			record.Setup.ProviderCoordinated = true
			if record.Setup.ProvisionedAt == nil {
				record.Setup.ProvisionedAt = cloneTime(&now)
			}
		}
	} else {
		return AdmissionResult{Record: cloneRecord(previous)}, nil
	}

	if repositoryIndex >= 0 {
		current.Records[repositoryIndex] = record
	} else {
		current.Records = append(current.Records, record)
	}
	if awaitingIndex >= 0 {
		current.Awaiting = slices.Delete(current.Awaiting, awaitingIndex, awaitingIndex+1)
	}
	if _, err := buildIndex(current); err != nil {
		return AdmissionResult{}, permanent(fmt.Errorf("repositories: invalid admission: %w", err))
	}
	updated, persistErr := s.persistLocked(current)
	return AdmissionResult{
		Record: recordByProjectID(updated, admission.Project.ID), NeedsProvision: needsProvision,
	}, persistErr
}

// RecoverSetups requeues interrupted work and legacy Cloud setups that have
// not completed provider coordination. All recoverable records advance in one
// repository generation.
func (s *Store) RecoverSetups(now time.Time) (int, error) {
	now, err := setupTime(now)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.snapshotLocked()
	next := current.Clone()
	recovered := 0
	for index := range next.Records {
		record := &next.Records[index]
		switch {
		case record.Setup.State == SetupStateSucceeded && record.CloudURL != "" && !record.Setup.ProviderCoordinated:
			record.Setup.State = SetupStatePending
			record.Setup.LastError = ""
			record.Setup.NextAttemptAt = nil
			record.Setup.ProvisionedAt = nil
			record.Setup.UpdatedAt = now
			recovered++
		case record.Setup.State == SetupStateRunning:
			record.Setup.State = SetupStatePending
			record.Setup.NextAttemptAt = nil
			record.Setup.UpdatedAt = now
			recovered++
		}
	}
	if recovered == 0 {
		return 0, nil
	}
	updated, persistErr := s.persistLocked(next)
	if updated.Generation == current.Generation {
		return 0, persistErr
	}
	return recovered, persistErr
}

// ClaimSetup durably claims the first due pending or failed setup. A failed
// setup is not claimable before its retry timestamp.
func (s *Store) ClaimSetup(now time.Time) (Record, bool, error) {
	now, err := setupTime(now)
	if err != nil {
		return Record{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.snapshotLocked()
	for index := range current.Records {
		record := &current.Records[index]
		if record.Setup.State != SetupStatePending && record.Setup.State != SetupStateFailed {
			continue
		}
		if record.Setup.NextAttemptAt != nil && record.Setup.NextAttemptAt.After(now) {
			continue
		}
		if record.Setup.Attempts == int(^uint(0)>>1) {
			return Record{}, false, errors.New("repositories: setup attempts exhausted")
		}
		record.Setup.State = SetupStateRunning
		record.Setup.Attempts++
		record.Setup.LastError = ""
		record.Setup.NextAttemptAt = nil
		record.Setup.UpdatedAt = now
		projectID := record.Project.ID
		expectedAttempt := record.Setup.Attempts
		previousGeneration := current.Generation
		updated, persistErr := s.persistLocked(current)
		claimed := recordByProjectID(updated, projectID)
		if updated.Generation != previousGeneration+1 || claimed.Setup.State != SetupStateRunning || claimed.Setup.Attempts != expectedAttempt {
			return Record{}, false, persistErr
		}
		return claimed, true, persistErr
	}
	return Record{}, false, nil
}

func (s *Store) CompleteSetup(projectID string, now time.Time) (Record, error) {
	projectID, err := canonicalProjectID(projectID)
	if err != nil {
		return Record{}, err
	}
	now, err = setupTime(now)
	if err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.snapshotLocked()
	index := recordIndexByProject(current, projectID)
	if index < 0 {
		return Record{}, fmt.Errorf("repositories: setup project %s not found", projectID)
	}
	record := &current.Records[index]
	if record.Setup.State == SetupStateSucceeded && record.Setup.ProviderCoordinated {
		return cloneRecord(*record), nil
	}
	if record.Setup.State != SetupStateRunning {
		return Record{}, permanent(fmt.Errorf("repositories: project %s setup is not running", projectID))
	}
	record.Setup.State = SetupStateSucceeded
	record.Setup.LastError = ""
	record.Setup.NextAttemptAt = nil
	record.Setup.UpdatedAt = now
	record.Setup.ProvisionedAt = cloneTime(&now)
	record.Setup.ProviderCoordinated = true
	updated, persistErr := s.persistLocked(current)
	return recordByProjectID(updated, projectID), persistErr
}

func (s *Store) FailSetup(projectID, detail string, nextAttemptAt, now time.Time) (Record, error) {
	projectID, err := canonicalProjectID(projectID)
	if err != nil {
		return Record{}, err
	}
	now, err = setupTime(now)
	if err != nil {
		return Record{}, err
	}
	nextAttemptAt, err = setupTime(nextAttemptAt)
	if err != nil {
		return Record{}, errors.New("repositories: setup retry time is required")
	}
	detail = canonicalSetupError(detail)

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.snapshotLocked()
	index := recordIndexByProject(current, projectID)
	if index < 0 {
		return Record{}, fmt.Errorf("repositories: setup project %s not found", projectID)
	}
	record := &current.Records[index]
	if record.Setup.State == SetupStateFailed && record.Setup.LastError == detail &&
		record.Setup.NextAttemptAt != nil && record.Setup.NextAttemptAt.Equal(nextAttemptAt) {
		return cloneRecord(*record), nil
	}
	if record.Setup.State != SetupStateRunning {
		return Record{}, permanent(fmt.Errorf("repositories: project %s setup is not running", projectID))
	}
	record.Setup.State = SetupStateFailed
	record.Setup.LastError = detail
	record.Setup.NextAttemptAt = cloneTime(&nextAttemptAt)
	record.Setup.UpdatedAt = now
	updated, persistErr := s.persistLocked(current)
	return recordByProjectID(updated, projectID), persistErr
}

func (s *Store) snapshotLocked() SourceState {
	return s.catalog.Snapshot()
}

func validateProjectAdmission(admission ProjectAdmission) error {
	if err := admission.Project.validate(); err != nil {
		return fmt.Errorf("repositories: admission: %w", err)
	}
	repository, origin, err := normalizeOrigin(admission.Origin)
	if err != nil || repository != admission.Repository || origin != admission.Origin {
		return errors.New("repositories: admission requires an exact canonical repository origin")
	}
	if !canonicalAbsolutePath(admission.LocalPath) || !canonicalAbsolutePath(admission.ManagedRoot) ||
		filepath.Dir(admission.LocalPath) != admission.ManagedRoot {
		return errors.New("repositories: admission requires a direct canonical repository path")
	}
	_, name, _ := strings.Cut(admission.Repository, "/")
	if filepath.Base(admission.LocalPath) != name || admission.Managed != admission.Bootstrap || !validBranch(admission.DefaultBranch) {
		return errors.New("repositories: admission repository policy is invalid")
	}
	if admission.CloudURL != "" {
		cloudURL, err := normalizeCloudURL(admission.CloudURL)
		if err != nil || cloudURL != admission.CloudURL {
			return errors.New("repositories: admission Cloud URL is not canonical")
		}
	}
	return nil
}

func admissionMatchesRecord(admission ProjectAdmission, current Record, admitted bool) error {
	if admission.Repository != current.Repository || admission.Origin != current.Origin ||
		admission.LocalPath != current.LocalPath || admission.DefaultBranch != current.DefaultBranch {
		return fmt.Errorf("repositories: project %s repository identity is immutable", admission.Project.ID)
	}
	switch current.Provenance {
	case ProvenanceCompiled:
		if admission.Managed || admission.Bootstrap || admission.ManagedRoot != filepath.Dir(current.LocalPath) {
			return fmt.Errorf("repositories: project %s conflicts with compiled repository policy", admission.Project.ID)
		}
		if !admitted && current.CloudURL != "" && admission.CloudURL != "" && current.CloudURL != admission.CloudURL {
			return fmt.Errorf("repositories: project %s Cloud URL conflicts with compiled repository", admission.Project.ID)
		}
	case ProvenanceProject:
		if !admission.Managed || admission.Bootstrap != current.Bootstrap || admission.ManagedRoot != current.ManagedRoot {
			return fmt.Errorf("repositories: project %s repository management policy is immutable", admission.Project.ID)
		}
	default:
		return errors.New("repositories: repository provenance is invalid")
	}
	return nil
}

func setupTime(value time.Time) (time.Time, error) {
	if value.IsZero() {
		return time.Time{}, errors.New("repositories: setup time is required")
	}
	return value.UTC(), nil
}

func canonicalSetupError(detail string) string {
	detail = strings.TrimSpace(strings.ToValidUTF8(detail, "\uFFFD"))
	if len(detail) <= 2048 {
		return detail
	}
	detail = detail[:2048]
	for !utf8.ValidString(detail) {
		detail = detail[:len(detail)-1]
	}
	return detail
}

func canonicalProjectID(projectID string) (string, error) {
	canonical := strings.TrimSpace(projectID)
	if projectID != canonical || !projectIDPattern.MatchString(canonical) {
		return "", permanent(errors.New("repositories: canonical project identity is required"))
	}
	return canonical, nil
}

func recordIndexByProject(state SourceState, projectID string) int {
	for index := range state.Records {
		if state.Records[index].Project.ID == projectID {
			return index
		}
	}
	return -1
}

func recordIndexByRepository(state SourceState, repository string) int {
	for index := range state.Records {
		if state.Records[index].Repository == repository {
			return index
		}
	}
	return -1
}

func awaitingIndexByProject(state SourceState, projectID string) int {
	for index := range state.Awaiting {
		if state.Awaiting[index].Project.ID == projectID {
			return index
		}
	}
	return -1
}

func awaitingProjectByID(state SourceState, projectID string) AwaitingProject {
	index := awaitingIndexByProject(state, projectID)
	if index < 0 {
		return AwaitingProject{}
	}
	return cloneAwaitingProject(state.Awaiting[index])
}

func recordByProjectID(state SourceState, projectID string) Record {
	index := recordIndexByProject(state, projectID)
	if index < 0 {
		return Record{}
	}
	return cloneRecord(state.Records[index])
}

func cloneAwaitingProject(awaiting AwaitingProject) AwaitingProject {
	awaiting.Setup = cloneSetup(awaiting.Setup)
	return awaiting
}
