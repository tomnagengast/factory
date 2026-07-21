# Factory workflow reference

Factory collaborates with the selected Codex or Claude Code harness to write
dynamic JavaScript workflows, then uses the standalone `workflow` CLI to
discover and execute them. Factory does not embed the workflow loader, DSL, or
subagent runtime.

## Required commands

The workflow runner and at least the selected harness must be on `PATH`:

```sh
brew tap tomnagengast/tap
brew install --cask workflow-cli
workflow --version
workflow --help

# Codex
codex --version
codex login status

# Claude Code
claude --version
```

The workflow CLI and its documentation are public at
[`tomnagengast/workflow`](https://github.com/tomnagengast/workflow).
Factory requires workflow CLI v0.0.6 or newer so a workflow source file and
agent working directory can be selected independently and human gates can
suspend and resume through task comments.
The built `factory` resource CLI defaults to `./factory`. Use `-codex`,
`-claude`, `-factory`, or `-workflow` to supply explicit paths. See
[usage.md](usage.md) for installation.

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

The coordinator's sequential authoring path:

1. finds the pending user message,
2. assigns `<workflow-workspace>/.claude/workflows/workflow-<id>.js`,
3. appends `workflow.authoring.started`,
4. runs the selected unrestricted harness with its model and reasoning level,
5. appends each completed semantic harness step as a non-final agent comment,
6. asks the workflow CLI to validate the complete written file,
7. asks the workflow CLI to rediscover the validated file,
8. appends a completed or failed event,
9. appends one final agent comment.

Codex runs with JSONL output and Claude Code runs with verbose `stream-json`
output. Factory normalizes exposed reasoning or thinking, agent messages, tool
calls, complete tool results, errors, and unknown semantic events into ordered
comments. Transport lifecycle records and token deltas do not become comments.
The stream contains only reasoning that the harness exposes. It does not expose
private chain-of-thought.

Factory holds the newest agent text as the possible final response. If later
semantic work arrives, that text becomes a non-final message. After a clean
process exit, Factory validates and rediscovers the workflow before it appends
the remaining text once as the final response. Process, stream, validation,
and discovery failures follow the same order and end with one final error
response.

The harness runs with the workflow workspace as its working directory. Factory
exposes the resource client as `$FACTORY_CLI` and the current server as
`$FACTORY_URL`. A user can therefore ask the same authoring conversation to
create or update the trigger that will run the workflow:

```sh
"$FACTORY_CLI" trigger create '{"eventType":"task.updated","workflowId":24,"enabled":true}'
```

The workflow detail page highlights the live source as plain JavaScript. The
page receives each durable comment through the live event stream and polls the
current file once per second while any user message lacks a final agent
response, highlighting each changed source response. Intermediate steps do not
stop the updating state. The chat keeps human requests in compact bubbles and
agent prose in the narrative. It groups consecutive reasoning, tool, result,
error, and harness comments into collapsed activity summaries. Expanding a
summary and its entries shows every stored label, kind, time, and complete
content in wire order. Chat and source scroll independently on wide screens and
stack on narrow screens. The conversation opens at its latest message and
follows new replies while the reader remains at the bottom. Scrolling up pauses
that following until the reader returns to the bottom. Refreshing the page
replays the same comments in wire order.

Use `/settings` or `factory settings update` to select the harness, model,
reasoning level, workflow capacity, and ordered canned reactions. The API
supplies the supported harness option catalog, so changing a harness also
changes the available models and reasoning levels. Capacity accepts zero
through ten and defaults to six. Reactions default to
`👍, 👎, ❤️, 🎉, 😂, 👀` and the web form edits one exact value per line. The
newest `settings.updated` event applies when the coordinator next chooses work
and updates open task and comment reaction controls. It does not alter a
process already running.

Revising a discovered user workflow creates or updates the Factory-owned
local copy. The original resolved source is provided to the agent as context,
but Factory does not write back to the source repository.

## Authoring contract

The selected harness is prompted to write one complete plain-JavaScript file.
The first statement must be a literal metadata export:

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
| `gate(prompt, options?)` | Review with the default agent route, a pinned backend, or a human |
| `parallel(thunks)` | Run independent thunks concurrently |
| `pipeline(items, ...stages)` | Process items through stages |
| `workflow(name, args?)` | Dispatch another workflow |
| `phase(title)` | Report the current phase |
| `log(message)` | Emit progress |
| `budget` | Inspect the run token budget |

Workflow authoring remains one session at a time in its own lane. Triggered
workflows continue to dispatch while authoring runs, overlap up to Factory's
configured workflow capacity, and may also use the runtime's internal
concurrency.

Authoring progress is durable but is not conversation input. Later authoring
prompts include user messages and final agent responses only. Reasoning, tool
input, tool output, and harness event records remain visible without being
sent back to the harness. Display grouping does not pair tool calls with
results or alter those distinct comment records.

The workflow loader expects deterministic source:

- plain JavaScript, not TypeScript,
- literal `meta` as the first statement,
- no `Date.now()`,
- no `Math.random()`,
- no argument-free `new Date()`,
- source no larger than 512 KiB.

The authoring prompt requires the harness to validate the exact target before
replying:

```sh
workflow validate /absolute/path/to/workflow.js
```

Factory runs the same read-only command before it records
`workflow.authoring.completed`. The command parses the complete workflow and
checks the loader contract without executing the body. `workflow list` and
`workflow show` read metadata and do not validate source.

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
  "workflowId":24,
  "enabled":true
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

Set `enabled` to `false` with a full trigger update to keep the definition
visible without admitting new runs. Full updates require an explicit boolean.
Events received while disabled are discarded because re-enabling advances the
trigger's eligibility boundary:

```sh
factory trigger update 41 '{"eventType":"release.ready","workflowId":24,"enabled":false}'
factory trigger update 41 '{"eventType":"release.ready","workflowId":24,"enabled":true}'
```

Factory passes the current settings to the public workflow CLI:

```text
workflow --cwd <event-working-directory> run <workflow-source-path> \
  --args <event-and-trigger-json> \
  --backend <selected-harness> \
  --model <selected-model> \
  --allow-mutating \
  --no-validate
```

For Codex it adds `--codex-yolo` and passes
`model_reasoning_effort="<selected-reasoning>"` through `--codex-arg`. For
Claude Code it adds `--claude-yolo` and passes
`--effort <selected-reasoning>` through `--claude-arg`. Factory also supplies
the executable path configured by `-codex` or `-claude`. It exports the
current server as `$FACTORY_URL` and the absolute resource client path as
`$FACTORY_CLI` to the workflow CLI and every agent it starts.

The workflow receives `args.event`, `args.trigger`, and its integer
`args.runId`. Run progress is recorded on the event wire:

1. `workflow.run.started` creates the history item and captures the workflow
   name, phase list, exact run settings, arguments, source, and working directory,
2. every workflow CLI journal line becomes one `workflow.run.event` while the
   process is active,
3. `workflow.run.waiting` and `workflow.run.resumed` preserve human review
   pauses without holding a process or capacity slot,
4. `workflow.run.retry.requested` projects a failed run as `retrying`, and the
   next admitted attempt appends `workflow.run.resumed`,
5. `workflow.run.completed` or `workflow.run.failed` closes the run.

Workflow usage counts each `workflow.run.started` event once, regardless of
whether the run remains active, waits for a human, completes, or fails. Its
distinct task count uses only direct task context from a `task.created`,
`task.updated`, or `task.deleted` source. A workflow started from another
workflow's terminal event does not inherit the upstream task.

The CLI journal is the complete semantic runtime stream: runtime lifecycle,
phases, workflow logs, diagnostics, agent and gate prompts, cache hits, nested
workflows, results, token counts, and failures. Factory passes an explicit
temporary `--journal` path and follows appended bytes incrementally. It
durably forwards each event without parsing
stderr, filtering fields, or collapsing lifecycle pairs. A wire write failure
cancels the workflow. The temporary file is removed only after the follower
finishes.

### Retrying a failed run

Retry is a manual same-run operation:

```sh
factory history retry 30
```

`POST /api/history/{id}/retry` accepts only a run currently projected as
`failed`. It appends `workflow.run.retry.requested`, clears the old projected
output, error, and gate fields, and returns the same run as `retrying`. A
duplicate request, or a request for a running, retrying, waiting, or completed
run, returns a conflict and appends nothing.

The coordinator admits requested retries after answered human gates and before
new event triggers. It starts the same run with its complete durable journal as
both `--resume` and `--journal`. The external workflow runtime turns prior
successful steps into `step.cached` events, skips their backend calls, and
continues unfinished work. Failed steps are not cache entries. The new attempt
keeps the original run ID, `args.runId`, settings, arguments, working directory,
and workflow source path. Factory reads the current file at that captured path.
It does not snapshot workflow source or project files.

Each retry appends `workflow.run.resumed` before process start and ends in
waiting, completed, or failed. A retry that reaches a human gate uses the same
task-comment path described below. Repeated manual retries are allowed after
each later failure. They do not append another `workflow.run.started`, add a
task run row, or increment workflow usage. `runtime.started` and each later
`runtime.resumed` journal event remain the attempt boundaries in run detail.

A `retrying` request survives restart. Capacity zero or closed deployment
admission leaves it pending without a process. A crash after
`workflow.run.resumed` leaves a running run, which startup changes to failed as
usual. Missing captured context or an invalid journal returns the retry to
failed without starting the workflow. A wire write failure still cancels the
process and ends the run as failed.

Factory does not classify transient errors, retry automatically, add backoff,
or run a separate retry service. The version-pinned coordinator integration
test exercises this cache contract through the official workflow CLI v0.0.6,
which remains the minimum supported version.

### Human review gates

Gate routing stays within the existing signature:

```js
const review = await gate("Review this task before deployment.", {
  reviewer: "human",
})
```

Omitting `reviewer`, or setting it to `agent`, keeps the default
opposite-backend gate. `codex` and `claude` pin the reviewer backend.

A human gate works only in a workflow triggered by `task.created`,
`task.updated`, or `task.deleted`. When the runtime suspends, Factory:

1. records every journal event through `runtime.suspended`,
2. posts the gate prompt as an agent comment on the task,
3. projects the same run as `waiting`,
4. lets the workflow process exit and frees its capacity slot.

The next root user comment added to that task, or a direct reply to the gate
comment, resumes the run. Factory records the comment as the human
`step.completed` result, rebuilds the exact journal, and starts the same
workflow with that journal as both `--resume` and `--journal`. Earlier agent
steps replay as cache hits, the human gate returns the comment, and execution
continues after the gate under the original settings and arguments.

Without a gate `schema`, the workflow receives the comment text as a string.
With a schema, the comment must contain matching JSON. Factory replies with a
validation error and keeps the run waiting when the JSON is invalid. Unrelated
thread replies do not resume the run.

Emoji reactions never resume a human gate. Reacting to the task, the gate
prompt, or another task comment appends `reaction.updated`, not
`comment.created`. The coordinator selects only a later active user task
comment at the root or a direct reply to the gate prompt.

`/history` groups the newest five projected runs under Running, Waiting,
Retrying, Failed, and Completed, in that order. Each section links to its canonical
status path, which loads matching runs newest first in 25-run pages as the
reader scrolls. Lifecycle events move a run between these views without a page
reload. Every row links straight to its numeric `/history/{id}` detail.

Run detail displays narrative logs and collapsed activity summaries inside
contiguous phase groups. Failures remain marked while collapsed. Expanding a
summary keeps every journal event in sequence order and exposes its Factory
wire metadata, typed prompts and results, and complete raw journal object.
Step summaries correlate lifecycle records only inside one runtime attempt.
Each `runtime.started` or `runtime.resumed` event begins a new attempt, so a
cached replay after a human-gate resume stays separate from the original gate
even when both use the same workflow, step ID, and key.

Run content renders as Markdown: prose wraps within the page, while code
blocks, tables, and raw JSON scroll inside their detail panes. Links created
from Markdown syntax open in a new tab with `noreferrer`; trusted raw HTML
keeps its authored link behavior. A task-triggered run links back to its task,
including a `task.deleted` run because the task remains replayable after soft
deletion. The run detail opens at its latest event or final result and follows
later content while the reader remains at the bottom. Scrolling up pauses that
following until the reader returns to the bottom. History views update from
the same server-sent event stream as the event wire. Reader expansion choices
survive those live refetches while the view remains mounted.

### Task event triggers

For `task.created`, `task.updated`, and `task.deleted`, the event working
directory is the task project's configured local `path`. This also becomes
the working directory of agents started by the workflow. Project saves create
the required path. Factory passes the selected untracked workflow to the CLI
by its explicit source path and does not create discovery files in the project.

A trigger matches the event type only. Put finer conditions in the workflow:

```js
if (args.event.type === 'task.updated' && args.event.data.status !== 'todo') {
  return 'Ignored task outside todo.'
}
```

The only built-in content condition is terminal self-trigger suppression. A
workflow does not match its own `workflow.run.completed` or
`workflow.run.failed` event, which prevents a terminal-event trigger from
recursively starting the same workflow.

Deleted tasks remain in the projection, so Factory can still resolve the
project path for cleanup workflows triggered by `task.deleted`. Other event
types run from the configured workflow workspace. This includes
`reaction.updated`: it is observable and triggerable, but a run started from it
has no task ID or project working directory.

The four `deployment.*` event types are also generic, triggerable wire facts.
Events received while workflow admission is closed remain pending. On startup,
workflow discovery and interrupted-run recovery finish before
`deployment.resumed`, and scheduler dispatch begins only after that event.
Release, expiry, and cancellation also append `deployment.resumed` before new
admission.

## Cron triggers

Cron is represented as the event type `cron`:

```json
{
  "eventType": "cron",
  "schedule": "0 9 * * 1-5",
  "workflowId": 24,
  "enabled": true
}
```

Schedules use standard five-field cron syntax:

```text
minute hour day-of-month month day-of-week
```

Factory does not validate schedules when a trigger is written. The
coordinator ignores an invalid schedule. When a valid schedule is due and
dispatch capacity is available, Factory appends a targeted `cron` event and
lets the normal event-trigger path run the workflow.

A disabled cron trigger has no due time. Re-enabling anchors its schedule at
the update time, so missed ticks do not run immediately and the first later
scheduled tick resumes normal dispatch.

Only cron triggers need a schedule. Non-cron triggers ignore it operationally.

## Ordering and failures

One coordinator starts pending workflow conversations in a sequential authoring
lane, then prioritizes answered human gates, requested retries, event triggers,
and due cron ticks.
The authoring lane does not hold up those dispatch checks. The coordinator
claims matching trigger and source event pairs in wire order and starts
workflow processes until it reaches the configured capacity.

Trigger disable changes future admission only and does not cancel a run that
already has a `workflow.run.started` event. Conditional wire appends resolve a
concurrent disable and dispatch by durable order: disable first blocks the
stale start, while start first allows that active run to finish.

Capacity zero pauses retries, new event dispatch, and cron dispatch while
leaving authoring available. Lowering capacity does not cancel active runs; new attempts wait until
the active count falls below the new value. Raising it lets the coordinator
fill the new slots. Retrying is a durable run state, not a separate queue or service.

Deployment code can acquire `POST /api/quiescence` before replacing the
Factory process. The coordinator first closes all workflow admission, including
authoring, human-gate resumes, and failed-run retries, records `deployment.quiescing`, and then
waits until every admitted controller slot calls `Done`. A successfully
published operation records its terminal event before `Done`, but a trigger or
resume attempt whose conditional start is invalidated by the deployment event
publishes no workflow lifecycle facts. A terminal event racing with acquisition
can also appear immediately before `deployment.quiescing` while its slot still
counts active.

A completed drain records `deployment.quiesced` and returns an opaque lease
that keeps admission closed. `DELETE /api/quiescence/{lease}` records
`deployment.resumed` and reopens admission. Expiry and canceled acquisition do
the same with their own reason. If either happens before drain completion,
there is no quiesced event, resumption can report active older slots, and new
work can start before those older operations finish. Process replacement
clears the old in-memory lease. Only one quiescence acquisition can exist at a
time, and a required boundary-write failure keeps admission closed.

Failures remain observable:

- authoring errors preserve earlier steps, then become
  `workflow.authoring.failed` and one final error reply,
- run errors become `workflow.run.failed`,
- canceled runs become `workflow.run.failed` during graceful shutdown,
- startup appends `workflow.run.failed` for a prior run left `running`
  without a terminal event,
- waiting human gates survive restart because they have no active process,
- retrying requests survive restart and wait for normal admission,
- the history detail and event detail contain the recorded error,
- the server terminal contains process-level diagnostics.

There is no retry queue. A human can retry a failed run from its history detail
or with `factory history retry <id>` after correcting the cause.

## Trust boundary

Factory invokes Codex with approval, sandbox, hook-trust, and rule checks
disabled. It invokes Claude Code with permission checks disabled. Triggered
workflows also receive `--allow-mutating` and the selected backend's yolo
option.

Treat every prompt, workflow, event payload, and referenced repository as
trusted input. Keep Factory on loopback and do not use it as a multi-user or
internet-facing service.

Retrying a mutating workflow trusts the workflow CLI journal cache. Successful
cached steps are not dispatched again, but failed and unfinished work can
observe project files or other external state changed by the prior attempt.
