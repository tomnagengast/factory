# ENG-46 Research: Source-neutral Factory tasks

Linear: https://linear.app/nags-cloud/issue/ENG-46/move-task-management-from-linear-to-bespoke-factory-implementation

Branch: `eng-46-move-task-management-from-linear-to-bespoke-factory-implementation`

## Research questions

1. What is the current task, run, invocation, checkpoint, receipt, and completion identity, and where is Linear embedded in it?
2. Which durable storage and concurrency patterns can support native tasks, comments, gates, revisions, idempotency, crash recovery, and the required 1,000-task/10,000-message workload?
3. How can native and Linear tasks share one run lifecycle without weakening repository routing, one-task ownership, human merge, exact-head deployment, or completion validation?
4. What provider boundary is required for task context, communication, decisions, waits, and completion, including out-of-process agent helpers?
5. What authenticated HTTP and frontend seams implement `/tasks` while preserving current Linear behavior and privacy?
6. Which compatibility, security, corruption, migration, rollout, and rollback risks can change the design?
7. What observable acceptance criteria and exact verification prove the complete contract?
8. What exact post-merge deployment, health, identity, receipt, recovery, and cleanup procedure applies?

## Issue and decision evidence

- The issue's approved v1 contract requires two coexisting authoritative providers: `factory` for native tasks and `linear` for current Linear-backed tasks. Tasks are not mirrored or synchronized.
- Native identifiers are stable `FAC-N` values. Native workflow state is exactly `open`, `in_progress`, `completed`, or `canceled`; run and approval-gate state remain separate.
- Native task creation requires an admitted Linear Project ID. Linear project metadata remains the v1 repository-routing authority.
- `/tasks` is source-neutral. Native tasks are editable and communicative in Factory; Linear tasks are read-only with an **Open in Linear** action.
- A human reply on 2026-07-15 resolved the prior publication-unit blocker: all Factory work is authorized in one PR, while Network repository/provider-workflow changes remain a separate post-merge follow-up. No prior ENG-46 branch, PR, checkpoint, research gate, or implementation existed.
- Durable memory links the complete approved design at `/Users/tom/notes/agent/plans/planning/2026-07-14-factory-native-tasks.md`. Its provider, storage, gate, helper, migration, rollout, and verification decisions remain evidence. Its three-Factory-PR publication sequence is the only design section superseded by the later one-PR Linear comment. The current Linear issue's newer **Approved v1 product contract** is authoritative where its field list is narrower.
- ENG-40, ENG-41, ENG-42, ENG-48, and ENG-49 are present on fetched `origin/main`. The current runtime is healthy at `127.0.0.1:8092`, but it is deployed at `d87bf23d2d67...` while repository `main` is `92f36186f0c7...`; deployment must therefore use updated, clean, human-merged `main`, never the current issue worktree.
- The fetched baseline passes `go test ./...` before ENG-46 implementation, including the existing agentrun, routing, server, auth, and workflow suites.

## 1. Current identity and Linear coupling

### Observed facts

- `internal/agentrun/store.go` has no source-neutral identity. `IssueIdentifier string` is the owning key on `Trigger`, `Run`, and `InvocationClaim`; `ValidIssueIdentifier` accepts only canonical `TEAM-N` strings. Both legacy claims and invocation promotion enforce one active Run by comparing that string.
- `internal/triggerrouter/model.go` persists `Invocation.IssueIdentifier`; `internal/triggerrouter/admission.go` resolves fixed, subject, or attribute targets to an uppercased Linear-shaped identifier; `internal/triggerrouter/manager.go` serializes promotion by that identifier.
- `internal/agentrun/launcher.go` derives the tmux session from the issue identifier and launches `agent-exec --issue`. `internal/agentrun/execute.go` renders a Linear-specific principal prompt.
- `internal/agentrun/repository.go` exposes a useful `RepositoryResolver` interface, but its production implementation fetches `issue(id).project.description` from Linear and resolves exact `GitHub Repo` and `Local Path` values against `RepositoryCatalog`.
- `internal/agentrun/completion_system.go` directly queries Linear for `issue.state.type == completed`. `internal/agentrun/completion.go` names that terminal evidence `LinearComplete` and makes it mandatory.
- `/agents/{issue}/{started}/run`, `AgentView`, and frontend run types all expose the Linear issue string. `internal/viewerauth/auth.go` separately hard-codes a Linear-like route regex.
- `ReadyCheckpoint` and deployment receipts are bound to a Run/repository/PR/head but do not currently record a provider-aware task identity. Completion derives the task indirectly from `Run.IssueIdentifier`.

