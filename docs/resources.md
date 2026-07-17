# Factory resource reference

The web application and `factory` CLI are clients of the same JSON API.
Unless noted otherwise:

- IDs are positive integers.
- request and response bodies are JSON.
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
| `path` | string or null | no |
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
or group it by any task field. The project must exist and not be deleted. Task
detail includes comments and artifacts.

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
```

`GET /api/events?after=42` returns events after ID 42.
`GET /api/events/stream?after=42` opens a server-sent event stream after ID
42. Event types returns all observed types plus `cron`.

## Triggers

| Field | Type | Required |
| --- | --- | --- |
| `eventType` | string | yes |
| `schedule` | string or null | no |
| `workflowId` | integer | yes |

```sh
factory trigger create '{
  "eventType":"release.ready",
  "workflowId":24
}'
```

Cron trigger:

```sh
factory trigger create '{
  "eventType":"cron",
  "schedule":"0 9 * * 1-5",
  "workflowId":24
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

The event selector in the web application is derived from observed wire
types. Publish one event of a new type to make it available, create the
trigger, then publish a second event to run it. Older events are not replayed
into a newly created trigger.

Triggers for `task.created`, `task.updated`, or `task.deleted` run from the
task project's configured `path`. A missing project path produces a failed
workflow run.

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

Create through a Codex conversation:

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

## Health

```text
GET /api/health
```

The response includes status, active agent backend, event and resource
counts, and Nags release identity when those environment variables are set.

## CLI command matrix

```text
project   list, get, create, update, delete
task      list, get, create, update, delete, comment
comment   get, update, delete
artifact  list, get, create, update, delete
event     list, get, create
trigger   list, get, create, update, delete
workflow  list, get, create, update, delete, comment
```

General form:

```text
factory [--url URL] <resource> <action> [id] [json|@file]
```

Successful responses are pretty-printed JSON. A successful delete prints
nothing. API errors are returned on stderr with the HTTP status and server
message.
