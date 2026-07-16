# ENG-47 Simplification Research, Revision 2

## Research questions

1. What did the latest human feedback change about acceptable scope, risk, compatibility, and sequencing?
2. Which Factory mechanisms are current product capabilities or mechanical safeguards, and which are transitional implementations that can be deleted?
3. Where does one logical concept currently have multiple durable owners, models, stores, or execution loops?
4. What first-principles architecture materially reduces packages, durable artifacts, state machines, exported concepts, and repeated frontend machinery?
5. How can current durable Factory state, including this active Run, cross the cut without being erased or silently reinterpreted?
6. Which security, routing, human-merge, exact-head, deployment-source, and completion invariants must remain mechanically unchanged?
7. What observable acceptance criteria prove that every current feature capability survived?
8. What exact verification, deployment, health, recovery, and post-cutover checks apply?

## Issue, routing, and revised human direction

- Linear issue: `ENG-47`, "Simplify simplify simplify."
- Project routing remains authoritative: `tomnagengast/factory` at `/Users/tom/repos/tomnagengast/factory`. The managed repository and normalized `origin` match that metadata.
- The isolated branch is `eng-47-simplify-simplify-simplify`, based on `origin/main` at `e5034d6208fbc7cfaa41fc24aa4793f2c8870c4b`.
- The branch already contains the approved revision-1 research, plan, dual review evidence, a typed Tasks frontend module, shared frontend helpers, and a candidate browser fixture through `70c3544391e0190b1d8b74de278ba73650ac1208`.
- The issue remains `In Review` with `Factory` and `Yolo`; pull request 18 is open and currently points at that head.
- Tom's later contextual reply `dd8ab575-ddf6-4a60-87a3-932f49c1b3a6` rejects the conservative scope. Factory is unreleased greenfield software in a first-principles refactor phase. Risk tolerance is explicitly very high, and maximum-ambition consolidation and legacy cleanup are preferred over a benign patch.
- That reply invalidates the old plan's central non-goal, which excluded production Go, persistence, lifecycle, rollback, and repository-wide frontend changes. The completed Tasks extraction remains useful evidence and implementation, but it is no longer the endpoint.
- A fresh label snapshot is still required at each gate. `Yolo` authorizes progression only after the normal revised artifact and same-thread gate evidence are published.

## Evidence-gathering method

- The complete issue conversation, project routing, PR attachment, labels, and reply ancestry were fetched through the Factory Linear GraphQL helper.
- Durable memory and repository history were searched for ENG-47, event-wire, workflow, task-source, and prior compatibility decisions.
- `nr` was attempted for graph-backed orientation. The repository index did not return within bounded probes, so exact `rg`, package imports, history, full-source reads, tests, and live runtime state were used instead. No index was silently built.
- The complete root and worktree instructions, README, current research and approved plan, composition root, relevant event, trigger, settings, workflow, task, run, server, deployment, and frontend sources were read.
- Three read-only Factory tmux children independently assessed frontend ownership, backend architecture, and lifecycle/compatibility. Their durable outputs are under:
  - `$FACTORY_RUN_DIR/children/steward-frontend-r2-c9a4ef18/`
  - `$FACTORY_RUN_DIR/children/steward-backend-r2-5c388cb0/`
  - `$FACTORY_RUN_DIR/children/steward-lifecycle-r2-e162a57b/`
- The existing service on `127.0.0.1:8092` was inspected rather than starting another server.

## Runtime and repository baseline

- Factory is one Go 1.26.5 service/CLI plus a SolidJS 1.9 and TypeScript 5.9 frontend built by Bun 1.3.11 and Vite 7.
- Production Go is approximately 22,277 lines across 20 packages; Go tests are approximately 13,170 lines. `agentrun`, root composition, `server`, `taskstore`, and `triggerrouter` dominate.
- The root imports every internal package. `serveConfigured` is a 500-plus-line composition root that opens the stores, constructs policy and repository adapters, recovers state, and starts five post-readiness loops plus HTTP.
- The current service and public endpoint both report healthy commit `e5034d6208fbc7cfaa41fc24aa4793f2c8870c4b`. The wire is current, has no pending records, and has one historical rejection.
- Live data contains 59 retained Runs: 12 succeeded, 28 failed, 18 blocked, and one running Run for ENG-47. Pin shapes are 15 legacy, six Markdown, and 38 without a workflow pin.
- The active ENG-47 Run is pinned to the old provider-specific `full-sdlc` Markdown. Its helper path is the immutable currently deployed release, but a service restart or retry after deployment could use the new release. Existing-pin execution therefore cannot be removed in this change.
- Live settings are schema 2 revision 7 with `workflowRollbackIncompatible=true`, the old `full-sdlc`, and `full-sdlc-provider-neutral`. The protected feedback binding and old trigger fields still point at `full-sdlc`.
- `workflow-drafts.json` is empty, no persisted `triggers.json` exists, and the task-source-neutral marker exists. The schema-1 backup and old provider journal files also exist.
- `system-events.jsonl` is the authoritative event ledger. `github-events.json` and `linear-comments.json` retain lifetime totals but zero records; no production helper reads them.
- Baseline `go test ./...`, `go test -race ./...`, `go vet ./...`, frozen Bun install, frontend typecheck, and frontend build pass on the existing branch.

