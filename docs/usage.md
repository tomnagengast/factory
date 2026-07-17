# Factory usage

Factory is a local, intentionally unsafe demonstrator for an event wire,
resource and task intake, and one sequential Codex loop. It serves a Solid
web application and a JSON API from one Go process. A separate `factory`
binary provides the same resource operations to people and agents at the
command line.

Factory has no authentication or permission boundary. It invokes Codex
without approvals or sandboxing. Run it only on a machine and with data you
trust. Do not bind it to a public interface.

## Getting started

### 1. Get access and install prerequisites

The current setup uses two private repositories. Your GitHub account needs
access to:

- `tomnagengast/factory`, the application
- `tomnagengast/cmptr`, which contains the standalone `workflow` runner and
  workflow examples

Install these tools:

- Git
- Go compatible with the version in `go.mod` (currently Go 1.26.5)
- Bun 1.3.11
- Node.js 22 or newer for the `workflow` runner
- the current [OpenAI Codex CLI](https://github.com/openai/codex)

On macOS, Codex can be installed with Homebrew:

```sh
brew install --cask codex
```

The official Codex repository also documents standalone and npm installation
options for macOS and Linux.

Clone both repositories at the paths expected by Factory:

```sh
mkdir -p "$HOME/repos/tomnagengast"
git clone git@github.com:tomnagengast/factory.git \
  "$HOME/repos/tomnagengast/factory"
git clone git@github.com:tomnagengast/cmptr.git "$HOME/cmptr"
```

Put the standalone workflow runner on `PATH`:

```sh
mkdir -p "$HOME/.local/bin"
ln -sfn "$HOME/cmptr/bin/workflow" "$HOME/.local/bin/workflow"
export PATH="$HOME/.local/bin:$PATH"
```

Add that `PATH` export to your shell profile if `~/.local/bin` is not already
available in new terminals.

Authenticate Codex:

```sh
codex login
codex login status
```

Verify the complete toolchain:

```sh
go version
bun --version
node --version
codex --version
workflow --help
```

Factory depends on the current Codex automation flags. If the server reports
an unknown Codex option, update Codex and confirm that `codex exec --help`
includes `--ephemeral`, `--dangerously-bypass-approvals-and-sandbox`,
`--dangerously-bypass-hook-trust`, and `--ignore-rules`.

### 2. Build Factory

```sh
cd "$HOME/repos/tomnagengast/factory"

bun install --cwd web --frozen-lockfile
bun run --cwd web typecheck
bun run --cwd web build

mkdir -p api/dist/assets
cp -R web/dist/. api/dist/

go build -o factory-api ./api
go build -o factory ./cli
```

The web build must be copied into `api/dist` before the API binary is built
because Go embeds those files in `factory-api`.

### 3. Start the server

Before starting Factory, check whether another copy is already listening:

```sh
lsof -nP -iTCP:8092 -sTCP:LISTEN
```

If the port is free:

```sh
./factory-api
```

Leave that terminal open. Factory logs its listening address, wire path,
workflow workspace, and backend at startup.

Open [http://127.0.0.1:8092](http://127.0.0.1:8092) in a browser. The overview
should load and the lower-left status should say `Codex connected`.

Verify the API from a second terminal:

```sh
curl -fsS http://127.0.0.1:8092/api/health
```

The response should contain `"status":"ok"` and `"agent":"codex"`.

### 4. Create the first resources

The shortest path is through the web application:

1. Open **Projects** and create a project.
2. Open **Tasks** and create a task assigned to that project.
3. Open the task to add a comment or artifact.
4. Open **Event wire** to see the creation events.

The CLI performs the same operations:

```sh
./factory project create \
  '{"name":"Factory demo","description":"First local project"}'

./factory task create \
  '{"title":"Try the event wire","status":"todo"}'

./factory event create \
  '{"type":"demo.started","data":{"source":"getting-started"}}'
```

Every CLI response is JSON. Use the returned integer `id` for later `get`,
`update`, `delete`, or `comment` commands.

### 5. Create the first workflow

Open **Workflows**, select **New workflow**, and describe the orchestration
you want. For example:

```text
Create a read-only workflow that asks three agents to review a plan for
correctness, maintainability, and missing tests, then returns one combined
report with blocking findings first.
```

Factory appends the request to the wire, assigns an untracked workflow file,
and gives the conversation to Codex. The workflow page shows the conversation
and live file contents side by side while Codex writes it.

Generated files live under:

```text
~/.local/share/factory/workflow-workspace/.claude/workflows/
```

They are deliberately outside the Factory repository and are not committed
to Git.

## Everyday commands

Build only the agent-facing client:

```sh
go build -o factory ./cli
```

Inspect its complete command surface:

```sh
./factory help
```

Common operations:

```sh
./factory project list
./factory task list
./factory task get 12
./factory task comment 12 '{"content":"The local build passed."}'
./factory artifact list
./factory event list
./factory workflow list
./factory workflow comment 24 '{"message":"Add a test-coverage reviewer."}'
```

Pass request JSON inline or from a file:

```sh
./factory task create @task.json
```

Point the CLI at another Factory server with either form:

```sh
FACTORY_URL=http://127.0.0.1:9090 ./factory task list
./factory --url http://127.0.0.1:9090 task list
```

See [resources.md](resources.md) for accepted fields and route mappings.

## Server configuration

Show the runtime flags:

```sh
./factory-api -h
```

The defaults are:

| Setting | Default |
| --- | --- |
| Listen address | `127.0.0.1:$PORT`, or `127.0.0.1:8092` |
| Event wire | `~/.local/share/factory/wire.jsonl` |
| Workflow workspace | `~/.local/share/factory/workflow-workspace` |
| Agent executable | `codex` |
| Workflow executable | `workflow` |

Example with isolated state and a different port:

```sh
./factory-api \
  -addr 127.0.0.1:9090 \
  -data "$HOME/.local/share/factory-demo/wire.jsonl" \
  -workflow-workspace "$HOME/.local/share/factory-demo/workflows"
```

Use `-agent` and `-workflow` when the executables are not on `PATH`:

```sh
./factory-api \
  -agent /path/to/codex \
  -workflow "$HOME/cmptr/bin/workflow"
```

## Stopping, restarting, and preserving data

Press `Ctrl-C` in the server terminal. Factory stops the HTTP server and
sequential loop together.

Restarting with the same `-data` and `-workflow-workspace` values preserves
resources, comments, events, triggers, and generated workflow files.

To begin with empty state, stop Factory and move both paths aside:

```sh
mv "$HOME/.local/share/factory/wire.jsonl" \
  "$HOME/.local/share/factory/wire.backup.jsonl"
mv "$HOME/.local/share/factory/workflow-workspace" \
  "$HOME/.local/share/factory/workflow-workspace.backup"
```

Factory recreates the paths on the next start. Resource deletion in the UI
or API is soft deletion and does not remove historical wire entries.

## Troubleshooting

### The server exits immediately

Read the terminal error first. Startup requires a writable wire path, the
embedded frontend, and a working `workflow` command.

Verify:

```sh
workflow --cwd "$HOME/.local/share/factory/workflow-workspace" list --json
```

That command must print a JSON array, including `[]` when no workflows exist.

### Codex authoring fails

Check authentication and automation flags:

```sh
codex login status
codex exec --help
```

The workflow conversation records the agent process error as an agent reply,
and the event wire records `workflow.authoring.failed`.

### The browser shows an older UI

Rebuild and re-embed the web bundle, then rebuild `factory-api`:

```sh
bun run --cwd web build
cp -R web/dist/. api/dist/
go build -o factory-api ./api
```

Restart the server after rebuilding.

### Port 8092 is already in use

Inspect the owner:

```sh
lsof -nP -iTCP:8092 -sTCP:LISTEN
```

Reuse the running server, stop it deliberately, or choose another loopback
port with `-addr`.

### A trigger did not run

The event must be received after the trigger was created or last updated.
For cron triggers, use a valid standard five-field cron expression. Invalid
cron schedules are ignored. See [workflows.md](workflows.md) for trigger
semantics.
