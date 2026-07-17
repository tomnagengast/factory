package migration

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	manifestSchema      = 1
	backupReceiptSchema = 1
	dryRunReportSchema  = 1
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
	if r.Manifest.MigrationID != r.Backup.MigrationID || r.Manifest.SourceRootDigest != r.Backup.SourceRootDigest || r.Manifest.AuditDigest != r.AuditDigest || !slices.Equal(r.Manifest.Sources, r.Backup.Files) || !slices.Equal(r.Manifest.Directories, r.Backup.Directories) {
		return errors.New("migration: report artifacts disagree")
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
