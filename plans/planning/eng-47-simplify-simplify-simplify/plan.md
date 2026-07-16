# ENG-47: Simplify the Frontend Tasks Boundary

> updated: 2026-07-16T08:48:49-07:00

## Issue context

Linear ENG-47 asks to apply the `codebase-steward` lens and make Factory as simple as possible without giving up feature capabilities. The issue intentionally supplies no narrower acceptance criteria. Research therefore selects the smallest high-leverage slice supported by code, history, tests, and runtime evidence instead of treating the request as permission for a rewrite.

Factory is routed through the allowlisted `tomnagengast/factory` project metadata. Work occurs only on `eng-47-simplify-simplify-simplify` in its Worktrunk checkout. Human merge authority, exact verified-head ancestry, clean-main deployment, rollback compatibility, and mechanical completion safeguards remain out of scope for redesign.

## Acceptance criteria

- [ ] The Tasks frontend has one cohesive vertical owner outside the global application entrypoint.
- [ ] Native and managed Linear task details use provider-typed fetch and page owners; the broad native-or-Linear response union and runtime `"task" in value` discrimination are removed.
- [ ] `/tasks` and `/tasks/(factory|linear)/{id}` retain all existing URLs, presentation states, navigation, and capabilities.
- [ ] Native creation, editing, messages, replies, links, gates, decisions, start, cancel, revision conflicts, lifecycle evidence, and completion evidence behave unchanged.
- [ ] Managed Linear detail remains live and completely read-only, including discussion and its external Linear link.
- [ ] Same-origin credentials, per-request idempotency keys, expected revisions, conflict refetch, and current error semantics remain unchanged.
- [ ] The entrypoint remains an explicit dependency-light composition and exact-route owner; no router, query/state framework, generated client, or universal mutation abstraction is added.
- [ ] Backend APIs, persisted formats, global CSS, package dependencies, and protected lifecycle behavior are unchanged.
- [ ] Focused checks, full Factory checks, frontend typecheck/build, and proportional desktop/mobile browser verification pass.

## Research summary and evidence

The complete evidence is in `plans/planning/eng-47-simplify-simplify-simplify/research.md`.

### Current architecture

- `frontend/src/index.tsx` is 3,273 lines and owns 44 API/domain types, all frontend transport, every workspace, shared UI, formatting, polling, and route dispatch.
- `frontend/src/styles.css` is 3,321 lines. Its cascade and responsive order are behavior and will remain unchanged.
- The frontend has one Vite entry (`frontend/index.html` -> `/src/index.tsx`) and one eagerly built application bundle. The Go server owns authentication, canonical paths, and API behavior.
- History shows the monolith grew additively as Settings, Triggers, Workflows, and Tasks were delivered; it is not one stable domain.

### Representative Tasks flow

1. `/tasks` builds a filtered query, loads the retained task ledger and eligible projects, and creates native tasks only for enabled projects.
2. `/tasks/(factory|linear)/{id}` already knows the provider from the route regex.
3. The current `getTaskDetail` nevertheless returns `NativeTaskDetail | TaskSummary`.
4. `TaskDetailPage` rediscovers the provider with `"task" in value`, initializes both branches' state owners, and combines the read-only Linear surface with native mutation state.
5. Native writes use `credentials: "same-origin"`, JSON, one new `Idempotency-Key`, exact `expectedRevision` fields, refetch after success, and refetch after `409`. The server independently enforces authentication, origin, idempotency, provider ownership, strict decoding, terminal state, and revision rules.

### Baseline

- `go test ./...` passes.
- Bun 1.3.11 frozen install, frontend typecheck, and production build pass.
- Baseline production output is one JavaScript application asset and one CSS asset.
- Existing local and public Factory health match main commit `e5034d6208fbc7cfaa41fc24aa4793f2c8870c4b` with lifecycle contract 1.
- The connected in-app browser is unavailable and the `agent-browser` CLI is not installed. Browser verification will use an available authenticated UI surface, with macOS Computer Use as the fallback, rather than starting an unauthorized alternative server or skipping visual acceptance.

## Root cause

Feature implementations were appended to `frontend/src/index.tsx` because it was the only frontend module. That made the composition root an owner of unrelated domain contracts, transports, state machines, and page bodies. The Tasks route already encodes a provider distinction, but the client erased that distinction into a union and reconstructed it at runtime. This creates unnecessary invalid states and obscures which code owns native mutations versus read-only Linear presentation.

