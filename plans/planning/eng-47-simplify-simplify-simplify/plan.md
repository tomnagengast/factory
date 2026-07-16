# ENG-47 First-Principles Simplification Plan, Revision 2

> updated: 2026-07-16T18:12:22Z

## Issue context and outcome

ENG-47 originally approved and implemented a conservative provider-typed Tasks frontend extraction. Tom's later reply changes the governing scope: Factory is unreleased greenfield software at its explicit first-principles rewrite stage, and high-risk consolidation plus legacy deletion is preferred over a safe narrow patch.

This revision keeps the completed Tasks extraction but replaces the revision-1 endpoint. The implementation will make Factory's architecture express one owner for each durable concept:

1. one authoritative event ledger;
2. one admission and Run lifecycle;
3. one private task transaction log;
4. one published policy owner;
5. one repository model and catalog;
6. one supervised application runtime;
7. one frontend feature owner and one shared owner for each repeated invariant.

The implementation must preserve live Factory data through a fail-closed one-shot migration. Greenfield status does not authorize deleting the current active Run, retained history, tasks, workflow pins, routing evidence, or completion evidence.

## Acceptance criteria

1. Every current page, API, webhook, helper, task operation, workflow edit, trigger/schedule operation, project activation, repository setup, agent observation, and deployment/completion flow remains available with the same security and user-visible capability.
2. `system-events.jsonl` is the only event journal opened and written by Factory. GitHub and Linear normalization and helper cursor behavior remain, but the provider projection journals, seeds, mirrors, interfaces, and tests are deleted.
3. One durable Run authority owns admission outcomes, workflow/task/policy/repository causation, runnable state, retries, merge parking, GitHub remediation, and terminal completion. There is no separate Invocation lifecycle, reflection receipt, or claim manager.
4. Linear rules, schedules, native starts, protected feedback, native task feedback, and GitHub remediation enter that same Run owner without weakening hop/cycle/rate limits, same-task serialization, repository routing, or idempotency.
5. A normal human Linear comment performs protected continuation only. The compiled duplicate generic comment rule is gone; an explicitly configured visible generic comment rule remains supported and intentionally additive.
6. Native task commands and their pending/apply/result lifecycle live in the native task journal. Private bodies remain outside the global wire, and `task-operations/` plus its duplicate execution protocol are deleted.
7. Published workflows, protected bindings, trigger rules, schedules, agent/runtime settings, and project task activation have one durable policy owner and one internal admission generation while preserving their current independent public conflict revisions. Drafts remain separate and nonauthoritative.
8. New admissions use one compiled provider-neutral Full SDLC default. Existing retained Runs preserve their exact immutable old pins, and the old pin executor remains only for retained nonterminal compatibility.
9. One repository record and catalog supply onboarding, task routing, launch configuration, completion evidence, and UI choices. No legacy resolver can invent or bypass an allowlisted route.
10. All service-owned components recover before readiness and are started, canceled, error-propagated, and joined by one supervisor. In-process loops and ephemeral subprocesses leave no residue; durable Run-owned `factory-agents` tmux sessions intentionally survive service shutdown and are reconciled after restart.
11. Current state migrates into one canonical generation without losing or duplicating Runs, tasks, gates, messages, links, policy, repository routes, event cursors, or evidence. Unknown or ambiguous source state fails before activation.
12. The Run that implements ENG-47 remains resumable after deployment even though it is pinned to the old provider-specific workflow.
13. Human-only merge, exact verified-head ancestry, allowlisted routing, clean updated-main deployment, review/check non-regression, task/child completion, and branch/worktree cleanup remain mechanically enforced and covered by rejection tests.
14. `frontend/src/index.tsx` becomes a small exact-route composition root. Home/activity, wire, agents/observer, workflows, triggers, settings, and Tasks have cohesive owners.
15. Frontend reads and writes share one transport core with semantic wrappers; tasks-only idempotency and endpoint-specific conflict semantics remain exact. Settings/triggers/protected binding share one optimistic-save machine, workflows retain their distinct autosave queue, and polling has one owner.
16. Full Go tests, race detector, vet, frozen Bun install, typecheck, production build, migration/crash/security matrices, and authenticated desktop/mobile browser verification pass.
17. The result moves toward 10 to 12 Go packages, removes at least 15 percent of production Go lines unless preserved invariants prove a lower honest result, removes roughly half of the current durable authorities, and reduces the frontend entry to roughly 70 to 120 lines. Budgets never justify deleting a safeguard.

## Evidence and root cause

