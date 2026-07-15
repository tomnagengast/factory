# ENG-46 implementation plan: Source-neutral Factory tasks

> updated: 2026-07-15T18:49:02Z

Linear: https://linear.app/nags-cloud/issue/ENG-46/move-task-management-from-linear-to-bespoke-factory-implementation

Branch: `eng-46-move-task-management-from-linear-to-bespoke-factory-implementation`

Research: `plans/planning/eng-46-move-task-management-from-linear-to-bespoke-factory-implementation/research.md`

## Issue context

Factory currently treats a Linear issue identifier as the task identity throughout durable Runs, routed Invocations, repository resolution, principal prompts, completion, and the observer. ENG-46 adds a native Factory task provider without replacing or mirroring Linear. The two providers remain authoritative for their own records:

- `factory` owns native tasks, identified for humans as stable monotonic `FAC-N` values;
- `linear` owns current Linear issues and remains read-only in the Factory task workspace;
- both providers enter the same protected Invocation, Run, checkpoint, human-merge, exact-head deployment, completion, and cleanup lifecycle;
- native task creation selects an already succeeded admitted Linear Project ID, so existing project routing remains the only repository authority;
- native task, Run, and approval-gate states remain separate;
- all ENG-46 implementation work ships in this one pull request; Network repository workflow changes are explicitly deferred until after this pull request merges and deploys. The required post-deploy native canary is a separate Factory task with its own independently approved gates and verification PR, not a second ENG-46 implementation PR.

The research artifact was approved through the Linear research gate on 2026-07-15. This plan does not authorize implementation until it passes the required dual-provider adversarial review and the subsequent Linear plan gate.

## Acceptance criteria

1. Existing Linear webhooks, routing, comments, gates, Runs, completion, observer routes, and persisted data continue to work through a `linear` task adapter without duplication or changed ownership.
2. A canonical `TaskRef` containing source, provider ID, and display identifier owns Runs, Invocations, checkpoints, helper context, and completion. In v1 the Linear adapter deterministically sets both provider ID and display identifier to the normalized canonical Linear issue identifier because that is the only identity present in retained records; it retains a known Linear UUID as non-owning external metadata. Factory provider IDs remain opaque native IDs. Source plus normalized provider ID, not unqualified display text, is the uniqueness key.
3. Existing Run and Invocation persistence migrates legacy issue identifiers deterministically to Linear TaskRefs, rejects conflicting dual identities, and preserves branch, tmux, checkpoint, receipt, and observer references.
4. Factory stores native tasks, immutable messages/replies, links, gates/decisions, revisions, idempotency outcomes, routing snapshots, and completion evidence durably. Recovery permits only torn final-record truncation and never duplicates an applied operation.
5. Native task workflow state is exactly `open`, `in_progress`, `completed`, or `canceled`. Run and gate states are displayed separately.
6. Native creation accepts only a succeeded admitted Linear Project ID. Arbitrary repository paths, clone URLs, deployment commands, and routing overrides cannot enter task requests.
7. Native task mutations use the protected coordinated event wire with body-free global metadata. Titles, descriptions, messages, capabilities, local paths, and secrets do not enter public health, global events, logs, receipts, or unauthenticated responses.
8. A Run-scoped provider-neutral helper can read context, post or reply, add links, change task state, open/wait on a gate, and observe new activity for its exact task. It cannot select or mutate another task.
9. Native gates explicitly record `gated` or `automatic` decisions. Human native messages can wake or continue a Run; agent and system messages cannot. Linear keeps its existing contextual comment/reaction/Yolo semantics behind the adapter.
10. Authenticated, no-store task APIs provide a bounded managed list, safe admitted-project choices, native create/edit/message/gate/start operations, and read-only Linear detail. Browser mutations are same-origin, strictly decoded, bounded, attributable, optimistic, and idempotent.
11. `/tasks` provides a source-neutral list and detail workspace. Native tasks can be created, edited, discussed, gated, and started there; Linear tasks are visibly read-only with **Open in Linear**.
12. The managed Linear index includes only tasks already known through retained Runs or admitted Invocations, not the entire Linear workspace backlog.
13. At least 1,000 tasks and 10,000 messages reopen and page without a product cap near 250. Focused performance tests enforce a one-second development-machine budget for reopen and first-page queries.
14. Native creation and start remain dark until migration, provider-neutral helper parity, the revised protected Full SDLC workflow, and read-only coexistence are proven. After deployment, ENG-46 remains incomplete until an explicitly authorized Factory-project rollout is enabled and one separately scoped native Factory task proves create, edit, comment, reply, both independently human-approved gates, start, human continuation, its own checkpointed PR and human merge, deployment, cleanup, and provider-neutral completion. Before the first incompatible source-neutral Run, Invocation, or native-task record is persisted, Factory records the monotonic rollback-compatibility boundary.
15. Factory's human-only merge, exact verified-head ancestry, clean updated-main deployment, repository isolation, completion evidence, remote branch cleanup, child-window consumption, and Worktrunk safeguards remain unchanged.