## Decisions

### 1. Extract one vertical feature, not the whole frontend

Tasks is the largest recent cohesive feature and has a concrete provider-typing problem. This issue will establish one clear module convention without forcing unrelated Workflows, Triggers, Wire, Settings, Agents, or CSS through a repository-wide shuffle.

### 2. Keep exact route composition explicit

`frontend/src/index.tsx` will continue to inspect the pathname and render exact components. For a task detail route, it will explicitly choose `NativeTaskDetailPage` or `LinearTaskDetailPage` from the already validated provider segment. No client router dependency is needed.

### 3. Extract only proven shared capabilities

Shared code will be placed in narrowly named modules only where at least two current features consume the concept:

- `frontend/src/activity.tsx`: the existing activity header, connection/loading elements, and generic activity formatting/state helpers used by Tasks and other workspaces.
- `frontend/src/agent.ts`: retained agent-run summary contracts and `agentRunHref`, used by the Agents workspace and task lifecycle evidence.
- `frontend/src/http.ts`: the existing read-only `getJSON` helper used by multiple feature reads.

These modules are not generic dumping grounds. Task-only types, errors, transport, state, and helpers remain in `tasks.tsx`.

### 4. Preserve meaningful transport differences

The native task request stays task-owned because it requires a fresh idempotency key and has task-specific conflict semantics. Workflow, Trigger, and Settings mutation paths remain separate. The change must not create a universal HTTP abstraction that hides incompatible conflict payloads or empty-response behavior.

### 5. Preserve CSS and runtime graph

The global stylesheet remains byte-for-byte unchanged. Static imports remain eager so Vite continues producing one normal application entry rather than route chunks with new loading behavior.

## Alternatives considered

- Delete the near-duplicate Linear and GitHub legacy journals: rejected because they are documented exact-sequence rollback projections, even though current helper reads use the unified wire.
- Generalize atomic persistence across Go stores: rejected because durability, fsync, temporary-file, and recovery details differ across critical stores.
- Split every frontend feature and CSS block: rejected as a broad review-obscuring shuffle with cascade risk.
- Add a client router or state/query library: rejected because current exact dispatch and Solid primitives are simpler and server authentication/canonicalization remains authoritative.
- Generalize all mutation requests: rejected because conflict, idempotency, and response contracts differ meaningfully.
- Remove dead CSS in the same change: rejected as unrelated scope.

## Non-goals

- No product redesign, new task capability, copy change, route change, styling change, or accessibility redesign.
- No Go API, server route, authorization, lifecycle, persistence, settings, workflow, trigger, deployment, or rollback change.
- No package or lockfile change.
- No migration, compatibility alias, feature flag, or staged rollout.
- No refactor of Workflows, Triggers, Wire, Settings, Agents, or their state machines beyond importing the exact shared symbols moved from the entrypoint.
- No removal of compatibility projections or weakening of human-merge and exact-head safeguards.

## Impacted files and interfaces

| File | Planned responsibility and change |
| --- | --- |
| `frontend/src/index.tsx` | Remain the application entry and exact router. Remove Tasks contracts/transport/pages/helpers and moved shared symbols. Import the new modules. Explicitly dispatch native versus Linear task detail from the validated provider route. Keep all other page bodies unchanged. |
| `frontend/src/tasks.tsx` | New cohesive Tasks vertical. Own task contracts, task/project reads, native mutation transport, list/create page, provider-typed detail pages, and task-only actor/error helpers. Export only route-level page components. |
| `frontend/src/activity.tsx` | New narrow shared activity UI module. Own the unchanged `ActivityHeader`, `InlineError`, `LoadingRows`, `formatTime`, `runStateLabel`, and `resourceState` symbols that have multiple current consumers. |
| `frontend/src/agent.ts` | New shared lifecycle model. Own the unchanged run summary/checkpoint/completion types and canonical `agentRunHref`. |
| `frontend/src/http.ts` | New shared read-only JSON helper, preserving cache, credential, status, and JSON behavior. |
| `frontend/src/styles.css` | Must not change. |
| `frontend/package.json`, `frontend/bun.lock` | Must not change. |
| `internal/server/tasks_test.go`, `internal/server/server_test.go` | Verification evidence only; no planned edits. Existing tests prove the backend invariants consumed by the moved client code. |

