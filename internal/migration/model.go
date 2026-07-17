package migration

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	manifestSchema      = 2
	backupReceiptSchema = 1
	dryRunReportSchema  = 2
)

// SourceHash is immutable evidence for one source artifact observed by a dry run.
type SourceHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
}

type SourceDirectory struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
}

// MigrationManifest is the immutable portion of the future generation selector.
// Phase 0 only reports this value. It never persists or selects it.
type MigrationManifest struct {
	Schema           int               `json:"schema"`
	MigrationID      string            `json:"migrationId"`
	ObservedAt       time.Time         `json:"observedAt"`
	SourceRootDigest string            `json:"sourceRootDigest"`
	Sources          []SourceHash      `json:"sources"`
	Directories      []SourceDirectory `json:"directories"`
	SourceSchemas    map[string]int    `json:"sourceSchemas"`
	TargetSchemas    TargetSchemas     `json:"targetSchemas"`
	AuditDigest      string            `json:"auditDigest"`
	RetainedTotals   map[string]uint64 `json:"retainedTotals"`
}

// BackupReceipt describes the exact source set a later activating phase must
// preserve. Phase 0 does not create a backup.
type BackupReceipt struct {
	Schema           int               `json:"schema"`
	MigrationID      string            `json:"migrationId"`
	ObservedAt       time.Time         `json:"observedAt"`
	SourceRootDigest string            `json:"sourceRootDigest"`
	Files            []SourceHash      `json:"files"`
	Directories      []SourceDirectory `json:"directories"`
}

// TargetSchemas declares the prospective canonical artifacts proven by the
// dry run. The harness reports these values but never creates either artifact.
type TargetSchemas struct {
	Policy       int `json:"policy"`
	Repositories int `json:"repositories"`
}

type CanonicalPolicyAudit struct {
	Schema                 int    `json:"schema"`
	Generation             uint64 `json:"generation"`
	Digest                 string `json:"digest"`
	RegistrySourcePresent  bool   `json:"registrySourcePresent"`
	CompatibilityValidated bool   `json:"compatibilityValidated"`
	SettingsRevision       uint64 `json:"settingsRevision"`
	RegistryRevision       uint64 `json:"registryRevision"`
	TaskControlRevision    uint64 `json:"taskControlRevision"`
	Workflows              uint64 `json:"workflows"`
	Rules                  uint64 `json:"rules"`
	Schedules              uint64 `json:"schedules"`
	EnabledProjects        uint64 `json:"enabledProjects"`
}

type CanonicalRepositoryAudit struct {
	Schema     int    `json:"schema"`
	Generation uint64 `json:"generation"`
	Digest     string `json:"digest"`
	Compiled   uint64 `json:"compiled"`
	Admitted   uint64 `json:"admitted"`
	Awaiting   uint64 `json:"awaiting"`
	Routable   uint64 `json:"routable"`
}

// Audit is a body-free, deterministic cross-artifact characterization.
type Audit struct {
	TaskIdentities        uint64 `json:"taskIdentities"`
	WorkflowPins          uint64 `json:"workflowPins"`
	RepositoryRoutes      uint64 `json:"repositoryRoutes"`
	Decisions             uint64 `json:"decisions"`
	Invocations           uint64 `json:"invocations"`
	Runs                  uint64 `json:"runs"`
	ActiveRuns            uint64 `json:"activeRuns"`
	NativeTasks           uint64 `json:"nativeTasks"`
	NativeOutcomes        uint64 `json:"nativeOutcomes"`
	LinearBindings        uint64 `json:"linearBindings"`
	ActivityLifetime      uint64 `json:"activityLifetime"`
	ActivityRetained      uint64 `json:"activityRetained"`
	PrivatePayloads       uint64 `json:"privatePayloads"`
	WireTotal             uint64 `json:"wireTotal"`
	WireDispatched        uint64 `json:"wireDispatched"`
	WorkflowDrafts        uint64 `json:"workflowDrafts"`
	ScheduleCursors       uint64 `json:"scheduleCursors"`
	AgentEventCursors     uint64 `json:"agentEventCursors"`
	TaskIdentityDigest    string `json:"taskIdentityDigest"`
	WorkflowPinDigest     string `json:"workflowPinDigest"`
	RepositoryRouteDigest string `json:"repositoryRouteDigest"`
	InvocationRunDigest   string `json:"invocationRunDigest"`
	ActiveOwnershipDigest string `json:"activeOwnershipDigest"`
	EventSequenceDigest   string `json:"eventSequenceDigest"`
	LinearBijectionDigest string `json:"linearBijectionDigest"`
	ActivityHistoryDigest string `json:"activityHistoryDigest"`
	PayloadCorpusDigest   string `json:"payloadCorpusDigest"`
	RetainedTotalsDigest  string `json:"retainedTotalsDigest"`

	CompiledRepositoryInputDigest string                   `json:"compiledRepositoryInputDigest"`
	CanonicalPolicy               CanonicalPolicyAudit     `json:"canonicalPolicy"`
	CanonicalRepositories         CanonicalRepositoryAudit `json:"canonicalRepositories"`
	TargetSchemas                 TargetSchemas            `json:"targetSchemas"`
}

