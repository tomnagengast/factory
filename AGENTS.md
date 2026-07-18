# Factory agent instructions

## Product boundary

- Keep Factory focused on its three mechanisms: the event wire, task intake,
  and the sequential agent loop.
- Factory is an intentionally unsafe trusted-environment demonstrator. Do not
  add authentication, permissions, policy, routing, migration, or deployment
  lifecycle machinery without explicit product direction.
- Prefer the least code that makes those mechanisms clear. Replace superseded
  designs completely instead of preserving compatibility paths.
- Codex is the default agent harness, and Claude Code is also supported. The
  external `workflow` CLI owns workflow discovery, loading, validation, and
  execution. Do not duplicate that runtime inside Factory.
- Install and document that dependency from the public
  `tomnagengast/workflow` Homebrew cask. Do not introduce private repository
  dependencies.

## Architecture patterns

- Treat the append-only JSONL wire as the durable source of truth. Resource
  writes append an event, and `api/internal/state` rebuilds projections by
  replaying the wire.
- A resource creation event ID is also its resource ID. All resources and
  events share one global integer sequence, so gaps in resource IDs are
  expected.
- The singleton harness, model, and reasoning selection is projected from
  `settings.updated` events and defaults to Codex.
- The Solid app and Go CLI are peers over the same HTTP API. A resource change
  normally touches the state data/event shapes, API handlers, CLI command
  mapping, Solid types and views, and the matching reference documentation.
- Keep workflow source outside the repository under the configured workflow
  workspace. The wire stores workflow metadata and conversation history, while
  the workflow detail endpoint reads live source from disk.
- The selected workflow-authoring harness runs in that workspace and receives
  the current server and resource CLI as `FACTORY_URL` and `FACTORY_CLI`.
- Preserve one sequential worker and its priority: pending workflow
  conversations, matching event triggers, then due cron triggers. Do not add a
  queue or parallel worker pool unless the product direction changes.
- Forward every workflow CLI semantic journal line as its own
  `workflow.run.event`. Never derive history from human stdout/stderr, filter
  journal fields, or collapse lifecycle pairs. Cancel the workflow if its next
  event cannot be appended to the wire.
- Event triggers only see matching events received after the trigger's latest
  update. Cron appends a targeted `cron` event and then uses the same trigger
  path.
- Every project requires a local path, which the API creates on save. Every
  task requires a project. Workflows triggered by `task.created`,
  `task.updated`, or `task.deleted` run from that project's configured local
  path; finer conditions such as status checks belong in workflow code.

## Working in the monorepo

- Read the relevant files under `docs/` before changing behavior, and update
  those references with the implementation.
- `api/` contains the Go server and embedded frontend, `cli/` contains the Go
  resource client, and `web/` contains the SolidJS app built with Bun.
- The frontend must be built and copied into `api/dist` before building the API
  binary. `web/dist`, `web/node_modules`, and generated `api/dist` contents are
  ignored.
- Assume a development server may already be running. Check the target port
  before starting one, use isolated wire and workflow-workspace paths for smoke
  tests, and stop every process you start.

## Verification and publication

- Before publication, run the frozen frontend build and all Go checks:

  ```sh
  bun install --cwd web --frozen-lockfile
  bun run --cwd web typecheck
  bun run --cwd web build
  rm -rf api/dist
  mkdir -p api/dist
  touch api/dist/.keep
  cp -R web/dist/. api/dist/
  go test ./...
  go test -race ./...
  go vet ./...
  ```

- Humans retain merge authority for this repository.
- Deploy only clean, merged `main` commits from
  `~/repos/tomnagengast/factory`.
- Never deploy from the T9 working mirror.
