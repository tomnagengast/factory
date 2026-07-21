# Factory resource reference

The web application and `factory` CLI are clients of the same JSON API.
Unless noted otherwise:

- IDs are positive integers.
- request and response bodies are JSON, except media uploads,
- unknown request fields are rejected,
- `createdAt`, `updatedAt`, and `deletedAt` are server-managed,
- deletion is soft,
- list routes omit deleted records.

The default API base URL is `http://127.0.0.1:8092`.

## Common record fields

Every projected resource includes:

| Field | Type | Notes |
| --- | --- | --- |
| `id` | integer | Creation event ID |
| `createdAt` | timestamp | Creation event receipt time |
| `updatedAt` | timestamp | Latest update or deletion event time |
| `deletedAt` | timestamp or omitted | Present after soft deletion |

## Projects

| Field | Type | Required |
| --- | --- | --- |
| `name` | string | yes |
| `description` | string or null | no |
| `repo` | string or null | no |
| `path` | string | yes |
| `url` | string or null | no |

```sh
factory project create '{
  "name":"Example",
  "description":"Example project",
  "repo":"tomnagengast/example",
  "path":"/Users/me/repos/example",
  "url":"https://github.com/tomnagengast/example"
}'
```

Routes:

```text
GET    /api/projects
POST   /api/projects
GET    /api/projects/{id}
PUT    /api/projects/{id}
DELETE /api/projects/{id}
```

Project detail includes the project's active tasks. Its task objects use the
same list summaries and snapshot checkpoint as `GET /api/tasks`, described
below.
Creating or updating a project creates its local `path` if needed.

## Tasks

| Field | Type | Required |
| --- | --- | --- |
| `title` | string | yes |
| `description` | string or null | no |
| `parentTaskId` | integer or null | no |
| `status` | string | no, defaults to `backlog` |
| `projectId` | integer | yes |
| `reactions` | string array | response only; configured active values, then retired active values |

Allowed status values:

```text
backlog
todo
in progress
in review
done
canceled
```

```sh
factory task create '{
  "title":"Review the plan",
  "description":"Find blocking gaps before implementation.",
  "status":"todo",
  "projectId":12
}'
```

Routes:

```text
GET    /api/tasks
POST   /api/tasks
GET    /api/tasks/{id}
PUT    /api/tasks/{id}
DELETE /api/tasks/{id}
PUT    /api/tasks/{id}/reactions
POST   /api/tasks/{id}/comments
```

The API task list is sorted by ID descending. Each list task adds a
`commentCount` and `workflowRuns` summary. Project detail uses the same task
shape. The list and project-detail response also includes
`checkpointEventId`, the last event included in the snapshot:

```json
{
  "checkpointEventId": 123,
  "tasks": [
    {
      "id": 81,
      "title": "Improve task list display",
      "commentCount": 3,
      "workflowRuns": [
        {
          "runId": 120,
          "triggerId": 28,
          "workflowId": 24,
          "workflowName": "rpi-agentic-light",
          "status": "waiting",
          "createdAt": "2026-07-20T21:30:00Z",
          "updatedAt": "2026-07-20T21:31:12Z"
        }
      ]
    }
  ]
}
```

`commentCount` counts all active comments related to the task, including
roots, replies, user comments, and agent comments. Deleted comments do not
count, and zero is explicit. `workflowRuns` includes every task-associated
run. A retry updates the same row and preserves the same run ID.
Waiting means that a human gate has suspended the run; completed reports
workflow lifecycle completion and does not imply that the workflow changed
the task. Runs sort by workflow ID and then run ID. Empty summaries are `[]`.
Task detail returns the same complete `workflowRuns` array and a
`checkpointEventId` for live updates. Each run includes its start time in
`createdAt` and its latest lifecycle event time in `updatedAt`. The web task
detail shows elapsed wall time. Completed and failed run lengths stop at
`updatedAt`; running, waiting, and retrying lengths continue from the browser clock.

Clients that need live summaries should open
`GET /api/events/stream?after=<checkpointEventId>` with the checkpoint from
the displayed list response. The stream replays any event appended after that
atomic snapshot, including events written before the stream connection opens.

