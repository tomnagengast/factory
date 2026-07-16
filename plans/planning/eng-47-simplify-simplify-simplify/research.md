# ENG-47 Simplification Research

## Research questions

1. Where is Factory's current accidental complexity concentrated, and which candidate can be simplified without changing product capabilities or protected lifecycle behavior?
2. What representative runtime flows and interfaces participate in the chosen slice?
3. Which invariants, compatibility boundaries, and security properties must remain unchanged?
4. What is the smallest high-leverage refactor that improves ownership rather than merely moving code or introducing a generic abstraction?
5. How can the observable acceptance criteria be verified with the repository's actual tools and runtime?
6. What exact post-merge deployment, health, rollback, and recovery procedures apply?
7. Which alternatives were considered, and why are they not the right slice for ENG-47?

## Issue and routing context

- Linear issue: `ENG-47`, "Simplify simplify simplify."
- Explicit direction: use the `codebase-steward` skill and make the repository as simple as possible without giving up feature capabilities.
- The issue has no parent, children, attachments, relations, prior comments, or more specific acceptance criteria.
- The Linear project routes to `tomnagengast/factory` at `/Users/tom/repos/tomnagengast/factory`. The Factory-managed repository and project metadata resolve to the same Git top-level and normalized origin.
- The isolated issue branch is `eng-47-simplify-simplify-simplify`, based on `origin/main` at `e5034d6208fbc7cfaa41fc24aa4793f2c8870c4b`.
- The fresh issue label snapshot contains `Yolo`, but each approval gate must refresh that label independently.

## Repository and runtime baseline

- Factory is a single Go 1.26.5 service and CLI with a SolidJS 1.9 / TypeScript 5.9 frontend built by Bun 1.3.11 and Vite 7 (`go.mod`, `frontend/package.json`, `frontend/tsconfig.json`, `nags.toml`).
- `serveConfigured` is the composition root. It opens persistent stores, builds the unified event wire, repository resolvers, run manager, task service, HTTP server, schedulers, and lifecycle observers (`main.go:88-617`).
- The Go server serves the already-built `frontend/dist` directory and returns the frontend document for recognized page routes (`main.go:98-102`, `internal/server/server.go:472-482`, `internal/server/server.go:1219-1229`). Vite's only source entry is `frontend/src/index.tsx` (`frontend/index.html:16`).
- The current deployed loopback and public health endpoints both reported `status=ok`, `contractVersion=1`, and commit `e5034d6208fbc7cfaa41fc24aa4793f2c8870c4b`. An existing Factory process already owns `127.0.0.1:8092`; no additional server was started.
- Baseline `go test ./...` passed across all packages. The frozen Bun install, frontend typecheck, and production build passed. The baseline production bundle was one 109.13 kB JavaScript asset and one 50.30 kB CSS asset.
- GitHub repository policy is already compatible with Factory's exact-head gate: merge commits enabled, squash and rebase disabled, and automatic branch deletion enabled.

## Architecture and concentration of complexity

### Go lifecycle

A representative managed run flows through these owners:

1. `main` dispatches the CLI surface or calls `serveConfigured` (`main.go:61-88`).
2. The server authenticates and normalizes Linear or GitHub webhooks into `eventwire.Event` records (`internal/server/server.go:708-931`).
3. The coordinated wire evaluates generic routing, while protected dispatch maintains activity and compatibility projections (`main.go:192-252`, `internal/server/server.go:933-1097`).
4. `triggerrouter.Manager` and `agentrun.Manager` claim, prepare, launch, park, resume, and mechanically validate repository-scoped runs (`main.go:437-459`, `internal/agentrun/manager.go:137-513`).
5. Completion evidence requires exact verified-head ancestry, clean updated main, deployment identity where applicable, child completion, task or Linear completion, and branch/worktree cleanup (`internal/agentrun/completion.go`, `internal/agentrun/completion_system.go`).

This flow is large but protects consequential routing, durability, human-merge, deployment-source, and completion invariants. Broadly reorganizing it would have a wide blast radius and would not be the smallest safe simplification.

