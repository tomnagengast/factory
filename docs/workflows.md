# Factory workflow reference

Factory collaborates with Codex to write dynamic JavaScript workflows, then
uses the standalone `workflow` CLI to discover and execute them. Factory does
not embed the workflow loader, DSL, or subagent runtime.

## Required commands

Codex and the workflow runner must be on `PATH` when `factory-api` starts:

```sh
brew tap tomnagengast/tap
brew install --cask workflow-cli
codex --version
workflow --version
workflow --help
codex login status
```

The workflow CLI and its documentation are public at
[`tomnagengast/workflow`](https://github.com/tomnagengast/workflow).
The built `factory` resource CLI defaults to `./factory`. Use `-agent`,
`-factory`, or `-workflow` to supply explicit paths. See [usage.md](usage.md)
for installation.

## Discovery

At startup and after successful authoring, Factory runs:

```text
workflow --cwd <workflow-workspace> list --json
```

The workflow runner resolves definitions from:

1. user workflows under `~/.claude/workflows`,
2. project workflows under `.claude/workflows` from the repository root down
   to the selected working directory,
3. Factory-created workflows under the configured workflow workspace.

A project workflow shadows a user workflow with the same name. Factory
projects resolved name, path, scope, description, phases, and mutating state
onto the event wire.

## Creating and revising a workflow

Creating a workflow requires a conversation message, not source code:

```json
{
  "message": "Create a workflow that reviews a release plan with three independent agents."
}
```

The sequential loop:

1. finds the pending user message,
2. assigns `<workflow-workspace>/.claude/workflows/workflow-<id>.js`,
3. appends `workflow.authoring.started`,
4. runs unrestricted, ephemeral `codex exec`,
5. asks the workflow CLI to rediscover the written file,
6. appends a completed or failed event,
7. appends Codex's response as an agent comment.

Codex runs with the workflow workspace as its working directory. Factory
exposes the resource client as `$FACTORY_CLI` and the current server as
`$FACTORY_URL`. A user can therefore ask the same authoring conversation to
create or update the trigger that will run the workflow:

```sh
"$FACTORY_CLI" trigger create '{"eventType":"task.updated","workflowId":24}'
```

The workflow detail page polls the current file once per second while a user
message awaits an agent reply. Chat and source scroll independently on wide
screens and stack on narrow screens.

Revising a discovered user workflow creates or updates the Factory-owned
local copy. The original resolved source is provided to Codex as context, but
Factory does not write back to the source repository.

## Authoring contract

Codex is prompted to write one complete plain-JavaScript file. The first
statement must be a literal metadata export:

```js
export const meta = {
  name: 'release-review',
  description: 'Review a release plan and return blocking findings first.',
  phases: [
    { title: 'Review' },
    { title: 'Synthesize' },
  ],
}
```

The standalone runtime provides:

| Global | Purpose |
| --- | --- |
| `args` | Input supplied to the run |
| `agent(prompt, options?)` | Run one subagent |
| `gate(prompt, options?)` | Run a cross-model verdict |
| `parallel(thunks)` | Run independent thunks concurrently |
| `pipeline(items, ...stages)` | Process items through stages |
| `workflow(name, args?)` | Dispatch another workflow |
| `phase(title)` | Report the current phase |
| `log(message)` | Emit progress |
| `budget` | Inspect the run token budget |

Although workflows can use internal concurrency, Factory starts only one
workflow authoring session or trigger run at a time.

The workflow loader expects deterministic source:

- plain JavaScript, not TypeScript,
- literal `meta` as the first statement,
- no `Date.now()`,
- no `Math.random()`,
- no argument-free `new Date()`,
- source no larger than 512 KiB.

Factory-triggered runs currently pass `--no-validate`, but generated
workflows should still follow the portable contract.

## Triggering from an event

Create or observe the event type first:

```sh
factory event create '{
  "type":"release.ready",
  "data":{"version":"1.0","environment":"staging"}
}'
```

Create the trigger using the workflow's integer ID:

```sh
factory trigger create '{
  "eventType":"release.ready",
  "workflowId":24
}'
```

Publish a new matching event:

```sh
factory event create '{
  "type":"release.ready",
  "data":{"version":"1.1","environment":"production"}
}'
```

Only events received after the trigger's latest update are eligible. The
first event above makes the type selectable; it does not run the trigger
created afterward.

Factory calls:

```text
workflow --cwd <event-working-directory> run <name> \
  --args <event-and-trigger-json> \
  --backend codex \
  --allow-mutating \
  --no-validate \
  --codex-yolo
```

The workflow receives `args.event` and `args.trigger`. Run progress is
recorded as `workflow.run.started`, `workflow.run.completed`, or
`workflow.run.failed`.

### Task event triggers

For `task.created`, `task.updated`, and `task.deleted`, the event working
directory is the task project's configured local `path`. This also becomes
the working directory of agents started by the workflow. Project saves create
the required path. Factory temporarily links the selected untracked workflow
into the project's workflow catalog for the run, then removes the link.

A trigger matches the event type only. Put finer conditions in the workflow:

```js
if (args.event.type === 'task.updated' && args.event.data.status !== 'todo') {
  return 'Ignored task outside todo.'
}
```

Deleted tasks remain in the projection, so Factory can still resolve the
project path for cleanup workflows triggered by `task.deleted`. Other event
types run from the configured workflow workspace.

## Cron triggers

Cron is represented as the event type `cron`:

```json
{
  "eventType": "cron",
  "schedule": "0 9 * * 1-5",
  "workflowId": 24
}
```

Schedules use standard five-field cron syntax:

```text
minute hour day-of-month month day-of-week
```

Factory does not validate schedules when a trigger is written. The loop
ignores an invalid schedule. When a valid schedule is due, Factory appends a
targeted `cron` event and lets the normal event-trigger path run the workflow.

Only cron triggers need a schedule. Non-cron triggers ignore it operationally.

## Ordering and failures

One worker handles all authoring and trigger work. It prioritizes pending
workflow conversations, then event triggers, then due cron ticks. Later work
waits for the current Codex or workflow process to finish.

Failures remain observable:

- authoring errors become `workflow.authoring.failed` and an agent reply,
- run errors become `workflow.run.failed`,
- the event detail contains the recorded error,
- the server terminal contains process-level diagnostics.

There is no retry queue. A human can continue a failed workflow conversation
with another comment or publish another event after correcting the cause.

## Trust boundary

Factory invokes Codex with approval, sandbox, hook-trust, and rule checks
disabled. Triggered workflows also receive `--allow-mutating` and
`--codex-yolo`.

Treat every prompt, workflow, event payload, and referenced repository as
trusted input. Keep Factory on loopback and do not use it as a multi-user or
internet-facing service.