The web task list can filter by status and project, then sort or group by any
task field. Several selections within one filter match any selected value;
status and project selections must both match. Filtering happens before
sorting and grouping, so group counts include only visible tasks. The filter
control reports the visible and total task counts. Each field can select all
current choices or unselect all of them. An empty field selection removes that
restriction, so Unselect all restores unrestricted matching for that field.
The task-wide clear action removes both restrictions. A loaded list with no
matches has a separate empty state from an API response with no tasks.

The complete task-list view lives in the `/tasks` query string. Its owned keys
are `sort`, `direction`, `group`, repeated `status`, and repeated `project`.
Default and empty values are omitted. Status values use the allowed status
catalog; project values are positive integer IDs available from active
projects or represented tasks. Repeated statuses follow catalog order, and
repeated projects sort by ID to keep copied links stable. Invalid, duplicate,
and unavailable values are removed from the canonical URL without disturbing
unrelated query keys. View changes add browser-history entries, so back and
forward restore earlier filters, sorting, and grouping. The browser also
saves the latest complete view under `factory.tasks.view`. Opening a bare
`/tasks` route on a later visit restores that view and writes its canonical
query parameters. An explicit task-view query takes priority and becomes the
new saved view. Live task, comment, reaction, and workflow-run refetches keep
applying the current view.

After a task is created successfully, the web application also remembers its
project in the browser. Later task creation forms restore that project while
it remains active. A missing, invalid, or inactive saved project leaves the
required project choice blank. Failed creations and task edits do not change
the remembered project. The project must exist and not be deleted. Task
detail includes comments and artifacts.
Task resource responses always include `description` and `parentTaskId`;
unset values are `null`, so a client can use a fetched task as the basis for a
full `PUT`.

The web task detail is rendered by default and enters its form only after
selecting **Edit task**. Save persists the task; cancel discards the form.
It lists every associated workflow run between the task metadata and comments.
Each row links to run history and updates when the run starts, waits, requests
a retry, resumes, completes, or fails.
Task titles use inline Markdown, while descriptions and comments use full
trusted Markdown or HTML with syntax-highlighted code blocks. Links created
from Markdown syntax open in a new tab with `noreferrer`; trusted raw HTML
keeps its authored link behavior.

