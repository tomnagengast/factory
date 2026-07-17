package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerscheduler"
	"github.com/tomnagengast/factory/internal/workflow"
)

const (
	generationManifestSchema = 1
	stateGeneration          = 1
	generationManifestFile   = "generation.json"
	generationAuditFile      = "audit.json"
	generationMigrationFile  = "migration.json"
	generationBackupFile     = "backup-receipt.json"
	generationBackupDir      = "backup"
	generationRuntimeDir     = "runtime"
)

type RuntimeArtifacts struct {
	WorkflowDrafts string
	TriggerCursors string
	AgentOffsets   string
}

// GenerationManifest binds one complete, still-unselected canonical
// generation to its immutable migration evidence and initial artifact bytes.
// Initial hashes characterize staging only; selected mutable stores validate
// through strict replay after canonical writes begin.
type GenerationManifest struct {
	Schema           int           `json:"schema"`
	StateGeneration  int           `json:"stateGeneration"`
	MigrationID      string        `json:"migrationId"`
	SourceRootDigest string        `json:"sourceRootDigest"`
	AuditDigest      string        `json:"auditDigest"`
	TargetSchemas    TargetSchemas `json:"targetSchemas"`
	Artifacts        []SourceHash  `json:"artifacts"`
}

type Generation struct {
	Path     string
	Manifest GenerationManifest
	Report   DryRunReport
}

type SelectedGeneration struct {
	Generation   Generation
	Policy       *policy.Store
	Repositories *repositories.Store
	Runs         *runs.Store
	Tasks        *taskstore.Store
	Wire         *eventwire.Journal
	Activity     *eventwire.ActivityStore
	Runtime      RuntimeArtifacts
}

func (s *SelectedGeneration) Close() error {
	if s == nil || s.Runs == nil {
		return nil
	}
	return s.Runs.Close()
}

// OpenStagedGeneration reconstructs the immutable report from a generation's
// own evidence, then performs the same strict validation as initial staging.
// It is the restart path before any selector or write boundary exists.
func OpenStagedGeneration(path string) (Generation, error) {
	path = filepath.Clean(path)
	report, err := readGenerationReport(path)
	if err != nil {
		return Generation{}, err
	}
	return ValidateStagedGeneration(path, report)
}

func readGenerationReport(path string) (DryRunReport, error) {
	var audit Audit
	var manifest MigrationManifest
	var backup BackupReceipt
	if err := readStrictJSON(filepath.Join(path, generationAuditFile), &audit); err != nil {
		return DryRunReport{}, err
	}
	if err := readStrictJSON(filepath.Join(path, generationMigrationFile), &manifest); err != nil {
		return DryRunReport{}, err
	}
	if err := readStrictJSON(filepath.Join(path, generationBackupFile), &backup); err != nil {
		return DryRunReport{}, err
	}
	auditDigest, err := digestJSON(audit)
	if err != nil {
		return DryRunReport{}, err
	}
	report := DryRunReport{
		Schema: dryRunReportSchema, Manifest: manifest, Backup: backup,
		Audit: audit, AuditDigest: auditDigest,
	}
	if err := report.validate(); err != nil {
		return DryRunReport{}, err
	}
	return report, nil
}

