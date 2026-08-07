package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tomnagengast/factory/api/internal/agent"
	"github.com/tomnagengast/factory/api/internal/quiescence"
	"github.com/tomnagengast/factory/api/internal/server"
	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/store"
	"github.com/tomnagengast/factory/api/internal/workflow"
)

const systemDeadline = 10 * time.Second

type systemFixture struct {
	root                string
	databasePath        string
	mediaPath           string
	workflowWorkspace   string
	projectPath         string
	workflowCommand     string
	codexCommand        string
	claudeCommand       string
	factoryCommand      string
	workflowDefinitions string
	workflowMode        string
	invocationsPath     string
}

type runningSystem struct {
	fixture   *systemFixture
	url       string
	client    *http.Client
	store     *store.Store
	cancel    context.CancelFunc
	http      *http.Server
	listener  net.Listener
	loopDone  chan error
	httpDone  chan error
	closeOnce sync.Once
	closeErr  error
}

func newSystemFixture(t *testing.T) *systemFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &systemFixture{
		root:                root,
		databasePath:        filepath.Join(root, "factory.db"),
		mediaPath:           filepath.Join(root, "media"),
		workflowWorkspace:   filepath.Join(root, "workflow-workspace"),
		projectPath:         filepath.Join(root, "project"),
		workflowCommand:     filepath.Join(root, "workflow"),
		codexCommand:        filepath.Join(root, "codex"),
		claudeCommand:       filepath.Join(root, "claude"),
		factoryCommand:      filepath.Join(root, "factory"),
		workflowDefinitions: filepath.Join(root, "workflow-definitions.json"),
		workflowMode:        filepath.Join(root, "workflow-mode"),
		invocationsPath:     filepath.Join(root, "invocations"),
	}
	for _, path := range []string{fixture.workflowWorkspace, fixture.invocationsPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(fixture.workflowDefinitions, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.workflowMode, []byte("complete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, fixture.workflowCommand, systemWorkflowScript)
	writeExecutable(t, fixture.codexCommand, "#!/bin/sh\nexit 0\n")
	writeExecutable(t, fixture.claudeCommand, "#!/bin/sh\nexit 0\n")
	buildFactoryCLI(t, fixture.factoryCommand)
	return fixture
}

func (f *systemFixture) setWorkflow(t *testing.T, name string) workflow.Definition {
	t.Helper()
	source := filepath.Join(f.workflowWorkspace, name+".js")
	if err := os.WriteFile(source, []byte("export const meta = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := workflow.Definition{
		Name: name, Path: source, Scope: "system test", Description: "System test workflow",
		Phases: []string{"Review"}, Mutating: true,
	}
	data, err := json.Marshal([]workflow.Definition{definition})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.workflowDefinitions, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return definition
}

func (f *systemFixture) setWorkflowMode(t *testing.T, mode string) {
	t.Helper()
	if err := os.WriteFile(f.workflowMode, []byte(mode+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *systemFixture) Start(t *testing.T) *runningSystem {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + listener.Addr().String()
	eventStore, err := store.Open(f.databasePath)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	workflowCLI := workflow.CLI{
		Command: f.workflowCommand, Workspace: f.workflowWorkspace,
		CodexCommand: f.codexCommand, ClaudeCommand: f.claudeCommand,
		FactoryCommand: f.factoryCommand, FactoryURL: url,
	}
	if err := workflowCLI.Prepare(); err != nil {
		eventStore.Close()
		listener.Close()
		t.Fatal(err)
	}
	admission := quiescence.New(quiescence.Hooks{})
	loop, err := agent.NewLoop(eventStore, agent.CommandRunner{
		CodexCommand: f.codexCommand, ClaudeCommand: f.claudeCommand,
		Workspace: f.workflowWorkspace, FactoryCommand: f.factoryCommand, FactoryURL: url,
	}, workflowCLI, admission)
	if err != nil {
		eventStore.Close()
		listener.Close()
		t.Fatal(err)
	}
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}
	app, err := server.New(eventStore, assets, f.mediaPath, admission, state.ReleaseIdentity{})
	if err != nil {
		eventStore.Close()
		listener.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := loop.Initialize(ctx); err != nil {
		cancel()
		eventStore.Close()
		listener.Close()
		t.Fatal(err)
	}
	httpServer := newHTTPServer(app.Handler(), ctx)
	running := &runningSystem{
		fixture: f, url: url, client: &http.Client{Timeout: systemDeadline}, store: eventStore,
		cancel: cancel, http: httpServer, listener: listener,
		loopDone: make(chan error, 1), httpDone: make(chan error, 1),
	}
	go func() { running.loopDone <- loop.Run(ctx) }()
	go func() { running.httpDone <- httpServer.Serve(listener) }()
	running.getJSON(t, "/api/health", nil)
	t.Cleanup(func() { running.Close(t) })
	return running
}

func (s *runningSystem) Close(t *testing.T) {
	t.Helper()
	s.closeOnce.Do(func() {
		s.cancel()
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), systemDeadline)
		shutdownErr := s.http.Shutdown(shutdownContext)
		shutdownCancel()
		loopErr := receiveComponent(s.loopDone)
		httpErr := receiveComponent(s.httpDone)
		storeErr := s.store.Close()
		for _, err := range []error{shutdownErr, loopErr, httpErr, storeErr} {
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
				s.closeErr = errors.Join(s.closeErr, err)
			}
		}
	})
	if s.closeErr != nil {
		t.Fatalf("close system: %v", s.closeErr)
	}
}

func receiveComponent(result <-chan error) error {
	select {
	case err := <-result:
		return err
	case <-time.After(systemDeadline):
		return errors.New("timed out stopping system component")
	}
}

func (s *runningSystem) requestJSON(
	t *testing.T,
	method string,
	path string,
	body any,
	target any,
) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, s.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := s.client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("%s %s returned %s: %s", method, path, response.Status, data)
	}
	if target != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("decode %s %s response %q: %v", method, path, data, err)
		}
	}
}

