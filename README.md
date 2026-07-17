# Factory

Factory turns a prompt into an observable agent run on one event wire.

It is intentionally a small, unsafe trusted-environment demonstrator. There is
no authentication, permission boundary, webhook verification, policy engine,
repository routing, workflow registry, approval gate, migration layer, or
deployment lifecycle.

## The loop

```text
POST /api/tasks
        |
        v
  task.submitted
        |
        v
   events.jsonl  ---> task projection ---> HTTP API + browser
        |
        v
   agent loop ---> unrestricted Codex process
        |
        +-------> run.started
        +-------> agent.output
        +-------> run.completed | run.failed
```

The append-only JSONL wire is Factory's only durable state. Task and Run views
are rebuilt by replaying these five event types:

- `task.submitted`
- `run.started`
- `agent.output`
- `run.completed`
- `run.failed`

One sequential worker always selects the oldest queued task. It runs an
ephemeral Codex process with approval prompts and sandboxing disabled, streams
stdout and stderr back through the wire, and records the terminal result before
selecting another task.

If Factory restarts while an agent is running, the interrupted Run becomes
failed during replay. Queued tasks continue normally.

## Run locally

Factory requires Go 1.26.5, Bun 1.3.11, and an authenticated `codex` CLI.

```bash
export MISE_BUN_VERSION=1.3.11
bun install --cwd frontend --frozen-lockfile
bun run --cwd frontend typecheck
bun run --cwd frontend build
go build -o factory .
./factory --workspace /path/to/a/workspace
```

Open [http://127.0.0.1:8092](http://127.0.0.1:8092).

The defaults are:

| Option | Default |
| --- | --- |
| `--addr` | `127.0.0.1:${PORT:-8092}` |
| `--data` | `~/.local/share/factory/events.jsonl` |
| `--workspace` | current directory |
| `--agent` | `codex` |

Use `./factory --help` for the complete command surface.

## HTTP API

Submit a task:

```bash
curl -sS http://127.0.0.1:8092/api/tasks \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"Explain this repository and improve its README."}' | jq .
```

Read projected tasks:

```bash
curl -sS http://127.0.0.1:8092/api/tasks | jq .
```

Read the wire from the beginning or after a sequence:

```bash
curl -sS http://127.0.0.1:8092/api/events | jq .
curl -sS 'http://127.0.0.1:8092/api/events?after=20' | jq .
```

Follow the wire as server-sent events:

```bash
curl -N 'http://127.0.0.1:8092/api/events/stream?after=0'
```

Inspect health:

```bash
curl -sS http://127.0.0.1:8092/api/health | jq .
```

## Source layout

```text
main.go             composition, configuration, process lifecycle
internal/eventwire  append, replay, wait, and event identities
internal/state      pure task and Run projection
internal/agent      sequential loop and Codex process adapter
internal/server     HTTP, SSE, and embedded frontend delivery
frontend            one-page task, Run, and wire interface
```

The frontend source is embedded directly into the Go binary. Vite is retained
only for TypeScript checking and production-build verification.

## Deliberate non-goals

Factory trusts every caller, prompt, event, configured path, and agent process.
Prompts and complete agent output are written to the wire in plain text. Codex
runs with unrestricted host access in the configured workspace.

Do not expose this service to untrusted users or networks.

Git history retains the previous production-oriented implementation and its
security, lifecycle, migration, Linear, GitHub, and deployment machinery.
Those capabilities are intentionally absent from this core.

## Verify

```bash
go test ./...
go test -race ./...
go vet ./...
MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build
```