## Current capabilities and protected invariants

The following are capabilities or non-waivable safeguards. Simplification must give each one a clearer owner, never remove it.

| Invariant or capability | Current owner and proof obligation |
| --- | --- |
| Signed, bounded, replay-protected Linear and GitHub ingress | `internal/server`, provider normalizers, private payload staging |
| Body-free ordered event replay, acknowledgement, rejection, retention, and recovery | `internal/eventwire`; `system-events.jsonl` remains authoritative |
| Generic rules, schedules, hop/cycle/rate limits, and policy/admission serialization | `triggerregistry`, `triggerrouter`, `triggerscheduler`, coordinated wire |
| Protected human-feedback continuation and active-run coalescing | server dispatch plus Run store continuation claim |
| Native and managed Linear tasks, gates, messages, links, evidence, start, cancellation, and read-only Linear presentation | task store/service/server and Tasks UI |
| Private Linear and task bodies | payload and pending-operation storage; never the global wire or logs |
| Allowlisted repository routing before any runnable ownership | repository catalog/resolvers, project setup, run admission |
| Human-only merge authority | Run parking and completion validation; no principal merge or auto-merge |
| Exact checkpointed head and merge ancestry | ready checkpoint plus `git merge-base --is-ancestor` completion proof |
| Clean updated primary-main deployment | system completion evidence and `nags deploy --expected-commit` |
| Review/check non-regression, task completion, child completion, branch/worktree cleanup | mechanical completion validators |
| Viewer authentication, same-origin writes, idempotency, optimistic revisions, permissions, path and symlink protections | auth, HTTP, task, and durable store boundaries |
| Full-page frontend navigation and server-owned route authentication | Go route map plus one eager Vite entry |

Linear itself is current capability. The provider-neutral workflow is a successor execution model, not authorization to remove Linear ingress, task presentation, project metadata, or comments.

## Root cause: parallel authorities and appended feature machinery

Factory accumulated compatibility layers during rapid delivery of unified events, generic triggers, workflow Markdown, provider-neutral tasks, and native task management. The protected gates remain coherent, but one logical operation often crosses multiple durable authorities:

1. A webhook enters `system-events.jsonl`, then is copied into a provider-specific journal that no current reader needs.
2. Generic admission persists a `Decision` and `Invocation`; a claim manager later duplicates task, workflow, repository, causation, and policy identity into a separate `Run` state machine.
3. Native task mutation writes a private staged command, emits a body-free wire event, then applies and records the command in a second journal.
4. Settings, workflows, trigger rules, schedules, protected bindings, and project activation are one policy but are split across several packages and files.
5. Repository onboarding, routing, launch configuration, and completion evidence translate among several overlapping repository models and resolver interfaces.
6. Runtime goroutines are constructed in the root and stopped indirectly through context cancellation instead of one owned supervisor.
7. The frontend composition root still owns most features, four write transports, three copies of the same optimistic-save lifecycle, four polling loops, and repeated page shells.

The root cause is not lack of generic abstraction. It is multiple owners for the same lifecycle plus feature implementations appended to global composition files.

## First-principles target architecture

### 1. One event authority

- Keep `system-events.jsonl` and the event-wire ordering, acknowledgement, rejection, payload-privacy, channel-cursor, and crash-recovery contract.
- Delete the two provider-specific JSON journal implementations, startup seeds, server interfaces/configuration, and post-wire mirror writes.
- Keep provider normalization and wire adapters. `agent github-events`, `agent linear-comments`, and the generic event/task activity helpers continue reading the unified wire while an existing pin can still require them.
- Existing provider journal files remain untouched on disk as archival evidence but are no longer opened or updated.
- This intentionally retires rollback to a binary that requires gap-free projection journals after the cutover. Recovery after the cut must use a compatible retained release and the authoritative wire or a forward corrective commit.

### 2. One admission and Run lifecycle

