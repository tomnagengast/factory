package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
)

const (
	managementHealthTimeout = 3 * time.Second
	managementStartTimeout  = 20 * time.Second
	managementStopTimeout   = 10 * time.Second
	maxManagementBody       = 1 << 20
)

const managementUsage = `Factory manages autonomous engineering runs.

Usage:
  factory
  factory serve
  factory start [--host HOST] [--port PORT]
  factory status [--host HOST] [--port PORT] [--json]
  factory stop [--host HOST] [--port PORT]
  factory doctor [--host HOST] [--port PORT] [--json]
  factory --help | -h
  factory --version | -v

Commands:
  start    Start a managed install, or run an unmanaged server in the foreground
  status   Read bounded health status
  stop     Stop the managed or recorded local instance
  doctor   Run read-only configuration and runtime diagnostics
  serve    Run the managed server entry point
`

type managementAddress struct {
	Host string
	Port int
}

type managementHealth struct {
	Status          string    `json:"status"`
	App             string    `json:"app"`
	Commit          string    `json:"commit"`
	Tree            string    `json:"tree"`
	BuildID         string    `json:"buildId"`
	DeploymentID    string    `json:"deploymentId"`
	ContractVersion string    `json:"contractVersion"`
	StartedAt       time.Time `json:"startedAt"`
}

type managementProbe struct {
	Health     managementHealth
	Body       []byte
	HTTPStatus int
}

type localRuntimeRecord struct {
	Schema          int       `json:"schema"`
	Mode            string    `json:"mode"`
	PID             int       `json:"pid"`
	StartedAt       time.Time `json:"startedAt"`
	Host            string    `json:"host"`
	Port            int       `json:"port"`
	Executable      string    `json:"executable"`
	Commit          string    `json:"commit"`
	Tree            string    `json:"tree"`
	BuildID         string    `json:"buildId"`
	DeploymentID    string    `json:"deploymentId"`
	ContractVersion string    `json:"contractVersion"`
}

type managementFlags struct {
	host         string
	port         string
	hostExplicit bool
	portExplicit bool
	json         bool
}

type trackedString struct {
	value *string
	set   *bool
}

type managedPaths struct {
	Plist       string
	Wrapper     string
	Release     string
	Receipt     string
	Environment string
}

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type doctorReport struct {
	Status string        `json:"status"`
	Mode   string        `json:"mode"`
	Checks []doctorCheck `json:"checks"`
}

type managementCommandRunner interface {
	Run(context.Context, string, ...string) error
}

type execManagementCommandRunner struct{}

func (execManagementCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

func (v trackedString) String() string {
	if v.value == nil {
		return ""
	}
	return *v.value
}

func (v trackedString) Set(value string) error {
	*v.value = value
	*v.set = true
	return nil
}

func runManagementHelp(args []string, output, errorOutput io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(errorOutput, "usage: factory --help")
		return 2
	}
	_, _ = io.WriteString(output, managementUsage)
	return 0
}

func runManagementVersion(args []string, output, errorOutput io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(errorOutput, "usage: factory --version")
		return 2
	}
	fmt.Fprintf(
		output,
		"factory commit=%s tree=%s build=%s deployment=%s contract=%s\n",
		buildCommit,
		buildTree,
		buildID,
		buildDeploymentID,
		buildContractVersion,
	)
	return 0
}

func runManagementStatus(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	options, err := parseManagementFlags("status", args, true, errorOutput)
	if err != nil {
		return 2
	}
	runtime, err := runtimeRecordForAddress(options)
	if err != nil {
		fmt.Fprintf(errorOutput, "factory status: %v\n", err)
		return 1
	}
	address, err := resolveManagementAddress(options, runtime)
	if err != nil {
		fmt.Fprintf(errorOutput, "factory status: %v\n", err)
		return 2
	}
	probe, err := probeManagementHealth(ctx, address, nil)
	if err != nil {
		fmt.Fprintf(errorOutput, "factory status: %v\n", err)
		return 1
	}
	if options.json {
		_, _ = output.Write(probe.Body)
		if len(probe.Body) == 0 || probe.Body[len(probe.Body)-1] != '\n' {
			_, _ = io.WriteString(output, "\n")
		}
	} else {
		fmt.Fprintf(output, "Factory is %s at %s", probe.Health.Status, address.URL())
		if probe.Health.Commit != "" {
			fmt.Fprintf(output, " (%s)", probe.Health.Commit)
		}
		fmt.Fprintln(output)
	}
	if probe.HTTPStatus == http.StatusOK && probe.Health.Status == "ok" && probe.Health.App == "factory" {
		return 0
	}
	return 1
}