## Evidence-backed current behavior and root cause

- `internal/agentrun/store.go` stores `IssueIdentifier` on triggers, Runs, and claims and rewrites a schema-1 snapshot. `ValidIssueIdentifier` admits only Linear-shaped identifiers.
- `internal/triggerrouter/model.go`, `admission.go`, and `manager.go` persist, resolve, and serialize Invocations by the same issue string.
- `internal/agentrun/launcher.go`, `execute.go`, `repository.go`, `completion_system.go`, and `completion.go` independently assume Linear for launch context, repository lookup, and terminal evidence.
- `internal/eventwire/journal.go` and `internal/triggerrouter/store.go` already define the required JSONL append, fsync, poisoned-write, strict replay, torn-tail recovery, deterministic ordering, and checkpoint compaction patterns.
- `internal/projectsetup` and `RepositoryCatalog` already enforce immutable, allowlisted Linear Project routing. The live setup store proves static catalog entries are not automatically admitted project choices.
- `internal/server/server.go` already supplies authenticated Go 1.22 routes, readiness guards, same-origin browser mutation protection, strict bounded JSON decoding, and `409` conflict conventions.
- `internal/viewerauth/auth.go` authenticates page/API access but exposes no stable actor to mutation handlers, and its page allowlist does not include `/tasks`.
- The Solid frontend is intentionally small and hand-typed, with no browser-test harness. Interactive acceptance therefore requires focused Go/frontend checks plus manual authenticated desktop and narrow-width inspection.
- `internal/agentrun/completion.go`, ready checkpoints, GitHub event reconciliation, deployment receipts, and cleanup validators are already the code-owned trust boundary. Task controls must call into that lifecycle rather than copy it.

The root cause is not merely missing task CRUD. Linear identity and API access cross several otherwise independent protected subsystems. Adding native records beside those assumptions would create split ownership, bypass routing, or weaken completion. The implementation must first make the existing lifecycle source-neutral, then add native operations through the same durable admission and validation path.

## Decisions and alternatives

### Canonical identity

Add a shared task model with `TaskRef { Source, ProviderID, Identifier }`. Canonical equality and ownership use normalized source plus provider ID. For `linear` v1, every legacy record, webhook, GraphQL result, helper request, and managed-index entry sets `ProviderID` to the same normalized canonical issue identifier (`TEAM-N`); the Linear UUID is retained separately when available and a conflicting UUID-to-identifier observation fails closed. For `factory`, `ProviderID` is the opaque native internal ID and `Identifier` is `FAC-N`. The display identifier remains only for human-facing labels and compatibility URLs. New internal tmux sessions, checkout/workspace directories, and default branch identities are namespaced by source plus Run ID or canonical provider identity; retained legacy Linear Runs continue to locate their existing identifier-only resources without renaming them.

Rejected alternatives:

- Using `FAC-N` or `TEAM-N` alone would permit cross-provider collisions and cannot represent a renamed provider display key safely.
- Retaining `IssueIdentifier` as the authoritative key and adding an optional provider would leave ownership and migration ambiguous.

### Provider boundary

Introduce a task service over `factory` and `linear` provider implementations. The service owns provider-neutral summaries, context, messages, links, gates, task completion, and project resolution. Linear GraphQL details remain in the Linear adapter; the native provider owns its journal and projection.

Rejected alternatives:

- Mirroring Linear into native persistence violates the one-authority contract and creates synchronization conflict.
- Letting each caller switch on provider would reproduce current coupling in a second form.

### Native persistence and wire coordination

Use a private append-only native-task operation journal with a checkpointed in-memory projection, stable operation/idempotency IDs, expected task revisions, strict validation on mutation and replay, and repository-standard crash behavior. Browser/helper writes stage private command data, publish bounded body-free normalized metadata, let a protected dispatcher apply the native journal exactly once, and acknowledge the wire only after projection succeeds.

Rejected alternatives:

- Extending the Run snapshot would mix task history with process state and rewrite growing conversations on every mutation.
- Direct concurrent journal access from helpers would violate the single-owner durability model.
- Publishing bodies on the global wire would expose private task content and expand retention cost.

### Routing and workflow binding

`GET /api/task-projects` exposes only succeeded admitted project IDs and privacy-safe labels. Native create stores the selected project ID; native start resolves it through current project setup/catalog authority, snapshots the exact route on the admitted Run, and fails closed if admission no longer succeeds. Native start binds the enabled protected Full SDLC workflow at admission. The current v1 contract does not expose per-task workflow selection, so the UI and API do not invent it.

Rejected alternatives:

- Accepting a path or repository URL from the client bypasses project admission and repository allowlisting.
- Storing an editable workflow selection on the task expands the current issue contract and risks changing behavior between gates.

### Helper authorization

Expose provider-neutral `factory agent task ...` commands through a service-owned Unix-socket or loopback RPC capability. The launcher writes the exact TaskRef and a scoped capability into the private Run context, passes only the endpoint/capability into the session, and redacts it from observer/event output. The server resolves task identity from the active Run, never from a caller-supplied arbitrary task ID.

Rejected alternatives:

- Continuing to expose `LINEAR_API_KEY` to every principal preserves unnecessary workspace-wide authority after helper parity exists.
- Filesystem access by the helper risks concurrent writers and grants cross-task access.

### Authenticated actor

Extend viewer authentication with a server-derived actor accessor. Google sessions use the verified normalized email and subject-equivalent stable identity. Authorized unmanaged loopback access uses an explicit `local-operator` actor. Request bodies never supply audit identity.

### Rollout and rollback boundary

Introduce a runtime control with native create/start dark by default. Source-neutral migration and read-only coexistence can ship before native activation within the same binary and PR. Before the first schema-v2 Run snapshot, Invocation checkpoint/journal record, or native-task record is written, persist and fsync a monotonic source-neutral compatibility marker. The marker write is part of the same fail-closed precondition as the incompatible mutation and must succeed first. Startup and deployment rollback preflight refuse an older task-unaware binary after that marker. Recovery after the boundary uses a TaskRef-aware release or forward fix even if no native task has been activated.

Rejected alternative: treating the feature flag as sufficient rollback safety would allow a prior binary to reinterpret native ownership or persistence.

## Non-goals

- Network repository changes or publishing the Network provider workflow; those begin only after this Factory PR is merged and deployed.
- Synchronizing, importing, editing, or closing Linear issues from `/tasks`.
- Copying Linear descriptions or comments into native persistence.
- Replacing admitted Linear Project metadata as the v1 repository-routing authority.
- Task message editing/deletion, task archival/export UI, automated remote backups, arbitrary Markdown/HTML rendering, or cross-provider task conversion.
- Per-task workflow choice, multiple concurrent Runs for one exact TaskRef, auto-merge, merge buttons, deployment buttons, safeguard overrides, or a synthetic/no-change completion exception for the rollout canary.
- A new frontend testing framework solely for this change; manual browser evidence is required unless an existing merged harness becomes available.
- Broad redesign of `/agents`, settings, trigger rules, or global event retention.

## Impacted files and interfaces

Exact new package names may be adjusted to match Go package boundaries during implementation, but responsibility must not drift.