// DryRunReport is deliberately non-activating. No generation path, selection,
// or writer appears in this schema.
type DryRunReport struct {
	Schema      int               `json:"schema"`
	Activates   bool              `json:"activates"`
	Manifest    MigrationManifest `json:"manifest"`
	Backup      BackupReceipt     `json:"backup"`
	Audit       Audit             `json:"audit"`
	AuditDigest string            `json:"auditDigest"`
}

func (m MigrationManifest) validate() error {
	if m.Schema != manifestSchema || m.MigrationID == "" || m.ObservedAt.IsZero() || m.SourceRootDigest == "" || m.AuditDigest == "" || len(m.Sources) == 0 || len(m.Directories) == 0 {
		return errors.New("migration: invalid manifest identity")
	}
	if !slices.IsSortedFunc(m.Sources, func(a, b SourceHash) int { return compare(a.Path, b.Path) }) {
		return errors.New("migration: manifest sources are not canonical")
	}
	if err := validateHashes(m.Sources); err != nil {
		return err
	}
	if m.TargetSchemas.Policy <= 0 || m.TargetSchemas.Repositories <= 0 {
		return errors.New("migration: target schemas are required")
	}
	return validateDirectories(m.Directories)
}

func (r BackupReceipt) validate() error {
	if r.Schema != backupReceiptSchema || r.MigrationID == "" || r.ObservedAt.IsZero() || r.SourceRootDigest == "" || len(r.Files) == 0 || len(r.Directories) == 0 {
		return errors.New("migration: invalid backup receipt identity")
	}
	if err := validateHashes(r.Files); err != nil {
		return err
	}
	return validateDirectories(r.Directories)
}

func (r DryRunReport) validate() error {
	if r.Schema != dryRunReportSchema || r.Activates {
		return errors.New("migration: dry-run report may not activate state")
	}
	if err := r.Manifest.validate(); err != nil {
		return err
	}
	if err := r.Backup.validate(); err != nil {
		return err
	}
	if err := r.Audit.validate(); err != nil {
		return err
	}
	if r.Manifest.MigrationID != r.Backup.MigrationID || r.Manifest.SourceRootDigest != r.Backup.SourceRootDigest || r.Manifest.AuditDigest != r.AuditDigest || r.Manifest.TargetSchemas != r.Audit.TargetSchemas || !slices.Equal(r.Manifest.Sources, r.Backup.Files) || !slices.Equal(r.Manifest.Directories, r.Backup.Directories) {
		return errors.New("migration: report artifacts disagree")
	}
	return nil
}

func (a Audit) validate() error {
	if len(a.CompiledRepositoryInputDigest) != 64 || len(a.CanonicalPolicy.Digest) != 64 || len(a.CanonicalRepositories.Digest) != 64 {
		return errors.New("migration: canonical audit digests are invalid")
	}
	if a.TargetSchemas.Policy <= 0 || a.TargetSchemas.Repositories <= 0 ||
		a.CanonicalPolicy.Schema != a.TargetSchemas.Policy ||
		a.CanonicalRepositories.Schema != a.TargetSchemas.Repositories {
		return errors.New("migration: canonical audit schemas disagree")
	}
	if a.CanonicalPolicy.Generation == 0 || a.CanonicalRepositories.Generation == 0 ||
		!a.CanonicalPolicy.CompatibilityValidated || a.CanonicalPolicy.Workflows == 0 ||
		a.CanonicalRepositories.Compiled == 0 {
		return errors.New("migration: canonical audit evidence is incomplete")
	}
	return nil
}

func validateDirectories(values []SourceDirectory) error {
	seen := make(map[string]bool, len(values))
	if !slices.IsSortedFunc(values, func(a, b SourceDirectory) int { return compare(a.Path, b.Path) }) {
		return errors.New("migration: source directories are not canonical")
	}
	for _, value := range values {
		if value.Path == "" || value.Mode != 0o700 || seen[value.Path] {
			return fmt.Errorf("migration: invalid source directory %q", value.Path)
		}
		seen[value.Path] = true
	}
	return nil
}

func validateHashes(values []SourceHash) error {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value.Path == "" || len(value.SHA256) != 64 || value.Mode != 0o600 || value.Size < 0 || seen[value.Path] {
			return fmt.Errorf("migration: invalid source hash for %q", value.Path)
		}
		seen[value.Path] = true
	}
	return nil
}

func compare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