- Fold durable routing decisions, runnable invocations, repository claim, and Runs into one journal and state machine.
- Preserve durable suppressed/rejected admission outcomes without creating runnable owners.
- A runnable record progresses through admitted, routing, pending, starting, running, awaiting-human-merge, post-merge, and terminal states.
- Preserve current rule identity, workflow pin and digest, policy revision, task identity, causation, hop/cycle/rate evidence, repository route, serialization key, retry, checkpoint, remediation, and completion evidence once rather than reflecting them between stores.
- Route registry events, native starts, protected feedback continuations, and GitHub remediation through one admission API. Native task events need no synthetic routing sequence.
- Delete the separate claim manager, invocation-to-Run reflection receipts, duplicated identifiers, and `GenericTriggers=false` direct label path.

### 3. One private task transaction log

- Move pending private task commands into the native task journal as an outbox record.
- Append the private pending operation durably, publish only its opaque body-free reference to the wire, apply it idempotently during dispatch, append the result, and mark the operation applied.
- Preserve API idempotency, exact revision conflict semantics, private bodies, crash replay, and result recovery.
- Delete the separate `task-operations/` filesystem protocol and the current double execution call.

### 4. One authoritative policy

- Consolidate published workflow definitions, protected bindings, trigger rules, schedules, agent settings, runtime limits, and per-project native-task activation into one revisioned policy owner.
- Keep workflow drafts separate and nonauthoritative so an unavailable draft store cannot disable execution.
- Keep scheduler cursors as runtime state, not published policy.
- Converge future admissions on one compiled provider-neutral Full SDLC definition. Preserve existing immutable workflow bodies on retained Runs and retain the narrow legacy-pin executor while any retained nonterminal pin may need it.
- Remove the compiled default generic Linear-comment rule. Protected feedback continuation remains mandatory; operators may still create an ordinary visible generic comment rule intentionally.
- Preserve the current schema-2 envelope for the immediate cutover so the previous task-aware release can still parse policy state. Retire schema-1 migration, schema-1 backup creation/preflight, and the workflow rollback latches only after the compatible-release floor is explicit.

### 5. One repository model and catalog

- Replace setup specs, existing-repository models, run repository configs, resolver variants, completion-reader maps, and registrar adapters with one repository record and one catalog.
- The catalog contains compiled and admitted projects, resolves a `TaskRef`, supplies launch configuration, and constructs completion evidence from the same record.
- Preserve exact normalized-origin matching, allowlisted local paths, managed-root containment, default branch, deployment metadata, bootstrap rules, and project-provider coordination.
- Remove the optional legacy resolver and fail closed when an explicit catalog is unavailable.

### 6. One supervised application runtime

- Move runtime composition to `internal/app` and CLI parsing/agent commands to `internal/cli`; leave root `main` as dispatch.
- Recover all durable state before readiness.
- Own named HTTP, run/admission, repository-onboarding, and clock components under one supervisor. Propagate component failure, cancel siblings, and join every goroutine on shutdown.
- Consolidate schedules and service heartbeat on one clock owner.
- Add one narrow durable atomic-replacement primitive for identical temp, permission, sync, rename, and directory-sync behavior. Append-journal poisoning, rollback, and compaction remain domain-specific.

### 7. One feature owner and one invariant owner in the frontend

- Keep server-owned authentication, exact full-page route dispatch, plain anchors, one eager Vite entry, Solid primitives, and the global CSS cascade.
- Reduce `frontend/src/index.tsx` from 2,584 lines to a small composition root that imports pages and selects the exact route.
- Give home/activity, wire, agents/run observer, workflows, triggers, settings, and Tasks cohesive modules.
- Retain the completed provider-typed Tasks extraction.
- Consolidate the common request transport into one `sendJSON` core with thin typed wrappers. Preserve tasks-only idempotency and each endpoint's exact conflict payload and throw/return behavior.
- Consolidate the repeated settings/triggers/protected-binding save lifecycle into one optimistic-editor primitive. Keep workflow draft autosave bespoke because its queue, debounce, and unload guard are materially different.
- Consolidate polling, page shells, resource gates, fields, toggles, charts, and pagination only where current consumers share the same semantics.
- Remove confirmed orphan CSS selectors after route screenshots; do not reorder the cascade or add a client router, query/state library, lazy route chunks, or new dependency.

## Expected reduction

The exact result is a plan-time budget rather than a reason to delete safeguards. The target is:

- 20 Go packages reduced toward 10 to 12 cohesive packages;
- 22,277 production Go lines reduced by at least 15 percent, with a stretch target near 20 percent;
- one event ledger instead of three;
- one admission/Run lifecycle instead of two;
- one task transaction log instead of a stage directory plus journal;
- one published policy owner instead of settings/workflow/registry/schedule/control authorities;
- one repository model and catalog instead of repeated translations and resolver adapters;
- five post-readiness loops reduced to three supervised owners;
- approximately half of named data-root authorities removed or absorbed;
- `index.tsx` reduced to an explicit route composition root of roughly 70 to 120 lines;
- four frontend write transports reduced to one core plus semantic wrappers;
- three copied optimistic save machines reduced to one primitive plus the intentionally bespoke workflow editor;
- four polling loops reduced to one helper.

Budgets are guardrails. A smaller reduction is acceptable only when evidence shows the allegedly duplicate mechanism owns a different invariant.

## State migration and active-Run handling

No production data wipe is inferred from the greenfield comment. The cut uses a fail-closed one-shot migration and preserves current state.

1. Before deployment, require a quiescent service boundary and capture a permission-preserving backup of the complete Factory data root.
2. Validate the current settings, routing journal, task journal, repository/setup state, Run ledger, workflow pins, drafts, event wire, and cursor files before mutation.
3. Migrate into sibling temporary artifacts, validate them completely, fsync them, and atomically replace only after the full converted set is consistent. Mixed old/new authorities must be rejected.
4. Preserve the active ENG-47 Run's immutable old workflow pin, repository identity, checkpoint state, attempts, children, and session. The new executor must still understand that pin if the service restarts before this Run reaches terminal.
5. Preserve historical terminal Runs for UI/history. Compatibility readers may normalize their old identity and pin shapes in memory, but new writes use only canonical shapes.
6. Consolidate the known live compiled workflows and default policy without overwriting an unknown customized definition. A non-recognized conflict stops migration and requires explicit operator resolution.
7. The authoritative event wire is not rewritten. Removed projection files and compatibility markers are archived in place and ignored.
8. After successful migration and health verification, all new admissions use only the canonical authorities. No runtime dual writes remain.

Source rollback alone is insufficient after the one-way state cut. Recovery requires stopping the service, restoring the complete pre-cut data backup, activating a release that declares support for that state, and re-verifying both health endpoints. After the cut has accepted new work, prefer a reviewed forward correction unless a complete compatible snapshot is restored.

## Observable acceptance criteria

1. Every current route and API remains available with the same authentication, authorization, payload privacy, revision, idempotency, conflict, and error behavior.
2. Linear labels, explicit generic rules, schedules, native starts, protected comments, native messages/gate decisions, and GitHub remediation each create or resume exactly one appropriate owner, including duplicate delivery and restart cases.
3. A default human Linear comment no longer creates both a protected continuation and an independent generic invocation.
4. Repository resolution is complete and allowlisted before any runnable state can launch.
5. The Run record is the only runnable lifecycle authority; no Invocation-to-Run reflection protocol or second claim loop remains.
6. Private task and Linear bodies never appear in the global wire, logs, error responses, or public/authenticated summaries.
7. The unified wire is the only event journal opened by the service; provider-specific journal code and configuration are gone, while helper cursor behavior remains stable.
8. Native task mutation crash recovery works at every pending, publish, apply, result, and acknowledgement boundary without a separate stage directory.
9. Published policy mutations cannot overtake undecided admissions, and workflow drafts remain nonauthoritative.
10. Current durable state migrates without losing active or retained Runs, tasks, gates, links, messages, workflows, repository routes, event cursors, or completion evidence.
11. Human merge, exact verified-head ancestry, non-regressed checks/reviews, clean-main deployment, receipt/health identity, task/child completion, and cleanup rejection tests remain mechanically enforced.
12. Runtime component failure cancels and joins sibling components; shutdown leaves no Factory-owned temporary process or goroutine.
13. The frontend entry owns only imports and exact route composition. Feature modules own their contracts and pages, and invariant searches find one shared transport, one normal optimistic-save machine, and one polling helper.
14. Frontend output remains one normal JS entry and one CSS asset, with no new package dependency or route/load behavior.
15. Full Go, race, vet, frozen frontend, browser, migration, crash, security, deployment, and post-deploy checks pass.

## Verification matrix