### Frontend

`frontend/src/index.tsx` is 3,273 lines and currently owns all of the following:

- 44 API and domain contract types (`index.tsx:15-454`);
- every GET and mutation transport, including distinct conflict and idempotency behavior (`index.tsx:469-691`);
- the public home, authenticated home, wire, agents, triggers, workflows, settings, tasks, and run-observer workspaces (`index.tsx:693-3068`);
- shared navigation, fields, toggles, loading/error elements, charts, formatting, and lifecycle helpers;
- exact-path and dynamic route dispatch (`index.tsx:3228-3273`).

`frontend/src/styles.css` is 3,321 lines, but its cascade and media-query order are behavior. CSS partitioning is not required to establish the selected TypeScript ownership boundary and would add visual risk.

History proves the TypeScript monolith is additive growth rather than one deliberately cohesive module. Settings added roughly 568 TypeScript lines, Triggers 603, Workflows hundreds more, and native Tasks 466 plus later increments, all in the same file. The repository still favors explicit Solid primitives and plain functions; adding a client router, query library, dependency-injection layer, generated client, or general state framework would increase machinery.

## Chosen slice: a typed Tasks vertical module

### Current behavior and root cause

The Tasks surface is the clearest stable feature seam and exposes one concrete invalid-state representation:

- `/tasks` lists retained native Factory and managed Linear tasks and creates native tasks only for enabled projects (`index.tsx:2508-2660`).
- `/tasks/{provider}/{id}` already limits `provider` to `factory|linear` in the route regex (`index.tsx:3235`).
- Despite that known provider, `getTaskDetail` returns `Promise<NativeTaskDetail | TaskSummary>` (`index.tsx:504-510`).
- `TaskDetailPage` then rediscovers the provider by checking `"task" in value`, creates both native and Linear memos, and owns native mutation/editor state alongside the completely read-only Linear view (`index.tsx:2662-2832`).
- Native writes require a new idempotency key per request, exact expected revisions, authoritative refetch after success, and authoritative refetch on `409` (`index.tsx:511-544`, `index.tsx:2690-2748`). Linear detail is fetched live and has no mutation controls.

The root cause is that new product work was appended to one composition-root file. It obscures a domain boundary that the URL, API, authorization policy, and UI behavior already recognize.

### Proposed direction

Create one cohesive `frontend/src/tasks.tsx` module that owns:

- task-specific API contracts;
- task index and project reads;
- the idempotent native mutation request;
- `TasksPage`;
- a tiny provider-aware task detail dispatcher or explicit route exports;
- separately typed `NativeTaskDetailPage` and `LinearTaskDetailPage` implementations;
- task-only formatting and error helpers.

Move only genuinely shared UI and lifecycle concepts to narrowly named shared modules so `index.tsx` can import Tasks without a circular dependency. The likely shared surface is:

- common activity shell components and generic formatting/resource-state helpers;
- agent-run summary types and the canonical run link helper, because task lifecycle evidence legitimately embeds a retained run.

Keep `frontend/src/index.tsx` as the explicit application composition root and exact route dispatcher. Do not add a client-router dependency, a universal API client, a generic state layer, or barrel exports.

### Required preserved capabilities and invariants

The refactor must preserve all of the following exactly:

- `/tasks` and `/tasks/(factory|linear)/{id}` URLs, navigation order, titles, loading, empty, failure, and success states;
- authenticated server ownership of protected pages and API routes;
- native task creation, editing, approval mode, messages and replies, evidence links, gates and decisions, start, cancel, lifecycle evidence, completion evidence, and refresh controls;
- managed Linear task live detail, discussion, external link, and complete read-only presentation;
- fresh random `Idempotency-Key` on every native task mutation;
- exact optimistic `expectedRevision` fields and refetch-on-conflict behavior;
- same-origin credentials, JSON bodies, response handling, and current error semantics;
- no change to Go APIs, persisted data, lifecycle state, routing, authorization, or deployment behavior;
- the single eager Vite application entry and existing global stylesheet/cascade;
- polling ownership and stop behavior for agent history referenced by Tasks.

