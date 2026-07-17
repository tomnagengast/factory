# Factory

Factory is an intentionally unsafe trusted-environment demonstrator for three
mechanisms:

1. an append-only event wire,
2. resource and task intake,
3. one sequential Codex loop.

Projects, tasks, comments, artifacts, triggers, and workflow metadata are
projections of a JSONL event log. The Solid web app and `factory` CLI use the
same HTTP API. No authentication, permissions, policy engine, migration
framework, or deployment lifecycle lives in the application.

## Monorepo

```text
api/    Go API, event wire, projections, workflow adapter, sequential loop
cli/    Go resource client
web/    SolidJS application built with Bun and Vite
```

The root holds repository orchestration such as `go.mod`, `nags.toml`, and
this document.

## Documentation

- [Usage](docs/usage.md) covers installation, first run, everyday operation,
  configuration, and troubleshooting.
- [Concepts](docs/concepts.md) explains the event wire, projections, task
  intake, and the sequential agent loop.
- [Resource reference](docs/resources.md) lists resource fields, HTTP routes,
  payloads, and matching CLI commands.
- [Workflow reference](docs/workflows.md) covers discovery, Codex
  collaboration, workflow files, triggers, and cron behavior.

## Run locally

Build the web bundle into the API's ignored embed staging directory:

```sh
bun install --cwd web --frozen-lockfile
bun run --cwd web typecheck
bun run --cwd web build
mkdir -p api/dist/assets
cp -R web/dist/. api/dist/
```

Then run the API:

```sh
go run ./api
```

Factory listens on `127.0.0.1:8092` by default.

```text
Usage: factory-api [options]

  -addr string
        HTTP listen address
  -agent string
        Codex executable
  -data string
        append-only event wire path
  -workflow string
        workflow CLI executable
  -workflow-workspace string
        untracked dynamic workflow workspace
```

The new domain wire defaults to
`~/.local/share/factory/wire.jsonl`. The prior demonstrator's
`events.jsonl` is left untouched as an archive.

## Web routes

```text
/                                      overview
/projects                              project list
/projects/new                          create project
/projects/:project                     view and edit project
/tasks                                 sortable and groupable task list
/tasks/new                             create task
/tasks/:task                           view and edit task, comments, artifacts
/tasks/:task/comments/:comment         directly address a comment
/events                                live event wire
/events/:event                         directly address an event
/triggers                              trigger list
/triggers/new                          create trigger
/triggers/:trigger                     view and edit trigger
/workflows                             discovered workflow list
/workflows/new                         create through Codex chat
/workflows/:workflow                   chat beside the live workflow source
```

All route IDs are integers. Deletion is soft and list routes omit deleted
records.

## Resource API

The API exposes JSON CRUD routes under `/api`:

```text
projects     GET / POST, GET / PUT / DELETE by ID
tasks        GET / POST, GET / PUT / DELETE by ID
comments     POST under a task or workflow, GET / PUT / DELETE by ID
artifacts    GET / POST, GET / PUT / DELETE by ID
events       GET / POST, GET by ID, GET types, SSE stream
triggers     GET / POST, GET / PUT / DELETE by ID
workflows    GET / POST, GET / PUT / DELETE by ID
```

`POST /api/events` accepts any event:

```json
{
  "type": "release.ready",
  "data": {
    "version": "1.0"
  }
}
```

Every accepted event type appears in the trigger event selector. `cron` is
always included.

## CLI

Build the agent-facing client:

```sh
go build -o factory ./cli
```

The client prints JSON and accepts inline JSON or `@file` bodies:

```text
factory [--url URL] <resource> <action> [id] [json|@file]
```

Examples:

```sh
factory project list
factory task create '{"title":"Review the PR","status":"todo","projectId":1}'
factory task comment 12 '{"content":"The build passed."}'
factory artifact get 18
factory workflow create '{"message":"Build a review-panel workflow."}'
factory workflow update 24 '{"message":"Add a security reviewer."}'
factory event create '{"type":"release.ready","data":{"version":"1.0"}}'
```

`FACTORY_URL` changes the default server from `http://127.0.0.1:8092`.

## Workflow loop

Factory asks the external `workflow` CLI to discover and execute dynamic
workflows. It does not embed that CLI's loader, DSL, or agent runtime.

Factory-created workflow files live outside git at:

```text
~/.local/share/factory/workflow-workspace/.claude/workflows/
```

Creating or updating a workflow appends a user chat comment. The sequential
loop sends that conversation to unrestricted Codex, Codex writes the workflow
file, and Factory appends the agent reply. Trigger execution is also explicit
Codex:

```text
workflow --cwd <task.project.path-or-workspace> run <name> \
  --backend codex \
  --allow-mutating \
  --no-validate \
  --codex-yolo \
  --args <source-event-and-trigger>
```

Event triggers match events received after the trigger's latest update. Cron
triggers append a targeted `cron` event and follow the same execution path.
Task events resolve their required project and run from its configured local
path, so workflow agents operate in the task's repository.
Workflow conversations and trigger runs share one sequential worker.

## Verify

```sh
go test ./...
go test -race ./...
go vet ./...
bun install --cwd web --frozen-lockfile
bun run --cwd web typecheck
bun run --cwd web build
```

The Nags adapter builds both Go binaries, embeds the frozen Solid bundle in
`factory-api`, and runs `factory-api`.
