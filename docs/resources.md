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

Project detail includes the project's active tasks.
Creating or updating a project creates its local `path` if needed.

## Tasks

| Field | Type | Required |
| --- | --- | --- |
| `title` | string | yes |
| `description` | string or null | no |
| `parentTaskId` | integer or null | no |
| `status` | string | no, defaults to `backlog` |
| `projectId` | integer | yes |

Allowed status values:

```text
backlog
todo
in progress
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
POST   /api/tasks/{id}/comments
```

The API task list is sorted by ID descending. The web application can re-sort
or group it by any task field. It stores the selected sort field, direction,
and group field in the browser and restores them on later visits. A missing or
invalid saved preference uses ID descending with no grouping. The project must
exist and not be deleted. Task detail includes comments and artifacts. Task
resource responses always include `description` and `parentTaskId`; unset
values are `null`, so a client can use a fetched task as the basis for a full
`PUT`.

The web task detail is rendered by default and enters its form only after
selecting **Edit task**. Save persists the task; cancel discards the form.
Task titles use inline Markdown, while descriptions and comments use full
trusted Markdown or HTML with syntax-highlighted code blocks.

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
| `content` | string | Comment text |

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
```

Task comment bodies use `content`; workflow conversation bodies use
`message`.

Root task comments and replies use the same media button, paste, and drop
behavior as task descriptions. Media-only comments remain valid because the
generated markup is nonblank content.

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

`GET /api/events?after=42` returns events after ID 42.
`GET /api/events/stream?after=42` opens a server-sent event stream after ID
42. Event types returns all observed types plus `cron`.

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
current source file text.

## Workflow history

Workflow history is read-only and projected from workflow run events. A run
ID is its `workflow.run.started` event ID.

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
| `status` | string | `running`, `completed`, or `failed`; interrupted runs become `failed` |
| `output` | string or omitted | Final workflow result |
| `error` | string or omitted | Terminal error |

Run detail includes the complete chronological semantic event stream copied
from the workflow CLI journal. Each event has its Factory wire ID, run ID,
recorded time, workflow sequence and timestamp, type, and workflow name.
Depending on the event, it also includes phase, step ID, cache key, agent ID,
backend, kind, prompt or log message, result, error, tokens, concurrency, and
budget. Starts, cache hits, completions, and failures remain distinct events.
Graceful shutdown closes canceled runs as failed. On startup, Factory appends
a failure for any prior run still projected as `running`.

```sh
factory history list
factory history get 30
```

Routes:

```text
GET /api/history
GET /api/history/{id}
```

## Settings

Settings are one global projection rather than an ID-addressed resource.

| Field | Type | Notes |
| --- | --- | --- |
| `harness` | string | `codex` or `claude` |
| `model` | string | Must belong to the selected harness |
| `reasoning` | string | Must be supported by the selected model |
| `workflowCapacity` | integer | Concurrent triggered runs, from `0` through `10` |

```sh
factory settings get
factory settings update '{
  "harness":"claude",
  "model":"sonnet",
  "reasoning":"high",
  "workflowCapacity":6
}'
```

Routes:

```text
GET /api/settings
PUT /api/settings
```

The GET response contains `settings` and a `harnesses` option catalog used by
the web form. An update appends `settings.updated`; replay restores the latest
selection. Codex, `gpt-5.6-sol`, `low`, and workflow capacity `6` are the
defaults before the first update. Capacity zero pauses new event and cron
trigger runs. Lowering it does not cancel active runs.

## Health

```text
GET /api/health
```

The response includes status, active harness, workflow capacity, event and
resource counts, and release identity when deployment environment variables
are set.

## CLI command matrix

```text
project   list, get, create, update, delete
task      list, get, create, update, delete, comment
comment   get, update, delete
artifact  list, get, create, update, delete
media     create <file>
event     list, get, create
trigger   list, get, create, update, delete
workflow  list, get, create, update, delete, comment
history   list, get
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
