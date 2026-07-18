# Factory concepts

Factory intentionally concentrates on three mechanisms:

1. an append-only event wire,
2. resource and task intake,
3. one sequential agent loop.

Everything else exists to make those mechanisms visible and usable.

## The system in one view

```text
Web UI ─┐
        ├─> JSON resource API ─> append-only JSONL wire
CLI ────┘                              │
                                      ├─> projected resources
                                      ├─> live event stream
                                      └─> one sequential loop
                                               │
                         ┌─────────────────────┴────────────────────┐
                         │                                          │
             selected harness authoring                  workflow CLI run
                         │                                          │
                         └──────────────> new events <──────────────┘
```

There is no database, queue service, permission layer, or embedded workflow
engine.

## Event wire

The wire is one append-only JSONL file. Every accepted fact has:

| Field | Meaning |
| --- | --- |
| `id` | Global, monotonically increasing integer event ID |
| `type` | Event name such as `task.created` or `release.ready` |
| `at` | UTC receipt timestamp |
| `data` | Event-specific JSON |

Factory writes an event before a resource appears. A creation event's ID also
becomes the created resource's ID. Updates and deletions are later events
whose data refers back to that resource ID.

`POST /api/events` accepts arbitrary event types. Custom types are useful for
triggers. Prefer names that do not collide with Factory's reserved resource
events because reserved event data is interpreted by the projection.

`/api/ingest` and every path below it accept any HTTP request without a
provider adapter. Factory records the method, URL, headers, and exact body as
one event. UTF-8 bodies remain text and other bytes use base64. A `source`
query value selects an automatically namespaced event type such as
`ingress.github`; requests without one use `ingress.received`.

`GET /api/events/stream` exposes new events as server-sent events. The web
event page and detail views use that stream to update without a page reload.

## Projections and resources

Projects, tasks, comments, artifacts, triggers, workflow metadata, workflow
run history, and the singleton agent settings are rebuilt by replaying the
wire. The wire is the durable source of truth; the resource view is derived
state.

Agent settings select a harness, model, and reasoning level. They default to
Codex and change through one `settings.updated` event. New authoring sessions
and trigger runs read the latest projection when they start.

Every project has a required local path. The API creates that directory when
the project is created or updated.

This gives Factory a deliberately simple write path:

1. validate the request,
2. append one event,
3. replay the wire,
4. return the projected resource.

Deletes are soft. A deletion event sets `deletedAt`; list routes hide deleted
records, but historical events remain on the wire.

Resource IDs share the same global sequence as events. Gaps between task or
project IDs are normal because comments, updates, workflow runs, and custom
events consume IDs too.

## Task intake

A task is a domain record, not an agent job. It carries a title, required
project, optional description and parent task, and one of five statuses:

```text
backlog, todo, in progress, done, canceled
```

Tasks can have threaded comments and polymorphic artifacts. Creating or
editing a task does not automatically invoke an agent. The task intake
mechanism is simply the shared API and wire path through which humans and
agents record work.

The CLI and web application are peers. Both call the same API, which means an
agent can create a task or comment and a human sees the result immediately in
the web application.

## Sequential agent loop

Factory owns one worker. It checks work in this order:

1. the oldest workflow conversation awaiting an agent reply,
2. an event trigger that has not started a run,
3. a due cron trigger.

Only one authoring session or workflow CLI run executes at a time. A long run
delays later work. This is the intentional sequential-loop demonstration, not
an attempt at distributed scheduling.

The loop records progress back on the same wire:

- workflow authoring started, completed, or failed,
- agent replies as workflow comments,
- every ordered runtime, phase, log, diagnostic, agent, gate, cache, nested
  workflow, result, and failure event emitted by a workflow run,
- workflow runs completed or failed,
- cron ticks as targeted `cron` events.

The event page therefore shows both user intake and the loop's response.
The history pages project those wire events into live and completed workflow
runs without a separate log store. Each semantic runtime event remains one
distinct wire record; the projection never collapses lifecycle pairs.

## Workflow source and metadata

The wire stores workflow metadata and conversation events. Workflow source is
an external file discovered by the `workflow` CLI.

Factory-created files live in:

```text
<workflow-workspace>/.claude/workflows/
```

When a user starts a workflow conversation, Factory assigns the local target
before the selected harness begins. The workflow detail API reads the current
file on every request, allowing the web application to show live source while
the agent writes. The authoring harness runs in the workflow workspace and
receives `$FACTORY_CLI` and `$FACTORY_URL`, so the same conversation can
inspect resources or configure a trigger when the user asks.

After authoring, Factory asks the workflow CLI to rediscover definitions and
projects the resolved name, description, phases, scope, path, and mutating
flag onto the wire.

## Triggers

A trigger connects an event type to a workflow. It runs only for matching
events received after the trigger's most recent update. Creating a trigger
does not replay older matching events.

The run receives:

```json
{
  "event": {
    "id": 42,
    "type": "release.ready",
    "at": "2026-07-17T23:00:00Z",
    "data": {
      "version": "1.0"
    }
  },
  "trigger": {
    "id": 41,
    "eventType": "release.ready",
    "workflowId": 12
  }
}
```

Cron uses the same path. A due schedule appends a targeted `cron` event, and
the normal event-trigger logic runs its workflow.

For `task.created`, `task.updated`, and `task.deleted`, Factory resolves the
task and its required project before running the workflow. The workflow CLI
and every agent it starts use the project's configured local `path` as their
working directory. A workflow can inspect `args.event.data`, such as a
`task.updated` status, and return without acting when its condition is not met.

## Deliberate trust model

Factory has no authentication, authorization, approval prompts, sandbox, or
policy engine. Codex and Claude Code are launched with their unrestricted
options.

That is part of the demonstrator. Factory should remain bound to loopback and
used only with trusted prompts, workflows, repositories, and local data.