func runManagementStart(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	options, err := parseManagementFlags("start", args, false, errorOutput)
	if err != nil {
		return 2
	}
	paths, err := factoryManagedPaths()
	if err != nil {
		fmt.Fprintf(errorOutput, "factory start: %v\n", err)
		return 1
	}
	if managedInstallationDetected(paths) {
		if options.hostExplicit || options.portExplicit {
			fmt.Fprintln(errorOutput, "factory start: --host and --port are not valid for a managed installation")
			return 2
		}
		address, err := resolveManagementAddress(options, nil)
		if err != nil {
			fmt.Fprintf(errorOutput, "factory start: %v\n", err)
			return 2
		}
		if err := startManagedFactory(ctx, paths, address, execManagementCommandRunner{}); err != nil {
			fmt.Fprintf(errorOutput, "factory start: %v\n", err)
			return 1
		}
		fmt.Fprintf(output, "Managed Factory is running at %s\n", address.URL())
		return 0
	}
	address, err := resolveManagementAddress(options, nil)
	if err != nil {
		fmt.Fprintf(errorOutput, "factory start: %v\n", err)
		return 2
	}
	if err := prepareLocalRuntime(); err != nil {
		fmt.Fprintf(errorOutput, "factory start: %v\n", err)
		return 1
	}
	if err := serveConfigured(ctx, serveOptions{address: address, localStart: true, output: output}); err != nil {
		fmt.Fprintf(errorOutput, "factory start: %v\n", err)
		return 1
	}
	return 0
}

func runManagementStop(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	options, err := parseManagementFlags("stop", args, false, errorOutput)
	if err != nil {
		return 2
	}
	paths, err := factoryManagedPaths()
	if err != nil {
		fmt.Fprintf(errorOutput, "factory stop: %v\n", err)
		return 1
	}
	if managedInstallationDetected(paths) {
		if options.hostExplicit || options.portExplicit {
			fmt.Fprintln(errorOutput, "factory stop: --host and --port are not valid for a managed installation")
			return 2
		}
		if err := stopManagedFactory(ctx, paths, execManagementCommandRunner{}); err != nil {
			fmt.Fprintf(errorOutput, "factory stop: %v\n", err)
			return 1
		}
		fmt.Fprintln(output, "Managed Factory is stopped")
		return 0
	}
	if err := stopLocalFactory(ctx, options, signalLocalProcess); err != nil {
		fmt.Fprintf(errorOutput, "factory stop: %v\n", err)
		return 1
	}
	fmt.Fprintln(output, "Local Factory is stopped")
	return 0
}