### Public and internal interface rules

- `tasks.tsx` exports `TasksPage`, `NativeTaskDetailPage`, and `LinearTaskDetailPage`; other task types and helpers stay module-private unless a real current caller requires them.
- `activity.tsx`, `agent.ts`, and `http.ts` export only symbols used by both `index.tsx` and `tasks.tsx`.
- Imports must remain acyclic: `index.tsx` composes feature modules; feature modules may use shared modules; shared modules never import `index.tsx` or Tasks.
- The browser-visible endpoint set, method set, body fields, headers, and error messages remain unchanged.

## Vertical implementation phases

### Phase 1: Establish narrow shared ownership

1. Move the existing generic read-only `getJSON` body unchanged into `frontend/src/http.ts`.
2. Move the run summary/checkpoint/completion types and `agentRunHref` unchanged into `frontend/src/agent.ts`.
3. Move the activity shell/error/loading and generic formatting/resource-state symbols unchanged into `frontend/src/activity.tsx`.
4. Update the existing `index.tsx` callers and imports without changing rendered markup, copy, timing, or state transitions.
5. Run frontend typecheck and production build.

Success criteria: imports are acyclic, all existing non-Tasks routes still typecheck, and the production build still emits one application entry and the stylesheet.

### Phase 2: Extract and type the Tasks vertical

1. Move task-specific contracts, reads, `TaskConflict`, `taskRequest`, `TasksPage`, detail UI, and task-only helpers into `frontend/src/tasks.tsx`.
2. Replace `getTaskDetail(provider, id): NativeTaskDetail | TaskSummary` with separate typed native and Linear reads.
3. Split the detail implementation into `NativeTaskDetailPage` and `LinearTaskDetailPage`, preserving the existing native effects, form reset timing, mutations, conflict refresh, notices, and all existing JSX.
4. Make `index.tsx` dispatch the already validated task provider explicitly to the matching page.
5. Do not touch CSS, dependencies, server code, endpoint shapes, or text.
6. Run exact simplification searches, focused server tests, frontend typecheck, and production build.

Success criteria: no broad detail union or runtime `"task" in value` discrimination remains; every task capability still has the same code path and transport contract under a provider-typed owner.

### Phase 3: Review and full verification

1. Review the base diff with moved-code detection for accidental edits, duplicated code, stale exports, circular imports, debug output, secrets, generated assets, and unrelated churn.
2. Confirm `styles.css`, `package.json`, and `bun.lock` have no diff.
3. Run focused server tests and every required Factory publication command.
4. Exercise the browser matrix at desktop and mobile sizes. Use an existing authenticated browser surface; if the connected browser remains unavailable, use macOS Computer Use. Do not expose cookies or credentials.
5. Check keyboard focus/navigation, console errors, failed network requests, and loading, empty, offline/error, conflict, and success behavior. Production browser inspection is read-only. Exercise mutation success and conflict through existing automated server tests or a disposable local fixture only; never create or modify production task data for verification.
6. Stop any process started during verification. The current managed server already owns port 8092, so do not start a duplicate unless a separate bounded local run is genuinely necessary.

Success criteria: the complete matrix passes, the worktree contains only the planned source and artifact changes, and no temporary process remains.

## Data, security, compatibility, migration, rollout, and rollback

### Data and migration

There is no database, journal, file-format, task-record, API, route, configuration, or dependency migration. The server remains the authority for all task data and lifecycle rules.

### Security

- Page and API authentication remain server-owned.
- Task requests preserve `credentials: "same-origin"`.
- Native task writes preserve JSON content type and a fresh cryptographically generated `Idempotency-Key`.
- Expected revision fields remain attached to each mutation that currently requires them.
- Managed Linear tasks remain read-only in the client and server.
- No browser credentials, cookies, local storage, or private payloads may be printed during verification.

### Compatibility

- Exact URLs, endpoint methods, payloads, response parsing, errors, styles, eager loading, and browser history behavior remain unchanged.
- Go behavior and persisted state are unchanged.
- Source rollback is an ordinary Git revert because no durable state changes.

### Rollout

The merged frontend bundle is deployed as part of the normal immutable Factory release. There is no partial or feature-flag rollout.

### Operational rollback and recovery