- Production Go is approximately 22,277 lines across 20 packages. The root imports every internal package, and the HTTP server imports 17.
- The data root contains 16 named durable artifacts plus sidecars and run directories.
- `system-events.jsonl` is authoritative. `github-events.json` and `linear-comments.json` contain zero retained records and are not read by current agent helpers.
- Generic admission persists `Decision` and `Invocation`, then a second manager copies the same identity, workflow, task, causation, policy, and repository data into `agentrun.Run`. Terminal state is reflected back with receipts.
- Native task mutation persists a private filesystem stage, publishes a body-free wire record, applies the command through dispatch, then calls the task store a second time to recover the result.
- Published policy is split across settings, workflows, registry, schedules, and task control despite coordinated mutation requirements.
- Repository identity is translated among setup specs, existing repositories, run configs, resolvers, registrar adapters, and completion readers.
- `serveConfigured` opens the stores and starts five post-readiness loops without a join-owning runtime supervisor.
- After the revision-1 Tasks extraction, `index.tsx` is still 2,584 lines with 35 feature types, four write transports, three copied optimistic-save machines, four polling loops, and repeated route shells.
- Live state has 59 retained Runs and one active ENG-47 Run. Settings are schema 2. The wire has no pending records, task staging is empty, workflow drafts are empty, and no registry file exists. These facts make a fail-closed one-shot migration possible, but the code must also reject non-empty or ambiguous variants safely.

The root cause is parallel ownership, not insufficient abstraction. Rapid feature delivery added transitional journals, state machines, models, stores, and UI implementations beside earlier authorities. The plan removes or folds those authorities before extracting shared mechanics.

## Architectural decisions

### Canonical state generation

Add one small state manifest that selects a complete canonical generation. On first start of the cutover release:

1. open and strictly validate every source artifact without mutating it;
2. create a permission-preserving complete backup receipt;
3. build canonical `policy`, `repositories`, `runs`, and `tasks` artifacts in a new private generation directory;
4. validate cross-artifact identities, counts, digests, ownership, cursors, and active states;
5. fsync files and directories;
6. recover the authoritative wire and open the staged canonical stores read-only, then serve exact deployment identity health while every advancing endpoint and manager remains gated and no manifest selects the generation;
7. after the deployment provider writes the exact successful receipt, acquire the state-transition lease, revalidate unchanged source hashes and the complete staged generation, atomically publish and fsync the manifest, fsync `canonicalWritesStarted`, and start advancing work.

The existing wire, private Linear payloads, run directories, and deployment receipts are not copied into new domain stores. Old source artifacts remain intact and become archival after activation. The service never dual-writes old and canonical stores.

The manifest records source artifact hashes, source and target schema versions, migration ID/time, initial canonical artifact hashes and counts as immutable audit evidence, the activation inventory of every nonterminal Run and live effect-capable agent session, and the release contract. Initial hashes are never treated as current hashes after activation. Mutable canonical stores validate themselves through strict schema, replay, operation, poison, and cross-reference rules on every open.

The generation also owns a monotonic, fsynced `canonicalWritesStarted` boundary. The staged generation is not selected before the exact successful deployment receipt. Under the state-transition lease, manifest publication is followed by the boundary write and directory sync before the first canonical domain mutation. Recovery and rollback preflight read the boundary rather than infer write state from mutable artifact hashes.

One data-root state-transition lease excludes canonical manifest publication and boundary advancement from rollback. `bin/network-app` acquires the lease before it quiesces Factory and retains the same lease token through preflight, optional restore, manifest deactivation, provider activation, health verification, and deployment-receipt finalization. The application must acquire that same exclusion before observing a receipt as authority, publishing the manifest, fsyncing `canonicalWritesStarted`, or starting advancing managers. If rollback owns it, the service remains health-identifiable but advancement-gated. The lease is fail closed, process- and token-bound, and cannot be bypassed by a stale file or a second wrapper.

### Unified Run model

Create a single append-only Run journal with operations for:

- admission batch and durable suppressed/rejected outcomes;
- runnable Run creation;
- repository route resolved/rejected;
- lifecycle transitions and attempts;
- delivery coalescing and feedback resume;
- GitHub cursor/remediation scheduling;
- ready checkpoint and post-merge resume;
- completion validation;
- rate-bucket increments and expiry;
- checkpoint compaction and retention.

A Run stores one immutable admission/causation ID, rule identity, workflow pin/digest, policy revision, task identity, root event, parent Run, hop/ancestor rules, delivery IDs, repository, lifecycle, checkpoint, and completion once. Migrated Invocation IDs become admission IDs. New IDs remain deterministic from event/rule identity. Derived wire events continue populating the retained `parentInvocationId` field from that immutable admission ID and also carry `parentRunId`, so retained wire decoding does not change even though admission has no separate lifecycle.

`AdmitBatch` runs synchronously inside the existing event/policy serialization boundary and appends one atomic batch operation for all outcomes and new Runs. Event-derived Runs cannot route or start until their source event sequence is globally dispatched. Repository resolution remains an asynchronous transition within the same Run manager because it can depend on provider state. The oldest admitted Run per task owns routing/start; later Runs remain admitted until ownership clears. Protected feedback and native feedback coalesce into the active Run when required.