// OpenSelectedGeneration strictly replays mutable canonical stores without
// comparing them to their initial staging hashes. Immutable migration evidence
// and the whole-source backup remain exact, and the monotonic write boundary
// must exist as a private regular file.
func OpenSelectedGeneration(path string) (*SelectedGeneration, error) {
	path = filepath.Clean(path)
	if err := requirePrivateDirectory(path); err != nil {
		return nil, err
	}
	if err := validateGenerationInventory(path); err != nil {
		return nil, err
	}
	boundaryInfo, err := os.Lstat(filepath.Join(path, writeBoundaryArtifactName))
	if err != nil || !boundaryInfo.Mode().IsRegular() || boundaryInfo.Mode()&os.ModeSymlink != 0 || boundaryInfo.Mode().Perm() != 0o600 {
		return nil, errors.New("migration: selected generation write boundary is missing or unsafe")
	}
	report, err := readGenerationReport(path)
	if err != nil {
		return nil, err
	}
	var manifest GenerationManifest
	if err := readStrictJSON(filepath.Join(path, generationManifestFile), &manifest); err != nil {
		return nil, err
	}
	if err := validateGenerationManifest(manifest, report); err != nil {
		return nil, err
	}
	if err := verifyBackup(filepath.Join(path, generationBackupDir), report.Backup); err != nil {
		return nil, err
	}
	policyStore, err := policy.Open(filepath.Join(path, "policy.json"))
	if err != nil {
		return nil, err
	}
	repositoryStore, err := repositories.Open(filepath.Join(path, "repositories.json"))
	if err != nil {
		return nil, err
	}
	runStore, err := runs.Open(path, filepath.Join(path, "runs.jsonl"), validationLimit)
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = runStore.Close()
		}
	}()
	taskStore, err := taskstore.OpenExisting(filepath.Join(path, "tasks.jsonl"))
	if err != nil {
		return nil, err
	}
	wire, err := eventwire.OpenExisting(filepath.Join(path, "system-events.jsonl"), validationLimit, nil)
	if err != nil {
		return nil, err
	}
	activity, err := eventwire.OpenActivityStore(filepath.Join(path, "activity"), validationLimit)
	if err != nil {
		return nil, err
	}
	succeeded = true
	return &SelectedGeneration{
		Generation: Generation{Path: path, Manifest: manifest, Report: report},
		Policy:     policyStore, Repositories: repositoryStore, Runs: runStore,
		Tasks: taskStore, Wire: wire, Activity: activity, Runtime: runtimeArtifacts(path),
	}, nil
}