| Area | Existing paths and symbols | Planned change |
| --- | --- | --- |
| Shared identity | new `internal/taskmodel`; `internal/agentrun/store.go`; `internal/triggerrouter/model.go` | Define/validate/canonicalize TaskRef and the Linear identifier ownership algorithm; migrate Run/Invocation ownership; retain read compatibility aliases |
| Admission/serialization | `internal/triggerrouter/admission.go`, `manager.go`, `store.go`; `internal/triggerregistry`; `internal/agentrun/manager.go` | Construct provider refs at boundaries; default persisted target policy providers before digesting; group by canonical TaskRef; preserve pinned workflow and one active Run |
| Launch/context | `internal/agentrun/launcher.go`, `execute.go`, observer/view models | Persist exact task context/capability; namespace new sessions/workspaces by source and Run identity; make prompts/events source-neutral; retain legacy resource lookup and display routes |
| Repository routing | `internal/agentrun/repository.go`; `internal/projectsetup`; `main.go` | Add project-ID resolution from succeeded admitted setup; expose safe choices; pin exact route |
| Completion/checkpoints | `internal/agentrun/completion.go`, `completion_system.go`, ready/deploy/receipt validators | Rename provider evidence to TaskComplete; call provider adapter; bind TaskRef without weakening GitHub/deploy evidence |
| Native domain | new `internal/taskstore` and `internal/taskservice` packages | Journal, projection, identifiers, revisions, idempotency, messages, links, gates, state, compaction, performance |
| Coordinated events | `internal/eventwire`; dispatch wiring in `main.go` | Add protected body-free task actions, staging/apply/ack ordering, replay/idempotency |
| Providers/helpers | new provider/RPC code; `agent_commands.go`; launcher environment | Implement Factory/Linear adapters and `factory agent task show/messages/comment/reply/link/state/gate`; remove principal Linear key only after parity |
| Workflow | compiled/published `full-sdlc` definition and authoring/publication tests | Publish provider-neutral revision while preserving code-owned safeguards and dual-provider plan review |
| Authentication/API | `internal/viewerauth/auth.go`; `internal/server/server.go` and tests | Actor accessor, `/tasks` page routes, exact task APIs, auth/no-store/same-origin/bounds/conflicts |
| Frontend | `frontend/src/index.tsx`, `frontend/src/styles.css` | Tasks navigation, list/detail/create/edit/messages/gates/start, read-only Linear views, responsive/a11y states |
| Rollout/docs | startup wiring, compatibility/deployment preflight, README/operator docs | Dark control, marker, backup/restore, recovery and canary procedure |

## Vertical implementation phases

Each phase ends in a focused check and a logical commit. If a phase disproves an identity, durability, routing, or trust-boundary premise, implementation returns to research and reviewed planning rather than patching around it.

### Phase 1: Source-neutral Linear lifecycle

1. Add TaskRef normalization, validation, stable ownership-key helpers, and source-specific display validation.
2. Version and migrate Run snapshots. A legacy `IssueIdentifier` deterministically becomes a Linear TaskRef whose normalized `ProviderID` and `Identifier` are both that canonical `TEAM-N` value. New Linear ingress uses the same algorithm and stores any observed Linear UUID only as non-owning metadata. Keep legacy JSON readable; reject missing, malformed, conflicting dual representations, or conflicting UUID observations.
3. Version and migrate Invocation checkpoints/journal records before ownership grouping. Default legacy trigger targets to `linear` before digesting, deduplication, or manager serialization.
4. Update claims, manager ownership, launcher context, event subjects, observer models, repository/completion interfaces, ready checkpoints, and receipts to carry TaskRef where task ownership matters. New sessions and checkout/workspace identities use source plus Run ID or canonical identity so equal display text cannot collide. Existing retained Linear Runs detect and reuse their identifier-only tmux/worktree resources; no live resource is renamed. Retain compatibility JSON/URLs that external clients currently use.
5. Put current Linear GraphQL repository and completion behavior behind a Linear provider adapter without changing webhook, label, comment, gate, routing, completion, or retry semantics.
6. Focused verification: identity unit tests; legacy/live/terminal migration fixtures; conflicting-dual-field rejection; one-owner collision tests across and within providers; all existing agentrun/triggerrouter/server tests.

Exit: every current Linear lifecycle test passes through the source-neutral seam, old persisted Runs/Invocations reopen, and no native write surface is reachable.

### Phase 2: Native task durability and protected operations