### Design consequence

Introduce one canonical `TaskRef { source, providerID, identifier }` and make its source/provider identity the ownership key for Runs, invocations, repository resolution, checkpoints, completion evidence, helper context, agent views, and task-aware events. The display identifier remains available for branches, sessions, URLs, and humans. Canonical ownership keys must include the source so text shared across providers cannot collide. Existing `IssueIdentifier` JSON must remain readable and migrate to a Linear TaskRef without losing live or retained Runs.

Linear webhook adapters can continue parsing Linear identifiers, but they must create a Linear `TaskRef` at their boundary. Native start/continue paths create Factory `TaskRef` values directly. Existing Linear routes and JSON fields should remain as compatibility aliases or redirects where necessary while source-neutral routes become authoritative.

## 2. Durable native task persistence

### Observed facts

- `internal/eventwire/journal.go` and `internal/triggerrouter/store.go` already establish the repository's append-only JSONL discipline: a versioned checkpoint, append plus `fsync`, rollback/truncate on short or failed writes, torn-tail recovery only for an incomplete final line, strict replay, a poisoned latch after irrecoverable write failure, deterministic ordering, and atomic checkpoint compaction.
- `internal/settings`, `internal/triggerregistry`, and workflow draft stores establish optimistic revision conflicts. Event IDs, webhook delivery IDs, and deterministic invocation IDs establish current idempotency conventions.
- The Run store is different: `internal/agentrun/store.go` rewrites one schema-1 JSON snapshot on every mutation and rejects unknown versions. It currently holds live retained data and therefore needs an explicit compatible migration rather than a field rename.
- Private Factory data lives under `~/.local/share/factory/data`, is created under `0700` directories, and is persisted with `0600` files.
- The live store currently retains 47 Runs in an approximately 80 KiB snapshot. The normalized wire is approximately 8.9 MiB at its 10,000-record retention contract, and trigger routing is approximately 2.0 MiB. These measurements show that a compacted projection for 1,000 tasks and 10,000 bounded messages is a realistic local workload, but replay and query latency must be measured rather than assumed.

### Design consequence

The native task store should use a private append-only operation journal and exact in-memory projection, following trigger-router crash semantics rather than inventing a database. Operations need stable command/idempotency keys and per-task expected revisions. The projection must retain current task, links, messages/replies, gates/decisions, completion metadata, and routing snapshot, while obsolete mutation operations compact to a checkpoint so memory and replay cost grow with retained domain data rather than retry/update count.

Native browser mutations follow the already approved coordinated-wire protocol: strictly validate and stage the private mutation by operation ID, publish one body-free normalized task event, let a protected dispatcher append/project the operation exactly once, and acknowledge the wire record only after projection succeeds. Replay after a crash checks the durable operation ID and becomes a no-op. This preserves ordered health semantics and avoids a second ingestion queue.

Agent helpers do not open the journal concurrently. They use a service-owned loopback RPC or capability-checked local endpoint scoped to the active Run and exact TaskRef. Task bodies remain only in the private staged sidecar/journal and authenticated task responses. Global `factory / task / <action>` events contain only bounded provider/ref/project/state/gate metadata.

## 3. Repository routing and shared lifecycle

### Observed facts

- `RepositoryCatalog.ResolveProject` already rejects missing, duplicate, mismatched, or non-allowlisted project metadata. `projectsetup.Parser` and store make admitted repository/local-path identity immutable.
- Static repositories such as Factory are allowlisted in `main.go`, but their Linear Project IDs are not necessarily present in `project-setups.json`. The approved rollout requires re-sending or reconciling desired Linear Project metadata so every project offered for native creation has a succeeded admitted setup entry.
- `LinearRepositoryResolver` can resolve a Linear issue to its project metadata, but there is no `ResolveProjectID` operation. The current live setup store contains only dynamic projects, proving the distinction matters.
- ENG-40's invocation lifecycle already provides deterministic invocation/Run IDs, durable claim intent, one-owner serialization, pinned workflows, terminal reflection, and additive protected routes.
- The launcher can already persist and execute a pinned workflow for invocation Runs. Legacy Runs fall back to a live workflow selected by trigger kind.

### Design consequence

