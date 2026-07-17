package activation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/migration"
)

const providerAcknowledgementFile = "provider-finalization.json"

var (
	ErrReceiptPending            = errors.New("activation: successful deployment receipt is not available")
	ErrDeploymentLockUnavailable = errors.New("activation: provider deployment lock is unavailable")
	hex40Pattern                 = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64Pattern                 = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type BuildIdentity struct {
	Commit          string
	Tree            string
	BuildID         string
	DeploymentID    string
	ContractVersion int
}

type FinalizerConfig struct {
	Home             string
	StateRoot        string
	DataRoot         string
	GenerationPath   string
	ReceiptPath      string
	CurrentPath      string
	ExecutablePath   string
	RuntimeArtifacts []string
	Identity         BuildIdentity
	Now              func() time.Time
	Inject           func(string) error
}

type Activation struct {
	Lease           *Lease
	Generation      migration.Generation
	Acknowledgement ProviderAcknowledgement
	Boundary        WriteBoundary
}

func (a *Activation) Close() error {
	if a == nil {
		return nil
	}
	return a.Lease.Close()
}

type deploymentReceipt struct {
	ContractVersion  int       `json:"contractVersion"`
	CommandVersion   int       `json:"commandVersion"`
	DeploymentID     string    `json:"deploymentId"`
	BuildID          string    `json:"buildId"`
	Status           string    `json:"status"`
	App              string    `json:"app"`
	SourceRepository string    `json:"sourceRepository"`
	SourceBranch     string    `json:"sourceBranch"`
	SourceCommit     string    `json:"sourceCommit"`
	SourceTree       string    `json:"sourceTree"`
	ManifestSHA256   string    `json:"manifestSha256"`
	BinarySHA256     string    `json:"binarySha256"`
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt"`
	Message          string    `json:"message,omitempty"`
}

type FinalizedArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
}

type ProviderAcknowledgement struct {
	ContractVersion int                 `json:"contractVersion"`
	StateGeneration int                 `json:"stateGeneration"`
	MigrationID     string              `json:"migrationId"`
	DeploymentID    string              `json:"deploymentId"`
	SourceCommit    string              `json:"sourceCommit"`
	SourceTree      string              `json:"sourceTree"`
	BuildID         string              `json:"buildId"`
	CurrentTarget   string              `json:"currentTarget"`
	ReceiptSHA256   string              `json:"receiptSha256"`
	Artifacts       []FinalizedArtifact `json:"artifacts"`
	FinalizedAt     time.Time           `json:"finalizedAt"`
}

// SelectedGenerationPath resolves the one selected generation from the
// provider acknowledgement and cross-checks the immutable generation plus its
// monotonic write boundary. It performs no mutation and grants no advancement
// authority; a restart must still call Finalize for the returned path so it
// reacquires the state lease and revalidates the live provider graph.
func SelectedGenerationPath(stateRoot, dataRoot string) (string, error) {
	stateRoot, dataRoot = filepath.Clean(stateRoot), filepath.Clean(dataRoot)
	if !filepath.IsAbs(stateRoot) || !filepath.IsAbs(dataRoot) || dataRoot != filepath.Join(stateRoot, "data") {
		return "", errors.New("activation: Factory state paths are invalid")
	}
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
	boundary, err := ReadWriteBoundary(path)
	if err != nil {
		return "", err
	}
	selected, err := migration.OpenSelectedGeneration(path)
	if err != nil {
		return "", err
	}
	defer selected.Close()
	if selected.Generation.Manifest.MigrationID != acknowledgement.MigrationID || selected.Generation.Manifest.StateGeneration != selection.StateGeneration {
		return "", errors.New("activation: selected generation conflicts with provider acknowledgement")
	}
	if boundary.MigrationID != acknowledgement.MigrationID || boundary.DeploymentID != acknowledgement.DeploymentID ||
		boundary.SourceCommit != acknowledgement.SourceCommit || boundary.StateGeneration != acknowledgement.StateGeneration ||
		boundary.StartedAt != acknowledgement.FinalizedAt {
		return "", errors.New("activation: selected generation boundary conflicts with provider acknowledgement")
	}
	return path, nil
}