1. Implement FAC-N sequence allocation, strict domain validation, journal append/fsync/replay/poisoning, checkpoint compaction, and exact projection for tasks, messages/replies, links, gate records/decisions, revisions, command outcomes, routing snapshots, and completion.
2. Make idempotency identity include actor/task/operation semantics. Identical retries return the original result; a reused key with a different canonical command fails. Expected-revision writes return authoritative conflict state without mutation.
3. Add private staging and body-free task event metadata. Register protected dispatcher operations for create, update, message/reply, link, gate open/decision, state, start, and continuation. Apply the staged operation before wire acknowledgment; replay checks applied operation IDs.
4. Add startup recovery and coordinated failure tests for crash points before publish, after publish, after native append, and before wire acknowledgment. Verify bodies/capabilities never enter global records or sanitized responses.
5. Add 1,000-task/10,000-message reopen and first-page focused benchmarks/tests with a one-second development-machine threshold, stable ordering, bounded pagination, and no 250-record pruning.

Exit: native domain operations are durable and replay-safe behind internal APIs, but external create/start remain dark.

### Phase 3: Admitted routing, native admission, and helpers

1. Expose privacy-safe choices derived only from succeeded `projectsetup` entries and reconcile desired static Factory project metadata before runtime enablement. Add `ResolveProjectID`-style authority that rechecks the catalog and immutable setup at start.
2. Generalize Invocation admission to Factory TaskRefs. Native start verifies task/project/runtime control, resolves and pins the exact admitted route plus protected Full SDLC workflow, and coalesces/resumes under the same one-TaskRef manager rules.
3. Implement the Run-scoped helper endpoint/capability and CLI commands. The endpoint derives the exact TaskRef from validated active Run context and refuses mismatched, terminal, unknown, or cross-repository use.
4. Implement native human wake/continuation semantics and explicit `gated`/`automatic` gate operations. Keep agent/system messages non-waking. Implement equivalent Linear operations behind the same CLI while preserving existing marker/signature rules.
5. Publish a provider-neutral Full SDLC workflow under the new reserved ID `full-sdlc-provider-neutral` and exact compiled digest for task intake, conversation, gates, feedback, links, and completion. Add an idempotent management reconciliation operation that uses the existing coordinator's expected policy/workflow revisions to insert or confirm that exact definition, never overwrites a customized existing workflow, and fails closed if the reserved ID has a different digest. Native admission requires that exact enabled ID/digest. Fresh installs may seed it, but startup defaults alone are not rollout authority. Preserve human merge, exact-head, deployment-source, cleanup, completion, and dual-provider adversarial-review enforcement in code and workflow tests.
6. Only after helper parity tests pass, stop injecting `LINEAR_API_KEY` into new Runs pinned to the exact provider-neutral workflow digest. Retained running, parked, remediation, and post-merge Linear Runs pinned to an older workflow revision/digest, plus legacy Runs without a pin, keep the current key injection until they terminate. The Factory service retains the key for the Linear adapter. Compatibility tests cover all retained nonterminal segment types and prove the key is absent only for the new provider-neutral pin.

Exit: one protected lifecycle can operate through either provider-neutral helper context, but native browser create/start are still controlled off.

### Phase 4: Authenticated APIs and managed index

Register these authenticated, no-store interfaces using existing readiness/same-origin conventions:

```text
GET    /api/tasks
GET    /api/task-projects
POST   /api/tasks
GET    /api/tasks/{provider}/{id}
PATCH  /api/tasks/{provider}/{id}
POST   /api/tasks/{provider}/{id}/messages
POST   /api/tasks/{provider}/{id}/gates
POST   /api/tasks/{provider}/{id}/gates/{gateID}/decision
POST   /api/tasks/{provider}/{id}/start
GET    /tasks
GET    /tasks/{provider}/{id}
```

1. Add bounded cursor pagination and provider/project/task-state/approval/activity filters. Build Linear summaries only from retained Run and admitted Invocation TaskRefs, loading live Linear detail on demand.
2. Derive mutation actors from authenticated context. Add a verified Google actor accessor and stable `local-operator`; ignore no client-supplied author fields because unknown fields are rejected.
3. Enforce strict JSON sizes/types, URL and parent validation, native-only ownership for writes, idempotency keys for append/create/start semantics, expected revision for mutations, and `409` authoritative snapshots.
4. Keep native create/start disabled unless rollout preconditions are satisfied. Linear mutations return an ownership/method error plus canonical external URL.
5. Verify every task response is authenticated/no-store and `/api/home`, `/api/healthz`, events, errors, and logs remain body-free.