Expose only succeeded admitted Project setups as safe task choices: project ID, name, repository display value, and optional public Cloud URL, never local paths or deployment commands. Native creation selects one such Project ID. Every start resolves it through the current setup/catalog, pins the exact route on the Run, and fails closed if it is no longer admitted. Desired static projects missing from the setup store must be reconciled before native creation is enabled; arbitrary repository paths never enter a client request.

Generalize existing claims and invocation promotion to `TaskRef`, preserving one active Run per exact provider/ref. Native `start` selects and pins the configured protected Full SDLC workflow, creates or resumes a Run through the same manager, and returns the durable Run identity. It does not merge, deploy, or bypass checkpoints. Linear label, feedback, GitHub, and post-merge routes continue to create Linear task refs and preserve current behavior.

Trigger target policy also needs an optional provider, defaulting legacy schema-1 values to `linear`. This keeps existing rules readable while allowing future fixed Factory task targets. Provider omission must canonicalize during read and before digesting; it must not split invocation identity or one-task ownership.

## 4. Provider-neutral task operations and agent helpers

### Provider contract

The source-neutral service needs provider operations for:

- list summaries and load detail/context;
- native create/update, comments/replies, links, gates/decisions, start/continue, and completion;
- read-only Linear summaries/detail plus canonical Linear URL;
- post progress/message, request a decision gate, wait for later task activity, and mark completion from an authenticated Factory Run context;
- query whether a task is completed for mechanical terminal validation.

Factory-provider writes use the native operation journal. Linear-provider reads/writes continue through Linear GraphQL with the inherited API key. Every helper invocation derives its `TaskRef`, Run ID, state root, and repository from validated Factory environment variables, not user-supplied filesystem paths. Native helpers must mark agent-authored messages distinctly so they do not wake or impersonate human feedback.

The approved helper command family is `factory agent task show|messages|comment|reply|link|state|gate`. It can coexist with the existing low-level Linear helpers during migration, but new published workflow revisions use only the provider-neutral contract. Its normalized responses do not invent semantic approval for Linear: the Linear adapter maps the current contextual comment/reply/reaction/Yolo protocol, while native gate decisions are explicit structured records.

Native approval modes are `gated` and `automatic`. A native gate is always durably recorded; automatic mode records an `automatically_approved` decision, while gated mode waits for explicit approve or revision-requested input. Gate state never changes task workflow state by itself. Human native messages wake or continue Runs; agent/system messages never do.

## 5. HTTP, auth, and frontend integration

### Observed facts

- `internal/server/server.go` uses Go 1.22 method routes, `ViewerAuthenticator.API` for authenticated APIs, `ViewerAuthenticator.Page` for protected pages, `requireReady` for mutations, `sameOrigin` as the CSRF boundary, strict JSON decoding, bounded request bodies, `DisallowUnknownFields`, and `409` responses carrying authoritative revision state.
- ENG-46 explicitly specifies `PATCH`, so it should be introduced for task updates even though older resource APIs use `PUT`.
- `internal/viewerauth/auth.go` hard-codes safe post-login pages; `/tasks` and task detail routes must be added.
- Google auth currently validates a signed email but exposes only a boolean middleware boundary; native audit attribution requires an authenticated actor accessor. The unmanaged loopback authenticator has no person identity, so it needs a stable explicit `local-operator` actor tied to its authorized loopback authority rather than accepting actor data from requests.
- The Solid frontend is a single `index.tsx` plus `styles.css`, uses `createResource`, hand-written JSON types, full-page links, local draft signals, conflict states, and CSS media queries. There is no Vitest/Playwright/jsdom harness.
- `/agents` is intentionally run telemetry. Its list/detail should show source-neutral task refs and link back to `/tasks`, but the task domain belongs only in the task service/store.
- Linear comment bodies are not retained locally, so read-only Linear detail must come from live Linear GraphQL, not expired payload sidecars. The managed Linear task index includes only tasks with Factory Run history or an admitted Invocation, not the workspace backlog.

### API consequence

Implement the approved routes exactly, all authenticated and no-store:

- `GET /api/tasks` with bounded pagination and provider/project/state/approval/activity filters;
- `GET /api/task-projects` for privacy-safe succeeded admitted project choices;
- `POST /api/tasks` for native tasks only, requiring `Idempotency-Key` and admitted Linear Project ID;
- `GET /api/tasks/{provider}/{id}`;
- `PATCH /api/tasks/{provider}/{id}` for native expected-revision edits only;
- `POST /api/tasks/{provider}/{id}/messages` for native human comments/replies;
- `POST /api/tasks/{provider}/{id}/gates` and `/gates/{gateID}/decision` for native gates;
- `POST /api/tasks/{provider}/{id}/start` for native start/continue.

Mutations require same-origin JSON, an idempotency key where creation/append semantics apply, and the expected task revision. Duplicate keys with identical commands return the prior result; key reuse with different content fails. Stale revisions return `409` with the authoritative detail. Linear provider mutations return a clear method/ownership error and an **Open in Linear** URL.

### UI consequence

Add `/tasks` and `/tasks/{provider}/{id}` to the authenticated page allowlist, server SPA routes, header navigation, and frontend path dispatcher. The workspace needs:

- a source-neutral list with provider/project/state/approval/activity filters and provider badges;
- native create and edit controls, links, discussion/replies, gate decisions, start/continue, Run links, and completion evidence;
- read-only Linear detail and **Open in Linear** action;
- server-authoritative refresh after every mutation and conflict handling, rather than a second frontend task model;
- accessible loading, empty, error, offline/conflict, success, focus, and keyboard behavior at desktop and narrow widths.

## 6. Security, compatibility, corruption, and rollback

- Human-only merge, exact verified-head ancestry, clean merged-main deployment, repository isolation, and mechanical completion remain unchanged. Task UI controls can start or communicate with Runs but cannot merge, enable auto-merge, deploy, delete branches, or override blockers.
- Native bodies must never enter public `/api/home`, `/api/healthz`, normalized global event attributes, logs, receipts, or unauthenticated responses.
- All persisted IDs, lengths, parent relationships, link URLs, state transitions, gate decisions, revisions, and idempotency records require strict validation during both mutation and replay. Malformed complete journal operations fail startup; only an incomplete final line may be truncated.
- Run-store migration must canonicalize every legacy `IssueIdentifier` as `linear/<id>`, retain a compatibility field while old clients exist, reject conflicting dual identities, and test live/nonterminal/terminal snapshots. Invocation-store migration must do the same before ownership grouping.
- Existing Linear target policies default to provider `linear`. The first durable Factory task or Factory-task Run is a monotonic rollback boundary: older binaries do not understand native task ownership. Extend the existing rollback preflight/compatibility evidence and documentation so recovery after activation uses a TaskRef-aware release or a forward fix, not silent prior-binary activation.
- A Linear outage must not make stored native task detail unreadable. It can block new project admission, Linear detail, Linear communication/completion, or a start that needs unresolved routing, and those failures must be explicit and retryable.
- Native creation and start remain dark behind a runtime rollout control until migration, provider-neutral helper behavior, the newly published workflow revision, and read-only coexistence have been verified. Existing pinned Runs keep their old workflow. After deployment, enable one Factory-repository canary through both gates before broad use.
- Native history is intentionally not pruned near 250 tasks and the current repository has no provider-owned backup API for new private state. V1 recovery therefore documents quiesced filesystem backup/restore of the journal, staging directory, and compatibility metadata as one unit; export/archive automation remains a later non-goal.

## 7. Observable acceptance and verification

| Acceptance or risk | Evidence to produce |
| --- | --- |
| Legacy Linear behavior survives | Existing Linear webhook, routing, continuation, helper, repository, completion, and server tests remain green; add migration and provider-parity tests |
| Native create/edit/link/state | Store and authenticated handler tests for valid, invalid, stale, duplicate, unauthorized, and non-admitted operations |
| Comments and replies | Store replay plus API/helper tests for parent validation, ordering, body privacy, and idempotent retry |
| Gates and decisions | Gated/automatic, approve/revision-requested, concurrent/stale decision, duplicate command, wait/wake, and separate task/run/gate state tests |
| Start and continuation | Provider-aware Run ownership, pinned workflow, repository resolution, duplicate start, active owner, feedback continuation, and Agent links |
| Completion | Provider-neutral completion reader tests for both Linear and Factory tasks plus unchanged merge/deploy/cleanup safeguards |
| Crash/corruption recovery | Torn-tail truncation, malformed complete operation rejection, short-write rollback, poisoned store, checkpoint/compaction reopen |
| Provider isolation | Same textual ID across providers, Linear mutation refusal, native data absent from Linear paths, and body-free global events |
| 1,000 tasks / 10,000 messages | Generated workload test measuring create/append, compaction, reopen/replay, filtered list, and first paged detail/list latency within the approved one-second focused-test budget; assert projection size is domain-bounded and no operation-history leak remains |
| Auth and CSRF | Unauthenticated API `401`, protected page redirect, cross-site `403`, media type/body/unknown-field bounds, no-store/nosniff headers |
| Responsive interactive UI | Authenticated desktop and narrow-screen browser pass covering keyboard/focus plus loading, empty, error, conflict/offline, success, Linear read-only, and native write flows; inspect console/network |
| Required Factory checks | `go test ./...`; `go test -race ./...`; `go vet ./...`; frozen Bun install, typecheck, and build |