func runManagementDoctor(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	options, err := parseManagementFlags("doctor", args, true, errorOutput)
	if err != nil {
		return 2
	}
	report := doctorReport{Status: "ok", Mode: "local", Checks: []doctorCheck{}}
	paths, pathErr := factoryManagedPaths()
	managed := pathErr == nil && managedInstallationDetected(paths)
	if managed {
		report.Mode = "managed"
	}
	add := func(name, status, message string) {
		report.Checks = append(report.Checks, doctorCheck{Name: name, Status: status, Message: message})
		if status == "fail" {
			report.Status = "degraded"
		}
	}
	if pathErr != nil {
		add("home", "fail", "home directory could not be resolved")
	}

	var runtimeRecord *localRuntimeRecord
	runtimePath, runtimePathErr := localRuntimePath()
	if runtimePathErr != nil {
		add("local-runtime", "fail", "local runtime path could not be resolved")
	} else if record, err := readLocalRuntimeRecord(runtimePath); err == nil {
		runtimeRecord = &record
		if processAlive(record.PID) {
			add("local-runtime", "ok", fmt.Sprintf("local runtime record identifies live process %d", record.PID))
		} else {
			add("local-runtime", "fail", fmt.Sprintf("local runtime record identifies stopped process %d", record.PID))
		}
	} else if errors.Is(err, os.ErrNotExist) {
		add("local-runtime", "info", "no local runtime is recorded")
	} else {
		add("local-runtime", "fail", "local runtime record is invalid")
	}

	address, addressErr := resolveManagementAddress(options, runtimeRecord)
	if addressErr != nil {
		add("address", "fail", addressErr.Error())
	} else {
		add("address", "ok", address.URL())
	}

	if buildCommit == "" || buildTree == "" || buildID == "" || buildDeploymentID == "" || buildContractVersion != strconv.Itoa(agentrun.LifecycleContractVersion) {
		add("build-identity", "fail", "build identity is incomplete or has the wrong lifecycle contract")
	} else {
		add("build-identity", "ok", fmt.Sprintf("commit %s, contract %s", buildCommit, buildContractVersion))
	}
	if info, err := os.Stat(filepath.Join("frontend", "dist", "index.html")); err != nil || !info.Mode().IsRegular() {
		add("frontend", "fail", "frontend/dist/index.html is missing; run the frozen frontend build from the repository root")
	} else {
		add("frontend", "ok", "frontend assets are built")
	}

	requiredEnvironment := []string{"LINEAR_WEBHOOK_SECRET", "GITHUB_WEBHOOK_SECRET", "LINEAR_API_KEY", "LINEAR_TRIGGER_ACTOR_ID"}
	if managed || (addressErr == nil && !isLoopbackManagementHost(address.Host)) {
		requiredEnvironment = append(requiredEnvironment,
			"FACTORY_GOOGLE_CLIENT_ID", "FACTORY_GOOGLE_CLIENT_SECRET", "FACTORY_GOOGLE_ALLOWED_EMAILS", "FACTORY_SESSION_KEY",
		)
	}
	managedEnvironment := map[string]bool{}
	if managed {
		var err error
		managedEnvironment, err = readEnvironmentNames(paths.Environment)
		if err != nil {
			add("managed-environment", "fail", "private managed environment is missing or invalid")
		} else {
			add("managed-environment", "ok", "private managed environment is readable and secure")
		}
	}
	for _, name := range requiredEnvironment {
		present := os.Getenv(name) != ""
		location := "current environment"
		if managed {
			present = managedEnvironment[name]
			location = "managed environment"
		}
		if !present {
			add("environment:"+name, "fail", name+" is not set in the "+location)
		} else {
			add("environment:"+name, "ok", name+" is set in the "+location)
		}
	}
	if !managed && addressErr == nil && !isLoopbackManagementHost(address.Host) {
		redirect, err := url.Parse(os.Getenv("FACTORY_GOOGLE_REDIRECT_URL"))
		if err != nil || redirect.Scheme != "https" || redirect.Host == "" || redirect.User != nil {
			add("environment:FACTORY_GOOGLE_REDIRECT_URL", "fail", "FACTORY_GOOGLE_REDIRECT_URL must be an explicit HTTPS URL for non-loopback start")
		} else {
			add("environment:FACTORY_GOOGLE_REDIRECT_URL", "ok", "FACTORY_GOOGLE_REDIRECT_URL is a valid HTTPS URL")
		}
	} else if !managed {
		add("viewer-auth", "ok", "loopback-only local authorization is selected")
	}

	for _, command := range []string{"git", "gh", "wt", "tmux", "codex", "claude"} {
		if _, err := exec.LookPath(command); err != nil {
			add("command:"+command, "fail", command+" is not on PATH")
		} else {
			add("command:"+command, "ok", command+" is available")
		}
	}
	if pathErr == nil {
		stateRoot := filepath.Dir(filepath.Dir(paths.Receipt))
		if info, err := os.Stat(stateRoot); errors.Is(err, os.ErrNotExist) {
			add("state-root", "info", "Factory state root will be created on first start")
		} else if err != nil || !info.IsDir() {
			add("state-root", "fail", "Factory state root is inaccessible or not a directory")
		} else if info.Mode().Perm()&0o200 == 0 {
			add("state-root", "fail", "Factory state root is not owner-writable")
		} else {
			add("state-root", "ok", "Factory state root is accessible")
		}
	}

	var receipt agentrun.DeploymentReceipt
	managedValid := false
	if managed {
		if validated, err := validateManagedInstallation(paths); err != nil {
			add("managed-installation", "fail", err.Error())
		} else {
			receipt = validated
			managedValid = true
			add("managed-installation", "ok", "launchd artifacts and successful receipt match the fixed Factory contract")
		}
		target := "gui/" + strconv.Itoa(os.Getuid()) + "/com.nags.factory"
		if err := (execManagementCommandRunner{}).Run(ctx, requiredCommand("launchctl"), "print", target); err != nil {
			add("launchd", "fail", "com.nags.factory is not loaded")
		} else {
			add("launchd", "ok", "com.nags.factory is loaded")
		}
	} else if pathErr == nil {
		provider := filepath.Join(filepath.Dir(filepath.Dir(paths.Wrapper)), "bin", "nags")
		if _, err := os.Stat(provider); err != nil {
			add("provider", "info", "private deployment provider is absent and is not required for local start")
		} else {
			add("provider", "info", "private deployment provider is available but local start does not require it")
		}
	}

	if addressErr == nil {
		probe, err := probeManagementHealth(ctx, address, nil)
		if err != nil {
			add("health", "fail", "Factory health is unreachable at "+address.URL())
		} else if managed && managedValid && !managedHealthMatchesReceipt(probe, receipt) {
			add("health", "fail", "health does not match the current successful deployment receipt")
		} else if runtimeRecord != nil && !localHealthMatchesRecord(probe, *runtimeRecord) {
			add("health", "fail", "health does not match the local runtime record")
		} else if probe.HTTPStatus != http.StatusOK || probe.Health.Status != "ok" || probe.Health.App != "factory" {
			add("health", "fail", "Factory health is "+probe.Health.Status)
		} else {
			add("health", "ok", "Factory health is ok")
		}
	}

	if options.json {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintln(errorOutput, "factory doctor: encode report failed")
			return 1
		}
	} else {
		fmt.Fprintf(output, "Factory doctor: %s mode is %s\n", report.Mode, report.Status)
		for _, check := range report.Checks {
			fmt.Fprintf(output, "[%s] %s: %s\n", check.Status, check.Name, check.Message)
		}
	}
	if report.Status == "ok" {
		return 0
	}
	return 1
}