// Finalize acquires the state lease before the provider lock, proves the exact
// successful deployment graph, fsyncs it, writes the provider acknowledgement,
// then publishes selection and the write boundary. Its returned Activation
// intentionally retains the lease until service shutdown.
func Finalize(ctx context.Context, config FinalizerConfig) (*Activation, error) {
	if err := validateFinalizerConfig(config); err != nil {
		return nil, err
	}
	generation, err := migration.OpenStagedGeneration(config.GenerationPath)
	if err != nil {
		return nil, err
	}
	if generation.Path != filepath.Join(config.StateRoot, "generations", generation.Manifest.MigrationID) {
		return nil, errors.New("activation: staged generation is outside the Factory runtime")
	}
	lease, err := AcquireLease(filepath.Join(config.DataRoot, "state-transition.lock"))
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = lease.Close()
		}
	}()
	providerLock, err := acquireProviderLock(ctx, config.StateRoot)
	if err != nil {
		return nil, err
	}
	defer providerLock.Close()
	if err := injectFinalizer(config, "after-provider-lock"); err != nil {
		return nil, err
	}
	receipt, receiptDigest, currentTarget, artifacts, err := validateProviderGraph(config)
	if err != nil {
		return nil, err
	}
	if err := migration.VerifySourceSnapshot(config.DataRoot, generation.Report); err != nil {
		return nil, err
	}
	if err := syncProviderGraph(currentTarget, config.ReceiptPath, config.CurrentPath, config.RuntimeArtifacts); err != nil {
		return nil, err
	}
	if err := injectFinalizer(config, "after-provider-sync"); err != nil {
		return nil, err
	}
	finalizedAt := config.Now().UTC()
	if finalizedAt.IsZero() {
		return nil, errors.New("activation: finalization time is required")
	}
	acknowledgement := ProviderAcknowledgement{
		ContractVersion: selectionContractVersion, StateGeneration: generation.Manifest.StateGeneration,
		MigrationID: generation.Manifest.MigrationID, DeploymentID: receipt.DeploymentID,
		SourceCommit: receipt.SourceCommit, SourceTree: receipt.SourceTree, BuildID: receipt.BuildID,
		CurrentTarget: currentTarget, ReceiptSHA256: receiptDigest, Artifacts: artifacts,
		FinalizedAt: finalizedAt,
	}
	acknowledgement, err = installAcknowledgement(filepath.Join(config.DataRoot, providerAcknowledgementFile), acknowledgement)
	if err != nil {
		return nil, err
	}
	if err := injectFinalizer(config, "after-provider-acknowledgement"); err != nil {
		return nil, err
	}
	boundaryTime := acknowledgement.FinalizedAt
	if existing, err := ReadWriteBoundary(config.GenerationPath); err == nil {
		boundaryTime = existing.StartedAt
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	boundary, err := publishSelection(config.DataRoot, config.GenerationPath, lease, PublishOptions{
		DeploymentID: receipt.DeploymentID, SourceCommit: receipt.SourceCommit,
		Now: boundaryTime, Inject: config.Inject,
	})
	if err != nil {
		return nil, err
	}
	succeeded = true
	return &Activation{Lease: lease, Generation: generation, Acknowledgement: acknowledgement, Boundary: boundary}, nil
}

// ResumeSelected reacquires continuous advancement authority for an already
// selected generation. Unlike Finalize, it validates mutable canonical stores
// by strict replay instead of comparing them with staging hashes, and it never
// rewrites acknowledgement, selection, or boundary evidence.
func ResumeSelected(ctx context.Context, config FinalizerConfig) (*Activation, error) {
	if err := validateFinalizerConfig(config); err != nil {
		return nil, err
	}
	selectedPath, err := SelectedGenerationPath(config.StateRoot, config.DataRoot)
	if err != nil {
		return nil, err
	}
	if selectedPath != config.GenerationPath {
		return nil, errors.New("activation: configured generation is not selected")
	}
	lease, err := AcquireLease(filepath.Join(config.DataRoot, "state-transition.lock"))
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = lease.Close()
		}
	}()
	providerLock, err := acquireProviderLock(ctx, config.StateRoot)
	if err != nil {
		return nil, err
	}
	defer providerLock.Close()
	receipt, receiptDigest, currentTarget, artifacts, err := validateProviderGraph(config)
	if err != nil {
		return nil, err
	}
	var acknowledgement ProviderAcknowledgement
	if err := readExactJSON(filepath.Join(config.DataRoot, providerAcknowledgementFile), &acknowledgement); err != nil {
		return nil, err
	}
	selected, err := migration.OpenSelectedGeneration(selectedPath)
	if err != nil {
		return nil, err
	}
	defer selected.Close()
	if err := migration.VerifySourceSnapshot(config.DataRoot, selected.Generation.Report); err != nil {
		return nil, err
	}
	boundary, err := ReadWriteBoundary(selectedPath)
	if err != nil {
		return nil, err
	}
	expected := ProviderAcknowledgement{
		ContractVersion: selectionContractVersion, StateGeneration: selected.Generation.Manifest.StateGeneration,
		MigrationID: selected.Generation.Manifest.MigrationID, DeploymentID: receipt.DeploymentID,
		SourceCommit: receipt.SourceCommit, SourceTree: receipt.SourceTree, BuildID: receipt.BuildID,
		CurrentTarget: currentTarget, ReceiptSHA256: receiptDigest, Artifacts: artifacts,
		FinalizedAt: acknowledgement.FinalizedAt,
	}
	if !reflect.DeepEqual(acknowledgement, expected) || boundary.MigrationID != acknowledgement.MigrationID ||
		boundary.DeploymentID != acknowledgement.DeploymentID || boundary.SourceCommit != acknowledgement.SourceCommit ||
		boundary.StateGeneration != acknowledgement.StateGeneration || boundary.StartedAt != acknowledgement.FinalizedAt {
		return nil, errors.New("activation: selected deployment graph changed after finalization")
	}
	succeeded = true
	return &Activation{Lease: lease, Generation: selected.Generation, Acknowledgement: acknowledgement, Boundary: boundary}, nil
}