Exit: APIs satisfy the contract under focused auth, CSRF, validation, conflict, privacy, provider-unavailable, and outage tests.

### Phase 5: `/tasks` workspace

1. Add authenticated page allowlisting, SPA routing, and Tasks navigation while retaining `/agents` as execution telemetry.
2. Build a bounded list with provider/project/state/approval/activity filters, provider badges, Run/gate status, updated time, loading/empty/error/offline states, and stable pagination.
3. Build native create/detail flows for title, description, admitted project, approval mode, edits, links, immutable discussion/replies, explicit gate approve/revision actions, start/continue, Run/PR/checkpoint links, completion evidence, and server-authoritative conflict refresh.
4. Build read-only Linear detail with provider state, latest Factory Run summary, and prominent **Open in Linear**. Do not render provider HTML or inject raw Markdown.
5. Preserve keyboard reachability, semantic labels/errors, visible focus, focus restoration after mutations, desktop/720/320 layouts, and existing light/dark behavior.

Exit: manual authenticated use can create, discuss, gate, start, and follow a native task and inspect a managed Linear task without crossing ownership boundaries.

### Phase 6: Completion, compatibility, docs, and dark rollout

1. Make terminal completion require provider-neutral `TaskComplete` plus all existing repository/PR/check/review/comment/thread/verified-head/deployment/cleanup evidence. Native completion verifies task state and no later unanswered human feedback; Linear preserves current authoritative state/feedback checks.
2. Backfill only the managed Linear task index from retained Runs and Invocations. Do not persist Linear bodies.
3. Write and fsync the monotonic source-neutral compatibility marker before the first schema-v2 Run snapshot, Invocation journal/checkpoint record, or native-task record, including when ordinary Linear activity is the first writer. Extend startup/deployment rollback preflight to refuse task-unaware rollback after that point.
4. Document provider authority, storage permissions, quiesced native journal/staging/metadata backup and atomic restore, corruption handling, project-admission dependency, helper operations, feature control, recovery, and the Network follow-up boundary.
5. Keep native create/start dark in the committed default. ENG-46 plan-gate approval explicitly authorizes only post-deploy Factory-project activation and creation of a separately scoped canary task, not the canary's research/plan decisions or broad enablement. After the ENG-46 merge is deployed, reconcile the exact protected workflow and Factory project route, enable native create/start only for that admitted Factory project, and create one real native Factory task whose bounded plan includes a harmless reviewable repository change. That canary must independently cross human research and plan gates, then prove create, edit, comment, reply, start, later human continuation, ready checkpoint, its own human-merged exact-head PR, clean-main deployment, cleanup, and provider-neutral task completion. It receives no no-change or synthetic completion exception. Its PR is verification work owned by the new native task and is distinct from ENG-46's single implementation PR. ENG-46 must stay open until the canary task is terminal, has no unanswered human feedback, and its evidence is attached. Do not run the canary from the ENG-46 issue worktree.

Exit: all provider lifecycles retain current mechanical completion strength, rollback behavior is explicit, and operators can safely activate/recover the feature after deployment.

## Migration, compatibility, and recovery

### Run and Invocation migration

- Bump persistence versions explicitly. Decode legacy schemas into an intermediate representation, canonicalize legacy issue identifiers to Linear TaskRefs, validate, then project current records.
- Preserve stable Run IDs, Invocation IDs, start times, branch names, tmux session names, repository snapshots, workflow pins, checkpoints, receipts, and existing observer paths for retained Runs. Generate all new internal session/workspace identities from source plus Run/canonical identity, never the display identifier alone.
- During compatibility, emit legacy issue identifier fields only as aliases derived from the TaskRef. On read, conflicting legacy/current identities are corruption and fail closed.
- Migrate Invocation target provider defaults before hashing/digesting or grouping. Add fixtures for live, parked, retrying, terminal, checkpointed, and malformed records.
- Do not eagerly rewrite live stores merely by inspecting them. Persist the new form on the next normal coordinated checkpoint or one explicit idempotent migration path covered by crash tests.