func factoryManagedPaths() (managedPaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return managedPaths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	stateRoot := filepath.Join(home, ".local", "share", "factory")
	return managedPaths{
		Plist:       filepath.Join(home, "Library", "LaunchAgents", "com.nags.factory.plist"),
		Wrapper:     filepath.Join(home, ".local", "bin", "factory-run"),
		Release:     filepath.Join(stateRoot, "current", "factory"),
		Receipt:     filepath.Join(stateRoot, "deployments", "current.json"),
		Environment: filepath.Join(home, ".config", "network-app", "env"),
	}, nil
}

func readEnvironmentNames(path string) (map[string]bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return nil, errors.New("managed environment must be a regular 0600 file no larger than 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	names := make(map[string]bool)
	scanner := bufio.NewScanner(io.LimitReader(file, 1<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, found := strings.Cut(line, "=")
		if !found || !validEnvironmentName(name) {
			return nil, errors.New("managed environment contains an invalid assignment")
		}
		names[name] = strings.TrimSpace(value) != ""
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

func validEnvironmentName(name string) bool {
	if name == "" || (name[0] != '_' && (name[0] < 'A' || name[0] > 'Z') && (name[0] < 'a' || name[0] > 'z')) {
		return false
	}
	for _, character := range name[1:] {
		if character != '_' && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func managedInstallationDetected(paths managedPaths) bool {
	for _, path := range []string{paths.Plist, paths.Wrapper, paths.Release, paths.Receipt} {
		if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func startManagedFactory(ctx context.Context, paths managedPaths, address managementAddress, runner managementCommandRunner) error {
	receipt, err := validateManagedInstallation(paths)
	if err != nil {
		return err
	}
	if probe, err := probeManagementHealth(ctx, address, nil); err == nil && managedHealthMatchesReceipt(probe, receipt) {
		return nil
	}
	uid := strconv.Itoa(os.Getuid())
	launchctl := requiredCommand("launchctl")
	target := "gui/" + uid + "/com.nags.factory"
	if err := runner.Run(ctx, launchctl, "print", target); err == nil {
		if err := runner.Run(ctx, launchctl, "kickstart", "-k", target); err != nil {
			return fmt.Errorf("kickstart managed Factory: %w", err)
		}
	} else if err := runner.Run(ctx, launchctl, "bootstrap", "gui/"+uid, paths.Plist); err != nil {
		return fmt.Errorf("bootstrap managed Factory: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, managementStartTimeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if probe, err := probeManagementHealth(waitCtx, address, nil); err == nil && managedHealthMatchesReceipt(probe, receipt) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return errors.New("managed Factory did not reach receipt-matched health before the startup timeout")
		case <-ticker.C:
		}
	}
}

func stopManagedFactory(ctx context.Context, paths managedPaths, runner managementCommandRunner) error {
	if _, err := validateManagedInstallation(paths); err != nil {
		return err
	}
	launchctl := requiredCommand("launchctl")
	target := "gui/" + strconv.Itoa(os.Getuid()) + "/com.nags.factory"
	if err := runner.Run(ctx, launchctl, "print", target); err != nil {
		return nil
	}
	if err := runner.Run(ctx, launchctl, "bootout", target); err != nil {
		return fmt.Errorf("boot out managed Factory: %w", err)
	}
	return nil
}

func validateManagedInstallation(paths managedPaths) (agentrun.DeploymentReceipt, error) {
	artifacts := []struct {
		name string
		path string
	}{
		{name: "plist", path: paths.Plist},
		{name: "wrapper", path: paths.Wrapper},
		{name: "release", path: paths.Release},
		{name: "receipt", path: paths.Receipt},
	}
	for _, artifact := range artifacts {
		info, err := os.Lstat(artifact.path)
		if err != nil {
			return agentrun.DeploymentReceipt{}, fmt.Errorf("managed %s is missing at %s", artifact.name, artifact.path)
		}
		if !info.Mode().IsRegular() {
			return agentrun.DeploymentReceipt{}, fmt.Errorf("managed %s is not a regular file at %s", artifact.name, artifact.path)
		}
	}
	for _, artifact := range artifacts[1:3] {
		info, _ := os.Stat(artifact.path)
		if info.Mode().Perm()&0o111 == 0 {
			return agentrun.DeploymentReceipt{}, fmt.Errorf("managed %s is not executable at %s", artifact.name, artifact.path)
		}
	}
	if err := validateFactoryLaunchdPlist(paths.Plist, paths.Wrapper); err != nil {
		return agentrun.DeploymentReceipt{}, err
	}
	receipt, err := readManagedReceipt(paths.Receipt)
	if err != nil {
		return agentrun.DeploymentReceipt{}, err
	}
	if receipt.Status != "success" || receipt.App != "factory" || receipt.ContractVersion != agentrun.LifecycleContractVersion || receipt.SourceRepository != "tomnagengast/factory" || receipt.SourceBranch != "main" || receipt.SourceCommit == "" || receipt.SourceTree == "" || receipt.BuildID == "" || receipt.DeploymentID == "" || receipt.StartedAt.IsZero() {
		return agentrun.DeploymentReceipt{}, errors.New("managed deployment receipt is incomplete or not a successful Factory lifecycle-contract-1 deployment")
	}
	return receipt, nil
}

func readManagedReceipt(path string) (agentrun.DeploymentReceipt, error) {
	file, err := os.Open(path)
	if err != nil {
		return agentrun.DeploymentReceipt{}, fmt.Errorf("open managed deployment receipt: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(data) > 64<<10 {
		return agentrun.DeploymentReceipt{}, errors.New("managed deployment receipt is unreadable or too large")
	}
	var receipt agentrun.DeploymentReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return agentrun.DeploymentReceipt{}, fmt.Errorf("decode managed deployment receipt: %w", err)
	}
	return receipt, nil
}

func managedHealthMatchesReceipt(probe managementProbe, receipt agentrun.DeploymentReceipt) bool {
	health := probe.Health
	return probe.HTTPStatus == http.StatusOK && health.Status == "ok" && health.App == receipt.App &&
		health.Commit == receipt.SourceCommit && health.Tree == receipt.SourceTree &&
		health.BuildID == receipt.BuildID && health.DeploymentID == receipt.DeploymentID &&
		health.ContractVersion == strconv.Itoa(receipt.ContractVersion) && !health.StartedAt.Before(receipt.StartedAt)
}

func validateFactoryLaunchdPlist(path, wrapper string) error {
	if info, err := os.Stat(path); err != nil || info.Size() > 1<<20 {
		return errors.New("managed launchd plist is unreadable or too large")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open managed launchd plist: %w", err)
	}
	defer file.Close()
	values := make(map[string]any)
	decoder := xml.NewDecoder(io.LimitReader(file, 1<<20))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("decode managed launchd plist: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err := decoder.DecodeElement(&key, &start); err != nil {
			return fmt.Errorf("decode managed launchd plist key: %w", err)
		}
		if key != "Label" && key != "RunAtLoad" && key != "KeepAlive" && key != "ProgramArguments" {
			continue
		}
		valueStart, err := nextXMLStart(decoder)
		if err != nil {
			return err
		}
		switch valueStart.Name.Local {
		case "string":
			var value string
			if err := decoder.DecodeElement(&value, &valueStart); err != nil {
				return err
			}
			values[key] = value
		case "true":
			if err := decoder.Skip(); err != nil {
				return err
			}
			values[key] = true
		case "array":
			var value struct {
				Strings []string `xml:"string"`
			}
			if err := decoder.DecodeElement(&value, &valueStart); err != nil {
				return err
			}
			values[key] = value.Strings
		default:
			if err := decoder.Skip(); err != nil {
				return err
			}
		}
	}
	arguments, _ := values["ProgramArguments"].([]string)
	if values["Label"] != "com.nags.factory" || values["RunAtLoad"] != true || values["KeepAlive"] != true || len(arguments) == 0 || arguments[0] != wrapper {
		return errors.New("managed launchd plist does not match the fixed com.nags.factory wrapper contract")
	}
	return nil
}

func nextXMLStart(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.StartElement{}, fmt.Errorf("decode managed launchd plist value: %w", err)
		}
		if start, ok := token.(xml.StartElement); ok {
			return start, nil
		}
	}
}

func stopLocalFactory(ctx context.Context, options managementFlags, signal func(int, os.Signal) error) error {
	path, err := localRuntimePath()
	if err != nil {
		return err
	}
	record, err := readLocalRuntimeRecord(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no local Factory runtime is recorded")
		}
		return err
	}
	if !processAlive(record.PID) {
		return fmt.Errorf("recorded local Factory process %d is not running; remove the stale record only after verifying the process identity", record.PID)
	}
	address, err := resolveManagementAddress(options, &record)
	if err != nil {
		return err
	}
	probe, err := probeManagementHealth(ctx, address, nil)
	if err != nil || !localHealthMatchesRecord(probe, record) {
		return errors.New("recorded process, address, and Factory health identity do not match; refusing to signal")
	}
	if err := signal(record.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal local Factory process %d: %w", record.PID, err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, managementStopTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		probe, err := probeManagementHealth(waitCtx, address, nil)
		if err != nil || !localHealthMatchesRecord(probe, record) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return errors.New("local Factory did not stop before the shutdown timeout")
		case <-ticker.C:
		}
	}
}

func localHealthMatchesRecord(probe managementProbe, record localRuntimeRecord) bool {
	health := probe.Health
	validStatus := (probe.HTTPStatus == http.StatusOK && health.Status == "ok") ||
		(probe.HTTPStatus == http.StatusServiceUnavailable && health.Status == "degraded")
	return validStatus && health.App == "factory" && health.StartedAt.Equal(record.StartedAt) && health.Commit == record.Commit &&
		health.Tree == record.Tree && health.BuildID == record.BuildID && health.DeploymentID == record.DeploymentID &&
		health.ContractVersion == record.ContractVersion
}

func signalLocalProcess(pid int, signal os.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(signal)
}

func parseManagementFlags(command string, args []string, allowJSON bool, errorOutput io.Writer) (managementFlags, error) {
	var options managementFlags
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	flags.Var(trackedString{value: &options.host, set: &options.hostExplicit}, "host", "listen or probe host")
	flags.Var(trackedString{value: &options.port, set: &options.portExplicit}, "port", "listen or probe port")
	if allowJSON {
		flags.BoolVar(&options.json, "json", false, "emit JSON")
	}
	if err := flags.Parse(args); err != nil {
		return managementFlags{}, err
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(errorOutput, "usage: factory %s [--host HOST] [--port PORT]", command)
		if allowJSON {
			fmt.Fprint(errorOutput, " [--json]")
		}
		fmt.Fprintln(errorOutput)
		return managementFlags{}, errors.New("unexpected arguments")
	}
	return options, nil
}

func resolveManagementAddress(options managementFlags, runtime *localRuntimeRecord) (managementAddress, error) {
	host := "127.0.0.1"
	port := envOr("PORT", defaultPort)
	if runtime != nil {
		host = runtime.Host
		port = strconv.Itoa(runtime.Port)
	}
	if options.hostExplicit {
		host = options.host
	}
	if options.portExplicit {
		port = options.port
	}
	normalizedHost, err := normalizeManagementHost(host)
	if err != nil {
		return managementAddress{}, err
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return managementAddress{}, fmt.Errorf("port must be an integer from 1 through 65535")
	}
	return managementAddress{Host: normalizedHost, Port: parsedPort}, nil
}

func runtimeRecordForAddress(options managementFlags) (*localRuntimeRecord, error) {
	if options.hostExplicit && options.portExplicit {
		return nil, nil
	}
	path, err := localRuntimePath()
	if err != nil {
		return nil, err
	}
	record, err := readLocalRuntimeRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func normalizeManagementHost(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 253 || strings.ContainsAny(value, "/\\?#@[]") || strings.Contains(value, "://") {
		return "", errors.New("host must be an IP address or DNS name without URL syntax")
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.String(), nil
	}
	if strings.Contains(value, ":") || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return "", errors.New("host must be an IP address or DNS name without URL syntax")
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("host must be an IP address or DNS name without URL syntax")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return "", errors.New("host must be an IP address or DNS name without URL syntax")
			}
		}
	}
	return strings.ToLower(value), nil
}

func isLoopbackManagementHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

func (address managementAddress) NetworkAddress() string {
	return net.JoinHostPort(address.Host, strconv.Itoa(address.Port))
}

func (address managementAddress) URL() string {
	return "http://" + address.NetworkAddress()
}

func probeManagementHealth(ctx context.Context, address managementAddress, client *http.Client) (managementProbe, error) {
	if client == nil {
		client = &http.Client{
			Timeout: managementHealthTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, managementHealthTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, address.URL()+"/api/healthz", nil)
	if err != nil {
		return managementProbe{}, fmt.Errorf("create health request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return managementProbe{}, fmt.Errorf("health request to %s failed: %w", address.URL(), err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxManagementBody+1))
	if err != nil {
		return managementProbe{}, fmt.Errorf("read health response: %w", err)
	}
	if len(body) > maxManagementBody {
		return managementProbe{}, errors.New("health response is too large")
	}
	var health managementHealth
	if err := json.Unmarshal(body, &health); err != nil {
		return managementProbe{}, fmt.Errorf("decode health response: %w", err)
	}
	if health.Status == "" || health.App == "" {
		return managementProbe{}, errors.New("health response is incomplete")
	}
	return managementProbe{Health: health, Body: body, HTTPStatus: response.StatusCode}, nil
}

func newLocalRuntimeRecord(address managementAddress, executable string, startedAt time.Time) localRuntimeRecord {
	return localRuntimeRecord{
		Schema: 1, Mode: "local", PID: os.Getpid(), StartedAt: startedAt,
		Host: address.Host, Port: address.Port, Executable: executable,
		Commit: buildCommit, Tree: buildTree, BuildID: buildID,
		DeploymentID: buildDeploymentID, ContractVersion: buildContractVersion,
	}
}

func localRuntimePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "factory", "local-runtime.json"), nil
}

func prepareLocalRuntime() error {
	path, err := localRuntimePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Factory state directory: %w", err)
	}
	if err := removeStaleRuntimeLock(path + ".lock"); err != nil {
		return err
	}
	record, err := readLocalRuntimeRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if processAlive(record.PID) {
		return fmt.Errorf("local Factory process %d is already recorded; run factory status or factory stop", record.PID)
	}
	removeOwnedLocalRuntimeRecord(record)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("stale local runtime record could not be removed")
		}
		return fmt.Errorf("inspect stale local runtime record: %w", err)
	}
	return nil
}