Run transitions use a journal-owned outbox. One journal operation records the transition and its pending body-free wire event atomically. The collector publishes idempotently, records publication, and acknowledges only after the event wire reports the sequence dispatched. Terminal events are therefore never publishable before terminal Run state and the corresponding admission outcome are durable, replacing the old reflection receipt without weakening ordering.

### Task outbox

Extend the task journal with explicit `pending-unpublished`, `published`, `applied-result`, and `acknowledged` operation states keyed by the request's idempotency scope and command hash. Submission reuses an existing pending or result record for the same key. It returns the existing idempotent result only after the exact result is durable and synchronous wire publication/dispatch returns, but it reads that result from the same task projection rather than executing the domain command again.

The body-free wire event contains only the opaque operation ID, task identity, kind, producer, and provenance and has a deterministic ID known before publication. When dispatch receives the authoritative record, the task handler first appends its event ID and sequence as `published`, then validates metadata equality, applies once, and appends the result. If append succeeded but dispatch failed before that handler, recovery republishes the deterministic event idempotently, obtains the same record, and advances the same operation. The private command remains available while a later event handler can still fail. A named task-outbox reconciler marks it `acknowledged` only after the global wire dispatched cursor reaches its sequence. On startup and live transient failure, that owner republishes `pending-unpublished` operations or durably cancels one only when the wire proves its event absent and no publication attempt remains in flight.

### Coordinated policy

Create one immutable policy snapshot containing:

- published workflow definitions and protected feedback binding;
- trigger rules and schedules;
- principal/child model settings and runtime limits;
- enabled native-task projects;
- one internal global policy generation used for immutable admission pins, plus the existing independent settings, registry, task-control, workflow, rule, and schedule revision domains used by current APIs.

Policy mutation stays serialized against pending wire admission. One app-level coordinator owns the non-reentrant policy/admission lock while policy and Run admission remain separate packages. Existing request fields continue validating their current revision domains, so an unrelated settings or workflow write does not cause a new task-control or registry conflict. Every accepted mutation also advances the internal global policy generation captured by later admissions.

The migration recognizes the known compiled old `full-sdlc` and `full-sdlc-provider-neutral` definitions by exact canonical digest, publishes the provider-neutral body as the single default, repoints protected feedback and default label admission, and deletes only the exact compiled duplicate. Unknown customized definitions are preserved when non-conflicting or stop migration when they occupy a reserved identity. Custom visible rules are preserved; only the exact compiled default generic comment rule is removed.

### Repository catalog

Create one repository model containing project identity, repository/origin, local and managed paths, default branch, bootstrap/cloud/deployment metadata, setup state, provider-coordination evidence, and timestamps. One catalog API resolves a `TaskRef`, lists UI choices, supplies launch configuration, and creates completion readers.

Linear project metadata and compiled repositories remain input providers. Exact normalized-origin and path checks remain fail closed. Provider coordination remains an explicit onboarding state transition.

### Runtime and durable primitives

`internal/app` owns construction, recovery, readiness, and a supervisor. Named service components return errors, cancellation propagates, and shutdown joins in-process loops and ephemeral subprocesses. Durable `factory-agents` tmux sessions are Run-owned resources outside the supervisor process tree; they survive service restart and the Run manager reconciles them from canonical state. Schedules and heartbeat share one clock component; Run routing/execution share one manager; repository onboarding remains one manager.

`internal/durable` supplies only the identical private atomic replacement sequence: create temp in the destination directory, chmod `0600`, encode/write, fsync file, close, rename, open parent, fsync parent. Append journal truncation, poison latches, compaction, retention, and operation validation stay with their domains.

### Frontend ownership

Keep one eager bundle, plain anchors, server-owned auth, exact pathname dispatch, and the stylesheet cascade. Feature modules may import shared `http`, `activity`, `forms`, `editor`, `poll`, and `agent` modules; shared modules never import features.

One `sendJSON` core owns credentials, `no-store`, JSON encoding, empty responses, response text, and status. Thin feature wrappers retain tasks-only `Idempotency-Key`, typed conflict payloads, and caller-specific conflict delivery. One optimistic-editor primitive is used only where current state machines are equivalent. Workflow autosave remains bespoke.

## Non-goals

- Do not remove Linear provider capability, provider project routing, managed Linear task detail, or comments.
- Do not weaken, relocate into editable policy, or generalize away human merge, exact-head, completion, routing, deployment-source, authentication, privacy, or security validation.
- Do not wipe live Factory state or silently discard unknown records.
- Do not rewrite `system-events.jsonl`, payload bodies, deployment receipts, or run output directories.
- Do not retain runtime dual writes to make pre-cut binaries appear compatible.
- Do not add a client router, query/state framework, generated client, lazy route chunks, or runtime dependency.
- Do not force workflow autosave into the simpler optimistic-editor lifecycle.
- Do not create a universal persistence framework. Only identical atomic replacement is shared.
- Do not reorganize completion gate semantics merely to meet a package or line budget.