func (s *runningSystem) getJSON(t *testing.T, path string, target any) {
	t.Helper()
	s.requestJSON(t, http.MethodGet, path, nil, target)
}

func (s *runningSystem) runCLI(t *testing.T, arguments ...string) []byte {
	t.Helper()
	return runFactoryCLI(t, s.fixture.factoryCommand, s.url, arguments...)
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func buildFactoryCLI(t *testing.T, target string) {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Dir(workingDirectory)
	command := exec.Command("go", "build", "-o", target, "./cli")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Factory CLI: %v: %s", err, output)
	}
}

func runFactoryCLI(t *testing.T, command, url string, arguments ...string) []byte {
	t.Helper()
	args := append([]string{"--url", url}, arguments...)
	process := exec.Command(command, args...)
	var stdout, stderr bytes.Buffer
	process.Stdout, process.Stderr = &stdout, &stderr
	if err := process.Run(); err != nil {
		t.Fatalf("run Factory CLI %s: %v: %s", strings.Join(arguments, " "), err, stderr.String())
	}
	return stdout.Bytes()
}

func decodeCLI[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode CLI response %q: %v", data, err)
	}
	return value
}

func waitForSystemCondition(t *testing.T, label string, check func() (bool, error)) {
	t.Helper()
	deadline := time.NewTimer(systemDeadline)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		matched, err := check()
		if matched {
			return
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-deadline.C:
			if lastErr != nil {
				t.Fatalf("timed out waiting for %s: %v", label, lastErr)
			}
			t.Fatalf("timed out waiting for %s", label)
		case <-ticker.C:
		}
	}
}

func invocationDirectories(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			values = append(values, filepath.Join(path, entry.Name()))
		}
	}
	return values, nil
}

func releaseInvocation(path string) error {
	file, err := os.OpenFile(filepath.Join(path, "release"), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(file, "continue\n")
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

const systemWorkflowScript = `#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if [ "$#" -ge 3 ] && [ "$3" = "list" ]; then
  cat "$root/workflow-definitions.json"
  exit 0
fi
if [ "$#" -ge 3 ] && [ "$3" = "validate" ]; then
  exit 0
fi
if [ "$#" -lt 4 ] || [ "$3" != "run" ]; then
  exit 2
fi

directory=$2
source=$4
args_json=
journal=
backend=
model=
codex_bin=
expected=
for argument in "$@"; do
  if [ -n "$expected" ]; then
    case "$expected" in
      args) args_json=$argument ;;
      journal) journal=$argument ;;
      backend) backend=$argument ;;
      model) model=$argument ;;
      codex) codex_bin=$argument ;;
    esac
    expected=
    continue
  fi
  case "$argument" in
    --args) expected=args ;;
    --journal) expected=journal ;;
    --backend) expected=backend ;;
    --model) expected=model ;;
    --codex-bin) expected=codex ;;
  esac
done

invocation=$(mktemp -d "$root/invocations/run.XXXXXX")
printf '%s\n' "$@" > "$invocation/argv"
printf '%s\n' "$args_json" > "$invocation/args.json"
printf '%s\n' "$directory" > "$invocation/directory"
printf '%s\n' "$source" > "$invocation/source"
printf '%s\n' "$backend" > "$invocation/backend"
printf '%s\n' "$model" > "$invocation/model"
printf '%s\n' "$codex_bin" > "$invocation/codex-bin"
printf '%s\n' "$FACTORY_URL" > "$invocation/factory-url"
printf '%s\n' "$FACTORY_CLI" > "$invocation/factory-cli"
cd "$directory"
pwd > "$invocation/pwd"

mode=$(tr -d '\r\n' < "$root/workflow-mode")
if [ "$mode" = "blocking" ]; then
  mkfifo "$invocation/release"
  exec 3<> "$invocation/release"
  touch "$invocation/active"
  printf '%s\n' '{"sequence":1,"at":"2026-07-21T12:00:00Z","type":"runtime.started","workflow":"review","backend":"codex"}' >> "$journal"
  touch "$invocation/started"
  IFS= read -r ignored <&3
  rm "$invocation/active"
  printf '%s\n' '{"sequence":2,"at":"2026-07-21T12:00:01Z","type":"runtime.completed","workflow":"review","result":"complete"}' >> "$journal"
  printf '%s\n' complete
  exit 0
fi

touch workflow.marker
"$FACTORY_CLI" event create '{"type":"workflow.side-effect","data":{"source":"script"}}' > "$invocation/factory-output"
printf '%s\n' '{"sequence":1,"at":"2026-07-21T12:00:00Z","type":"runtime.started","workflow":"review","backend":"codex"}' >> "$journal"
printf '%s\n' '{"sequence":2,"at":"2026-07-21T12:00:01Z","type":"phase.started","workflow":"review","phase":"Review"}' >> "$journal"
printf '%s\n' '{"sequence":3,"at":"2026-07-21T12:00:02Z","type":"log","workflow":"review","phase":"Review","message":"observable","extension":{"kept":true}}' >> "$journal"
printf '%s\n' '{"sequence":4,"at":"2026-07-21T12:00:03Z","type":"runtime.completed","workflow":"review","result":"complete"}' >> "$journal"
touch "$invocation/started"
printf '%s\n' 'system complete'
`