### Native journal recovery

- Accept and truncate only an incomplete final JSONL record. Any malformed complete record, invalid transition, sequence gap, revision mismatch, duplicate identifier, or conflicting operation result poisons startup.
- Compaction writes a complete fsynced checkpoint atomically before removing superseded mutation records. Immutable messages, links, gates, decisions, and operation outcomes remain represented exactly.
- Backup/restore requires the Factory service to be quiesced and copies the journal, checkpoint, staged private operations, sequence state, and compatibility marker as one unit with original permissions.

### Runtime rollback

- Before the first incompatible source-neutral persistence write, rollback to the immediately previous compatible release remains allowed if existing deployment preflight and data-schema checks pass.
- The marker is written before any schema-v2 Run/Invocation checkpoint or native-task record, so ordinary Linear activity can cross this boundary before native activation. After the marker, prior binaries that cannot understand TaskRef ownership are invalid recovery targets. Restore a TaskRef-aware deployment or forward-fix; never delete or edit the marker to force rollback.
- A Linear outage leaves stored native detail readable. It may make live Linear detail, Linear completion/communication, new project reconciliation, or unresolved routing unavailable with explicit retryable errors.

## Post-merge deployment and recovery procedure

The principal never merges. The human must use **Create a merge commit** so the checkpointed verified head is an ancestor of the reported merge.

After GitHub reports the merge:

1. Reconstruct PR, base, branch, and verified head from the ready checkpoint and fresh GitHub/Linear state.
2. Run `git merge-base --is-ancestor <verified-head> <reported-merge-commit>`; stop with `verified_head_mismatch` if it fails.
3. Recheck all required/reported checks, review decision, issue comments, inline comments, unresolved threads, and later Linear feedback.
4. Resolve exactly one managed main checkout through Worktrunk. Require origin `tomnagengast/factory`, clean tracked state, no divergence, and no issue-worktree deployment.
5. `git fetch --prune origin`, fast-forward tracked `main`, and require local `HEAD == origin/main == reported merge commit` before deployment.
6. Capture current deployment ID/commit and compatibility status for recovery evidence.
7. From clean updated managed main only, run:

   ```text
   ~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
   ```

8. Verify local and public `/api/healthz`, deployed commit identity, current receipt, task-store/wire/router health, and `nags doctor factory --json`. Verify authenticated `/tasks` read-only coexistence without exposing native bodies.
9. If deployment fails before any incompatible source-neutral persistence write and compatibility preflight permits, restore the recorded prior deployment. If the source-neutral compatibility marker exists, use a TaskRef-aware recovery release or forward fix, never a prior incompatible binary, regardless of whether native tasks were enabled.
10. Treat approved ENG-46 plan-gate authorization as permission for the narrow Factory-project rollout and canary creation only. From deployed Factory, idempotently reconcile `full-sdlc-provider-neutral` to the reviewed digest and reconcile the Factory Linear Project setup to `succeeded`; refuse activation on any digest, route, or catalog mismatch.
11. Enable native create/start only for the admitted Factory project. Create a separately scoped native canary in `/tasks` with a bounded, harmless, reviewable Factory repository change. Record create/edit/comment/reply evidence, then let that task independently obtain human research and plan approvals. After start, add a later human comment and prove it resumes/continues the exact Run. The canary must produce its own ready checkpoint, human-merged exact-head PR, clean-main deployment and recovery evidence, branch/worktree cleanup, and terminal provider-neutral task completion. No ENG-46 approval substitutes for either canary gate, and no no-change completion shortcut is valid. Do not mark ENG-46 complete while the canary is active, blocked, awaiting feedback/merge, or missing any completion evidence.
12. Re-run local/public health, receipt, task-store/wire/router, helper-scope, and body-privacy probes after the canary. Keep rollout scoped to Factory; broader project enablement is deferred.
13. Verify GitHub auto-deleted the ENG-46 remote issue branch, fetch/prune, consume all child outputs, and remove the clean integrated ENG-46 checkout with Worktrunk without force.
14. Fresh-read GitHub and Linear, require the canary terminal with no later unanswered feedback, move ENG-46 to its unambiguous completed state, and publish merge, deployment, canary, activation-scope, and cleanup evidence.