## Impacted files and interfaces

The implementation will prefer moves followed by deletion over parallel packages. Expected final ownership is:

| Target owner | Absorbs or replaces |
| --- | --- |
| `internal/events` | `internal/eventwire`, hook wire normalization, activity projection where derivable; deletes hook journals |
| `internal/policy` | `settings`, published `workflow`, `triggerregistry`, `triggerscheduler` policy, `taskcontrol`; drafts remain a small separate file/owner |
| `internal/repositories` | `projectsetup`, root setup adapters, `agentrun` repository config/resolvers/readers |
| `internal/tasks` | `taskmodel`, `taskstore`, `taskservice`; retains Linear provider adapter but replaces stage directory with journal outbox |
| `internal/runs` | `triggerrouter` decision/invocation/rates plus `agentrun` store/manager/launcher/observer/checkpoint/GitHub/completion |
| `internal/app` | `serveConfigured`, recovery/readiness, component lifecycle |
| `internal/cli` | root command dispatch and agent helper implementations |
| `internal/auth` | existing viewer authentication and capability authentication, with behavior unchanged |
| `internal/durable` | private atomic replacement only |
| `internal/server` | HTTP transport and provider webhook normalization; interfaces narrowed to canonical owners |

Package folding is complete only after callers import the canonical owner and the obsolete package is deleted. Compatibility decoders needed by the one-shot migration may live under an unexported migration package until post-cut validation, but no runtime writer may target old formats.

Frontend target files are `index.tsx`, `home.tsx`, `wire.tsx`, `agents.tsx`, `tasks.tsx`, `workflows.tsx`, `triggers.tsx`, `settings.tsx`, plus narrow `http.ts`, `activity.tsx`, `forms.tsx`, `editor.ts`, `poll.ts`, and `agent.ts` shared owners.

## Vertical implementation phases

Each phase ends in a logical commit and its focused verification. If evidence disproves a migration or ownership premise, stop and return to revised research and dual-provider planning rather than adding a bridge.

### Phase 0: characterize invariants and build the migration harness

- Add golden fixtures derived from sanitized current state shapes for settings/policy, registry defaults/custom rules, task control, repository setup, routing decisions/invocations/rates, Run pins/states/checkpoints, native tasks/outcomes, empty and pending outbox cases, workflow drafts, and event cursors.
- Add a cross-artifact audit that proves task identity, workflow digest, policy revision, repository route, invocation/Run linkage, active ownership, event sequence, and retained total counts.
- Define and test the source decoders, audit report, immutable manifest schema, backup receipt, source hashing, failure injection, and non-activating dry-run harness. Phase 0 does not create an authoritative canonical generation or alter startup selection because canonical readers and writers do not exist yet.
- Exercise unknown schema, unknown customized reserved workflow, duplicate active task, orphan invocation, missing Run, conflicting route, incomplete stage, pending wire, unsafe file mode/path/symlink, and altered source/audit rejection.
- Later owner phases add their canonical converter and validator to this harness. Atomic generation construction, activation, and startup selection are integrated only in Phase 4 after all canonical readers and writers exist.

Focused checks: source decoder/audit tests with injected read/hash/report failures; existing settings/routing/Run/task/project tests; `go test -race` for the new migration owner. Generation write/sync/rename/activation injection belongs to Phase 4.

### Phase 1: consolidate policy and repositories

- Implement the canonical policy snapshot/store and migrate settings, workflows, protected binding, registry, schedules, agent/runtime settings, and project activation.
- Preserve current API JSON and independent settings, registry, task-control, workflow, rule, and schedule conflict revisions through adapters backed by one policy snapshot, then simplify internal callers to the canonical owner.
- Publish one provider-neutral default and remove only the recognized compiled duplicate workflow/default generic comment rule.
- Keep existing immutable pins and the legacy pin executor; new policy never emits legacy pins.
- Implement the canonical repository model/catalog and migrate compiled plus admitted project/setup state.
- Replace setup, task, launch, and completion resolver conversions with catalog lookups.
- Replace `workflow-rollback-preflight` with `state-rollback-preflight` before deleting schema-specific rollback code. In the same commit, update `bin/network-app` and its tests so every rollback acquires the state-transition lease before quiescence and retains it across generation validation, proof-bounded optional restore, manifest deactivation, `nags rollback`, health, and receipt finalization. The application receipt transition uses the same exclusion, closing the gap between preflight and the provider's later deployment lock.
- Delete schema-1 settings migration/backup preflight, rollback latches, task-control store, default registry seeding from legacy settings, and the legacy repository fallback only after the generation-aware preflight and canonical fixtures pass.
- Keep the old compatibility marker files untouched on disk for archival/old-release refusal; the new runtime does not consult or update them.