### Observable acceptance criteria

1. The Tasks feature has one clear TypeScript owner outside the global entrypoint.
2. Native and Linear task detail fetches and page implementations are provider-typed; the broad `NativeTaskDetail | TaskSummary` fetch and runtime `"task" in value` discrimination are gone.
3. Every existing Tasks capability and route remains available with unchanged transport, security, revision, idempotency, error, and refresh behavior.
4. `frontend/src/index.tsx` remains a small explicit composition and routing owner, while shared modules contain only concepts used by more than one feature.
5. No new runtime or package dependency is introduced, global CSS behavior is unchanged, and Vite still emits the normal single application entry.
6. All required Factory verification and authenticated desktop/mobile browser checks pass.

## Interfaces and callers

- `frontend/index.html` imports only `/src/index.tsx`; that entrypoint must continue importing the stylesheet and rendering exactly one route.
- `internal/server/server.go` owns authentication, canonical paths, and all task endpoints. Existing server tests cover native/Linear auth, same-origin writes, idempotency, read-only Linear behavior, live Linear detail, conflicts, lifecycle projections, and route rejection (`internal/server/tasks_test.go`, `internal/server/server_test.go`).
- `TaskSummary.latestRun` and `NativeTaskDetail.latestRun` consume `AgentActivityRun`; `agentRunHref` constructs the canonical observer URL from issue identity and start time (`index.tsx:247-264`, `index.tsx:3070-3075`). That is a legitimate shared lifecycle concept, not a reason for Tasks to depend on the global entrypoint.
- Shared `ActivityHeader`, `InlineError`, `LoadingRows`, `formatTime`, `runStateLabel`, and `resourceState` are already used across multiple workspaces and can move without changing their bodies.

## Alternatives considered

### Remove the legacy Linear and GitHub projection journals

A read-only Go assessment identified roughly 500 lines of near-duplicate `linearhook.Journal` and `githubhook.Journal` code. Current agent helper reads use the unified `system-events.jsonl`, so deleting the old projections looks attractive.

Rejected for ENG-47. README explicitly states that `github-events.json` and `linear-comments.json` remain exact-sequence post-wire rollback projections. `main.go` seeds unified channel totals from them, and server dispatch updates them only after authoritative wire sequencing. Removing those stores would change the rollback contract, violating the repository instruction to preserve rollback compatibility and the issue's no-capability-loss condition.

### Generalize the atomic persistence writers

Many Go stores repeat atomic temporary-file, permission, sync, and rename patterns. Rejected because their durability and directory-sync details differ, and changing nearly every critical store at once has a much larger correctness and recovery blast radius than the selected frontend seam.

### Split every frontend feature and stylesheet in one change

The feature seams are real, but a repository-wide file shuffle would make review and behavior comparison harder. Global CSS order is a real cascade dependency. The selected Tasks vertical is the largest recent cohesive feature and establishes a repeatable local convention without forcing unrelated Workflows, Triggers, Wire, Settings, or Agents code through the same change.

### Add a generic HTTP request abstraction

Rejected. Task requests, workflow requests, trigger saves, and settings saves have meaningfully different conflict payloads, empty-response behavior, idempotency requirements, and ownership semantics. A universal wrapper would hide rather than simplify those differences.

### Add a client router or state/query library

Rejected. Static exact-path dispatch and Solid primitives are explicit, dependency-light, and already aligned with server-owned authentication and canonicalization.

### Remove dead CSS while restructuring

Three selectors (`.event-feed`, `.trigger-grid`, and `.settings-note`) have no current source consumer. Their removal is low risk but unrelated to the chosen Tasks ownership boundary. Keeping them out of this slice makes the refactor easier to review and preserves the plan's bounded scope.

## Security, compatibility, migration, and rollback