No existing frontend browser-test harness is present, so browser acceptance is an explicit manual authenticated verification in addition to server/store automation and frontend typecheck/build.

## 8. Deployment, health, recovery, and cleanup evidence

After a human merge containing the exact checkpointed head:

1. Resolve the one managed main checkout through Worktrunk; require `/Users/tom/repos/tomnagengast/factory`, clean tracked state, `main`, and official `origin`.
2. `git fetch --prune origin`, fast-forward only, and prove local `HEAD == origin/main` and contains the verified PR head.
3. Re-run the approved required verification from clean merged main.
4. Capture `~/.local/share/factory/deployments/current.json` and its deployment ID for recovery.
5. From the primary checkout run `~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"`.
6. Verify:
   - `curl -fsS http://127.0.0.1:8092/api/healthz | jq .`
   - `curl -fsS https://factory.nags.cloud/api/healthz | jq .`
   - `jq . ~/.local/share/factory/deployments/current.json`
   - `~/.local/bin/nags doctor factory --json`
   - local/public commit, tree, build, deployment, contract, wire drain, project setups, and new task-store health agree.
7. Probe authenticated `/tasks`, create/read/mutate a disposable native task only if the approved rollout plan provides a safe admitted test project, and confirm Linear read-only rendering. Otherwise use a read-only health/content probe and the verified automated native flow to avoid inventing production data.
8. If deployment fails, preserve receipts and use `~/.local/bin/nags rollback factory --to <captured-deployment-id>` only when compatibility preflight permits it; otherwise forward-fix from reviewed merged main. Re-verify both health endpoints.
9. After success, verify GitHub auto-deleted the remote branch, fetch/prune, consume all child results, remove the clean integrated issue checkout with Worktrunk, and recheck health, receipt ancestry, GitHub, and Linear before completing the issue.

## Contradictions and resolved ambiguities

- The earlier issue text required separate publication units, but the later human comment explicitly supersedes it with one Factory PR. This research follows the later instruction and keeps Network repository work out of this PR.
- The repository usually uses `PUT`; the approved API explicitly requires `PATCH`, so task editing will use `PATCH` while retaining the repository's expected-revision/409 semantics.
- Static allowlisting and admitted Project-ID selection are distinct. Native creation exposes only succeeded setup entries; desired static projects missing there are reconciled before rollout, then every start revalidates the selected setup through the allowlisted catalog.
- The issue explicitly chooses live provider coexistence and no migration. Existing Linear issue history remains in Linear; only legacy Run/invocation identity storage is migrated to a Linear `TaskRef` for lifecycle compatibility.
- The earlier approved design included a per-task workflow field, while the current Linear issue's newer **Approved v1 product contract** omits it from the native field list. The current contract wins: v1 native starts use the protected published Full SDLC workflow binding and pin that revision at admission. This avoids silently reintroducing a removed field.

## Assumptions

- Factory tasks use an opaque internal provider ID plus the stable human-facing `FAC-N` identifier; Linear refs retain Linear's provider UUID plus canonical issue identifier. URLs use the display identifier, while ownership uses source plus provider ID.
- `gated` and `automatic` are the two v1 native approval modes from the approved design; the Linear adapter maps its Yolo label to the same automatic result without renaming native state.
- Linear list/detail comes from current Linear GraphQL and is bounded/paginated to managed tasks with Factory history; Factory does not copy Linear bodies into native storage.
- The existing protected Full SDLC workflow is the pinned default for native starts. Operator-configurable per-task workflow selection is not part of the current approved native field contract.

## Unresolved questions

None. The approved issue contract and later publication decision are sufficient to proceed to reviewed planning.