Focused checks: policy pending-admission serialization, workflow publish/delete/binding conflicts, schedule CRUD/status, project activation, exact default migration, custom preservation/conflict, repository origin/path/routing/completion fixtures, current API contract tests.

### Phase 2: unify admission and Runs

- Introduce the canonical Run journal, admission outcomes, rates, state transitions, compaction, retention, and poison behavior.
- Migrate every old routing decision/invocation and every retained Run into the canonical model. Merge linked pairs; retain direct historical Runs; create a routed Run for a recoverable queued/claiming invocation; reject ambiguous links or duplicate active ownership.
- Move registry batch admission under the policy/wire lock to `runs.AdmitBatch`.
- Preserve each old Invocation ID as the Run's immutable admission/causation ID; generate the same identity deterministically for new admissions and keep derived wire `parentInvocationId` compatibility.
- Add the Run-transition outbox. Transition and pending event append atomically; publication is idempotent; acknowledgement waits for the global dispatched cursor; terminal event publication requires durable terminal state and admission outcome.
- Move repository resolution, oldest-per-task ownership, starting/running lifecycle, retries, feedback coalescing, merge parking, GitHub reconciliation, and terminal completion into one manager.
- Replace native start/continuation synthetic invocations with `runs.AdmitNative` and `runs.Continue` using deterministic event/idempotency identity but no synthetic event sequence.
- Preserve active ENG-47 session, segment, attempts, pin, delivery IDs, invocation causation, checkpoint, and completion fields through fixture and live-shape migration.
- Delete `triggerrouter.Manager`, invocation transition/reflection APIs, `EnsureInvocationRun`, duplicated `IssueIdentifier` writes, `GenericTriggers`, and the old routing store after the canonical manager passes restart/fault tests.

Focused checks: all old admission and Run manager/completion suites translated to one store; multi-match and suppression; rate/hop/cycle/global limits; routing transient/permanent failure; crash at every transition; duplicate/coalesced feedback; ready/post-merge/terminal rejection; exact G1-G8 negative matrix.

### Phase 3: fold task operations into the task journal and delete event projections

- Add task `pending-unpublished`, `published`, `applied-result`, and `acknowledged` operations keyed by idempotency scope/hash, plus strict replay validation.
- Replace stager/coordinator/dispatcher with one task submission/outbox API and one wire apply handler.
- Add the task-outbox reconciler that republishes unpublished operations after startup/live transient failures, records authoritative event identity/sequence, reuses durable results, and cancels only with proof that publication never occurred. Acknowledgement waits for global dispatch, not merely the task apply handler.
- Require deployment migration to find no unaccounted staged file. Convert a valid pending file only when one exact pending wire record references it; otherwise fail.
- Preserve task API response, replay flag, idempotency scope/hash, expected revision, entity IDs, human feedback continuation, gates, routing snapshot, and completion evidence.
- Delete `task-operations/` runtime creation and staging code after fault-injection tests pass.
- Delete GitHub/Linear journal implementations, store interfaces, startup open/seed, dispatch mirror writes, and provider journal tests. Keep hook event parsing and unified-wire helper adapters.
- Remove the unreachable direct label claim path and build label event metadata only once for generic admission.

Focused checks: task command crash matrix; private-body scans; idempotent result replay; stale conflicts; human continuation; helper cursor golden tests; event-wire sequence/ack/reject/recovery; server webhook and GitHub remediation tests.

### Phase 4: supervise runtime, centralize exact durable replacement, and shrink CLI/composition

- Move construction and recovery to `internal/app` with explicit dependency groups.
- Complete the sibling generation build with every canonical converter/reader/writer, full cross-artifact validation, fsync, immutable audit hashes, receipt-gated atomic manifest activation, abandoned/interrupted-generation cleanup, and idempotent reopen. Mutable stores validate by replay rather than against their initial audit hashes.
- Add a supervisor for HTTP, unified Run manager, task outbox, repository onboarding, and clock work. Migration recovery and health identity may be served while advancing endpoints and managers remain gated. Canonical writes cannot begin until `deployments/current.json` is a successful receipt for the exact running deployment ID and the service acquires the unopposed state-transition lease.
- Before the first post-receipt mutation, acquire the state-transition lease, revalidate the exact receipt, unchanged source hashes, and staged generation while holding it, publish and fsync the manifest, fsync the monotonic `canonicalWritesStarted` boundary, and only then start advancing managers. Recovery and `state-rollback-preflight` use that boundary to choose safe manifest deactivation versus proof-bounded whole-backup restoration.
- Propagate component failure through cancellation and join all in-process loops and ephemeral subprocesses with bounded shutdown evidence. Never signal, kill, or join durable Run-owned `factory-agents` tmux sessions; reconcile them after restart.
- Move schedules and heartbeat to one clock component.
- Introduce the narrow atomic replacement primitive and migrate only stores whose byte/permission/sync behavior is identical under golden tests.
- Move CLI parsing and agent helpers to `internal/cli`; keep the current helper command names required by retained workflow pins. Add generation-aware `state-rollback-preflight` and fail-closed `state-restore` commands and update `bin/network-app` to use the preflight.
- Reduce root `main` to command dispatch and `app.Run`.
- Delete dead helpers and obsolete adapters after exact-call searches.