## Verification matrix

| Risk / acceptance | Focused evidence |
| --- | --- |
| TaskRef correctness | canonicalization, deterministic legacy/new Linear identifier ownership, UUID metadata conflict rejection, invalid source/provider/display values, cross-provider same-text separation, stable equality and serialization tests |
| Run migration | legacy live/parked/retrying/terminal fixtures, stable IDs/paths/checkpoints, conflicting dual identity rejection, crash-safe migration |
| Invocation migration | legacy provider default before digest/grouping, current journal/checkpoint replay, duplicate ownership and one-active-Run tests |
| Linear parity | existing webhook/label/comment/reaction/Yolo, repository, helper, continuation, completion, server, and end-to-end suites unchanged through adapter |
| Native durability | create/update/state/link/message/reply/gate/decision replay, fsync failure poison, torn tail, corrupt complete record, compaction, restart equivalence |
| Optimistic/idempotent writes | stale revision `409`, identical retry result, conflicting key reuse, concurrent gate/message/start, no mutation on rejected command |
| Wire coordination | every crash point, append-before-ack, replay no-op, staged-command cleanup, body-free metadata, dispatcher error health |
| Routing safety | only succeeded admitted project choices, missing/failed/duplicate/mismatched/stale setup, arbitrary path/URL rejection, pinned route snapshot |
| One-task lifecycle | native start/continue coalescing, active/terminal transitions, cross-provider isolation including equal display identifiers, namespaced tmux/workspace/branch identities, legacy resource reuse, workflow pin retention, no generic-rule cycle |
| Helper authorization | exact Run/TaskRef scope, unknown/mismatched/terminal/cross-repo denial, capability redaction, long-poll cursor, agent message non-wake |
| Gates | gated approve/revise, automatic durable approval, stale/concurrent decisions, artifact links, human wake, no emoji inference for native |
| Principal secret removal | full Linear helper parity before key removal, retained running/parked/remediation/post-merge/legacy-pin key compatibility, new-pin launcher environment/observer/event assertions, service-only key access |
| Actor/auth | allowed/denied Google identities, verified actor attribution, local loopback actor, no client actor injection, unauthenticated redirects/401 |
| API security | same-origin, no-store, method/provider ownership, JSON size/unknown fields, identifiers/URLs/parents, pagination/filter bounds, readiness |
| Managed Linear index | retained Run/Invocation inclusion, workspace backlog exclusion, live detail outage and retry behavior, no body persistence |
| UI behavior | authenticated desktop/720/320 list/detail/create/edit/message/gate/start, loading/empty/error/conflict/offline/success, keyboard/focus, console/network |
| Privacy | search global journal, logs, health, home, receipts, checkpoints, observer payloads, and error responses for seeded secret bodies/capabilities |
| Workload | 1,000 tasks/10,000 messages, reopen and first page under one second, stable pagination, no cap/prune near 250 |
| Completion safeguards | both providers require TaskComplete plus unchanged PR/check/review/thread/verified-head/deploy/branch/worktree/child evidence |
| Rollout/rollback | dark committed default, ENG-46 plan-gate authorization only for scoped activation/canary creation, idempotent reserved-workflow reconciliation and exact-digest admission, Factory-project route reconciliation, separate native canary task with independent human gates plus its own checkpointed PR/human merge/deployment/cleanup/completion before ENG-46 closure, marker-before-first-v2-write including Linear-only activity, incompatible rollback refusal, quiesced backup/restore rehearsal |
| Regression | all package tests, race detector, vet, frozen frontend install/typecheck/build, clean diff |

Final publication commands from the issue worktree:

```text
go test ./...
go test -race ./...
go vet ./...
MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build
git diff --check
```

For manual browser verification, first confirm whether a development server already exists. Reuse it if safe; otherwise start one recorded temporary process, exercise the authenticated task flows and responsive states, inspect browser console/network failures, and stop the process before finishing.

## Unresolved questions

None. The current Linear contract, repository evidence, and approved research determine the implementation boundaries. Any evidence that the current Full SDLC workflow cannot be published provider-neutrally in this PR, or that live persistence cannot be migrated without ambiguous ownership, invalidates a plan premise and requires returning to research rather than an implementation workaround.
