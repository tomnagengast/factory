package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
)

func TestManagementHelpAndVersion(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	if code := runManagementHelp(nil, &output, &errorOutput); code != 0 {
		t.Fatalf("help exit = %d, stderr %q", code, errorOutput.String())
	}
	for _, command := range []string{"start", "status", "stop", "doctor", "serve"} {
		if !strings.Contains(output.String(), "  "+command) {
			t.Fatalf("help omits %q:\n%s", command, output.String())
		}
	}
	for _, internal := range []string{"agent-exec", "child-exec", "workflow-rollback-preflight"} {
		if strings.Contains(output.String(), internal) {
			t.Fatalf("help exposes internal command %q", internal)
		}
	}

	output.Reset()
	if code := runManagementVersion(nil, &output, &errorOutput); code != 0 {
		t.Fatalf("version exit = %d, stderr %q", code, errorOutput.String())
	}
	for _, identity := range []string{buildCommit, buildTree, buildID, buildDeploymentID, buildContractVersion} {
		if !strings.Contains(output.String(), identity) {
			t.Fatalf("version omits identity %q: %s", identity, output.String())
		}
	}

	if code := runManagementHelp([]string{"extra"}, &output, &errorOutput); code != 2 {
		t.Fatalf("help extra-argument exit = %d, want 2", code)
	}
	if code := runManagementVersion([]string{"extra"}, &output, &errorOutput); code != 2 {
		t.Fatalf("version extra-argument exit = %d, want 2", code)
	}
}

func TestManagementDispatchPreservesServeContract(t *testing.T) {
	if code, handled := runAgentCommand(context.Background(), nil); code != 0 || handled {
		t.Fatalf("bare dispatch = (%d, %t), want (0, false)", code, handled)
	}
	if code, handled := runAgentCommand(context.Background(), []string{"serve"}); code != 0 || handled {
		t.Fatalf("serve dispatch = (%d, %t), want (0, false)", code, handled)
	}
	if code, handled := runAgentCommand(context.Background(), []string{"serve", "extra"}); code != 2 || !handled {
		t.Fatalf("serve extra dispatch = (%d, %t), want (2, true)", code, handled)
	}
}

func TestManagementAddressValidationAndPrecedence(t *testing.T) {
	t.Setenv("PORT", "9000")
	address, err := resolveManagementAddress(managementFlags{}, nil)
	if err != nil || address != (managementAddress{Host: "127.0.0.1", Port: 9000}) {
		t.Fatalf("default address = %#v, %v", address, err)
	}
	runtime := &localRuntimeRecord{Host: "::1", Port: 9001}
	address, err = resolveManagementAddress(managementFlags{}, runtime)
	if err != nil || address.NetworkAddress() != "[::1]:9001" {
		t.Fatalf("runtime address = %#v (%q), %v", address, address.NetworkAddress(), err)
	}
	address, err = resolveManagementAddress(managementFlags{
		host: "Factory.Example", port: "65535", hostExplicit: true, portExplicit: true,
	}, runtime)
	if err != nil || address != (managementAddress{Host: "factory.example", Port: 65535}) {
		t.Fatalf("explicit address = %#v, %v", address, err)
	}

	for _, test := range []managementFlags{
		{host: "https://127.0.0.1", hostExplicit: true},
		{host: "[::1]", hostExplicit: true},
		{host: "bad host", hostExplicit: true},
		{port: "0", portExplicit: true},
		{port: "65536", portExplicit: true},
		{port: "not-a-port", portExplicit: true},
	} {
		if _, err := resolveManagementAddress(test, nil); err == nil {
			t.Fatalf("invalid address was accepted: %#v", test)
		}
	}
}

