package activation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/migration"
)

const (
	restorationPendingFile = "state-restoration-pending.json"
	restorationReceiptFile = "state-restoration.json"
)

var deploymentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

type RestorationReceipt struct {
	ContractVersion  int           `json:"contractVersion"`
	StateGeneration  int           `json:"stateGeneration"`
	MigrationID      string        `json:"migrationId"`
	SourceRootDigest string        `json:"sourceRootDigest"`
	ArchivedPath     string        `json:"archivedPath"`
	Boundary         WriteBoundary `json:"boundary"`
	RestoredAt       time.Time     `json:"restoredAt"`
}

type RestoreOptions struct {
	DataRoot         string
	MigrationReceipt string
	Lease            *Lease
	LiveSessions     func() ([]string, error)
	Now              time.Time
	Inject           func(string) error
}

// RollbackPreflight proves that the target is a retained successful Factory
// release and no selected canonical generation can advance against stale
// source stores. A selected generation must first cross RestoreState.
func RollbackPreflight(dataRoot, toDeployment string, lease *Lease) error {
	dataRoot = filepath.Clean(dataRoot)
	if err := validateRecoveryRoot(dataRoot); err != nil {
		return err
	}
	if err := requireLease(dataRoot, lease); err != nil {
		return err
	}
	if !deploymentIDPattern.MatchString(toDeployment) {
		return errors.New("activation: rollback target deployment is invalid")
	}
	stateRoot := filepath.Dir(dataRoot)
	var target deploymentReceipt
	if err := readExactJSON(filepath.Join(stateRoot, "deployments", "history", toDeployment+".json"), &target); err != nil {
		return fmt.Errorf("activation: read rollback target receipt: %w", err)
	}
	if target.Status != "success" || target.App != "factory" || target.DeploymentID != toDeployment ||
		target.ContractVersion != deploymentContract || target.SourceRepository != "tomnagengast/factory" ||
		target.SourceBranch != "main" || !hex40Pattern.MatchString(target.SourceCommit) ||
		!hex40Pattern.MatchString(target.SourceTree) || !hex64Pattern.MatchString(target.ManifestSHA256) ||
		!hex64Pattern.MatchString(target.BinarySHA256) {
		return errors.New("activation: rollback target receipt is not an exact successful Factory deployment")
	}
	if _, err := os.Lstat(filepath.Join(dataRoot, restorationPendingFile)); err == nil {
		return errors.New("activation: state restoration is incomplete")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := ReadSelection(dataRoot); err == nil {
		return errors.New("activation: canonical state is selected; state-restore is required before rollback")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PrepareRollback removes only a selected generation whose monotonic write
// boundary was never established. Selected state with a boundary must cross
// the explicit whole-state restoration proof instead.
func PrepareRollback(dataRoot string, lease *Lease) error {
	dataRoot = filepath.Clean(dataRoot)
	if err := validateRecoveryRoot(dataRoot); err != nil {
		return err
	}
	if err := requireLease(dataRoot, lease); err != nil {
		return err
	}
	if _, err := ReadSelection(dataRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	generationPath, err := selectedRecoveryGenerationPath(filepath.Dir(dataRoot), dataRoot)
	if err != nil {
		return err
	}
	if err := DeactivatePreWrite(dataRoot, generationPath, lease); err != nil {
		if strings.Contains(err.Error(), "writes started") {
			return errors.New("activation: canonical state changed; state-restore is required before rollback")
		}
		return err
	}
	return nil
}

func selectedRecoveryGenerationPath(stateRoot, dataRoot string) (string, error) {
	selection, err := ReadSelection(dataRoot)
	if err != nil {
		return "", err
	}
	var acknowledgement ProviderAcknowledgement
	if err := readExactJSON(filepath.Join(dataRoot, providerAcknowledgementFile), &acknowledgement); err != nil {
		return "", err
	}
	if acknowledgement.ContractVersion != selection.ContractVersion || acknowledgement.StateGeneration != selection.StateGeneration ||
		acknowledgement.MigrationID == "" || acknowledgement.DeploymentID == "" || !hex40Pattern.MatchString(acknowledgement.SourceCommit) ||
		!hex40Pattern.MatchString(acknowledgement.SourceTree) || acknowledgement.BuildID == "" || acknowledgement.FinalizedAt.IsZero() ||
		acknowledgement.FinalizedAt.Location() != time.UTC || !hex64Pattern.MatchString(acknowledgement.ReceiptSHA256) {
		return "", errors.New("activation: provider acknowledgement is invalid")
	}
	path := filepath.Join(stateRoot, "generations", acknowledgement.MigrationID)
	generation, err := migration.OpenStagedGeneration(path)
	if err != nil {
		if _, boundaryErr := ReadWriteBoundary(path); boundaryErr == nil {
			return path, nil
		}
		return "", err
	}
	if generation.Manifest.MigrationID != acknowledgement.MigrationID || generation.Manifest.StateGeneration != selection.StateGeneration {
		return "", errors.New("activation: selected generation conflicts with provider acknowledgement")
	}
	return path, nil
}

// RestoreState is deliberately narrow: it can deactivate generation 1 only
// when its immutable activation inventory was empty, no live agent session
// exists, every mutable canonical artifact still equals its initial staged
// bytes, and the untouched source snapshot plus whole backup both validate.
func RestoreState(options RestoreOptions) (RestorationReceipt, error) {
	dataRoot := filepath.Clean(options.DataRoot)
	if err := validateRecoveryRoot(dataRoot); err != nil {
		return RestorationReceipt{}, err
	}
	if err := requireLease(dataRoot, options.Lease); err != nil {
		return RestorationReceipt{}, err
	}
	if options.MigrationReceipt == "" || !filepath.IsAbs(options.MigrationReceipt) ||
		filepath.Clean(options.MigrationReceipt) != options.MigrationReceipt || options.LiveSessions == nil ||
		options.Now.IsZero() || options.Now.Location() != time.UTC {
		return RestorationReceipt{}, errors.New("activation: exact restoration evidence is required")
	}
	generationPath := filepath.Dir(options.MigrationReceipt)
	if filepath.Base(options.MigrationReceipt) != "backup-receipt.json" ||
		filepath.Dir(generationPath) != filepath.Join(filepath.Dir(dataRoot), "generations") {
		return RestorationReceipt{}, errors.New("activation: migration receipt is outside the selected generation")
	}
	stateRoot := filepath.Dir(dataRoot)
	archiveRoot := filepath.Join(stateRoot, "restorations")
	pendingPath := filepath.Join(dataRoot, restorationPendingFile)
	var receipt RestorationReceipt
	resuming := false
	if err := readExactJSON(pendingPath, &receipt); err == nil {
		resuming = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return RestorationReceipt{}, err
	}
	candidatePath := generationPath
	if resuming {
		if err := validatePrivateRecoveryDirectory(archiveRoot); err != nil {
			return RestorationReceipt{}, err
		}
		wantArchive := filepath.Join(archiveRoot, receipt.MigrationID)
		if receipt.ContractVersion != selectionContractVersion || receipt.StateGeneration != 1 ||
			receipt.MigrationID == "" || receipt.SourceRootDigest == "" || receipt.ArchivedPath != wantArchive ||
			receipt.RestoredAt.IsZero() || receipt.RestoredAt.Location() != time.UTC {
			return RestorationReceipt{}, errors.New("activation: pending restoration receipt is invalid")
		}
		originalExists, err := generationDirectoryExists(generationPath)
		if err != nil {
			return RestorationReceipt{}, err
		}
		archiveExists, err := generationDirectoryExists(receipt.ArchivedPath)
		if err != nil {
			return RestorationReceipt{}, err
		}
		if originalExists == archiveExists {
			return RestorationReceipt{}, errors.New("activation: pending restoration generation location is ambiguous")
		}
		if archiveExists {
			candidatePath = receipt.ArchivedPath
		}
	} else {
		selectedPath, err := SelectedGenerationPath(stateRoot, dataRoot)
		if err != nil {
			return RestorationReceipt{}, err
		}
		if selectedPath != generationPath {
			return RestorationReceipt{}, errors.New("activation: migration receipt does not own the selected generation")
		}
	}
	generation, err := migration.OpenStagedGeneration(candidatePath)
	if err != nil {
		return RestorationReceipt{}, fmt.Errorf("activation: canonical state changed after activation: %w", err)
	}
	if len(generation.Manifest.Activation.NonterminalRuns) != 0 || len(generation.Manifest.Activation.LiveSessions) != 0 {
		return RestorationReceipt{}, errors.New("activation: activation-spanning work makes restoration unsafe")
	}
	sessions, err := options.LiveSessions()
	if err != nil {
		return RestorationReceipt{}, fmt.Errorf("activation: inspect live agent sessions: %w", err)
	}
	if len(sessions) != 0 {
		return RestorationReceipt{}, errors.New("activation: live agent sessions make restoration unsafe")
	}
	if err := migration.VerifySourceSnapshot(dataRoot, generation.Report); err != nil {
		return RestorationReceipt{}, fmt.Errorf("activation: retained source state changed: %w", err)
	}
	boundary, err := ReadWriteBoundary(candidatePath)
	if err != nil {
		return RestorationReceipt{}, err
	}
	if resuming {
		if receipt.StateGeneration != generation.Manifest.StateGeneration || receipt.MigrationID != generation.Manifest.MigrationID ||
			receipt.SourceRootDigest != generation.Manifest.SourceRootDigest || receipt.Boundary != boundary {
			return RestorationReceipt{}, errors.New("activation: pending restoration receipt conflicts with generation evidence")
		}
	} else {
		if err := ensurePrivateRecoveryDirectory(archiveRoot); err != nil {
			return RestorationReceipt{}, err
		}
		archivePath := filepath.Join(archiveRoot, generation.Manifest.MigrationID)
		if _, err := os.Lstat(archivePath); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				err = errors.New("archive already exists")
			}
			return RestorationReceipt{}, fmt.Errorf("activation: inspect restoration archive: %w", err)
		}
		receipt = RestorationReceipt{
			ContractVersion: selectionContractVersion, StateGeneration: generation.Manifest.StateGeneration,
			MigrationID: generation.Manifest.MigrationID, SourceRootDigest: generation.Manifest.SourceRootDigest,
			ArchivedPath: archivePath, Boundary: boundary, RestoredAt: options.Now,
		}
		if err := installExactJSON(pendingPath, receipt); err != nil {
			return RestorationReceipt{}, err
		}
		if err := injectRestore(options, "after-pending-receipt"); err != nil {
			return RestorationReceipt{}, err
		}
	}
	for _, path := range []string{filepath.Join(dataRoot, selectionFileName), filepath.Join(dataRoot, providerAcknowledgementFile)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return RestorationReceipt{}, err
		}
	}
	if err := syncDirectory(dataRoot); err != nil {
		return RestorationReceipt{}, err
	}
	if err := injectRestore(options, "after-deactivation"); err != nil {
		return RestorationReceipt{}, err
	}
	if candidatePath == generationPath {
		if err := os.Rename(generationPath, receipt.ArchivedPath); err != nil {
			return RestorationReceipt{}, err
		}
		if err := syncDirectory(filepath.Dir(generationPath)); err != nil {
			return RestorationReceipt{}, err
		}
		if err := syncDirectory(archiveRoot); err != nil {
			return RestorationReceipt{}, err
		}
	}
	if err := injectRestore(options, "after-generation-archive"); err != nil {
		return RestorationReceipt{}, err
	}
	if err := installExactJSON(filepath.Join(dataRoot, restorationReceiptFile), receipt); err != nil {
		return RestorationReceipt{}, err
	}
	if err := injectRestore(options, "after-final-receipt"); err != nil {
		return RestorationReceipt{}, err
	}
	if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RestorationReceipt{}, err
	}
	if err := syncDirectory(dataRoot); err != nil {
		return RestorationReceipt{}, err
	}
	return receipt, nil
}

func generationDirectoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("activation: restoration generation path is unsafe")
	}
	return true, nil
}

func validateRecoveryRoot(dataRoot string) error {
	if !filepath.IsAbs(dataRoot) || filepath.Base(dataRoot) != "data" || filepath.Clean(dataRoot) != dataRoot {
		return errors.New("activation: canonical recovery data root is invalid")
	}
	return nil
}

func ensurePrivateRecoveryDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return validatePrivateRecoveryDirectory(path)
}

func validatePrivateRecoveryDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("activation: restoration archive directory is unsafe")
	}
	return nil
}

func injectRestore(options RestoreOptions, point string) error {
	if options.Inject == nil {
		return nil
	}
	if err := options.Inject(point); err != nil {
		return fmt.Errorf("activation: injected %s failure: %w", point, err)
	}
	return nil
}