Focused checks: activation only after exact success receipt, provider automatic fallback after candidate health failure and success-receipt-finalization failure with a staged but unselected generation, canonical-write boundary fsync/failure, pre-write manifest deactivation, post-write whole-backup restore, component failure/cancel/join tests, durable tmux survival and old-helper-path self-deploy restart, shutdown leak checks, signal behavior, helper environment/redaction, byte-identical store goldens, complete Go/race/vet suites.

### Phase 5: complete frontend feature ownership and invariant consolidation

- Extend `http.ts` with `sendJSON`; migrate settings first, then triggers/protected binding, workflows, and Tasks one wrapper at a time.
- Add the optimistic-editor primitive for settings, triggers, and protected binding only.
- Add the polling helper with conditional support for the live agent observer.
- Move shared forms, resource gate, page shell, chart, pagination, and truly shared agent types/helpers into acyclic shared owners.
- Extract settings, home/activity, wire, agents/observer, workflows, and triggers one vertical at a time, leaving exact route composition in `index.tsx`.
- Keep the completed Tasks provider typing and migrate it to the shared transport without weakening tasks-only idempotency.
- Remove confirmed dead selectors in one final CSS-only commit after desktop/mobile screenshots. Do not reorder remaining CSS.
- Broaden the candidate fixture to provide all canonical collaborators and render every authenticated route. Keep it environment gated and process bounded.

Focused checks after each slice: frontend typecheck/build; no dependency/lock/CSS diff except the CSS slice; exact transport/poll/save-state searches; route asset identity; focused server contract tests; candidate browser route and state matrix.

### Phase 6: delete transitional code and align documentation

- Delete empty packages, old schemas/writers, duplicate models, stale tests, migration-only defaults, obsolete comments, and unused exports proven by exact searches and package imports.
- Keep the one-shot source decoder needed to install from the immediately previous production state. Isolate it from runtime owners and record its removal condition after production cutover plus retained-state expiry.
- Rewrite README architecture, state inventory, event helpers, policy, migration, recovery, runtime supervision, frontend ownership, and troubleshooting to match executable behavior.
- Record actual package, LOC, exported declaration, durable artifact, loop, and frontend entry reductions. Explain every target miss with the distinct invariant that remains.
- Review the complete diff for secrets, debug output, generated artifacts, accidental public contract changes, and unrelated churn.

Focused checks: `rg` for obsolete paths/symbols, `go list` import boundaries, `git diff --check`, complete required verification.

## Migration, rollout, and rollback

### Before publication

- Tests create and destroy only disposable state roots.
- The production data root remains read-only during implementation and PR verification.
- A migration dry-run reads a copied current-state fixture, never the live directory, and emits the manifest/audit comparison.

### Deployment cutover

After the exact checkpointed head is human-merged with a merge commit and all safeguards remain green:

1. Resolve the single primary Worktrunk checkout at `/Users/tom/repos/tomnagengast/factory` and require it clean, on `main`, tracking the official origin, and exactly equal to fetched `origin/main`.
2. Fresh-read GitHub and Linear and prove the merge commit contains the checkpointed head.
3. Record the prior successful deployment ID and current health/receipt identity.
4. Require the live wire to have zero pending records, task staging to be fully accounted for, no policy mutation in flight, and the migration dry-run against a fresh permission-preserving copy to pass.
5. Deploy only with:

   ```text
   ~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
   ```

6. Startup creates the permission-preserving backup and staged canonical generation, validates and opens it read-only, recovers the authoritative wire, and serves exact health identity with advancing work gated. It does not publish the generation manifest. Capture the migration/backup receipt path from diagnostics.
7. `nags deploy` verifies loopback and public health and writes the exact successful current deployment receipt. Only then may the application acquire the state-transition lease, revalidate the receipt, unchanged source hashes, and staged generation while holding it, atomically publish and fsync the manifest, fsync `canonicalWritesStarted`, and start advancing managers or accept mutations. A concurrent rollback guard wins the exclusion and keeps advancement gated.
8. Verify active advancement state, receipt identity, immutable migration audit hashes, mutable store replay/counts, wire cursor, policy defaults/custom data, repository choices, task detail, retained Run history, and the active ENG-47 Run identity.
9. Exercise read-only APIs plus bounded duplicate-safe operations approved by the verification matrix.

### Recovery

