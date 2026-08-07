# Factory usage

Factory is a local, intentionally unsafe demonstrator for an event wire,
resource and task intake, and one capacity-limited workflow coordinator. It
serves a Solid web application and a JSON API from one Go process. A separate
`factory` binary provides the same resource operations to people and agents
at the command line.

Factory has no authentication or permission boundary. It invokes the selected
Codex or Claude Code harness without approvals or sandboxing. Run it only on a
machine and with data you trust. Do not bind it to a public interface.

## Getting started

### 1. Get access and install prerequisites

Your GitHub account needs access to `tomnagengast/factory`. The standalone
[`workflow`](https://github.com/tomnagengast/workflow) runner is public and
installed through Homebrew.

Install these tools:

- Git
- Go compatible with the version in `go.mod` (currently Go 1.26.5)
- Bun 1.3.11
- at least one supported harness: [OpenAI Codex CLI](https://github.com/openai/codex)
  or [Claude Code](https://docs.anthropic.com/en/docs/claude-code/getting-started)

Install the workflow runner and the harness you plan to select. For example:

```sh
brew tap tomnagengast/tap
brew install --cask workflow-cli

# Codex
brew install --cask codex

# Claude Code
curl -fsSL https://claude.ai/install.sh | bash
```

The linked harness documentation covers other platforms and install methods.

Clone Factory:

```sh
mkdir -p "$HOME/repos/tomnagengast"
git clone git@github.com:tomnagengast/factory.git \
  "$HOME/repos/tomnagengast/factory"
```

Authenticate the installed harness:

```sh
# Codex
codex login
codex login status

# Claude Code
claude
```

Verify the complete toolchain:

```sh
go version
bun --version
workflow --version
workflow --help
# Run one or both:
codex --version
claude --version
```

Only the selected harness needs to be installed. Factory depends on its
current automation flags, semantic journal, and durable human-gate lifecycle
in `workflow` 0.0.6 or newer. Confirm `codex exec --help` or `claude --help`
includes the model, reasoning or effort, and unrestricted execution options
documented in [workflows.md](workflows.md).

### 2. Build Factory

```sh
cd "$HOME/repos/tomnagengast/factory"

bun install --cwd web --frozen-lockfile
bun run --cwd web typecheck
bun run --cwd web build

rm -rf api/dist
mkdir -p api/dist
touch api/dist/.keep
cp -R web/dist/. api/dist/

go build -o factory-api ./api
go build -o factory ./cli
```

The web build must be copied into `api/dist` before the API binary is built
because Go embeds those files in `factory-api`.

The repository also contains a production container build. It performs the
same frozen frontend build, builds both Go binaries, installs pinned Linux
releases of `workflow`, Codex, Claude Code, GitHub CLI, and Cloudflare Tunnel,
and runs as a non-root user:

```sh
docker build -t factory .
docker run --rm -p 8092:8092 \
  -e DATABASE_URL -e S3_BUCKET -e S3_PREFIX -e S3_REGION \
  -e FACTORY_CREDENTIALS_KEY factory
```

The image binds the HTTP server on all container interfaces while giving
workflows the loopback server URL. It sets `SHELL=/bin/bash` for the bundled
agent harnesses and makes `/home/repos` and `/var/lib/factory/projects`
writable project roots. Durable state stays outside the container: Postgres
holds the event wire, projections, and encrypted harness credentials; S3 holds
media and validated Factory-authored workflow source. Local workflow, project,
and agent directories are scratch space.

For a hosted container platform, push the multi-platform image to a registry
and create a service from its immutable image tag. Configure:

- container port `8092`,
- health check path `/api/health`,
- managed Postgres and managed S3,
- a stable `FACTORY_CREDENTIALS_KEY` secret, and
- egress enabled for Codex, Claude Code, Git, and workflow network calls.

Supply `DATABASE_URL`, `S3_BUCKET`, `S3_PREFIX`, and `S3_REGION` through the
platform's normal secret and configuration system. No persistent filesystem is
required. Open **Settings → API credentials** to save or replace
`OPENAI_API_KEY` and `ANTHROPIC_API_KEY`.
Factory encrypts the keys outside the event wire with AES-GCM, never returns
their values, and passes both to authoring and workflow processes. Secret
environment values remain valid alternatives. Keep `FACTORY_CREDENTIALS_KEY`
stable across deployments or saved keys cannot be decrypted.

To let agents investigate repositories and act as a GitHub App instead of a
person, connect the image to the separate short-lived token broker. The
Factory image then configures `gh` and HTTPS Git automatically. The app private
key never enters the Factory container. See [GitHub App access](github-app.md)
for the complete portable setup and hosted-platform variables.

To add an external intake, create a remotely managed Cloudflare Tunnel and set
its token as the optional `TUNNEL_TOKEN` secret. Configure the tunnel with
these ordered public application routes:

1. the external hostname and path `^/api/ingest/external$`, served by
   `http://127.0.0.1:8092`, and
2. the same hostname served by `http_status:404`.

The image starts `cloudflared` only when the token exists and stops the
container if either Factory or the tunnel stops. Keep the normal private route
on port `8092`; it remains the direct internal transport.

### 3. Start the server

Before starting Factory, check whether another copy is already listening:

```sh
lsof -nP -iTCP:8092 -sTCP:LISTEN
```

If the port is free:

```sh
./factory-api
```

Leave that terminal open. Factory logs its listening address, managed storage
types, and workflow workspace at startup.

Open [http://127.0.0.1:8092](http://127.0.0.1:8092) in a browser. The overview
should load and the lower-left status should say `Coordinator connected`.
Factory follows the operating system or browser light or dark appearance and
updates when that preference changes. There is no manual appearance setting
in Factory.

Verify the API from a second terminal:

```sh
curl -fsS http://127.0.0.1:8092/api/health
```

The response should contain `"status":"ok"` and `"harness":"codex"`.
If you want Claude Code, open **Settings** and select its model and reasoning
level before starting workflow collaboration or publishing a triggered event.
The same page accepts OpenAI and Anthropic API keys. A blank credential field
keeps its current value, and the status beneath each field says whether a saved
key or server environment value will be used. CLI login state may work even
when no API key appears as configured.
The same page controls workflow capacity from zero through ten; the default is
six. It also controls the ordered canned reactions shown on every task and
task comment. Enter one exact value per line; the defaults are
`👍, 👎, ❤️, 🎉, 😂, 👀`.

### 4. Create the first resources

The shortest path is through the web application:

1. Open **Projects** and create a project.
2. Open **Tasks** and create a task assigned to that project.
3. Open the task to add a comment or artifact.
4. Open **Event wire** to see the creation events, including
   `task.comment.created` for the task discussion.

Use the image button below a task description, root comment, or reply to open
the device's native photo or file chooser. The same editors accept pasted or
dropped local PNG, JPEG, GIF, WebP, MP4, WebM, and QuickTime files. Factory
uploads each file, then inserts editable Markdown or video HTML at the current
text selection. Multiple files keep their order. There is no rendered preview
before save. Each file may be at most 25 MiB, and browser playback still
depends on codec support.

On mobile Safari, copy an image, focus the text editor, and select **Paste**
from the text menu. The visible image button remains available when the
clipboard does not expose the copied image as a file.

The CLI performs the same operations:

```sh
./factory project create \
  '{"name":"Factory demo","description":"First local project","path":"/path/to/project"}'

./factory task create \
  '{"title":"Try the event wire","status":"todo","projectId":1}'

./factory event create \
  '{"type":"demo.started","data":{"source":"getting-started"}}'
```

Every CLI response is JSON. Use the returned integer `id` for later `get`,
`update`, `delete`, or `comment` commands.

### 5. Connect an external producer

Point any HTTP webhook or log drain at the universal ingress endpoint. A
container deployment can use distinct paths to make the transport explicit:

```text
https://events.example.com/api/ingest/external?source=my-service
http://factory:8092/api/ingest/internal?source=my-service
```

Factory records each request as `ingress.my-service`. The complete headers
and body are retained, so credentials sent in headers are retained too. No
signature verification, payload schema, deduplication, or provider adapter is
involved. Bodies larger than 32 MiB are rejected without appending an event.

OTLP/HTTP exporters can use paths below the same endpoint:

```sh
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:8092/api/ingest/v1/traces?source=otel.traces
export OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://127.0.0.1:8092/api/ingest/v1/metrics?source=otel.metrics
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:8092/api/ingest/v1/logs?source=otel.logs
```

Use OTLP/HTTP with JSON or protobuf encoding. OTLP/gRPC is not supported.

### 6. Create the first workflow

Open **Workflows**, select **New workflow**, and describe the orchestration
you want. For example:

```text
Create a read-only workflow that asks three agents to review a plan for
correctness, maintainability, and missing tests, then returns one combined
report with blocking findings first.
```

Factory appends the request to the wire, assigns an untracked workflow file,
and gives the conversation to the selected harness. The workflow page shows
the conversation and live file contents side by side while the agent writes.
Exposed reasoning or thinking, tool calls, complete tool results, agent
messages, and errors appear as separate messages while the process runs. The
source stays marked **Updating** until Factory validates and rediscovers the
workflow and records one final response. Refreshing during or after the run
replays the same messages once in wire order.

That agent process runs in the workflow workspace. It receives the current
server as `$FACTORY_URL` and the CLI as `$FACTORY_CLI`, so you can ask it to
create or update a trigger alongside the workflow.

Generated files live under:

```text
~/.local/share/factory/workflow-workspace/.claude/workflows/
```

They are deliberately outside the Factory repository and are not committed
to Git. This directory is a local cache: Factory restores it from S3 at
startup and persists each successfully validated authored workflow back to S3.

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
./factory media create ./screen.gif
./factory artifact list
./factory event list
./factory workflow list
./factory workflow comment 24 '{"message":"Add a test-coverage reviewer."}'
./factory history list
./factory history get 30
./factory settings get
./factory settings update '{"harness":"claude","model":"sonnet","reasoning":"high","workflowCapacity":6,"reactionEmojis":["👍","🎉","🤔"]}'
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
| `DATABASE_URL` | required Postgres connection URL |
| `FACTORY_CREDENTIALS_KEY` | required stable base64-encoded 32-byte key |
| `S3_BUCKET` | required managed bucket |
| `S3_PREFIX` | required service-owned object prefix |
| `S3_REGION` | required AWS region |
| Workflow workspace | `~/.local/share/factory/workflow-workspace` |
| Codex executable | `codex` |
| Claude Code executable | `claude` |
| Factory CLI exposed to workflow agents | `./factory` |
| Workflow executable | `workflow` |

Example with managed state and a different port:

```sh
export DATABASE_URL='postgres://...'
export FACTORY_CREDENTIALS_KEY='<stable base64-encoded 32-byte key>'
export S3_BUCKET='...'
export S3_PREFIX='factory/local'
export S3_REGION='us-west-2'
./factory-api \
  -addr 127.0.0.1:9090 \
  -workflow-workspace "$HOME/.local/share/factory-demo/workflows"
```

Use `-codex`, `-claude`, `-factory`, and `-workflow` to supply explicit
executables:

```sh
./factory-api \
  -codex /path/to/codex \
  -claude /path/to/claude \
  -factory /path/to/factory \
  -workflow /path/to/workflow
```

## Stopping, restarting, and preserving data

Press `Ctrl-C` in the server terminal. Factory stops the HTTP server,
coordinator, and active workflow processes together. Canceled workflow runs
are recorded as failed before shutdown completes.

For a controlled deployment, acquire quiescence before replacing the process:

```sh
curl -fsS -X POST http://127.0.0.1:8092/api/quiescence
```

The request stops new workflow admission and returns only after active
controller slots drain. Factory appends `deployment.quiescing` when admission
closes and `deployment.quiesced` after the count reaches zero. Published
authoring and workflow operations write their terminal facts before freeing
their slots, though a stale conditional claim can free a slot without
publishing workflow lifecycle facts. Its response contains a 15-minute opaque
`lease`. If deployment stops before replacing the Factory process, reopen
admission with:

```sh
curl -fsS -X DELETE \
  http://127.0.0.1:8092/api/quiescence/<lease>
```

Deployment automation must release the lease on every failed or aborted path.
That release appends `deployment.resumed` with reason `released` before new
admission. Lease expiry uses reason `expired`. Canceling an acquisition uses
reason `canceled`. If cancellation or expiry happens before drain completion,
there is no `deployment.quiesced`, the resumed event can report a positive
`workflowActive`, and older work can finish after admission reopens. Factory
keeps admission closed if a required deployment event cannot be written. A
successful process replacement clears the old process's in-memory lease.

Restarting against the same Postgres database and S3 prefix preserves
resources, comments, media, events, triggers, workflow run history, encrypted
API credentials, and validated workflow files. Each process appends
`deployment.started` after opening the wire. It restores workflow source from
S3, discovers workflows, and appends a failure event for
any earlier run still projected as `running`, such as a process interrupted by
a crash or service replacement. Finally it appends `deployment.resumed` with
reason `startup` before it serves HTTP or starts scheduler admission. Waiting
human gates and pending events remain eligible after that boundary; active
workflow processes do not continue across replacement.

Nags deployments run the signed API and CLI from stable paths in
`~/.local/share/factory/bin`. This lets macOS reuse Factory's privacy grants
after a new immutable release replaces the prior build. Nags still owns the
build, replacement, health verification, rollback, and receipt. Factory's
deployment events report only the process and coordinator boundaries it can
observe.

Every semantic workflow event is part of the same durable wire. Restarting
does not depend on temporary journal files or terminal logs to rebuild run
history.

Back up or restore Postgres and the service's S3 prefix together. Postgres
identifies media and workflow records while their bytes live in S3. The local
workflow workspace is a disposable cache and is replaced from S3 at startup.
Resource deletion in the UI or API is soft deletion and does not remove
historical wire entries.
Finalized media is never deleted, including uploads left unreferenced by a
canceled editor, failed task or comment save, or event publication failure.

## Troubleshooting

### The server exits immediately

Read the terminal error first. Startup requires Postgres, S3, a valid
`FACTORY_CREDENTIALS_KEY`, the embedded frontend, and a working `workflow`
command.

Verify:

```sh
workflow --cwd "$HOME/.local/share/factory/workflow-workspace" list --json
```

That command must print a JSON array, including `[]` when no workflows exist.

### Workflow authoring fails

Check the selected harness authentication and automation flags:

```sh
codex login status
codex exec --help
claude --help
```

The workflow conversation keeps every step recorded before the failure and
ends with one final error reply. The event wire records
`workflow.authoring.failed`. Stream parsing, process, workflow validation, and
workflow discovery failures use the same terminal behavior.

### The browser shows an older UI

HTML routes revalidate on every navigation, while fingerprinted JavaScript and
CSS remain immutable. Rebuild into a clean embed directory, then rebuild
`factory-api`:

```sh
bun run --cwd web build
rm -rf api/dist
mkdir -p api/dist
touch api/dist/.keep
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
Check the trigger detail first: a disabled trigger stays visible but admits no
new runs, and work received while disabled is not replayed after re-enable.
Check `/settings` separately: workflow capacity zero pauses all trigger runs,
and a full capacity makes later runs wait for an active run to finish.
For cron triggers, use a valid standard five-field cron expression. Invalid
cron schedules are ignored. A re-enabled cron resumes at its first later
scheduled tick rather than catching up. See [workflows.md](workflows.md) for
trigger semantics.
