package migration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

type canonicalEvidence struct {
	CompiledRepositoryInputDigest string
	Policy                        CanonicalPolicyAudit
	Repositories                  CanonicalRepositoryAudit
	Runs                          CanonicalRunsAudit
	Tasks                         CanonicalTaskAudit
	Events                        CanonicalEventAudit
	TargetSchemas                 TargetSchemas
	policySnapshot                policy.Snapshot
	repositoryState               repositories.SourceState
	taskSnapshot                  taskstore.Snapshot
	activityProjection            eventwire.ActivityProjection
	activityCorpus                map[string][]byte
	wireRecords                   []eventwire.Record
	wireTotal                     uint64
	wireDispatched                uint64
}

func convertCanonicalSources(state sourceState, options Options) (canonicalEvidence, error) {
	if err := inject(options, "before-policy-conversion"); err != nil {
		return canonicalEvidence{}, err
	}
	var registry *triggerregistry.Snapshot
	if state.registryPresent {
		clone := state.registry.Clone()
		registry = &clone
	}
	policySnapshot, err := policy.ConvertSources(policy.Sources{
		Settings: state.settings, Registry: registry, TaskControl: state.taskControl,
		TriggerActorID: options.TriggerActorID,
	})
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: convert canonical policy: %w", err)
	}
	if err := policySnapshot.Validate(); err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: validate canonical policy: %w", err)
	}
	settingsView := policy.SettingsView(policySnapshot)
	if err := settingsView.Validate(); err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: validate canonical settings compatibility: %w", err)
	}
	registryView := policy.RegistryView(policySnapshot)
	if err := registryView.Validate(settingsView); err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: validate canonical registry compatibility: %w", err)
	}
	taskControlView := policy.TaskControlView(policySnapshot)
	if settingsView.Revision != state.settings.Revision || registryView.Revision != state.registry.Revision ||
		taskControlView.Version != state.taskControl.Version || taskControlView.Revision != state.taskControl.Revision ||
		!slices.Equal(taskControlView.EnabledProjectIDs, state.taskControl.EnabledProjectIDs) {
		return canonicalEvidence{}, errors.New("migration: canonical policy compatibility revisions disagree")
	}
	if err := inject(options, "after-policy-conversion"); err != nil {
		return canonicalEvidence{}, err
	}

	setupSources, err := repositorySetupSources(state.projects.Entries)
	if err != nil {
		return canonicalEvidence{}, err
	}
	if err := inject(options, "before-repository-conversion"); err != nil {
		return canonicalEvidence{}, err
	}
	repositoryState, err := repositories.ConvertSources(state.compiledRepositories, setupSources)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: convert canonical repositories: %w", err)
	}
	catalog, err := repositories.NewCatalog(repositoryState)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: validate canonical repository catalog: %w", err)
	}
	repositorySnapshot := catalog.Snapshot()
	if err := inject(options, "after-repository-conversion"); err != nil {
		return canonicalEvidence{}, err
	}

	if err := inject(options, "before-task-conversion"); err != nil {
		return canonicalEvidence{}, err
	}
	bindings := make([]taskstore.LinearBinding, len(state.identities.Bindings))
	for index, binding := range state.identities.Bindings {
		bindings[index] = taskstore.LinearBinding{Identifier: binding.Identifier, UUID: binding.UUID}
	}
	taskSnapshot, err := taskstore.ConvertLinearBindings(state.tasks, bindings)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: convert canonical task identities: %w", err)
	}
	canonicalWireRecords := slices.Clone(state.wireRecords)
	if state.pendingTask != nil {
		var canonicalRecord eventwire.Record
		taskSnapshot, canonicalRecord, err = taskstore.ConvertLegacyPendingOperation(
			taskSnapshot, state.pendingTask.OperationID, state.pendingTask.Command, state.wireRecords[state.pendingTask.RecordIndex],
		)
		if err != nil {
			return canonicalEvidence{}, fmt.Errorf("migration: convert pending canonical task operation: %w", err)
		}
		canonicalWireRecords[state.pendingTask.RecordIndex] = canonicalRecord
	}
	taskSnapshot, err = taskstore.CanonicalSnapshot(taskSnapshot)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: validate canonical task artifact: %w", err)
	}
	if err := inject(options, "after-task-conversion"); err != nil {
		return canonicalEvidence{}, err
	}
	if err := inject(options, "before-event-conversion"); err != nil {
		return canonicalEvidence{}, err
	}
	activityRecords := make([]eventwire.ActivitySourceRecord, len(state.activity.Events))
	for index, record := range state.activity.Events {
		activityRecords[index] = eventwire.ActivitySourceRecord{
			DeliveryID: record.DeliveryID, PayloadAvailable: record.PayloadAvailable,
			Event: eventwire.ActivityEvent{Type: record.Type, Action: record.Action, ReceivedAt: record.ReceivedAt},
		}
	}
	activityProjection, activityCorpus, err := eventwire.ConvertActivity(state.activity.Total, activityRecords, state.payloadBodies)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: convert canonical event activity: %w", err)
	}
	validationRoot, err := os.MkdirTemp("", "factory-event-conversion-*")
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: create event conversion fixture: %w", err)
	}
	defer os.RemoveAll(validationRoot)
	activityRoot := filepath.Join(validationRoot, "events")
	if err := eventwire.MaterializeActivity(activityRoot, activityProjection, activityCorpus); err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: materialize canonical event activity: %w", err)
	}
	reopenedActivity, reopenedCorpus, err := eventwire.ReadActivity(activityRoot)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: reopen canonical event activity: %w", err)
	}
	if !reflect.DeepEqual(reopenedActivity, activityProjection) || !reflect.DeepEqual(reopenedCorpus, activityCorpus) {
		return canonicalEvidence{}, errors.New("migration: canonical event activity changed during disposable materialization")
	}
	if err := inject(options, "after-event-conversion"); err != nil {
		return canonicalEvidence{}, err
	}

	if err := inject(options, "before-canonical-evidence"); err != nil {
		return canonicalEvidence{}, err
	}
	compiledDigest, err := digestJSON(state.compiledRepositories)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: digest compiled repository input: %w", err)
	}
	policyDigest, err := policySnapshot.Digest()
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: digest canonical policy: %w", err)
	}
	repositoryDigest, err := digestJSON(repositorySnapshot)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: digest canonical repositories: %w", err)
	}
	taskDigest, err := digestJSON(taskSnapshot)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: digest canonical tasks: %w", err)
	}
	activityDigest, err := digestJSON(activityProjection)
	if err != nil {
		return canonicalEvidence{}, fmt.Errorf("migration: digest canonical event activity: %w", err)
	}
	policyRegistry := policySnapshot.Registry()
	policyControl := policySnapshot.TaskControl()
	evidence := canonicalEvidence{
		CompiledRepositoryInputDigest: compiledDigest,
		Policy: CanonicalPolicyAudit{
			Schema: policySnapshot.Schema(), Generation: policySnapshot.Generation(), Digest: policyDigest,
			RegistrySourcePresent: state.registryPresent, CompatibilityValidated: true,
			SettingsRevision: policySnapshot.Settings().Revision, RegistryRevision: policyRegistry.Revision,
			TaskControlRevision: policyControl.Revision, Workflows: uint64(len(policySnapshot.Workflows())),
			Rules: uint64(len(policyRegistry.Rules)), Schedules: uint64(len(policyRegistry.Schedules)),
			EnabledProjects: uint64(len(policyControl.EnabledProjectIDs)),
		},
		Repositories: repositoryAudit(repositorySnapshot, repositoryDigest),
		Tasks: CanonicalTaskAudit{
			Schema: taskSnapshot.Schema, Digest: taskDigest, Tasks: uint64(len(taskSnapshot.Tasks)),
			Outcomes: uint64(len(taskSnapshot.Outcomes)), Operations: uint64(len(taskSnapshot.Operations)),
			LinearBindings: uint64(len(taskSnapshot.LinearBindings)),
		},
		Events: CanonicalEventAudit{
			Schema: activityProjection.Schema, Digest: activityDigest, ActivityLifetime: activityProjection.Total,
			ActivityRetained: uint64(len(activityProjection.Events)), PrivatePayloads: uint64(len(activityCorpus)),
		},
		TargetSchemas:      TargetSchemas{Policy: policy.SchemaVersion, Repositories: repositories.SchemaVersion, Runs: runs.SchemaVersion, Tasks: taskstore.SchemaVersion, Events: eventwire.ActivitySchemaVersion},
		policySnapshot:     policySnapshot,
		repositoryState:    repositorySnapshot,
		taskSnapshot:       taskSnapshot,
		activityProjection: activityProjection,
		activityCorpus:     activityCorpus,
		wireRecords:        canonicalWireRecords,
		wireTotal:          state.wireTotal,
		wireDispatched:     state.wireDispatched,
	}
	if err := inject(options, "after-canonical-evidence"); err != nil {
		return canonicalEvidence{}, err
	}
	return evidence, nil
}