If post-deploy identity or browser verification fails, preserve the failed deployment receipt and use the previously successful deployment ID through the repository's verified rollback entrypoint:

```text
bin/network-app rollback factory --to <previous-successful-deployment-id>
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

Do not stash, reset, deploy from the issue worktree, or delete branches to hide a failed deployment.

## Verification matrix

| Acceptance criterion or risk | Exact verification |
| --- | --- |
| Native and Linear API/security invariants remain server-enforced | `go test ./internal/server -run 'Test(Task|ManagedLinear|NativeTask)'` |
| Tasks has one vertical owner | Review `frontend/src/tasks.tsx` ownership and `rg -n '^(type (Task|NativeTask)|function (TasksPage|TaskDetailPage|NativeTaskDetailPage|LinearTaskDetailPage)|async function (getTask|getNativeTask|getLinearTask|taskRequest))' frontend/src` to confirm task code is not duplicated across modules. |
| Provider distinction is retained in types | `rg -n 'NativeTaskDetail \| TaskSummary|"task" in value|function TaskDetailPage' frontend/src` returns no obsolete union discrimination or combined detail owner. |
| Imports remain acyclic and types remain valid | `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck` |
| Dependency and CSS behavior remain unchanged | `git diff --exit-code origin/main...HEAD -- frontend/package.json frontend/bun.lock frontend/src/styles.css` after implementation commits. |
| Production entry remains buildable | `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile`; `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build`; inspect build output for one normal app entry and CSS asset. |
| Repository behavior remains intact | `go test ./...` |
| Concurrency safety remains intact | `go test -race ./...` |
| Static correctness remains intact | `go vet ./...` |
| Public page parity | At desktop and mobile widths, load `/` and `/home`; confirm title, navigation, health state, and responsive layout. |
| Authenticated route parity | Load `/wire`, `/agents`, `/tasks`, `/workflows`, `/triggers`, `/settings`, one canonical agent observer, one native task detail, and one managed Linear detail. Confirm active navigation and no route/auth regression. |
| Native Tasks success path | Use the focused server tests and, only if a disposable local fixture exists, browser interaction to verify create/edit/message/link/gate request behavior, idempotency, and authoritative revision refresh. Production browser inspection remains read-only. |
| Conflict path | Use the existing safe server conflict fixture and, only if a disposable local fixture exists, a stale browser revision; confirm `409` causes authoritative refetch and the established conflict/error semantics. |
| Managed Linear read-only path | Confirm live description/discussion/external link render and that no mutation controls appear. |
| Loading, empty, error, and offline states | Observe a naturally empty filtered list; use bounded request blocking or a stopped disposable local surface for loading/error/offline without mutating production data. Restore connectivity and confirm recovery. |
| Accessibility and browser diagnostics | Keyboard through navigation and forms, inspect visible focus, desktop/mobile layout, console errors, and failed network requests. |
| Diff hygiene | `git diff --check`; `git status --short`; inspect `git diff --stat origin/main...HEAD` and `git diff --color-moved=dimmed-zebra origin/main...HEAD -- frontend/src`. |

## Exact post-merge deployment and recovery commands

After GitHub authoritatively reports that a human created a merge commit containing the exact checkpointed head:

```text
wt -C /Users/tom/repos/tomnagengast/factory list --format=json --branches
git -C /Users/tom/repos/tomnagengast/factory fetch --prune origin
git -C /Users/tom/repos/tomnagengast/factory merge --ff-only origin/main
test "$(git -C /Users/tom/repos/tomnagengast/factory rev-parse HEAD)" = "$(git -C /Users/tom/repos/tomnagengast/factory rev-parse origin/main)"
```

Require the primary checkout to be the single Worktrunk `is_main` checkout, tracked state to be clean, and the default branch to be `main`. From that updated primary checkout only:

```text
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
```

Require commit, tree, build ID, deployment ID, and contract version to agree across local health, public health, the current receipt, and the active release. If verification fails, do not clean up the issue branch; inspect the failed receipt and recover with:

```text
bin/network-app rollback factory --to <previous-successful-deployment-id>
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

After successful deployment, require GitHub's automatic remote-branch deletion, fetch/prune, consume every child result, remove the clean integrated issue checkout through foreground Worktrunk without force, and repeat the final health and receipt ancestry checks.

## Unresolved questions

None.
