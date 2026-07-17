package activation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackPreflightValidatesTargetAndRefusesSelectedState(t *testing.T) {
	t.Parallel()
	config, _ := finalizerFixture(t)
	installTargetHistory(t, config)
	lease, err := AcquireLease(filepath.Join(config.DataRoot, "state-transition.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := RollbackPreflight(config.DataRoot, config.Identity.DeploymentID, lease); err != nil {
		t.Fatalf("legacy-source preflight: %v", err)
	}
	lease.Close()
	active, err := Finalize(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	active.Close()
	lease, err = AcquireLease(filepath.Join(config.DataRoot, "state-transition.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := RollbackPreflight(config.DataRoot, config.Identity.DeploymentID, lease); err == nil ||
		!strings.Contains(err.Error(), "state-restore") {
		t.Fatalf("selected-state preflight error = %v", err)
	}
	if err := RollbackPreflight(config.DataRoot, "missing", lease); err == nil ||
		!strings.Contains(err.Error(), "target receipt") {
		t.Fatalf("missing-target preflight error = %v", err)
	}
}

func TestRestoreStateRefusesActivationSpanningRunWithoutMutation(t *testing.T) {
	t.Parallel()
	config, _ := finalizerFixture(t)
	active, err := Finalize(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	active.Close()
	lease, err := AcquireLease(filepath.Join(config.DataRoot, "state-transition.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	_, err = RestoreState(RestoreOptions{
		DataRoot: config.DataRoot, MigrationReceipt: filepath.Join(config.GenerationPath, "backup-receipt.json"),
		Lease: lease, LiveSessions: func() ([]string, error) { return []string{}, nil }, Now: activationNow.Add(1),
	})
	if err == nil || !strings.Contains(err.Error(), "activation-spanning") {
		t.Fatalf("activation-spanning restore error = %v", err)
	}
	if _, err := ReadSelection(config.DataRoot); err != nil {
		t.Fatalf("refusal removed selection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.DataRoot, restorationPendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusal wrote pending restoration: %v", err)
	}
}

func TestRestoreStateArchivesExactNoWorkGenerationAndEnablesPreflight(t *testing.T) {
	t.Parallel()
	config, generation := terminalFinalizerFixture(t)
	installTargetHistory(t, config)
	active, err := Finalize(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	active.Close()
	lease, err := AcquireLease(filepath.Join(config.DataRoot, "state-transition.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	receipt, err := RestoreState(RestoreOptions{
		DataRoot: config.DataRoot, MigrationReceipt: filepath.Join(config.GenerationPath, "backup-receipt.json"),
		Lease: lease, LiveSessions: func() ([]string, error) { return []string{}, nil }, Now: activationNow.Add(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.MigrationID != generation.Manifest.MigrationID || receipt.ArchivedPath == config.GenerationPath {
		t.Fatalf("restoration receipt = %#v", receipt)
	}
	if _, err := os.Stat(receipt.ArchivedPath); err != nil {
		t.Fatalf("archived generation: %v", err)
	}
	for _, path := range []string{
		filepath.Join(config.DataRoot, selectionFileName), filepath.Join(config.DataRoot, providerAcknowledgementFile),
		filepath.Join(config.DataRoot, restorationPendingFile), config.GenerationPath,
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restoration residue %s: %v", path, err)
		}
	}
	if err := RollbackPreflight(config.DataRoot, config.Identity.DeploymentID, lease); err != nil {
		t.Fatalf("restored preflight: %v", err)
	}
}

func TestRestoreStatePendingReceiptBlocksRollbackAfterInterruptedRestore(t *testing.T) {
	t.Parallel()
	config, _ := terminalFinalizerFixture(t)
	installTargetHistory(t, config)
	active, err := Finalize(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	active.Close()
	lease, err := AcquireLease(filepath.Join(config.DataRoot, "state-transition.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	_, err = RestoreState(RestoreOptions{
		DataRoot: config.DataRoot, MigrationReceipt: filepath.Join(config.GenerationPath, "backup-receipt.json"),
		Lease: lease, LiveSessions: func() ([]string, error) { return []string{}, nil }, Now: activationNow.Add(1),
		Inject: func(point string) error {
			if point == "after-pending-receipt" {
				return errors.New("stop")
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "after-pending-receipt") {
		t.Fatalf("interrupted restore error = %v", err)
	}
	if err := RollbackPreflight(config.DataRoot, config.Identity.DeploymentID, lease); err == nil ||
		!strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("interrupted preflight error = %v", err)
	}
}

func installTargetHistory(t *testing.T, config FinalizerConfig) {
	t.Helper()
	var receipt deploymentReceipt
	if err := readExactJSON(config.ReceiptPath, &receipt); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(config.StateRoot, "deployments", "history")
	if err := os.MkdirAll(history, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installExactJSON(filepath.Join(history, config.Identity.DeploymentID+".json"), receipt); err != nil {
		t.Fatal(err)
	}
}