- `bin/network-app rollback factory --to <deployment-id>` first acquires one process- and token-bound state-transition lease, asks the current service to quiesce advancing work, proves quiescence, and retains the lease until the provider has completed activation, health, and receipt finalization. While holding it, the wrapper runs the new binary's read-only equivalent of:

  ```text
  factory state-rollback-preflight \
    --data-root /Users/tom/.local/share/factory/data \
    --to-deployment <deployment-id>
  ```

  The preflight validates the same lease token, requires a quiescent service, validates the target successful receipt and release contract, generation manifest, immutable migration audit, source backup receipt, and `canonicalWritesStarted`, and refuses to invoke the provider when source state would be stale. The provider's later deployment lock is additive; it does not replace this continuously held state exclusion.
- When canonical writes have started, whole-state restoration is an explicit preceding operation by the current compatible binary:

  ```text
  factory state-restore \
    --data-root /Users/tom/.local/share/factory/data \
    --migration-receipt <absolute-receipt-path>
  ```

  `state-restore` verifies the held lease, stopped service, exact receipt, and backup hashes. It then replays every canonical journal and compares its semantic digest, operation count, cursor, and identity graph to the immutable activation snapshot. It also proves that the event wire has not advanced and no post-cut admission, task, policy, repository, Run transition, external-effect receipt, or completion exists. A pre-cut backup is categorically ineligible after the write boundary when the activation inventory contained any nonterminal Run or live effect-capable agent session, including a retained legacy session: that process can mutate GitHub, Linear, or a repository before Factory receives an event or receipt. The proof also rejects any session created after activation. Only an activation snapshot with no such Run or session and exact no-post-cut-work equality may restore the complete source generation with modes, validate it, deactivate the canonical manifest, and write a restoration receipt. Any changed canonical state, activation-spanning Run/session, or unaccounted later session refuses restoration and requires a forward correction. The read-only preflight must then pass under the same lease before `bin/network-app` invokes `nags rollback`.
- If migration, startup, loopback/public health, or successful-receipt finalization fails, the manifest has not been published and source state remains authoritative. The provider's automatic fallback may therefore reactivate the previous release without a Factory rollback hook. Preserve failed staged-generation evidence; a later candidate must discard or independently revalidate and rebuild it after any source-store change.
- If the exact success receipt exists but manifest publication or boundary sync fails, advancing work remains gated. Recovery under the state-transition lease either completes the same validated activation when no rollback owns it, or deactivates a published pre-write manifest before an explicit provider rollback. The provider has no automatic fallback branch after a successful receipt, so it cannot restart the old release across this boundary.
- After `canonicalWritesStarted`, never start the old release against stale source stores. Under the continuous state-transition lease, quiesce Factory and preserve the failed canonical generation. Run fail-closed `state-restore` only when replay proves the canonical generation is still exactly the no-post-cut-work activation snapshot and the activation inventory proves there was no nonterminal Run or live effect-capable agent session. An activation-spanning retained session, any later session, or any post-cut work or effect makes restoration and rollback forbidden even when journals and the wire appear unchanged; merge and deploy a forward correction. Only an eligible no-session snapshot may proceed to `state-rollback-preflight` and `nags rollback` under the same lease.
- Never mix individual old and canonical store files. Recovery is whole-generation or whole-backup only.
- Preserve migration, deployment, and failed-release receipts for diagnosis.

Exact health and receipt probes:

```text
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . /Users/tom/.local/share/factory/deployments/current.json
```

## Verification matrix

| Surface | Exact verification |
| --- | --- |
| Migration | Fresh and current-shape fixtures; non-activating dry-run audit; immutable initial source/canonical hashes and counts; mutable journal replay after legitimate writes; manifest absent through candidate health and receipt finalization; provider fallback after health failure and success-receipt-finalization failure; exact-receipt activation; monotonic write boundary; rollback-versus-receipt race paused after preflight; unknown/ambiguous rejection; injected failure at every write/sync/rename/activation; idempotent reopen; no-post-cut-work restore rehearsal; restore refusal after task creation, Run transition, wire advance, external-effect receipt, later live tmux session, or an activation-spanning retained legacy session that makes a GitHub/Linear mutation with delayed or absent webhook delivery while journals and wire remain unchanged |
| Event wire | duplicate IDs, batch order, channel cursors, retention, torn tail, append/sync rollback, poison latch, handler retry/permanent reject, restart catch-up, body-free audit |
| Unified Runs | rule match/no-match/multi-match, suppression, hop/cycle/rate/global limits, same-task serialization, source-dispatch gate, immutable admission/causation identity, transition outbox publication/ack ordering, routing failures, native start, protected/native feedback, retry, crash transitions, GitHub remediation, retention/compaction |
| G1-G8 safeguards | human merge only, checkpoint binding, changed head, squash/rebase mismatch, unmerged close, review/check regression, dirty/divergent main, deployment identity mismatch, incomplete task/children, branch/worktree residue |
| Task outbox | every command kind, fresh/duplicate idempotency, stale revision, unpublished/published/applied/acknowledged crashes, pre-append failure, live/startup republish, proven cancellation, missing/conflicting pending body, private-body scans, restart recovery |
| Policy | workflow draft/publish/delete, protected binding, rules, schedules, agent/runtime settings, project activation, independent public revision domains, internal admission generation, pending-admission lock, default consolidation, custom preservation/conflict |
| Repositories | compiled/admitted routes, project metadata, normalized origin, local/managed path containment, default branch, bootstrap/cloud metadata, provider coordination, completion reader equality |
| Runtime | migration health before advancement, exact success-receipt activation, component start failure, runtime failure propagation, cancellation, bounded in-process/ephemeral joins, durable tmux survival/reconciliation, old-pin self-deploy restart, signals, no owned leak |
| Security | webhook signatures/timestamps/replay, OAuth/local auth, task capability token, same-origin, JSON limits/strict decoding, file modes, symlink/path traversal, environment allowlist/redaction |
| Frontend static | frozen install, typecheck, build, one JS/one CSS asset, no package/lock changes, exact route dispatch, raw `fetch` only in transport, `setInterval` only in poll owner, copied normal save state removed |
| Browser | candidate assets, all public/authenticated routes, desktop/mobile, keyboard/focus, loading, empty, error, 409 conflict, offline/recovery, success, console/network clean, Linear read-only, native idempotency |
| Required Factory suites | `go test ./...`; `go test -race ./...`; `go vet ./...`; `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile`; `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck`; `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` |
| Diff quality | `git diff --check`; no secret/debug/generated/unrelated files; actual package/LOC/artifact/API budgets recorded |

