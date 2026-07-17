package activation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/migration"
)

func TestFinalizeProvesProviderGraphBeforeSelectionAndRetainsLease(t *testing.T) {
	t.Parallel()
	config, generation := finalizerFixture(t)
	activation, err := Finalize(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(activation.Generation.Manifest, generation.Manifest) || activation.Boundary.MigrationID != generation.Manifest.MigrationID || activation.Acknowledgement.DeploymentID != config.Identity.DeploymentID {
		t.Fatalf("activation = %#v", activation)
	}
	if _, err := ReadSelection(config.DataRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWriteBoundary(config.GenerationPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config.DataRoot, providerAcknowledgementFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config.StateRoot, ".deployment-lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider lock remained after finalization: %v", err)
	}
	if activation.Lease == nil || len(activation.Lease.Environment()) != 2 {
		t.Fatal("activation did not retain its state-transition lease")
	}
	firstAcknowledgement := activation.Acknowledgement
	if err := activation.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Finalize(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if !reflect.DeepEqual(restarted.Acknowledgement, firstAcknowledgement) || restarted.Boundary != activation.Boundary {
		t.Fatalf("restart changed durable activation: %#v", restarted)
	}
}

func TestFinalizeRefusesReceiptOrReleaseMismatchBeforeAcknowledgement(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*FinalizerConfig)
	}{
		{name: "receipt identity", mutate: func(config *FinalizerConfig) {
			var receipt deploymentReceipt
			if err := readExactJSON(config.ReceiptPath, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt.SourceCommit = strings.Repeat("d", 40)
			if err := os.Remove(config.ReceiptPath); err != nil {
				t.Fatal(err)
			}
			if err := installExactJSON(config.ReceiptPath, receipt); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "release binary", mutate: func(config *FinalizerConfig) {
			file, err := os.OpenFile(config.ExecutablePath, os.O_WRONLY|os.O_APPEND, 0o700)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.WriteString("tamper")
			closeErr := file.Close()
			if err := errors.Join(writeErr, closeErr); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, _ := finalizerFixture(t)
			test.mutate(&config)
			if activation, err := Finalize(context.Background(), config); err == nil {
				activation.Close()
				t.Fatal("mismatched provider graph activated")
			}
			for _, path := range []string{filepath.Join(config.DataRoot, providerAcknowledgementFile), filepath.Join(config.DataRoot, selectionFileName)} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refusal created %s: %v", filepath.Base(path), err)
				}
			}
			if lease, err := AcquireLease(filepath.Join(config.DataRoot, "state-transition.lock")); err != nil {
				t.Fatalf("refusal leaked state lease: %v", err)
			} else {
				lease.Close()
			}
		})
	}
}

func TestFinalizePreservesAcknowledgementAcrossPostAckCrash(t *testing.T) {
	t.Parallel()
	config, _ := finalizerFixture(t)
	config.Inject = func(point string) error {
		if point == "after-provider-acknowledgement" {
			return errors.New("stop")
		}
		return nil
	}
	if activation, err := Finalize(context.Background(), config); err == nil {
		activation.Close()
		t.Fatal("injected finalization succeeded")
	}
	var first ProviderAcknowledgement
	if err := readExactJSON(filepath.Join(config.DataRoot, providerAcknowledgementFile), &first); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSelection(config.DataRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-ack crash published selector: %v", err)
	}
	config.Inject = nil
	config.Now = func() time.Time { return activationNow.Add(time.Hour) }
	activation, err := Finalize(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Close()
	if !reflect.DeepEqual(activation.Acknowledgement, first) {
		t.Fatalf("acknowledgement changed across recovery: first %#v recovered %#v", first, activation.Acknowledgement)
	}
}

func finalizerFixture(t *testing.T) (FinalizerConfig, migration.Generation) {
	t.Helper()
	dataRoot, generation := buildActivationFixture(t)
	stateRoot := filepath.Dir(dataRoot)
	home := filepath.Dir(filepath.Dir(filepath.Dir(stateRoot)))
	identity := BuildIdentity{
		Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40),
		BuildID: "build-1", DeploymentID: "deploy-1", ContractVersion: 1,
	}
	release := filepath.Join(stateRoot, "releases", identity.DeploymentID)
	if err := os.MkdirAll(release, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(release, "nags.toml")
	binaryPath := filepath.Join(release, "factory")
	if err := os.WriteFile(manifestPath, []byte("[app]\nname = \"factory\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("factory-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(release, filepath.Join(stateRoot, "current")); err != nil {
		t.Fatal(err)
	}
	runtimeArtifacts := []string{
		filepath.Join(home, ".local", "bin", "factory-run"),
		filepath.Join(home, "Library", "LaunchAgents", "com.nags.factory.plist"),
	}
	for _, directory := range []string{filepath.Dir(runtimeArtifacts[0]), filepath.Dir(runtimeArtifacts[1])} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range runtimeArtifacts {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifestDigest := sha256.Sum256([]byte("[app]\nname = \"factory\"\n"))
	binaryDigest := sha256.Sum256([]byte("factory-binary"))
	receiptPath := filepath.Join(stateRoot, "deployments", "current.json")
	if err := os.MkdirAll(filepath.Dir(receiptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	receipt := deploymentReceipt{
		ContractVersion: 1, CommandVersion: 1, DeploymentID: identity.DeploymentID, BuildID: identity.BuildID,
		Status: "success", App: "factory", SourceRepository: "tomnagengast/factory", SourceBranch: "main",
		SourceCommit: identity.Commit, SourceTree: identity.Tree, ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
		BinarySHA256: hex.EncodeToString(binaryDigest[:]), StartedAt: activationNow.Add(-time.Minute), FinishedAt: activationNow,
	}
	if err := installExactJSON(receiptPath, receipt); err != nil {
		t.Fatal(err)
	}
	return FinalizerConfig{
		Home: home, StateRoot: stateRoot, DataRoot: dataRoot, GenerationPath: generation.Path,
		ReceiptPath: receiptPath, CurrentPath: filepath.Join(stateRoot, "current"), ExecutablePath: binaryPath,
		RuntimeArtifacts: runtimeArtifacts, Identity: identity, Now: func() time.Time { return activationNow },
	}, generation
}