- This is a source-organization refactor. There is no persisted-data migration, API migration, route migration, configuration change, or dependency change.
- Server-side authentication, same-origin checks, provider ownership, strict decoding, idempotency, revision conflicts, terminal-task rules, and read-only Linear enforcement remain authoritative and unchanged.
- Source rollback is an ordinary Git revert because no durable format changes. The global CSS file stays unchanged.
- Operational rollback after deployment uses Factory's verified release mechanism, not a dirty checkout or issue worktree.

## Verification evidence and matrix

| Concern | Evidence or exact check |
| --- | --- |
| Baseline repository correctness | `go test ./...` passed before edits. |
| Baseline frontend graph | `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile`; `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck`; `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` passed before edits. |
| Native and Linear task API invariants | `go test ./internal/server -run 'Test(Task|ManagedLinear|NativeTask)'` plus `go test ./...`. |
| Provider typing simplification | Exact search confirms the broad union-returning task-detail fetch and `"task" in value` discrimination no longer exist. |
| No new dependency | `git diff -- frontend/package.json frontend/bun.lock` is empty. |
| CSS/cascade unchanged | `git diff -- frontend/src/styles.css` is empty. |
| Frontend types and application entry | Frozen install, `typecheck`, and `build`; inspect output for one normal application entry and successful style bundling. |
| Required Factory publication suites | `go test ./...`; `go test -race ./...`; `go vet ./...`; frozen Bun install; frontend typecheck; frontend build. |
| Public and authenticated route parity | Exercise `/`, `/home`, `/wire`, `/agents`, `/tasks`, one native task detail, one managed Linear detail, `/workflows`, `/triggers`, `/settings`, and one run observer using the existing authenticated Factory service or a bounded local browser fallback. |
| Responsive and accessible behavior | Inspect desktop and mobile widths; keyboard navigation and focus; loading, empty, error, conflict, offline, and success states; browser console and network failures. |
| Deployment identity | After human merge and clean-main deployment, loopback and public `/api/healthz` must report the exact deployed commit/tree/build/deployment/contract identity, and the current receipt must agree. |

The connected in-app browser was unavailable during research. That does not change the acceptance criteria; final verification will use the repository-approved local browser fallback if the authenticated browser surface remains unavailable.

## Deployment, health, and recovery

Factory is deployable and the change affects its production frontend bundle. After a human merge containing the exact checkpointed head:

1. Resolve the single primary Worktrunk checkout and require it to be `/Users/tom/repos/tomnagengast/factory`.
2. Require tracked and untracked state to be safe, fetch and prune `origin`, fast-forward `main`, and prove local `HEAD == origin/main`.
3. From that updated clean primary checkout only, run:

   ```text
   ~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
   ```

4. Verify exact identity with:

   ```text
   curl -fsS http://127.0.0.1:8092/api/healthz | jq .
   curl -fsS https://factory.nags.cloud/api/healthz | jq .
   jq . ~/.local/share/factory/deployments/current.json
   ```

5. Require commit, tree, build ID, deployment ID, and lifecycle contract to agree across local health, public health, the current receipt, and the active release.
6. If deployment verification fails, preserve the failed receipt and use the documented verified rollback path only when recovery is required:

   ```text
   bin/network-app rollback factory --to <previous-successful-deployment-id>
   curl -fsS http://127.0.0.1:8092/api/healthz | jq .
   curl -fsS https://factory.nags.cloud/api/healthz | jq .
   ```

7. Do not deploy from this issue worktree. Do not clean up the issue branch until deployment and health verification succeed.

## Contradictions and assumptions

- The issue title is intentionally broad, while the repository contains protected durability and lifecycle machinery. Applying the codebase-steward instruction means selecting one bounded, evidence-backed high-leverage slice instead of interpreting "simplify" as permission for a rewrite.
- The Go journal deletion candidate appears dead from the current reader graph but remains an explicit rollback capability. Repository documentation and invariants override a simple current-call-graph reading.
- Moving the Tasks vertical must not become a generic shared-layer project. Shared modules are acceptable only for concepts with multiple current consumers.

## Unresolved questions

None. Repository, runtime, history, tests, and independent assessments are sufficient to plan the typed Tasks vertical without a new product or security decision.
