package activation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/tomnagengast/factory/internal/migration"
)

const (
	selectionContractVersion = 1
	deploymentContract       = 1
	selectionFileName        = "state-generation.json"
	writeBoundaryFileName    = "canonicalWritesStarted"
)

type StateSelection struct {
	ContractVersion    int `json:"contractVersion"`
	StateGeneration    int `json:"stateGeneration"`
	DeploymentContract int `json:"deploymentContract"`
}

type WriteBoundary struct {
	ContractVersion int       `json:"contractVersion"`
	StateGeneration int       `json:"stateGeneration"`
	MigrationID     string    `json:"migrationId"`
	DeploymentID    string    `json:"deploymentId"`
	SourceCommit    string    `json:"sourceCommit"`
	StartedAt       time.Time `json:"startedAt"`
}

type PublishOptions struct {
	DeploymentID string
	SourceCommit string
	Now          time.Time
	Inject       func(string) error
}

// publishSelection validates an unchanged staged generation, atomically makes
// it provider-visible, then establishes the monotonic write boundary before a
// caller may start any advancing manager.
func publishSelection(dataRoot, generationPath string, lease *Lease, options PublishOptions) (WriteBoundary, error) {
	dataRoot = filepath.Clean(dataRoot)
	if err := requireLease(dataRoot, lease); err != nil {
		return WriteBoundary{}, err
	}
	if options.DeploymentID == "" || options.SourceCommit == "" || options.Now.IsZero() || options.Now.Location() != time.UTC {
		return WriteBoundary{}, errors.New("activation: exact deployment identity is required")
	}
	generation, err := migration.OpenStagedGeneration(generationPath)
	if err != nil {
		return WriteBoundary{}, fmt.Errorf("activation: validate staged generation: %w", err)
	}
	if err := migration.VerifySourceSnapshot(dataRoot, generation.Report); err != nil {
		return WriteBoundary{}, fmt.Errorf("activation: revalidate migration source: %w", err)
	}
	selection := StateSelection{
		ContractVersion: selectionContractVersion, StateGeneration: generation.Manifest.StateGeneration,
		DeploymentContract: deploymentContract,
	}
	selectionPath := filepath.Join(dataRoot, selectionFileName)
	if err := installExactJSON(selectionPath, selection); err != nil {
		return WriteBoundary{}, err
	}
	if err := injectPublish(options, "after-selection"); err != nil {
		return WriteBoundary{}, err
	}
	boundary := WriteBoundary{
		ContractVersion: selectionContractVersion, StateGeneration: generation.Manifest.StateGeneration,
		MigrationID: generation.Manifest.MigrationID, DeploymentID: options.DeploymentID,
		SourceCommit: options.SourceCommit, StartedAt: options.Now,
	}
	boundaryPath := filepath.Join(generation.Path, writeBoundaryFileName)
	if err := installExactJSON(boundaryPath, boundary); err != nil {
		return WriteBoundary{}, err
	}
	if err := syncDirectory(generation.Path); err != nil {
		return WriteBoundary{}, err
	}
	if err := injectPublish(options, "after-write-boundary"); err != nil {
		return WriteBoundary{}, err
	}
	return boundary, nil
}

func ReadSelection(dataRoot string) (StateSelection, error) {
	var value StateSelection
	if err := readExactJSON(filepath.Join(filepath.Clean(dataRoot), selectionFileName), &value); err != nil {
		return StateSelection{}, err
	}
	if value != (StateSelection{ContractVersion: selectionContractVersion, StateGeneration: 1, DeploymentContract: deploymentContract}) {
		return StateSelection{}, errors.New("activation: state selection is unsupported")
	}
	return value, nil
}

func ReadWriteBoundary(generationPath string) (WriteBoundary, error) {
	var value WriteBoundary
	if err := readExactJSON(filepath.Join(filepath.Clean(generationPath), writeBoundaryFileName), &value); err != nil {
		return WriteBoundary{}, err
	}
	if value.ContractVersion != selectionContractVersion || value.StateGeneration != 1 || value.MigrationID == "" ||
		value.DeploymentID == "" || value.SourceCommit == "" || value.StartedAt.IsZero() || value.StartedAt.Location() != time.UTC {
		return WriteBoundary{}, errors.New("activation: canonical write boundary is invalid")
	}
	return value, nil
}

// DeactivatePreWrite removes only a selector whose monotonic write boundary
// was never established. Once writes may have started, whole-backup recovery
// is required instead of manifest deactivation.
func DeactivatePreWrite(dataRoot, generationPath string, lease *Lease) error {
	dataRoot = filepath.Clean(dataRoot)
	if err := requireLease(dataRoot, lease); err != nil {
		return err
	}
	if _, err := ReadWriteBoundary(generationPath); err == nil {
		return errors.New("activation: canonical writes started; selector deactivation is unsafe")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	selectionPath := filepath.Join(dataRoot, selectionFileName)
	if err := os.Remove(selectionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(dataRoot)
}

func requireLease(dataRoot string, lease *Lease) error {
	if lease == nil || lease.file == nil || lease.Path() != filepath.Join(dataRoot, "state-transition.lock") {
		return errors.New("activation: matching live state-transition lease is required")
	}
	return validateLeaseFile(lease.path, lease.file)
}

func installExactJSON(path string, value any) error {
	if existing := reflect.New(reflect.TypeOf(value)); existing.IsValid() {
		if err := readExactJSON(path, existing.Interface()); err == nil {
			if reflect.DeepEqual(existing.Elem().Interface(), value) {
				return nil
			}
			return fmt.Errorf("activation: existing %s conflicts", filepath.Base(path))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	written, writeErr := temporary.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func readExactJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || stat.Nlink != 1 {
		return fmt.Errorf("activation: %s is unsafe", filepath.Base(path))
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
		return errors.New("activation: state artifact has trailing content")
	}
	return nil
}

func injectPublish(options PublishOptions, point string) error {
	if options.Inject == nil {
		return nil
	}
	if err := options.Inject(point); err != nil {
		return fmt.Errorf("activation: injected %s failure: %w", point, err)
	}
	return nil
}