// VerifySourceSnapshot rechecks every source file and directory captured by a
// generation while allowing known operational files such as the lease and
// selector to appear beside that immutable legacy set.
func VerifySourceSnapshot(root string, report DryRunReport) error {
	if err := report.validate(); err != nil {
		return err
	}
	root = filepath.Clean(root)
	for _, expected := range report.Manifest.Sources {
		path, err := safeRelativePath(root, expected.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != os.FileMode(expected.Mode) || info.Size() != expected.Size {
			return fmt.Errorf("migration: source artifact changed: %s", expected.Path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != expected.SHA256 {
			return fmt.Errorf("migration: source artifact changed: %s", expected.Path)
		}
	}
	for _, expected := range report.Manifest.Directories {
		path := root
		if expected.Path != "." {
			var err error
			path, err = safeRelativePath(root, expected.Path)
			if err != nil {
				return err
			}
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != os.FileMode(expected.Mode) {
			return fmt.Errorf("migration: source directory changed: %s", expected.Path)
		}
	}
	return nil
}

// BuildGeneration creates and reopens a complete sibling generation without
// selecting it or enabling canonical writes. Repeating the same build reuses
// only an exact, unchanged staged generation.
func BuildGeneration(sourceRoot, generationsRoot string, options Options) (Generation, error) {
	sourceRoot = filepath.Clean(sourceRoot)
	generationsRoot = filepath.Clean(generationsRoot)
	if sourceRoot == "." || generationsRoot == "." || sourceRoot == generationsRoot {
		return Generation{}, errors.New("migration: distinct source and generation roots are required")
	}
	report, err := DryRun(sourceRoot, options)
	if err != nil {
		return Generation{}, err
	}
	finalPath := filepath.Join(generationsRoot, report.Manifest.MigrationID)
	if _, err := os.Lstat(finalPath); err == nil {
		return ValidateStagedGeneration(finalPath, report)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Generation{}, fmt.Errorf("migration: inspect staged generation: %w", err)
	}
	if err := ensurePrivateDirectory(generationsRoot); err != nil {
		return Generation{}, err
	}
	temporary, err := os.MkdirTemp(generationsRoot, ".generation-*")
	if err != nil {
		return Generation{}, fmt.Errorf("migration: create staged generation: %w", err)
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return Generation{}, fmt.Errorf("migration: set staged generation permissions: %w", err)
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := materializeGeneration(sourceRoot, temporary, report, options); err != nil {
		return Generation{}, err
	}
	if err := syncTree(temporary); err != nil {
		return Generation{}, err
	}
	if err := inject(options, "before-generation-install"); err != nil {
		return Generation{}, err
	}
	if err := os.Rename(temporary, finalPath); err != nil {
		return Generation{}, fmt.Errorf("migration: install staged generation: %w", err)
	}
	installed = true
	if err := syncDirectory(generationsRoot); err != nil {
		return Generation{}, err
	}
	if err := inject(options, "after-generation-install"); err != nil {
		return Generation{}, err
	}
	return ValidateStagedGeneration(finalPath, report)
}

func materializeGeneration(sourceRoot, destination string, report DryRunReport, options Options) error {
	state, err := readSources(sourceRoot, options)
	if err != nil {
		return err
	}
	canonical, err := convertCanonicalSources(state, options)
	if err != nil {
		return err
	}
	runSnapshot, runMetrics, err := convertRunSources(state, canonical, report.Manifest.MigrationID, report.Manifest.SourceRootDigest)
	if err != nil {
		return err
	}
	runAudit, err := canonicalRunsEvidence(runSnapshot, runMetrics)
	if err != nil {
		return err
	}
	canonical.Runs = runAudit
	audit, _, err := auditSources(state, canonical)
	if err != nil {
		return err
	}
	auditDigest, err := digestJSON(audit)
	if err != nil || auditDigest != report.AuditDigest || !reflect.DeepEqual(audit, report.Audit) {
		return errors.New("migration: generation conversion disagrees with dry-run audit")
	}
	currentHashes, err := hashTree(sourceRoot)
	if err != nil || !slices.Equal(currentHashes, report.Manifest.Sources) {
		return errors.New("migration: source changed before generation construction")
	}
	currentDirectories, err := directoryModes(sourceRoot)
	if err != nil || !slices.Equal(currentDirectories, report.Manifest.Directories) {
		return errors.New("migration: source directories changed before generation construction")
	}

	if _, err := policy.Create(filepath.Join(destination, "policy.json"), canonical.policySnapshot); err != nil {
		return err
	}
	if _, err := repositories.Create(filepath.Join(destination, "repositories.json"), canonical.repositoryState); err != nil {
		return err
	}
	runStore, err := runs.Create(destination, filepath.Join(destination, "runs.jsonl"), runSnapshot, validationLimit)
	if err != nil {
		return err
	}
	if err := runStore.Close(); err != nil {
		return err
	}
	if _, err := taskstore.Create(filepath.Join(destination, "tasks.jsonl"), canonical.taskSnapshot); err != nil {
		return err
	}
	wireState := state.wireState
	wireState.Records = slices.Clone(canonical.wireRecords)
	if _, err := eventwire.Create(filepath.Join(destination, "system-events.jsonl"), validationLimit, wireState); err != nil {
		return err
	}
	if err := eventwire.MaterializeActivity(filepath.Join(destination, "activity"), canonical.activityProjection, canonical.activityCorpus); err != nil {
		return err
	}
	if err := materializeRuntimeArtifacts(destination, state); err != nil {
		return err
	}
	if err := copyBackup(sourceRoot, filepath.Join(destination, generationBackupDir), report.Backup); err != nil {
		return err
	}
	artifacts, err := canonicalArtifactHashes(destination)
	if err != nil {
		return err
	}
	manifest := GenerationManifest{
		Schema: generationManifestSchema, StateGeneration: stateGeneration,
		MigrationID: report.Manifest.MigrationID, SourceRootDigest: report.Manifest.SourceRootDigest,
		AuditDigest: report.AuditDigest, TargetSchemas: report.Manifest.TargetSchemas,
		Artifacts: artifacts,
	}
	for name, value := range map[string]any{
		generationManifestFile: manifest, generationAuditFile: report.Audit,
		generationMigrationFile: report.Manifest, generationBackupFile: report.Backup,
	} {
		if err := writeExclusiveJSON(filepath.Join(destination, name), value); err != nil {
			return err
		}
	}
	return inject(options, "after-generation-materialize")
}

func runtimeArtifacts(root string) RuntimeArtifacts {
	directory := filepath.Join(root, generationRuntimeDir)
	return RuntimeArtifacts{
		WorkflowDrafts: filepath.Join(directory, "workflow-drafts.json"),
		TriggerCursors: filepath.Join(directory, "trigger-cursors.json"),
		AgentOffsets:   filepath.Join(directory, "agent-event-offsets.json"),
	}
}

func materializeRuntimeArtifacts(destination string, state sourceState) error {
	paths := runtimeArtifacts(destination)
	if err := os.Mkdir(filepath.Dir(paths.WorkflowDrafts), 0o700); err != nil {
		return fmt.Errorf("migration: create runtime artifact directory: %w", err)
	}
	for path, value := range map[string]any{
		paths.WorkflowDrafts: state.drafts,
		paths.TriggerCursors: state.cursors,
		paths.AgentOffsets:   state.agentCursors,
	} {
		if err := writeExclusiveJSON(path, value); err != nil {
			return err
		}
	}
	return nil
}

func validateRuntimeArtifacts(root string) error {
	paths := runtimeArtifacts(root)
	entries, err := os.ReadDir(filepath.Dir(paths.WorkflowDrafts))
	if err != nil || len(entries) != 3 {
		return errors.New("migration: runtime artifact inventory is incomplete")
	}
	expected := map[string]bool{
		filepath.Base(paths.WorkflowDrafts): true,
		filepath.Base(paths.TriggerCursors): true,
		filepath.Base(paths.AgentOffsets):   true,
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !expected[entry.Name()] {
			return errors.New("migration: runtime artifact inventory is invalid")
		}
	}
	if _, err := workflow.OpenDraftStore(paths.WorkflowDrafts); err != nil {
		return err
	}
	if _, err := triggerscheduler.Open(paths.TriggerCursors); err != nil {
		return err
	}
	var offsets agentCursorState
	if err := readStrictJSON(paths.AgentOffsets, &offsets); err != nil {
		return err
	}
	if offsets.Version != 1 || offsets.Offsets == nil {
		return errors.New("migration: runtime agent offsets are invalid")
	}
	return nil
}

// ValidateStagedGeneration verifies immutable bytes, backup evidence, strict
// store replay, and canonical audit totals without mutating selection state.
func ValidateStagedGeneration(path string, expected DryRunReport) (Generation, error) {
	path = filepath.Clean(path)
	if err := requirePrivateDirectory(path); err != nil {
		return Generation{}, err
	}
	if err := validateGenerationInventory(path); err != nil {
		return Generation{}, err
	}
	var manifest GenerationManifest
	if err := readStrictJSON(filepath.Join(path, generationManifestFile), &manifest); err != nil {
		return Generation{}, err
	}
	if err := validateGenerationManifest(manifest, expected); err != nil {
		return Generation{}, err
	}
	artifacts, err := canonicalArtifactHashes(path)
	if err != nil || !slices.Equal(artifacts, manifest.Artifacts) {
		return Generation{}, errors.New("migration: staged canonical artifact hashes changed")
	}
	var audit Audit
	var migration MigrationManifest
	var backup BackupReceipt
	if err := readStrictJSON(filepath.Join(path, generationAuditFile), &audit); err != nil {
		return Generation{}, err
	}
	if err := readStrictJSON(filepath.Join(path, generationMigrationFile), &migration); err != nil {
		return Generation{}, err
	}
	if err := readStrictJSON(filepath.Join(path, generationBackupFile), &backup); err != nil {
		return Generation{}, err
	}
	if !reflect.DeepEqual(audit, expected.Audit) || !reflect.DeepEqual(migration, expected.Manifest) || !reflect.DeepEqual(backup, expected.Backup) {
		return Generation{}, errors.New("migration: staged immutable evidence changed")
	}
	if err := verifyBackup(filepath.Join(path, generationBackupDir), backup); err != nil {
		return Generation{}, err
	}
	policyStore, err := policy.Open(filepath.Join(path, "policy.json"))
	if err != nil {
		return Generation{}, err
	}
	policyDigest, err := policyStore.Snapshot().Digest()
	if err != nil || policyDigest != audit.CanonicalPolicy.Digest {
		return Generation{}, errors.New("migration: staged policy audit changed")
	}
	repositoryStore, err := repositories.Open(filepath.Join(path, "repositories.json"))
	if err != nil {
		return Generation{}, err
	}
	repositoryDigest, err := digestJSON(repositoryStore.Snapshot())
	if err != nil || repositoryDigest != audit.CanonicalRepositories.Digest {
		return Generation{}, errors.New("migration: staged repository audit changed")
	}
	runStore, err := runs.Open(path, filepath.Join(path, "runs.jsonl"), validationLimit)
	if err != nil {
		return Generation{}, err
	}
	runSnapshot, err := runStore.Snapshot()
	closeErr := runStore.Close()
	if err != nil || closeErr != nil {
		return Generation{}, errors.Join(err, closeErr)
	}
	runDigest, err := runSnapshot.Digest()
	if err != nil || runDigest != audit.CanonicalRuns.Digest {
		return Generation{}, errors.New("migration: staged Runs audit changed")
	}
	taskStore, err := taskstore.OpenExisting(filepath.Join(path, "tasks.jsonl"))
	if err != nil {
		return Generation{}, err
	}
	taskDigest, err := digestJSON(taskStore.Snapshot())
	if err != nil || taskDigest != audit.CanonicalTasks.Digest {
		return Generation{}, errors.New("migration: staged task audit changed")
	}
	wire, err := eventwire.OpenExisting(filepath.Join(path, "system-events.jsonl"), validationLimit, nil)
	if err != nil {
		return Generation{}, err
	}
	wireState := wire.State()
	if wireState.Total != audit.WireTotal || wireState.Dispatched != audit.WireDispatched {
		return Generation{}, errors.New("migration: staged wire cursors changed")
	}
	activity, corpus, err := eventwire.ReadActivity(filepath.Join(path, "activity"))
	if err != nil {
		return Generation{}, err
	}
	activityDigest, err := digestJSON(activity)
	if err != nil || activityDigest != audit.CanonicalEvents.Digest || uint64(len(corpus)) != audit.PrivatePayloads {
		return Generation{}, errors.New("migration: staged activity audit changed")
	}
	if err := validateRuntimeArtifacts(path); err != nil {
		return Generation{}, err
	}
	return Generation{Path: path, Manifest: manifest, Report: expected}, nil
}

func validateGenerationInventory(path string) error {
	expected := map[string]bool{
		"policy.json": true, "repositories.json": true, "runs.jsonl": true,
		"tasks.jsonl": true, "system-events.jsonl": true, "task-source-neutral.json": true,
		"activity": true, generationRuntimeDir: true, generationBackupDir: true, generationManifestFile: true,
		generationAuditFile: true, generationMigrationFile: true, generationBackupFile: true,
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != len(expected) && len(entries) != len(expected)+1 {
		return errors.New("migration: staged generation inventory is incomplete or contains unknown artifacts")
	}
	seen := make(map[string]bool, len(expected))
	for _, entry := range entries {
		if entry.Name() == writeBoundaryArtifactName && !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		if !expected[entry.Name()] || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("migration: staged generation inventory is incomplete or contains unknown artifacts")
		}
		seen[entry.Name()] = true
		shouldDirectory := entry.Name() == "activity" || entry.Name() == generationRuntimeDir || entry.Name() == generationBackupDir
		if entry.IsDir() != shouldDirectory {
			return errors.New("migration: staged generation artifact has the wrong type")
		}
	}
	if len(seen) != len(expected) {
		return errors.New("migration: staged generation inventory is incomplete or contains unknown artifacts")
	}
	return nil
}

const writeBoundaryArtifactName = "canonicalWritesStarted"

func validateGenerationManifest(value GenerationManifest, expected DryRunReport) error {
	if value.Schema != generationManifestSchema || value.StateGeneration != stateGeneration ||
		value.MigrationID != expected.Manifest.MigrationID || value.SourceRootDigest != expected.Manifest.SourceRootDigest ||
		value.AuditDigest != expected.AuditDigest || value.TargetSchemas != expected.Manifest.TargetSchemas || len(value.Artifacts) == 0 {
		return errors.New("migration: staged generation manifest disagrees with dry run")
	}
	if !slices.IsSortedFunc(value.Artifacts, func(left, right SourceHash) int { return compare(left.Path, right.Path) }) {
		return errors.New("migration: staged generation artifact hashes are not canonical")
	}
	return validateHashes(value.Artifacts)
}

func canonicalArtifactHashes(root string) ([]SourceHash, error) {
	var hashes []SourceHash
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		top := strings.Split(filepath.ToSlash(relative), "/")[0]
		canonical := top == "policy.json" || top == "repositories.json" || top == "runs.jsonl" || top == "tasks.jsonl" ||
			top == "system-events.jsonl" || top == "task-source-neutral.json" || top == "activity" || top == generationRuntimeDir
		if !canonical {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration: staged artifact is a symlink: %s", relative)
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0o700 {
				return fmt.Errorf("migration: staged directory is not private: %s", relative)
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("migration: staged artifact is not private: %s", relative)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		hashes = append(hashes, SourceHash{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(digest[:]), Mode: 0o600, Size: int64(len(data))})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(hashes, func(left, right SourceHash) int { return compare(left.Path, right.Path) })
	return hashes, nil
}

func copyBackup(sourceRoot, destination string, receipt BackupReceipt) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("migration: create generation backup: %w", err)
	}
	for _, directory := range receipt.Directories {
		if directory.Path == "." {
			continue
		}
		path, err := safeRelativePath(destination, directory.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(path, os.FileMode(directory.Mode)); err != nil {
			return err
		}
	}
	for _, source := range receipt.Files {
		from, err := safeRelativePath(sourceRoot, source.Path)
		if err != nil {
			return err
		}
		to, err := safeRelativePath(destination, source.Path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if int64(len(data)) != source.Size || hex.EncodeToString(digest[:]) != source.SHA256 {
			return errors.New("migration: source changed while copying generation backup")
		}
		if err := writeExclusive(to, data, os.FileMode(source.Mode)); err != nil {
			return err
		}
	}
	return nil
}

func verifyBackup(root string, receipt BackupReceipt) error {
	hashes, err := hashTree(root)
	if err != nil || !slices.Equal(hashes, receipt.Files) {
		return errors.New("migration: generation backup file evidence changed")
	}
	directories, err := directoryModes(root)
	if err != nil || !slices.Equal(directories, receipt.Directories) {
		return errors.New("migration: generation backup directory evidence changed")
	}
	return nil
}

func safeRelativePath(root, relative string) (string, error) {
	if relative == "" || relative == "." || filepath.IsAbs(relative) {
		return "", errors.New("migration: unsafe relative generation path")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("migration: generation path escapes root")
	}
	return filepath.Join(root, clean), nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("migration: create generation root: %w", err)
	}
	return requirePrivateDirectory(path)
}

func requirePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("migration: generation directory is unsafe")
	}
	return nil
}

func writeExclusiveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeExclusive(path, append(data, '\n'), 0o600)
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func readStrictJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("migration: immutable artifact is unsafe: %s", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("migration: immutable artifact has trailing content")
	}
	return nil
}

func syncTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		return errors.Join(err, closeErr)
	})
	if err != nil {
		return fmt.Errorf("migration: sync staged generation files: %w", err)
	}
	slices.Reverse(directories)
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	return errors.Join(err, closeErr)
}