| Concern | Required evidence |
| --- | --- |
| Canonical state migration | Fresh-state and current-state fixtures; unknown customization rejection; complete backup/restore rehearsal; no mixed artifacts after injected failure |
| Event durability | Duplicate publication, torn tail, append/sync/ack failure, permanent rejection, restart catch-up, retention, monotonic channel cursors |
| Unified admission and Run | Rule match/suppress, multi-match, hop/cycle/rate limits, same-task serialization, repository resolution failure, crash at each transition, retry, feedback coalescing, native start, GitHub remediation |
| Task outbox | Duplicate idempotency key, stale revision, private-body audit, crash before/after pending append, event publish, apply, result, acknowledgement |
| Policy | Publish/delete/binding/rule/schedule/project changes; optimistic conflicts; mutation blocked while admission pending; draft-store failure does not block execution |
| Repository catalog | Origin/path/managed-root/default-branch validation, compiled/admitted projects, bootstrap, provider coordination, completion reader identity |
| Mechanical lifecycle | Tampered checkpoint, changed head, unmerged close, squash/rebase ancestry failure, review/check regression, dirty/divergent main, receipt/health mismatch, incomplete task/children, branch/worktree residue |
| Security | Webhook HMAC and replay window, OAuth/local viewer auth, same-origin, strict bounded JSON, file modes, symlink/path traversal, environment allowlist and redaction |
| Runtime supervision | Recovery before readiness, component error propagation, cancellation, bounded join, no leaked temporary processes |
| Frontend types and ownership | Frozen install, typecheck, build; exact searches for raw `fetch`, `setInterval`, copied save states, broad task union, and index-owned feature contracts |
| Browser parity | Authenticated desktop/mobile pass across every route; keyboard/focus; loading, empty, error, conflict, offline/recovery, success; console/network inspection |
| Required publication suites | `go test ./...`; `go test -race ./...`; `go vet ./...`; `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile`; frontend typecheck; frontend build |

## Alternatives considered

### Keep the revision-1 typed Tasks slice as the endpoint

Rejected by the latest human direction and by repository evidence. It fixes one invalid union but leaves the parallel backend authorities and most frontend duplication intact.

### Delete current Linear capability

Rejected. Linear remains an active task provider, webhook source, routing metadata source, discussion surface, and protected feedback provider. Provider-neutral execution removes workflow coupling, not the provider capability.

### Wipe the live data root

Rejected without explicit destructive authorization. Greenfield risk tolerance permits a hard architectural cut, but it does not silently authorize erasing retained tasks, Runs, evidence, or this active lifecycle. Use a one-shot fail-closed migration and complete backup.

### Retain dual journals and dual state writers for rollback

Rejected. Runtime dual writing is the accidental complexity being removed. Preserve one complete pre-cut backup and a compatible release instead.

### Create a universal generic store abstraction first

Rejected. Atomic replacement is genuinely shared and can have one narrow primitive, but append journals, compaction, poisoning, outboxes, optimistic revisions, and retention have different semantics. Consolidate domain authorities before abstracting storage.

### Add a client router, state/query library, generated client, or lazy routes

Rejected. Full-page navigation, server-owned authentication, Solid primitives, and one eager entry are simpler and are current behavior.

### Reorder or partition the global stylesheet while moving features

Rejected as a default. Cascade order is behavior. Remove proven dead selectors under screenshot coverage, but do not mix stylesheet architecture with TypeScript ownership unless evidence requires it.

## Deployment, health, and recovery

After a human merge containing the exact checkpointed head:

1. Reconstruct and revalidate GitHub, Linear, the ready checkpoint, and the exact verified head.
2. Resolve the single primary Worktrunk checkout at `/Users/tom/repos/tomnagengast/factory`; require a safe clean checkout, fetch/prune, fast-forward `main`, and prove `HEAD == origin/main`.
3. Capture and validate the complete Factory data backup required by the approved migration phase.
4. From that updated clean primary checkout only, run:

   ```text
   ~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
   ```

5. Verify the migration receipt, new durable-authority inventory, wire continuity, active Run identity, task/policy/repository content, and runtime component status.
6. Verify exact deployment identity with:

   ```text
   curl -fsS http://127.0.0.1:8092/api/healthz | jq .
   curl -fsS https://factory.nags.cloud/api/healthz | jq .
   jq . ~/.local/share/factory/deployments/current.json
   ```

7. Require commit, tree, build ID, deployment ID, and contract identity to agree across loopback, public health, receipt, and active release.
8. Exercise one read-only route per authority and a bounded duplicate/restart-safe operation where the approved plan permits it.
9. If migration or deployment fails before new canonical writes, stop the service, preserve failed evidence, restore the complete pre-cut data backup, activate the last compatible verified release, and repeat health checks. After new canonical writes, use a reviewed forward correction unless restoring a complete compatible snapshot.
10. Never deploy from the issue worktree or the T9 mirror. Do not remove the issue checkout or branch until deployment and health verification succeed.

## Unresolved questions

None. The latest human comment supplies the risk and architectural direction. The non-destructive migration choice preserves live evidence without requiring an ungranted data wipe, and repository/runtime evidence is sufficient to plan the cut.