func validateFinalizerConfig(config FinalizerConfig) error {
	for _, path := range []string{config.Home, config.StateRoot, config.DataRoot, config.GenerationPath, config.ReceiptPath, config.CurrentPath, config.ExecutablePath} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("activation: canonical absolute finalizer paths are required")
		}
	}
	if config.StateRoot != filepath.Join(config.Home, ".local", "share", "factory") || config.DataRoot != filepath.Join(config.StateRoot, "data") || config.CurrentPath != filepath.Join(config.StateRoot, "current") ||
		config.ReceiptPath != filepath.Join(config.StateRoot, "deployments", "current.json") {
		return errors.New("activation: provider paths do not match the Factory runtime")
	}
	identity := config.Identity
	if !hex40Pattern.MatchString(identity.Commit) || !hex40Pattern.MatchString(identity.Tree) || identity.BuildID == "" || identity.DeploymentID == "" || identity.ContractVersion != deploymentContract || config.Now == nil {
		return errors.New("activation: exact build identity is required")
	}
	expectedRuntimeArtifacts := []string{
		filepath.Join(config.Home, ".local", "bin", "factory-run"),
		filepath.Join(config.Home, "Library", "LaunchAgents", "com.nags.factory.plist"),
	}
	if !slices.Equal(config.RuntimeArtifacts, expectedRuntimeArtifacts) {
		return errors.New("activation: complete Factory runtime artifact inventory is required")
	}
	for _, path := range config.RuntimeArtifacts {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("activation: runtime artifact paths must be canonical absolute paths")
		}
	}
	return nil
}