func publishLocalRuntimeRecord(record localRuntimeRecord) error {
	path, err := localRuntimePath()
	if err != nil {
		return err
	}
	if err := validateLocalRuntimeRecord(record); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Factory state directory: %w", err)
	}
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("claim local runtime record: %w", err)
	}
	defer func() {
		_ = lock.Close()
		_ = os.Remove(lockPath)
	}()
	if _, err := fmt.Fprintf(lock, "%d\n", os.Getpid()); err != nil {
		return fmt.Errorf("write local runtime lock: %w", err)
	}
	if err := lock.Sync(); err != nil {
		return fmt.Errorf("sync local runtime lock: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("local runtime record already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect local runtime record: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".local-runtime-*")
	if err != nil {
		return fmt.Errorf("create local runtime record: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure local runtime record: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		temporary.Close()
		return fmt.Errorf("encode local runtime record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync local runtime record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close local runtime record: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish local runtime record: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func readLocalRuntimeRecord(path string) (localRuntimeRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return localRuntimeRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 64<<10 {
		return localRuntimeRecord{}, errors.New("local runtime record must be a regular 0600 file no larger than 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return localRuntimeRecord{}, fmt.Errorf("open local runtime record: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var record localRuntimeRecord
	if err := decoder.Decode(&record); err != nil {
		return localRuntimeRecord{}, fmt.Errorf("decode local runtime record: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return localRuntimeRecord{}, errors.New("decode local runtime record: trailing content")
	}
	if err := validateLocalRuntimeRecord(record); err != nil {
		return localRuntimeRecord{}, err
	}
	return record, nil
}

func validateLocalRuntimeRecord(record localRuntimeRecord) error {
	host, err := normalizeManagementHost(record.Host)
	if err != nil || host != record.Host || record.Schema != 1 || record.Mode != "local" || record.PID < 1 || record.Port < 1 || record.Port > 65535 || record.StartedAt.IsZero() || !filepath.IsAbs(record.Executable) || record.Commit == "" || record.Tree == "" || record.BuildID == "" || record.DeploymentID == "" || record.ContractVersion == "" {
		return errors.New("local runtime record is invalid or uses an unsupported schema")
	}
	return nil
}

func removeOwnedLocalRuntimeRecord(record localRuntimeRecord) {
	path, err := localRuntimePath()
	if err != nil {
		return
	}
	current, err := readLocalRuntimeRecord(path)
	if err != nil || current != record {
		return
	}
	if os.Remove(path) == nil {
		_ = syncDirectory(filepath.Dir(path))
	}
}

func removeStaleRuntimeLock(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read local runtime lock: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid < 1 || processAlive(pid) {
		return errors.New("local runtime initialization is already in progress or has an invalid lock")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale local runtime lock: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open Factory state directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync Factory state directory: %w", err)
	}
	return nil
}