The web task form accepts local media selected through the image button below
the description, pasted from the clipboard, or dropped onto the editor. It
uploads files before task creation or update and inserts editable Markdown or
video HTML at the current selection. The picker accepts multiple files and
uses the device's native photo or file chooser. The form does not show a
rendered preview before save. See [Media](#media) for the supported formats
and limits.
On mobile Safari, copy an image, focus the editor, and select **Paste** from
the text menu. Factory accepts Safari clipboard file items as well as the
standard clipboard file list.
Task events contain those short references, not uploaded bytes, so existing
task triggers keep their normal JSON payload size and shape.

## Comments

Comments are polymorphic records related to a task or workflow. Task comments
can reply to another comment.

| Field | Type | Notes |
| --- | --- | --- |
| `relationType` | string | `task` or `workflow` |
| `relationId` | integer | Related resource ID |
| `parentCommentId` | integer or omitted | Reply parent |
| `author` | string | `user` or `agent` |
| `kind` | string | `message`, `reasoning`, `tool-use`, `tool-output`, `error`, or `event` |
| `label` | string or omitted | Tool or harness-specific label |
| `final` | boolean | Whether an agent comment answers its parent user comment |
| `content` | string | Comment text |
| `reactions` | string array | Configured active values, then retired active values; empty for workflow comments |

User comments are non-final. Workflow authoring progress comments use the root
user request as their parent and remain non-final. The one terminal agent
response is final. Historical workflow agent replies with a parent written
before this field existed replay as final responses.

Create a root task comment:

```sh
factory task comment 12 '{"content":"The build passed."}'
```

Create a reply through the API:

```sh
curl -fsS \
  -H 'Content-Type: application/json' \
  -d '{"content":"Confirmed locally.","parentCommentId":18}' \
  http://127.0.0.1:8092/api/tasks/12/comments
```

Routes:

```text
POST   /api/tasks/{taskId}/comments
POST   /api/workflows/{workflowId}/comments
GET    /api/comments/{id}
PUT    /api/comments/{id}
DELETE /api/comments/{id}
PUT    /api/comments/{id}/reactions
```

Task comment bodies use `content`; workflow conversation bodies use
`message`.

Deleting a comment soft-deletes that comment and every descendant reply.
Ancestors, sibling branches, unrelated comments, artifacts, and media remain
unchanged. The wire records one `comment.deleted` event for the selected
comment, and the projection applies the same subtree deletion. Direct comment lookup
continues to return a soft-deleted record with `deletedAt`; active relation
lists omit every deleted subtree member.

Root task comments and replies use the same media button, paste, and drop
behavior as task descriptions. Media-only comments remain valid because the
generated markup is nonblank content.

## Reactions

Tasks and task comments use one shared implicit reactor. A reaction write sets
the requested state instead of toggling it:

```sh
factory task react 12 '{"emoji":"👍","active":true}'
factory comment react 18 '{"emoji":"🎉","active":false}'
```

The same operations through HTTP are:

```text
PUT /api/tasks/{id}/reactions
PUT /api/comments/{id}/reactions
```

Both routes accept only `emoji` and the required boolean `active`. Before the
first settings update, the configured choices are:

```text
👍 👎 ❤️ 🎉 😂 👀
```

`reactionEmojis` in [Settings](#settings) replaces that ordered set. Factory
matches the decoded string exactly and accepts any configured Unicode string,
including skin-tone variants, multi-codepoint emoji, and text. It does not
trim, normalize, or resolve aliases. Responses return the updated task or
comment. Reaction arrays list configured active values in current settings
order, followed by active values removed from settings in their existing
relative order. Empty arrays serialize as `[]`. Because tasks and comments use shared record shapes, `reactions`
also appears in task lists, project detail, task detail, comment detail, and
workflow conversation responses. Workflow comments always carry an empty
array and cannot receive reactions.

Only active tasks and active task comments accept writes. Root comments,
replies at any depth, and agent gate prompts are supported. Deleting a task
alone does not disable reactions on its active comments. Deleting a comment
soft-deletes its full reply subtree, and later reaction writes to any member
of that subtree return `404`. Other missing or deleted targets also return
`404`. Adding an unconfigured value and reacting to a workflow comment return
`400`. An active value removed from settings remains visible and can be
cleared. Once cleared, it cannot be added or cleared again unless settings
include it again.

Every accepted request appends one `reaction.updated` event and advances the
target's `updatedAt`, even when the requested state already matches the current
state. These events are observable and triggerable like other wire facts. They
do not create comments, identify a reactor, carry task workflow context, or
count reactions.

## Media

Media records are immutable. Their creation event ID is also the media ID.
The wire stores metadata only; bytes are stored under the server's configured
`-media` directory with their SHA-256 value as the filename.

| Field | Type | Notes |
| --- | --- | --- |
| `name` | string | Original local filename |
| `contentType` | string | Revalidated allowed MIME type |
| `size` | integer | Byte count, at most 25 MiB |
| `sha256` | string | Lowercase content-addressed blob key |
| `url` | string | Relative retrieval URL, returned on upload |

Allowed content types:

```text
image/png
image/jpeg
image/gif
image/webp
video/mp4
video/webm
video/quicktime
```

Upload through the CLI:

```sh
factory media create ./screen.gif
```

Upload through HTTP:

```sh
curl -fsS -F file=@./clip.mp4 http://127.0.0.1:8092/api/media
```

Example response:

```json
{
  "id": 17,
  "createdAt": "2026-07-19T12:00:00Z",
  "updatedAt": "2026-07-19T12:00:00Z",
  "name": "clip.mp4",
  "contentType": "video/mp4",
  "size": 1024,
  "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "url": "/api/media/17"
}
```

Routes:

```text
POST /api/media
GET  /api/media/{id}
```

Retrieval supports HTTP ranges for video seeking and returns immutable cache
headers. Factory does not transcode media or guarantee that every browser can
play every codec inside an allowed video container.

Upload and later task or comment save are separate operations. Canceling the
editor or failing the later save leaves an unreferenced immutable blob. Media
has no update or delete route, and soft-deleting a task or comment does not
remove bytes. A publication failure after blob finalization also keeps the
unreferenced blob so a concurrent upload cannot delete shared content.

`POST /api/events` can append arbitrary `media.created` metadata. Before
retrieval, Factory revalidates the SHA-256 key, direct-child blob path,
regular-file status, allowed MIME type, declared and actual size, and safe
inline filename. A crafted event cannot select a path outside the media root
or inject response headers.

## Artifacts

Artifacts can attach content to tasks, comments, workflows, or another
relation understood by a client.

| Field | Type | Required |
| --- | --- | --- |
| `name` | string or null | no |
| `type` | string | yes |
| `content` | string | yes |
| `relationType` | string | yes |
| `relationId` | integer | yes |

Allowed types:

```text
text
link
image
document
```

```sh
factory artifact create '{
  "name":"Design notes",
  "type":"link",
  "content":"https://example.com/design",
  "relationType":"task",
  "relationId":12
}'
```

Routes:

```text
GET    /api/artifacts
POST   /api/artifacts
GET    /api/artifacts/{id}
PUT    /api/artifacts/{id}
DELETE /api/artifacts/{id}
```

Filter the list API with `relationType` and `relationId`:

```text
GET /api/artifacts?relationType=task&relationId=12
```

## Events

Events are immutable wire records rather than soft-deletable resources.

| Field | Type | Notes |
| --- | --- | --- |
| `id` | integer | Global event ID |
| `type` | string | Event type |
| `at` | timestamp | Receipt time |
| `data` | any JSON | Event payload |

Publish a custom event:

```sh
factory event create '{
  "type":"release.ready",
  "data":{"version":"1.0"}
}'
```

Routes:

```text
GET  /api/events
POST /api/events
GET  /api/events/{id}
GET  /api/events/types
GET  /api/events/stream
ANY  /api/ingest
ANY  /api/ingest/{remaining-path...}
```

`GET /api/events` returns the newest 200 events. `before=42` returns the next
older page and `limit` selects a positive page size.
`GET /api/events/stream?after=42` opens a server-sent event stream after ID
42. Event types returns all observed types plus `cron`.

The web event wire requests raw pages of 25 events and filters the loaded rows
in the browser. Its choices are the distinct types seen during the current page
session across the initial page, older pages, and live stream. Select all
chooses every current type; a type first seen later stays unselected. Unselect
all and Clear filters remove the restriction and show every loaded event. The
visible and loaded counts do not include wire history that has not been paged.
If filtering leaves the older-page marker visible, raw paging continues across
consecutive pages with no matching rows until the marker leaves the viewport,
history ends, or a request fails.

Factory reserves four projection-free deployment event types. They use the
same list, detail, type, SSE, CLI, trigger, and web event views as every other
wire fact:

```text
deployment.started
deployment.quiescing
deployment.quiesced
deployment.resumed
```

Each carries one flat payload:

| Field | Type | Notes |
| --- | --- | --- |
| `commit` | string | Emitting process release commit |
| `tree` | string | Emitting process release tree |
| `buildId` | string | Emitting process build ID |
| `deploymentId` | string | Emitting process deployment ID |
| `contractVersion` | string | Emitting process release contract |
| `workflowActive` | integer | Controller slots whose owners have not called `Done` |
| `reason` | string or omitted | `startup`, `released`, `expired`, or `canceled` on `deployment.resumed` |

The five release fields equal the matching fields in `/api/health` for the
process that wrote the event. Empty fields are kept on local starts.
`workflowActive` counts admission slots, not projected runs, and can include a
conditional trigger or resume claim that publishes no workflow facts. A
positive value on `deployment.resumed` means cancellation or expiry reopened
admission before older work finished. `deployment.quiesced` appears only after
a completed drain and proves zero slots, not one terminal fact per counted
slot. Every deployment event remains triggerable.

Universal ingress records any HTTP request without validating or normalizing
its payload:

```sh
curl -X POST \
  -H 'X-GitHub-Event: pull_request' \
  --data-binary '{"action":"opened"}' \
  'http://127.0.0.1:8092/api/ingest?source=github'
```

The optional `source` query value becomes a namespaced event type:

```text
?source=github  -> ingress.github
?source=linear  -> ingress.linear
no source       -> ingress.received
```

Event data contains `method`, `url`, `headers`, `bodyEncoding`, and `body`.
Valid UTF-8 bodies use `utf-8`; all other bytes use lossless `base64`. Headers
are stored without redaction. Every receipt, including a producer retry,
becomes a separate event.

The handler accepts configured paths beneath `/api/ingest` and returns an
empty successful OTLP/HTTP response for protobuf or `{}` for JSON. It does not
support OTLP/gRPC or provider-specific challenge responses.

## Triggers

| Field | Type | Required |
| --- | --- | --- |
| `eventType` | string | yes |
| `schedule` | string or null | no |
| `workflowId` | integer | yes |
| `enabled` | boolean | yes on PUT; creation defaults to `true` |

```sh
factory trigger create '{
  "eventType":"release.ready",
  "workflowId":24,
  "enabled":true
}'
```

Cron trigger:

```sh
factory trigger create '{
  "eventType":"cron",
  "schedule":"0 9 * * 1-5",
  "workflowId":24,
  "enabled":true
}'
```

Routes:

```text
GET    /api/triggers
POST   /api/triggers
GET    /api/triggers/{id}
PUT    /api/triggers/{id}
DELETE /api/triggers/{id}
```

Trigger updates replace the full definition and require an explicit boolean
`enabled`; omitting it or sending `null` returns `400 Bad Request`. Disable or
re-enable a trigger with the existing update command:

```sh
factory trigger update 41 '{
  "eventType":"release.ready",
  "workflowId":24,
  "enabled":false
}'

factory trigger update 41 '{
  "eventType":"release.ready",
  "workflowId":24,
  "enabled":true
}'
```

Disabled triggers retain their definitions and remain in list and detail
responses. They also remain part of the configured trigger count returned by
`GET /api/health`; only deletion removes a trigger from those views.

The web trigger list can be filtered by one or more represented event types,
workflow IDs, or both. Several selections within one filter match any selected
value; selections across the event and workflow filters must both match.
Disabled triggers remain eligible. Each active workflow option includes its
ID so workflows with the same name stay distinct, and a trigger that refers to
a missing workflow remains available as `Workflow <id>`. Filter state lasts
only while the page is mounted. Clearing the filters restores the complete
API-ordered list.

The event selector in the web application is derived from observed wire
types. Publish one event of a new type to make it available, create the
trigger, then publish a second event to run it. Older events are not replayed
into a newly created trigger. Events received while a trigger is disabled are
not replayed after it is re-enabled. Cron resumes at the first scheduled time
after the enabling update instead of running a missed tick.

Triggers for `task.created`, `task.updated`, or `task.deleted` run from the
task project's configured `path`.

## Workflows

Workflow metadata is projected from discovery and authoring events.

| Field | Type | Managed by |
| --- | --- | --- |
| `name` | string | workflow source `meta` |
| `description` | string or null | workflow source `meta` |
| `path` | string or null | Factory and workflow discovery |
| `scope` | string or null | workflow discovery |
| `phases` | string array | workflow source `meta` |
| `mutating` | boolean | workflow discovery |
| `runCount` | integer | recorded `workflow.run.started` event count |
| `taskCount` | integer | distinct directly associated task IDs across starts |

Create through an agent conversation:

```sh
factory workflow create '{
  "message":"Create a workflow that reviews a plan with three agents."
}'
```

Continue the conversation:

```sh
factory workflow comment 24 '{
  "message":"Add a final synthesis phase and list blockers first."
}'
```

`workflow update` is also accepted and has the same conversation behavior.

Routes:

```text
GET    /api/workflows
POST   /api/workflows
GET    /api/workflows/{id}
PUT    /api/workflows/{id}
DELETE /api/workflows/{id}
POST   /api/workflows/{id}/comments
```

Workflow detail includes metadata, conversation comments, artifacts, and the
current source file text. Comments appear exactly once in append-only wire
order, including live authoring reasoning, tool calls, complete tool results,
agent messages, errors, and unknown semantic harness events. The web view keeps
human and agent prose in the narrative and groups only consecutive operational
comments for display. Expanding a group shows every source comment without
pairing, filtering, or truncating it. Usage fields are recomputed from the wire
on each snapshot. Every start counts, including running, waiting, completed,
failed, and immediate-failure runs. Task usage counts distinct positive task
IDs from the run's direct task context; historical starts without `taskId`
recover it only when their immediate source is `task.created`, `task.updated`,
or `task.deleted`.

## Workflow history

Workflow history is projected from workflow run events. A run ID is its
`workflow.run.started` event ID. The one supported mutation requests a retry
of a failed run without creating a new run.

| Field | Type | Notes |
| --- | --- | --- |
| `id` | integer | Run start event ID |
| `createdAt` | timestamp | Run start time |
| `updatedAt` | timestamp | Latest run event time |
| `triggerId` | integer | Trigger that started the run |
| `workflowId` | integer | Workflow metadata ID |
| `workflowName` | string | Name captured when the run started |
| `workflowPhases` | string array | Phases captured when the run started |
| `sourceEventId` | integer | Event matched by the trigger |
| `taskId` | integer or omitted | Task associated with a task-triggered run; omitted for other runs |
| `status` | string | `running`, `waiting`, `retrying`, `completed`, or `failed`; interrupted running runs become `failed` |
| `waitingGate` | object or omitted | Current human gate prompt and journal identity |
| `gateCommentId` | integer or omitted | Agent task comment that requested review |
| `responseCommentId` | integer or omitted | User comment used for the latest resume |
| `output` | string or omitted | Final workflow result |
| `error` | string or omitted | Terminal error |

Run detail includes the complete chronological semantic event stream copied
from the workflow CLI journal. Each event has its Factory wire ID, run ID,
recorded time, workflow sequence and timestamp, type, and workflow name.
Depending on the event, it also includes phase, step ID, cache key, agent ID,
backend, kind, prompt or log message, schema, result, error, tokens,
concurrency, and budget. Starts, cache hits, suspensions, resumes, completions,
and failures remain distinct events. Each detail event also includes `raw`, the
complete journal object copied from the workflow CLI. It preserves every known
and extension field, including fields that Factory does not model in its typed
summary. `id`, `runId`, and `recordedAt` remain separate Factory wire metadata
and are not merged into `raw`.

The web run detail renders logs as narrative Markdown and groups other events
behind activity summaries. Expanded entries retain sequence order and show the
complete `raw` object. Runtime step correlation is presentation-only and is
scoped to boundaries formed by `runtime.started` and `runtime.resumed`; reused
step IDs in later attempts remain separate observations.
The web run detail links `taskId` back to the task. Replay recovers the task
association for older task-triggered runs that did not record `taskId` on the
run start event.
Graceful shutdown closes canceled runs as failed. On startup, Factory appends
a failure for any prior run still projected as `running`; `waiting` runs remain
open for a task comment.

```sh
factory history list
factory history get 30
factory history retry 30
```

Routes:

```text
GET /api/history
GET /api/history/{id}
POST /api/history/{id}/retry
```

`GET /api/history` returns runs newest first by run ID and includes the event
checkpoint captured with that page:

```json
{
  "history": [],
  "checkpointEventId": 123
}
```

The optional `status` query accepts only `running`, `waiting`, `retrying`,
`failed`, or `completed`. `before=<run-id>` uses an exclusive cursor and `limit` selects a
positive page size. The indexed `workflow_runs` SQLite projection applies the
status predicate, cursor, ordering, and limit before rows reach either client.
The web overview requests the newest five runs for each status. Its linked
status pages request 25 at a time as the reader scrolls. `factory history list`
keeps the unfiltered collection request, which defaults to 200 runs.

`GET /api/history/{id}` remains numeric run detail. Its semantic event pages
default to 200, use the same `before` and `limit` controls, and are restored to
chronological order in each response.

`POST /api/history/{id}/retry` has no request fields. It returns `202 Accepted`
with the same run projected as `retrying`, `404 Not Found` for an unknown run,
and `409 Conflict` unless the current state is `failed`. The request clears
projected terminal output, error, and stale gate fields while the earlier
terminal event and complete journal remain on the wire. Admission appends
`workflow.run.resumed` and replays that journal under the original context.
Retries do not increment workflow `runCount` or add another task-associated
run. A later matching source event still creates a distinct run.

## Settings

Settings are one global projection rather than an ID-addressed resource.

| Field | Type | Notes |
| --- | --- | --- |
| `harness` | string | `codex` or `claude` |
| `model` | string | Must belong to the selected harness |
| `reasoning` | string | Must be supported by the selected model |
| `workflowCapacity` | integer | Concurrent triggered runs, from `0` through `10` |
| `reactionEmojis` | string array | Required ordered canned-reaction choices |

```sh
factory settings get
factory settings update '{
  "harness":"claude",
  "model":"sonnet",
  "reasoning":"high",
  "workflowCapacity":6,
  "reactionEmojis":["🎉","👍","🤔"]
}'
```

Routes:

```text
GET /api/settings
PUT /api/settings
```

The GET response contains `settings` and a `harnesses` option catalog used by
the web form. An update replaces the complete settings value through one
`settings.updated` event. `reactionEmojis` must be a nonempty array of exact,
unique strings. Each entry must be valid UTF-8, nonempty, already trimmed, and
contain no carriage return or line feed. Factory performs no semantic emoji validation,
Unicode normalization, or count limit. Array order is significant.

Codex, `gpt-5.6-sol`, `low`, workflow capacity `6`, and `👍, 👎, ❤️, 🎉, 😂, 👀`
are the defaults before the first update. The `/settings` form edits reactions
as one value per line. Capacity zero pauses new event and cron trigger runs.
Lowering it does not cancel active runs. Changing reaction settings reorders
projected active reactions but does not delete them, change task or comment
timestamps, or append synthetic reaction events.

## Workflow quiescence

Quiescence is an in-memory deployment lease, not a projected resource.

```text
POST   /api/quiescence
DELETE /api/quiescence/{lease}
```

`POST` atomically stops new workflow authoring, resume, event, and cron
admission, appends `deployment.quiescing`, then waits for every admitted slot.
A successful drain appends `deployment.quiesced` before the response and keeps
admission closed:

```json
{
  "status": "quiescent",
  "lease": "<opaque token>",
  "expiresAt": "2026-07-19T20:15:00Z"
}
```

The lease lasts 15 minutes. Canceling the request before the drain completes
appends `deployment.resumed` with reason `canceled` and no quiesced fact.
Expiry uses reason `expired`; explicit `DELETE` uses `released`. The resumption
event commits before admission reopens and records the active slot count at
that boundary. A concurrent `POST` returns `409`; expiry before the drain
completes or a coordinator failure returns `503`. `DELETE` with the current
lease returns `{"status":"released"}`. An unknown or expired lease returns
`404`. Replacing the Factory process clears the lease. Any required deployment
event write failure keeps admission closed and returns `503` where a caller is
waiting.

## Health

```text
GET /api/health
```

The response includes status, active harness, workflow capacity, event and
resource counts, workflow state, and release identity when deployment
environment variables are set.

`workflowRunning` is the number of durable workflow-run projections whose
status is exactly `running`. Waiting, retrying, completed, and failed runs do not count.
This differs from `workflowActive`, which counts work admitted by the
in-memory coordinator, including workflow authoring, for quiescence and
deployment observability. `workflowQuiescing` reports whether that coordinator
has stopped accepting new work.

`checkpointEventId` is the newest event represented by the same database
snapshot as `workflowRunning`. A client displaying the health snapshot can
open `GET /api/events/stream?after=<checkpointEventId>` to replay every newer
transition, including one written before the stream connection opens. When a
view combines health with another checkpointed snapshot, it should stream
after the smaller checkpoint so no change to either displayed snapshot is
lost.

## CLI command matrix

```text
project   list, get, create, update, delete
task      list, get, create, update, delete, comment, react
comment   get, update, delete, react
artifact  list, get, create, update, delete
media     create <file>
event     list, get, create
trigger   list, get, create, update, delete
workflow  list, get, create, update, delete, comment
history   list, get, retry
settings  get, update
```

General form:

```text
factory [--url URL] <resource> <action> [id] [json|@file]
```

`media create` takes a local file path instead of JSON.

Successful responses are pretty-printed JSON. A successful delete prints
nothing. API errors are returned on stderr with the HTTP status and server
message.