func validateProviderGraph(config FinalizerConfig) (deploymentReceipt, string, string, []FinalizedArtifact, error) {
	var receipt deploymentReceipt
	if err := readExactJSON(config.ReceiptPath, &receipt); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return deploymentReceipt{}, "", "", nil, ErrReceiptPending
		}
		return deploymentReceipt{}, "", "", nil, err
	}
	identity := config.Identity
	if receipt.Status != "success" || receipt.App != "factory" || receipt.SourceRepository != "tomnagengast/factory" || receipt.SourceBranch != "main" || receipt.CommandVersion < 1 ||
		receipt.ContractVersion != identity.ContractVersion || receipt.DeploymentID != identity.DeploymentID || receipt.BuildID != identity.BuildID ||
		receipt.SourceCommit != identity.Commit || receipt.SourceTree != identity.Tree || !hex64Pattern.MatchString(receipt.ManifestSHA256) ||
		!hex64Pattern.MatchString(receipt.BinarySHA256) || receipt.StartedAt.IsZero() || receipt.StartedAt.Location() != time.UTC ||
		receipt.FinishedAt.IsZero() || receipt.FinishedAt.Location() != time.UTC || receipt.FinishedAt.Before(receipt.StartedAt) {
		return deploymentReceipt{}, "", "", nil, errors.New("activation: deployment receipt does not match the running release")
	}
	currentInfo, err := os.Lstat(config.CurrentPath)
	if err != nil || currentInfo.Mode()&os.ModeSymlink == 0 {
		return deploymentReceipt{}, "", "", nil, errors.New("activation: Factory current selection is invalid")
	}
	currentTarget, err := os.Readlink(config.CurrentPath)
	if err != nil || !filepath.IsAbs(currentTarget) || filepath.Clean(currentTarget) != currentTarget ||
		currentTarget != filepath.Join(config.StateRoot, "releases", receipt.DeploymentID) {
		return deploymentReceipt{}, "", "", nil, errors.New("activation: Factory current selection does not match the receipt")
	}
	releaseInfo, err := os.Lstat(currentTarget)
	releasesInfo, releasesErr := os.Lstat(filepath.Dir(currentTarget))
	if err != nil || releasesErr != nil || !releaseInfo.IsDir() || releaseInfo.Mode()&os.ModeSymlink != 0 ||
		!releasesInfo.IsDir() || releasesInfo.Mode()&os.ModeSymlink != 0 {
		return deploymentReceipt{}, "", "", nil, errors.New("activation: Factory release directories are unsafe")
	}
	manifestPath := filepath.Join(currentTarget, "nags.toml")
	binaryPath := filepath.Join(currentTarget, "factory")
	manifestArtifact, err := finalizedArtifact(manifestPath)
	if err != nil || manifestArtifact.SHA256 != receipt.ManifestSHA256 {
		return deploymentReceipt{}, "", "", nil, errors.New("activation: release manifest does not match the receipt")
	}
	binaryArtifact, err := finalizedArtifact(binaryPath)
	if err != nil || binaryArtifact.SHA256 != receipt.BinarySHA256 {
		return deploymentReceipt{}, "", "", nil, errors.New("activation: release binary does not match the receipt")
	}
	executableInfo, err := os.Stat(config.ExecutablePath)
	binaryInfo, binaryErr := os.Stat(binaryPath)
	if err != nil || binaryErr != nil || !os.SameFile(executableInfo, binaryInfo) {
		return deploymentReceipt{}, "", "", nil, errors.New("activation: running executable is not the selected release binary")
	}
	artifacts := []FinalizedArtifact{manifestArtifact, binaryArtifact}
	for _, path := range config.RuntimeArtifacts {
		artifact, err := finalizedArtifact(path)
		if err != nil {
			return deploymentReceipt{}, "", "", nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	slices.SortFunc(artifacts, func(left, right FinalizedArtifact) int { return strings.Compare(left.Path, right.Path) })
	receiptData, err := os.ReadFile(config.ReceiptPath)
	if err != nil {
		return deploymentReceipt{}, "", "", nil, err
	}
	digest := sha256.Sum256(receiptData)
	return receipt, hex.EncodeToString(digest[:]), currentTarget, artifacts, nil
}

func finalizedArtifact(path string) (FinalizedArtifact, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return FinalizedArtifact{}, fmt.Errorf("activation: provider artifact is unsafe: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FinalizedArtifact{}, err
	}
	digest := sha256.Sum256(data)
	return FinalizedArtifact{Path: path, SHA256: hex.EncodeToString(digest[:]), Mode: uint32(info.Mode().Perm()), Size: info.Size()}, nil
}

func installAcknowledgement(path string, expected ProviderAcknowledgement) (ProviderAcknowledgement, error) {
	var existing ProviderAcknowledgement
	if err := readExactJSON(path, &existing); err == nil {
		candidate := expected
		candidate.FinalizedAt = existing.FinalizedAt
		if existing.FinalizedAt.IsZero() || !reflect.DeepEqual(existing, candidate) {
			return ProviderAcknowledgement{}, errors.New("activation: provider acknowledgement conflicts with current deployment")
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProviderAcknowledgement{}, err
	}
	if err := installExactJSON(path, expected); err != nil {
		return ProviderAcknowledgement{}, err
	}
	return expected, nil
}

type providerLock struct {
	path string
}

func acquireProviderLock(ctx context.Context, stateRoot string) (*providerLock, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	path := filepath.Join(stateRoot, ".deployment-lock")
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrDeploymentLockUnavailable
		}
		return nil, err
	}
	owner := filepath.Join(path, "owner")
	if err := os.WriteFile(owner, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	if err := syncDirectory(path); err != nil {
		_ = os.Remove(owner)
		_ = os.Remove(path)
		return nil, err
	}
	return &providerLock{path: path}, nil
}

func (l *providerLock) Close() error {
	if l == nil || l.path == "" {
		return nil
	}
	ownerErr := os.Remove(filepath.Join(l.path, "owner"))
	parent := filepath.Dir(l.path)
	directoryErr := os.Remove(l.path)
	l.path = ""
	return errors.Join(ownerErr, directoryErr, syncDirectory(parent))
}

func syncProviderGraph(release, receipt, current string, runtimeArtifacts []string) error {
	if err := syncRecursive(release); err != nil {
		return err
	}
	paths := append([]string{receipt}, runtimeArtifacts...)
	parents := map[string]bool{filepath.Dir(current): true, filepath.Dir(receipt): true, filepath.Dir(release): true}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return err
		}
		parents[filepath.Dir(path)] = true
	}
	for parent := range parents {
		if err := syncDirectory(parent); err != nil {
			return err
		}
	}
	return nil
}

func syncRecursive(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
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
		return err
	}
	slices.Reverse(directories)
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func injectFinalizer(config FinalizerConfig, point string) error {
	if config.Inject == nil {
		return nil
	}
	if err := config.Inject(point); err != nil {
		return fmt.Errorf("activation: injected %s failure: %w", point, err)
	}
	return nil
}