func TestManagementStatusHumanAndJSON(t *testing.T) {
	body := `{"status":"ok","app":"factory","commit":"abc123","tree":"tree","buildId":"build","deploymentId":"deployment","contractVersion":"1","startedAt":"2026-07-15T00:00:00Z","wire":{"pending":0}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/healthz" {
			t.Fatalf("health path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, body)
	}))
	defer server.Close()
	host, port := testServerAddress(t, server.URL)

	var output bytes.Buffer
	var errorOutput bytes.Buffer
	args := []string{"--host", host, "--port", port}
	if code := runManagementStatus(context.Background(), args, &output, &errorOutput); code != 0 {
		t.Fatalf("status exit = %d, stdout %q, stderr %q", code, output.String(), errorOutput.String())
	}
	if !strings.Contains(output.String(), "Factory is ok") || !strings.Contains(output.String(), "abc123") {
		t.Fatalf("human status = %q", output.String())
	}

	output.Reset()
	errorOutput.Reset()
	if code := runManagementStatus(context.Background(), append(args, "--json"), &output, &errorOutput); code != 0 {
		t.Fatalf("JSON status exit = %d, stderr %q", code, errorOutput.String())
	}
	if strings.TrimSpace(output.String()) != body {
		t.Fatalf("JSON status = %q, want %q", output.String(), body)
	}
}

func TestManagementStatusReportsDegradedAndInvalidResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "degraded", status: http.StatusServiceUnavailable, body: `{"status":"degraded","app":"factory"}`},
		{name: "malformed", status: http.StatusOK, body: `{not-json`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = fmt.Fprintln(w, test.body)
			}))
			defer server.Close()
			host, port := testServerAddress(t, server.URL)
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			code := runManagementStatus(context.Background(), []string{"--host", host, "--port", port}, &output, &errorOutput)
			if code != 1 {
				t.Fatalf("status exit = %d, stdout %q, stderr %q", code, output.String(), errorOutput.String())
			}
		})
	}
}

func TestLocalRuntimeRecordIsPrivateAtomicAndOwnerBound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	record := newLocalRuntimeRecord(managementAddress{Host: "127.0.0.1", Port: 8092}, "/tmp/factory", time.Now().UTC())
	if err := publishLocalRuntimeRecord(record); err != nil {
		t.Fatalf("publish runtime record: %v", err)
	}
	path, _ := localRuntimePath()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime mode = %v", info.Mode())
	}
	loaded, err := readLocalRuntimeRecord(path)
	if err != nil || loaded != record {
		t.Fatalf("loaded runtime = %#v, %v", loaded, err)
	}

	other := record
	other.PID++
	removeOwnedLocalRuntimeRecord(other)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("non-owner removed runtime record: %v", err)
	}
	removeOwnedLocalRuntimeRecord(record)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner cleanup left runtime record: %v", err)
	}
}

func TestPrepareLocalRuntimeRemovesOnlyProvenDeadRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	record := newLocalRuntimeRecord(managementAddress{Host: "127.0.0.1", Port: 8092}, "/tmp/factory", time.Now().UTC())
	record.PID = 999999
	if err := publishLocalRuntimeRecord(record); err != nil {
		t.Fatal(err)
	}
	if err := prepareLocalRuntime(); err != nil {
		t.Fatalf("prepare stale runtime: %v", err)
	}
	path, _ := localRuntimePath()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale runtime remains: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"schema":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareLocalRuntime(); err == nil {
		t.Fatal("invalid runtime record was overwritten")
	}
}

func TestLocalStopSignalsOnlyMatchingRecordedHealth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	var stopped atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		health := managementHealth{
			Status: "ok", App: "factory", Commit: buildCommit, Tree: buildTree,
			BuildID: buildID, DeploymentID: buildDeploymentID,
			ContractVersion: buildContractVersion, StartedAt: startedAt,
		}
		if stopped.Load() {
			health.App = "other"
		}
		_ = json.NewEncoder(w).Encode(health)
	}))
	defer server.Close()
	host, portValue := testServerAddress(t, server.URL)
	port, _ := strconv.Atoi(portValue)
	record := newLocalRuntimeRecord(managementAddress{Host: host, Port: port}, "/tmp/factory", startedAt)
	if err := publishLocalRuntimeRecord(record); err != nil {
		t.Fatal(err)
	}
	var signaled atomic.Bool
	err := stopLocalFactory(context.Background(), managementFlags{}, func(pid int, signal os.Signal) error {
		if pid != os.Getpid() || signal != syscall.SIGTERM {
			t.Fatalf("signal = (%d, %v)", pid, signal)
		}
		signaled.Store(true)
		stopped.Store(true)
		return nil
	})
	if err != nil || !signaled.Load() {
		t.Fatalf("stop local = %v, signaled %t", err, signaled.Load())
	}
	removeOwnedLocalRuntimeRecord(record)
}

func TestLocalStopRejectsHealthIdentityMismatchWithoutSignal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(managementHealth{
			Status: "ok", App: "factory", Commit: "different", Tree: buildTree,
			BuildID: buildID, DeploymentID: buildDeploymentID,
			ContractVersion: buildContractVersion, StartedAt: startedAt,
		})
	}))
	defer server.Close()
	host, portValue := testServerAddress(t, server.URL)
	port, _ := strconv.Atoi(portValue)
	record := newLocalRuntimeRecord(managementAddress{Host: host, Port: port}, "/tmp/factory", startedAt)
	if err := publishLocalRuntimeRecord(record); err != nil {
		t.Fatal(err)
	}
	called := false
	err := stopLocalFactory(context.Background(), managementFlags{}, func(int, os.Signal) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("mismatched stop = %v, signal called %t", err, called)
	}
	removeOwnedLocalRuntimeRecord(record)
}

func TestManagedStartUsesExactBootstrapOrKickstartAndReceiptIdentity(t *testing.T) {
	for _, test := range []struct {
		name      string
		loaded    bool
		operation string
	}{
		{name: "unloaded", operation: "bootstrap"},
		{name: "loaded unhealthy", loaded: true, operation: "kickstart"},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths, receipt := writeManagedFixture(t)
			var healthy atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				health := managementHealth{
					Status: "degraded", App: "factory", Commit: receipt.SourceCommit, Tree: receipt.SourceTree,
					BuildID: receipt.BuildID, DeploymentID: receipt.DeploymentID,
					ContractVersion: strconv.Itoa(receipt.ContractVersion), StartedAt: receipt.StartedAt,
				}
				status := http.StatusServiceUnavailable
				if healthy.Load() {
					health.Status = "ok"
					status = http.StatusOK
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(health)
			}))
			defer server.Close()
			host, portValue := testServerAddress(t, server.URL)
			port, _ := strconv.Atoi(portValue)
			runner := &recordingManagementRunner{loaded: test.loaded, healthy: &healthy}
			if err := startManagedFactory(context.Background(), paths, managementAddress{Host: host, Port: port}, runner); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 2 || runner.calls[0][1] != "print" || runner.calls[1][1] != test.operation {
				t.Fatalf("launchctl calls = %#v", runner.calls)
			}
			if test.operation == "bootstrap" && (runner.calls[1][2] != "gui/"+strconv.Itoa(os.Getuid()) || runner.calls[1][3] != paths.Plist) {
				t.Fatalf("bootstrap call = %#v", runner.calls[1])
			}
			if test.operation == "kickstart" && strings.Join(runner.calls[1][2:], " ") != "-k gui/"+strconv.Itoa(os.Getuid())+"/com.nags.factory" {
				t.Fatalf("kickstart call = %#v", runner.calls[1])
			}
		})
	}
}

func TestManagedStopUsesFixedLaunchdTargetAndIsIdempotent(t *testing.T) {
	paths, _ := writeManagedFixture(t)
	runner := &recordingManagementRunner{loaded: true}
	if err := stopManagedFactory(context.Background(), paths, runner); err != nil {
		t.Fatal(err)
	}
	target := "gui/" + strconv.Itoa(os.Getuid()) + "/com.nags.factory"
	if len(runner.calls) != 2 || strings.Join(runner.calls[0][1:], " ") != "print "+target || strings.Join(runner.calls[1][1:], " ") != "bootout "+target {
		t.Fatalf("stop calls = %#v", runner.calls)
	}

	runner = &recordingManagementRunner{}
	if err := stopManagedFactory(context.Background(), paths, runner); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || runner.calls[0][1] != "print" {
		t.Fatalf("unloaded stop calls = %#v", runner.calls)
	}
}

func TestManagedDetectionFailsClosedOnPartialInstallation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths, err := factoryManagedPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Receipt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Receipt, []byte(`{"status":"success"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !managedInstallationDetected(paths) {
		t.Fatal("partial managed installation selected local mode")
	}
	if _, err := validateManagedInstallation(paths); err == nil {
		t.Fatal("partial managed installation passed validation")
	}
}

func TestManagementDoctorJSONDoesNotExposeSecretValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret := "sentinel-secret-value"
	for _, name := range []string{
		"LINEAR_WEBHOOK_SECRET", "GITHUB_WEBHOOK_SECRET", "LINEAR_API_KEY", "LINEAR_TRIGGER_ACTOR_ID",
		"FACTORY_GOOGLE_CLIENT_ID", "FACTORY_GOOGLE_CLIENT_SECRET", "FACTORY_GOOGLE_ALLOWED_EMAILS", "FACTORY_SESSION_KEY",
	} {
		t.Setenv(name, secret+"-"+name)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	code := runManagementDoctor(context.Background(), []string{"--host", "127.0.0.1", "--port", "1", "--json"}, &output, &errorOutput)
	if code != 1 {
		t.Fatalf("doctor exit = %d, stdout %q, stderr %q", code, output.String(), errorOutput.String())
	}
	if strings.Contains(output.String(), secret) || strings.Contains(errorOutput.String(), secret) {
		t.Fatalf("doctor exposed a secret: stdout %q, stderr %q", output.String(), errorOutput.String())
	}
	var report doctorReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor report: %v", err)
	}
	if report.Mode != "local" || report.Status != "degraded" || len(report.Checks) == 0 {
		t.Fatalf("doctor report = %#v", report)
	}
}

type recordingManagementRunner struct {
	loaded  bool
	healthy *atomic.Bool
	calls   [][]string
}

func (r *recordingManagementRunner) Run(_ context.Context, name string, args ...string) error {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if len(args) > 0 && args[0] == "print" && !r.loaded {
		return errors.New("not loaded")
	}
	if len(args) > 0 && (args[0] == "bootstrap" || args[0] == "kickstart") && r.healthy != nil {
		r.healthy.Store(true)
	}
	return nil
}

func writeManagedFixture(t *testing.T) (managedPaths, agentrun.DeploymentReceipt) {
	t.Helper()
	root := t.TempDir()
	paths := managedPaths{
		Plist:   filepath.Join(root, "Library", "LaunchAgents", "com.nags.factory.plist"),
		Wrapper: filepath.Join(root, ".local", "bin", "factory-run"),
		Release: filepath.Join(root, ".local", "share", "factory", "current", "factory"),
		Receipt: filepath.Join(root, ".local", "share", "factory", "deployments", "current.json"),
	}
	for _, path := range []string{paths.Plist, paths.Wrapper, paths.Release, paths.Receipt} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{paths.Wrapper, paths.Release} {
		if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>com.nags.factory</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>ProgramArguments</key><array><string>%s</string><string>serve</string></array>
</dict></plist>`, paths.Wrapper)
	if err := os.WriteFile(paths.Plist, []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt := agentrun.DeploymentReceipt{
		ContractVersion: agentrun.LifecycleContractVersion,
		DeploymentID:    "deployment", BuildID: "build", Status: "success", App: "factory",
		SourceRepository: "tomnagengast/factory", SourceBranch: "main",
		SourceCommit: "commit", SourceTree: "tree", BinarySHA256: strings.Repeat("a", 64),
		StartedAt: time.Now().UTC().Add(-time.Minute), FinishedAt: time.Now().UTC(),
	}
	data, _ := json.Marshal(receipt)
	if err := os.WriteFile(paths.Receipt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return paths, receipt
}

func testServerAddress(t *testing.T, rawURL string) (string, string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Hostname(), strconv.Itoa(port)
}
