# Factory



https://github.com/user-attachments/assets/a9cb3824-3a6f-486c-af14-d3ac01c54a93



Factory is an intentionally unsafe trusted-environment demonstrator for three
mechanisms:

1. an append-only event wire,
2. resource and task intake,
3. one capacity-limited workflow coordinator.

Projects, tasks, comments, artifacts, media metadata, triggers, and workflow metadata are
projections of a JSONL event log. The Solid web app and `factory` CLI use the
same HTTP API. No authentication, permissions, policy engine, migration
framework, or deployment lifecycle lives in the application.

Immutable media bytes live beside the wire in a configured local directory.
Task descriptions and task comments refer to them through `/api/media/{id}`.

## Monorepo

```text
api/    Go API, event wire, projections, workflow adapter, run coordinator
cli/    Go resource client
web/    SolidJS application built with Bun and Vite
```

The root holds repository orchestration, module metadata, and this document.

## Documentation

- [Usage](docs/usage.md) covers installation, first run, everyday operation,
  configuration, and troubleshooting.
- [Concepts](docs/concepts.md) explains the event wire, projections, task
  intake, and the workflow coordinator.
- [Resource reference](docs/resources.md) lists resource fields, HTTP routes,
  payloads, and matching CLI commands.
- [Workflow reference](docs/workflows.md) covers discovery, agent
  collaboration, workflow files, triggers, and cron behavior.

## Run locally

Build the web bundle into the API's ignored embed staging directory:

```sh
bun install --cwd web --frozen-lockfile
bun run --cwd web typecheck
bun run --cwd web build
rm -rf api/dist
mkdir -p api/dist
touch api/dist/.keep
cp -R web/dist/. api/dist/
```

Then run the API:

```sh
go build -o factory ./cli
go run ./api
```

Factory listens on `127.0.0.1:8092` by default.

```text
Usage: factory-api [options]

  -addr string
        HTTP listen address
  -claude string
        Claude Code executable
  -codex string
        Codex executable
  -data string
        append-only event wire path
  -factory string
        Factory CLI exposed to the authoring harness
  -media string
        immutable media blob directory
  -workflow string
        workflow CLI executable
  -workflow-workspace string
        untracked dynamic workflow workspace
```

The new domain wire defaults to
`~/.local/share/factory/wire.jsonl`. The prior demonstrator's
`events.jsonl` is left untouched as an archive.
Immutable media blobs default to `~/.local/share/factory/media`.

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
/triggers                              filterable trigger list with enabled state
/triggers/new                          create trigger
/triggers/:trigger                     view and edit trigger
/workflows                             discovered workflow list
/workflows/new                         create through agent chat
/workflows/:workflow                   chat beside the live workflow source
/history                               live, waiting, and completed workflow runs
/history/:item                         phase-grouped semantic event timeline
/settings                              select harness, model, reasoning, and run capacity
```

All route IDs are integers. Deletion is soft and list routes omit deleted
records.

## Resource API

The API exposes resource routes under `/api`; media creation uses multipart
form data and the other mutations use JSON:

```text
projects     GET / POST, GET / PUT / DELETE by ID
tasks        GET / POST, GET / PUT / DELETE by ID
comments     POST under a task or workflow, GET / PUT / DELETE by ID
artifacts    GET / POST, GET / PUT / DELETE by ID
media        POST one multipart file, GET immutable bytes by ID
events       GET / POST, GET by ID, GET types, SSE stream
triggers     GET / POST, GET / PUT / DELETE by ID
workflows    GET / POST, GET / PUT / DELETE by ID
history      GET list, GET run and event detail by ID
settings     GET / PUT singleton selection and option catalog
ingress      ANY request at /api/ingest or a path beneath it
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
always included. Disabled triggers remain visible through the list and detail
routes. Trigger PUT bodies must include their complete definition and an
explicit `enabled` boolean.

`/api/ingest?source=<name>` accepts any HTTP payload and records it as
`ingress.<name>` without a provider adapter. The event preserves the method,
URL, headers, and lossless UTF-8 or base64 body. Paths below `/api/ingest`
support configurable OTLP/HTTP signal endpoints.

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
factory project create '{"name":"Factory","path":"/path/to/factory"}'
factory task create '{"title":"Review the PR","status":"todo","projectId":1}'
factory task comment 12 '{"content":"The build passed."}'
factory media create ./screen.png
factory artifact get 18
factory workflow create '{"message":"Build a review-panel workflow."}'
factory workflow update 24 '{"message":"Add a security reviewer."}'
factory event create '{"type":"release.ready","data":{"version":"1.0"}}'
factory trigger update 41 '{"eventType":"release.ready","workflowId":24,"enabled":false}'
factory history get 30
factory settings update '{"harness":"claude","model":"sonnet","reasoning":"high","workflowCapacity":6}'
```

`FACTORY_URL` changes the default server from `http://127.0.0.1:8092`.

## Workflow coordinator

Factory asks the external [`workflow`](https://github.com/tomnagengast/workflow)
CLI to discover and execute dynamic workflows. It does not embed that CLI's
loader, DSL, or agent runtime.

Factory-created workflow files live outside git at:

```text
~/.local/share/factory/workflow-workspace/.claude/workflows/
```

Creating or updating a workflow appends a user chat comment. The coordinator
sends that conversation to the selected unrestricted harness. Factory appends
each exposed reasoning, tool, output, agent, error, or unknown semantic step as
an ordered live comment while the harness writes the workflow file. After
validation and rediscovery, Factory appends one final reply. The authoring
harness runs from the workflow workspace and can use `$FACTORY_CLI` against
`$FACTORY_URL` to inspect resources or create a trigger when asked. Trigger
execution uses the same selected harness, model, and reasoning:

```text
workflow --cwd <task.project.path-or-workspace> run <workflow-source-path> \
  --backend <codex-or-claude> \
  --model <selected-model> \
  --allow-mutating \
  --no-validate \
  --<harness>-yolo \
  --args <source-event-and-trigger>
```

Enabled event triggers match events received after the trigger's latest
update. Disabled triggers retain their definitions without admitting work;
events and cron ticks missed while disabled are not replayed after re-enable.
Cron resumes at the first schedule after that update. Cron triggers append a
targeted `cron` event and follow the same execution path. Disabling does not
cancel a run that already started.
Task events resolve their required project and run from its configured local
path, so workflow agents operate in the task's repository without copying or
linking the workflow source into it.
Workflow runs stream every ordered semantic journal event onto the durable
wire for the live and historical views. A task-triggered human gate posts its
prompt as a task comment, leaves the run waiting without a live process, and
resumes that same journal from the next root comment or direct reply.
Workflow conversations remain sequential. Triggered workflows run in parallel
up to the configured capacity, which defaults to six and can be set from zero
through ten in `/settings`.

## Verify

```sh
go test ./...
go test -race ./...
go vet ./...
bun install --cwd web --frozen-lockfile
bun run --cwd web typecheck
bun run --cwd web build
```

The local deployment manifest builds both Go binaries and embeds the frozen
Solid bundle in `factory-api`. Nags signs both binaries and runs them from
stable paths under `~/.local/share/factory/bin`, so macOS keeps one privacy
identity across deployments.