func repositorySetupSources(entries []projectsetup.Entry) ([]repositories.SetupSource, error) {
	sources := make([]repositories.SetupSource, 0, len(entries))
	for _, entry := range entries {
		state, err := repositorySetupState(entry.State)
		if err != nil {
			return nil, fmt.Errorf("migration: project %s: %w", entry.ProjectID, err)
		}
		sources = append(sources, repositories.SetupSource{
			ProjectID: entry.ProjectID, ProjectName: entry.ProjectName,
			Repository: entry.Repository, RepoURL: entry.RepoURL, LocalPath: entry.LocalPath,
			ManagedRoot: entry.ManagedRoot, CloudURL: entry.CloudURL, BaseBranch: entry.BaseBranch,
			Bootstrap: entry.Bootstrap, Managed: entry.Managed, State: state, Attempts: entry.Attempts,
			LastError: entry.LastError, NextAttemptAt: cloneTimePointer(entry.NextAttemptAt),
			CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
			ProvisionedAt: cloneTimePointer(entry.ProvisionedAt), ProviderCoordinated: entry.ProviderCoordinated,
		})
	}
	return sources, nil
}

func repositorySetupState(state projectsetup.State) (repositories.SetupState, error) {
	switch state {
	case projectsetup.StateAwaitingMetadata:
		return repositories.SetupStateAwaitingMetadata, nil
	case projectsetup.StatePending:
		return repositories.SetupStatePending, nil
	case projectsetup.StateRunning:
		return repositories.SetupStateRunning, nil
	case projectsetup.StateSucceeded:
		return repositories.SetupStateSucceeded, nil
	case projectsetup.StateFailed:
		return repositories.SetupStateFailed, nil
	default:
		return "", fmt.Errorf("unknown repository setup state %q", state)
	}
}

func repositoryAudit(snapshot repositories.SourceState, digest string) CanonicalRepositoryAudit {
	audit := CanonicalRepositoryAudit{
		Schema: snapshot.Schema, Generation: snapshot.Generation, Digest: digest,
		Awaiting: uint64(len(snapshot.Awaiting)),
	}
	for _, record := range snapshot.Records {
		if record.Provenance == repositories.ProvenanceCompiled {
			audit.Compiled++
		}
		if !record.Project.IsZero() {
			audit.Admitted++
		}
		if record.Routable() {
			audit.Routable++
		}
	}
	return audit
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := value.UTC()
	return &clone
}