## Principal risks and mitigations

- **P0: a protected lifecycle gate moves or weakens.** Keep G1-G8 behavior in explicit negative tests before and after package folding. Editable policy cannot alter the validators.
- **P0: migration loses or ambiguously merges durable ownership.** Validate the complete source graph first, generate side-by-side, activate once, and reject every orphan, collision, unknown schema, or count/hash mismatch.
- **P0: private bodies enter the wire or logs.** Keep opaque operation references and scan serialized wire, logs, errors, and APIs with secret sentinel fixtures.
- **P1: unified Run admission creates duplicate active ownership.** Append one admission batch, use deterministic identities, retain oldest-per-task serialization, and fault-test each state transition.
- **P1: task outbox acknowledges too early.** The private pending append precedes publication; applied result precedes acknowledgement; each boundary is restart tested.
- **P1: policy mutation races undecided events.** Retain one non-reentrant policy/admission lock and prove pending decisions block mutation.
- **P1: post-cut rollback starts against stale source stores.** Fsync the monotonic write boundary before mutation; require generation-aware preflight; require exact whole-backup restoration after the boundary.
- **P1: preflight races receipt-triggered advancement.** Hold one state-transition lease from pre-quiescence through provider receipt finalization, and require the application boundary transition to acquire the same exclusion.
- **P1: provider fallback bypasses Factory rollback state repair.** Keep the canonical generation staged and unselected through both provider failure branches; publish the manifest only after the exact successful receipt under the state-transition lease.
- **P1: backup restore discards post-cut state or misses an agent side effect.** Permit restoration only when canonical replay, wire cursors, effect receipts, and live-session inventory prove the exact no-post-cut-work activation snapshot, and categorically refuse when any nonterminal Run or effect-capable session spanned activation; otherwise require a forward correction.
- **P1: terminal or derived Run events publish before their source state.** Preserve immutable causation identity and require the transition/outbox journal operation before publication and global-dispatch acknowledgement.
- **P1: active ENG-47 cannot resume.** Preserve exact pin/session/segment/attempt identity and keep required helper commands plus legacy pin execution until no retained nonterminal pin needs them.
- **P1: supervisor cancellation loses an acknowledgement.** Component shutdown has explicit ownership, ordering, and bounded join tests.
- **P2: API or frontend DOM changes during consolidation.** Preserve current server contracts through projections, move one caller/feature per commit, and use fixture/browser parity.
- **P2: abstraction hides different durability semantics.** Share only atomic replacement; keep append/outbox/compaction logic domain owned.
- **P3: package/LOC budgets encourage cosmetic movement.** Count owners, exports, artifacts, and state machines, not only lines; explain preserved distinct invariants.

## Unresolved questions

1. Will a separately routed `tomnagengast/network` issue authorize the two provider prerequisites from round 8 before ENG-47 planning resumes: a pre-activation Factory state-contract/lease guard on direct deploy and rollback, and an fsync-durable finalization acknowledgement for active selection, runtime artifacts, receipt, and parent directories? ENG-47's Factory project metadata does not grant cross-repository write authority, and the cutover is unsafe without both provider guarantees.

All Factory-local product and security choices are resolved. The plan deliberately chooses non-destructive one-shot migration over an ungranted live-data wipe and preserves current Linear capability plus the active old workflow pin. Provider routing is the sole authority blocker.
