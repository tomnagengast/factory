package activation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAcquireLeasePublishesExactPrivateCapability(t *testing.T) {
	t.Parallel()
	path := filepath.Join(privateTemp(t), "state-transition.lock")
	lease, err := AcquireLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record leaseRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.ContractVersion != 1 || record.OwnerPID != os.Getpid() || record.OwnerStartTime == "" {
		t.Fatalf("lease record = %#v", record)
	}
	environment := lease.Environment()
	if len(environment) != 2 || !strings.HasPrefix(environment[0], "NAGS_FACTORY_STATE_LEASE_FD=") || !strings.HasPrefix(environment[1], "NAGS_FACTORY_STATE_LEASE_TOKEN=") {
		t.Fatalf("lease environment = %#v", environment)
	}
	descriptor, err := strconv.Atoi(strings.TrimPrefix(environment[0], "NAGS_FACTORY_STATE_LEASE_FD="))
	if err != nil {
		t.Fatal(err)
	}
	if uintptr(descriptor) != lease.file.Fd() {
		t.Fatal("lease environment descriptor changed")
	}
	descriptorInfo, err := lease.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pathInfo, err := os.Stat(path)
	if err != nil || !os.SameFile(descriptorInfo, pathInfo) {
		t.Fatalf("lease descriptor identity mismatch: %v", err)
	}
	token := strings.TrimPrefix(environment[1], "NAGS_FACTORY_STATE_LEASE_TOKEN=")
	digest := sha256.Sum256([]byte(token))
	if len(token) < 32 || record.TokenSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("lease token does not match its private digest")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lease mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestLeaseConfigureCommandPassesLockedInodeAndPrivateToken(t *testing.T) {
	path := filepath.Join(privateTemp(t), "state-transition.lock")
	lease, err := AcquireLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	command := exec.Command(os.Args[0], "-test.run=TestLeaseDescriptorHelper")
	command.Env = append(os.Environ(), "FACTORY_TEST_LEASE_DESCRIPTOR_HELPER=1", "FACTORY_TEST_LEASE_PATH="+path)
	if err := lease.ConfigureCommand(command); err != nil {
		t.Fatal(err)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("descriptor helper: %v: %s", err, output)
	}
}

func TestLeaseDescriptorHelper(t *testing.T) {
	if os.Getenv("FACTORY_TEST_LEASE_DESCRIPTOR_HELPER") != "1" {
		return
	}
	descriptor, err := strconv.Atoi(os.Getenv("NAGS_FACTORY_STATE_LEASE_FD"))
	if err != nil || descriptor < 3 || os.Getenv("NAGS_FACTORY_STATE_LEASE_TOKEN") == "" {
		t.Fatal("inherited lease capability is missing")
	}
	file := os.NewFile(uintptr(descriptor), "inherited-state-transition-lease")
	if file == nil {
		t.Fatal("inherited lease descriptor is invalid")
	}
	info, err := file.Stat()
	pathInfo, pathErr := os.Stat(os.Getenv("FACTORY_TEST_LEASE_PATH"))
	if err != nil || pathErr != nil || !os.SameFile(info, pathInfo) {
		t.Fatal("inherited lease descriptor changed identity")
	}
}

func TestQuiesceAndAcquireSignalsExactLeaseOwner(t *testing.T) {
	path := filepath.Join(privateTemp(t), "state-transition.lock")
	ready := path + ".ready"
	command := exec.Command(os.Args[0], "-test.run=TestLeaseOwnerHelper")
	command.Env = append(os.Environ(),
		"FACTORY_TEST_LEASE_HELPER=1", "FACTORY_TEST_LEASE_QUIESCE=1",
		"FACTORY_TEST_LEASE_PATH="+path, "FACTORY_TEST_LEASE_READY="+ready,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	lease, err := QuiesceAndAcquire(context.Background(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	command.Process = nil
}

func TestAcquireLeaseRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		setup func(string) error
	}{
		{name: "symlink", setup: func(path string) error {
			target := path + ".target"
			if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
		{name: "mode", setup: func(path string) error { return os.WriteFile(path, []byte("{}\n"), 0o644) }},
		{name: "hard link", setup: func(path string) error {
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				return err
			}
			return os.Link(path, path+".second")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(privateTemp(t), "state-transition.lock")
			if err := test.setup(path); err != nil {
				t.Fatal(err)
			}
			if lease, err := AcquireLease(path); err == nil {
				lease.Close()
				t.Fatal("unsafe lease was accepted")
			}
		})
	}
}

func TestAcquireLeaseRejectsForeignLockOwner(t *testing.T) {
	path := filepath.Join(privateTemp(t), "state-transition.lock")
	ready, release := path+".ready", path+".release"
	command := exec.Command(os.Args[0], "-test.run=TestLeaseOwnerHelper")
	command.Env = append(os.Environ(), "FACTORY_TEST_LEASE_HELPER=1", "FACTORY_TEST_LEASE_PATH="+path, "FACTORY_TEST_LEASE_READY="+ready, "FACTORY_TEST_LEASE_RELEASE="+release)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lease, err := AcquireLease(path); !errors.Is(err, ErrLeaseUnavailable) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("contended acquire error = %v", err)
	}
	if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	command.Process = nil
}

func TestLeaseOwnerHelper(t *testing.T) {
	if os.Getenv("FACTORY_TEST_LEASE_HELPER") != "1" {
		return
	}
	lease, err := AcquireLease(os.Getenv("FACTORY_TEST_LEASE_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	var quiesce chan os.Signal
	if os.Getenv("FACTORY_TEST_LEASE_QUIESCE") == "1" {
		quiesce = make(chan os.Signal, 1)
		signal.Notify(quiesce, syscall.SIGUSR1)
		defer signal.Stop(quiesce)
	}
	if err := os.WriteFile(os.Getenv("FACTORY_TEST_LEASE_READY"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if quiesce != nil {
		<-quiesce
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("FACTORY_TEST_LEASE_RELEASE")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("lease helper release timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func privateTemp(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
