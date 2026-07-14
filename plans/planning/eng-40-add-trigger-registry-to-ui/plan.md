# ENG-40 Implementation Plan: Generic Trigger Registry and Durable Workflow Invocations
> updated: 2026-07-14T21:49:36Z

Linear: https://linear.app/nags-cloud/issue/ENG-40/add-trigger-registry-to-ui

## Current baseline

- Draft PR #9 is open from `eng-40-add-trigger-registry-to-ui` into `main` at approved research head `a232675862e94953ce9be7cecdd74798f5a05f99`.
- The branch contains research only. No implementation plan or product code from the rejected closed trigger design exists to preserve or unwind.
- The current wire retains 10,000 recent records plus every pending record, publishes collector output through `PublishBatch`, dispatches record routes in registration order, and stops ordered catch-up on transient failure.
- The current Run store retains 100 Runs while preserving active/transition-bearing records, supports 1 through 10 concurrent Runs, and coalesces same-issue triggers in the legacy `Claim` path.
- Settings permit 1 through 8 workflows, each with 1 through 20 bounded steps. The only current runner is `do`, but generic routing must select and snapshot a configured workflow rather than encode a trigger kind.
- Existing protected contextual feedback, GitHub remediation, post-merge continuation, human-only merge, exact verified-head deployment, and cleanup remain authoritative system behavior.

## Objective

Add an authenticated `/triggers` workspace for generic exact event-to-workflow rules and independent cron schedules. Every event accepted onto Factory's normalized wire must be evaluated against every enabled rule. Persist one immutable decision per retained event, one pinned invocation per matching rule revision, and distinct serialized Runs per issue without replacing protected lifecycle handlers or legacy Runs.

## Approved product contract and scope boundaries

- A rule has stable ID, revision, name, enabled state, optional exact filter over open source token, type, action, nullable subject, and attribute membership, an enabled workflow reference, issue target policy, and configurable hop/outstanding/rate controls.
- Omitted filter fields are wildcards. A nullable subject distinguishes wildcard from exact subject absence. All matches fire in stable rule-ID order.
- The authenticated normalized wire is the only generic admission gate. Linear, GitHub, service, project, cron, `agent-record`, `agent-run`, lifecycle, derived Factory, and future normalized events are all matchable.
- Actor, provenance, producer, issue identity, added labels, and causation are bounded normalized filter/audit metadata, not hidden eligibility checks.
- A derived event carries causal root, parent invocation, parent Run, hop, and ancestor stable rule path. Suppress only if the candidate stable rule ID is already in that ancestry path. Sibling events may match the same rule independently.
- Every admitted invocation snapshots the complete validated workflow definition and resolved target identity. Live workflow or rule edits cannot alter it.
- Current workflows remain Linear-issue-centric. Targets are fixed issue, event subject, or exactly one configured event attribute. Workflows without a Linear issue remain out of scope.
- Cron schedules contain timing and event context only. They emit normal `factory / cron / due` events; ordinary rules select workflow and target.
- Configured matches are additive to protected contextual feedback, GitHub remediation, post-merge, merge authority, exact-head deployment, and cleanup. Registry CRUD cannot disable or mutate those routes.
- Routing durability is bounded to the wire contract. Prune only after wire acknowledgment, terminal reflection for all invocations, and eviction of the event from the retained wire window.
- Keep the legacy settings schema readable. Once a non-legacy source token is observed, persist that prior-binary activation is unavailable and use forward correction or revert on current `main`.
- Do not add a generic ingestion API, expression language, regex/script predicates, configurable repositories/commands/providers, a permanent event history, fixed segment-size product contract, or a prior-binary migration subsystem.

## Planning-derived implementation bounds

These are validation and operating defaults, not new event-admission semantics:

- Registry: at most 32 rules and 32 schedules. This caps one event's fan-out at 32 while remaining four times the current maximum of 8 workflows.
- Rule attributes and schedule context: at most 32 keys, reusing the event envelope's existing maximum; keys and values reuse the existing 256-byte event-field bound.
- Rule names: 80 bytes, matching workflow names. Stable IDs reuse the existing lowercase identifier shape and 48-byte bound.
- Causal hop: default 4, configurable 1 through 8. Eight permits one hop per currently configurable workflow while keeping malformed cross-rule chains bounded.
- Per-rule outstanding invocations: default 10 and maximum 100. The default matches maximum configured Run concurrency; the maximum matches retained Run capacity. Count by stable rule ID across revisions so an edit cannot evade the ceiling while older work remains nonterminal.
- Per-rule admissions per rolling hour: default 120 and maximum 10,000. The default permits the current 30-second heartbeat rate; the maximum matches one complete retained wire window. Persist 60 UTC minute-count buckets per stable rule ID across revisions so an edit or early wire eviction cannot erase enforcement. Retries and replay of retained decisions do not consume rate again.
- Global nonterminal invocation ceiling: 100, matching retained Run capacity. A gate-passing match above a rule or global ceiling becomes a durable visible `suppressed` outcome rather than growing the queue.
- Routing persistence uses an append/projection bounded by retained decisions, nonterminal invocations, and at most 60 one-hour rate buckets per stable rule. It has no guessed byte/segment product limit. Compaction runs when obsolete operations exceed the current retained projection, mirroring the wire's logical `2 * live` compaction trigger.
- Terminal invocations compact to audit summaries containing identity, rule/revision, target, workflow ID/digest, outcome, Run ID, timestamps, and suppression/rejection reason. The full pinned workflow remains only while the invocation is nonterminal.
- Validate these bounds with generated worst-case fixtures during implementation. If the measured compacted projection or one full compaction violates a 1-second focused-test budget on the development machine, lower fan-out/outstanding maxima in the plan review cycle rather than adding a second storage subsystem.

## Implementation

### 1. Open the event vocabulary and add exact nullable matching plus causation

Files: `internal/eventwire/event.go`, `internal/eventwire/journal.go`, `internal/eventwire/query.go`, `internal/eventwire/wire.go`, `agent_commands.go`, `internal/server/server.go`, focused eventwire/server/command tests.

- Validate `Source` as a bounded lowercase token instead of the `linear | github | factory` enum. Retain constants as conveniences.
- Remove the repeated closed source allowlists from `/api/wire` query parsing and `agent events`; validate the same open token in all three paths.
- Add immutable optional causation fields to `eventwire.Event`: root event ID, parent invocation ID, parent Run ID, hop, and ancestor rule IDs. Direct producers canonicalize missing causation to root=self, hop zero, empty ancestry before journal append.
- Add a registry-owned filter type with pointer/nullable subject semantics. Keep the current internal `eventwire.Filter` behavior for existing system routes where changing it would add risk; share exact scalar/attribute helpers so generic and system matching cannot drift.
- Canonicalize event/filter maps before digests and persistence. Preserve event clone isolation and strict bounds.
- Test open unfamiliar sources across validation, journal reopen, query, CLI, and HTTP; nullable subject wildcard/absence/value; exact AND attributes; causal validation; clone safety; and no regression to current system filters.

### 2. Add batch admission before per-record wire dispatch

Files: `internal/eventwire/wire.go`, `internal/eventwire/wire_test.go`, new `internal/triggerrouter/coordinator.go`, focused coordinator tests.

- Add a leading `BatchHandler` registration surface to `Wire`. `catchUpLocked` snapshots one stable contiguous pending prefix, invokes batch handlers once with cloned records, then runs existing per-record routes in their current order.
- Batch-handler failure is transient and prevents all per-record dispatch/ack for that prefix. Existing per-record permanent rejection and channel acknowledgment semantics remain unchanged.
- Wrap the raw wire in `triggerrouter.CoordinatedWire`. Keep the raw pointer private after handler registration and expose `Publish`, `PublishBatch`, `CatchUp`, status/query/record reads, and coordinated registry/settings mutation.
- Enforce lock order: policy coordinator, wire dispatch mutex, one journal/route mutex, then registry/settings/routing/Run store locks. Handlers never reacquire the coordinator, republish synchronously, or call outward while holding a store lock.
- Register generic batch admission first and protected Linear/GitHub handlers afterward.
- Prove with an injected write counter that an N-record `PublishBatch` invokes generic `ApplyDecisionBatch` once before N per-record handlers. Test empty, existing-decided, mixed decided/undecided, transient batch failure, later protected-handler failure, replay, and `CatchUp` prefixes.

### 3. Add the versioned rule and schedule registry

Files: new `internal/triggerregistry/model.go`, `store.go`, `defaults.go`, tests; coordinated settings validation in `internal/settings` and `internal/server`.

- Define schema-1 registry snapshot with revision/update time, rules, schedules, and monotonic `LegacyRollbackIncompatible` compatibility evidence.
- Rule revision increments only when execution semantics change: filter, workflow reference, target policy, admission limits, or enabled state. Name-only edits keep semantic revision stable.
- Validate all approved bounds, unique stable IDs, nullable subject, reserved metadata keys, target policy shape, enabled workflow reference, cron/context separation, and immutable server-owned revisions/timestamps.
- On absent registry only, synthesize defaults from legacy settings without writing until first successful mutation. Seed the current label and comment choices using explicit normalized actor/provenance/added-label/issue filters and current workflow references.
- Keep legacy trigger fields round-tripped in `settings.json` for prior-binary readability but remove their editing controls from `/settings`.
- Coordinate registry and workflow mutations. Reject a settings change that removes/disables a workflow referenced by an enabled rule; admitted invocations continue with their pinned snapshot.
- Fixed-target rule saves preflight canonical issue/repository routing through the existing allowlisted `RepositoryResolver` before taking the policy coordinator, using captured registry/settings revisions. After preflight, acquire the coordinator and recheck both revisions and workflow availability before committing. Do not hold event publication on a Linear network call. Subject/attribute targets validate syntax at admission and resolve repository metadata during promotion; promotion re-resolves every target before queueing so repository metadata changes remain safe.
- Test absent-store synthesis, first write, reopen, strict schema/size/permission behavior, optimistic revision conflicts, semantic revision rules, workflow cross-validation, fixed-target preflight, malformed/broad candidates, and no mutation on error.

### 4. Normalize complete filter and causation metadata for every producer

Files: `internal/server/server.go`, `internal/linearhook/wire.go`, `internal/githubhook/wire.go`, `internal/agentrun/collector.go`, service/project event producers in `main.go` and `internal/projectsetup`, focused adapter/collector tests.

- Linear normalization emits delivery ID, actor ID, provenance, canonical issue identity, every newly added label ID, and one trimmed Unicode case-folded canonical label name. Apply the identical canonicalizer to reserved label filters.
- Preserve raw payload privacy. Comment bodies, commands, secrets, credentials, repository paths, and raw webhook data never enter filterable attributes or trigger API responses.
- GitHub normalization adds bounded producer/provenance and existing repository/PR context without changing protected wake conversion.
- Cron, service, and project events use direct causation roots.
- For invocation-owned Runs, `agent-record` and `agent-run` events inherit root/parent/hop/ancestry from the Run's pinned invocation metadata. Legacy Runs emit direct legacy causation and remain matchable.
- Test current actor/label/comment defaults, broader operator filters, Factory-authored comments remaining matchable when explicitly configured, signed GitHub events, heartbeat/project/agent/lifecycle matching, inherited sibling causation, and no private payload leakage.

### 5. Persist bounded batch decisions and invocation projections

Files: new `internal/triggerrouter/store.go`, `admission.go`, `retention.go`, tests; small retained-record inspection extension in `internal/eventwire/journal.go`/`wire.go` if needed.

- Use an append-only schema-1 JSONL operation log with an in-memory exact projection. One `ApplyDecisionBatch` operation contains every decision for the coordinated undecided prefix and performs one append plus one fsync.
- Recovery truncates only an incomplete final line. Malformed complete operations, duplicate decision identity with conflicting content, illegal invocation transition, or projection inconsistency fail closed.
- Admission sorts enabled rules by stable ID and evaluates every record against one registry/settings snapshot. It persists one immutable decision per event with zero or more `invocation`, `rejected`, or `suppressed` outcomes.
- Invocation key is the domain-separated digest of event ID, stable rule ID, and rule revision. Replay inside retention reuses the prior decision and never consumes rate/outstanding limits twice.
- Snapshot the complete workflow value, rule/revision/filter, causal ancestry extended with the selected stable rule ID, target identity, settings revision/digest, and timestamps for every admitted invocation.
- Apply ancestry-cycle, hop, rolling-hour, per-rule outstanding, and global outstanding checks in stable order. Outstanding and rolling-hour accounting use the stable rule ID across revisions; rolling counts live in durable UTC minute buckets that age out after one hour independently of wire retention. Suppression is durable and visible but does not block protected per-record handlers.
- Append invocation state-transition operations. Keep terminal full snapshots until reflection is durable, then project compact audit summaries.
- Compact atomically to one checkpoint plus live retained projection when obsolete operation count exceeds live operation count. Retain decisions while their wire record is pending or retained, and retain nonterminal invocations regardless of wire eviction. Prune only acknowledged, terminal-reflected, wire-evicted decision groups.
- Set `LegacyRollbackIncompatible` monotonically when any admitted batch contains a source outside the prior vocabulary.
- Test one-fsync batch admission, zero/multi-match, all event classes, sibling matches, A-to-A, A-to-B-to-A, hop/rate/outstanding suppression, retained replay, policy edit after decision, tail recovery, complete corruption, compaction, pruning prerequisites, compatibility marker, and generated 10,000-record compact projection timing/size.

### 6. Add deterministic invocation promotion and distinct serialized Runs

Files: new `internal/triggerrouter/manager.go`, `reconcile.go`, tests; `internal/agentrun/store.go`, `manager.go`, `collector.go` and focused tests.

- Resolve admitted target/repository metadata asynchronously. Transient Linear failures remain retryable; invalid/missing targets and non-allowlisted routes transition only that invocation to `rejected`.
- Promote by wire sequence, stable rule ID, rule revision, invocation ID. Different issues may use normal global concurrency. Only the oldest nonterminal invocation for an issue may claim a Run.
- Add `claiming`. Derive deterministic Run ID from invocation ID, append claim intent, call `RunStore.EnsureInvocationRun`, then append `claimed`.
- Add optional `InvocationID`, pinned causation, pinned workflow, and reflection receipt fields to Run. `EnsureInvocationRun` returns an identical existing pair, rejects collisions, refuses another issue owner, and never uses legacy duplicate coalescing.
- Once the registry is active, remove legacy Linear-label Run admission from per-record dispatch so the synthesized default label rule is the only new label admission and cannot duplicate generic work. Preserve reading and launching pre-registry legacy Runs.
- Retain protected contextual comment `ClaimContinuation`/resume, GitHub remediation, and post-merge continuation for their original Run. A configured generic comment rule may independently admit a distinct queued invocation, as required by the additive contract.
- Gate manager start for invocation Runs on a durable matching `claimed` projection. Existing startup/session recovery remains responsible after `MarkStarting`.
- Reflect terminal Run outcome and completion evidence into the invocation before promoting the next same-issue item, then mark the Run reflection receipt. Do not let the collector acknowledge/prune the terminal transition before reflection.
- Startup sequence: open/validate stores, register handlers, reconcile invocation/Run pairs, coordinated wire catch-up, reconcile again, then enable mutating APIs, cron, invocation promotion, and Run manager. Health may listen earlier; readiness gates all mutations/workers.
- Fault-inject event append, batch decision append, claim intent, Run replacement, claimed confirmation, manager notification, terminal Run write, invocation reflection, Run receipt, collector acknowledgment, and next promotion for succeeded/blocked/failed outcomes. Prove one label delivery creates only its generic invocation while a contextual comment can both resume its protected Run and independently enqueue a configured generic invocation.

### 7. Execute the pinned workflow snapshot

Files: `internal/agentrun/launcher.go`, `execute.go`, `agent_commands.go`, related tests.

- For an invocation Run, materialize the complete pinned workflow JSON in the private Run directory with `0600` permissions before launch and pass an explicit `--workflow-file` to `agent-exec`.
- `agent-exec` strictly reads and validates that file from the expected Run directory and constructs `ExecuteConfig.Workflow` from it. Do not reload the live workflow or infer it from source/type/legacy trigger kind.
- Preserve existing legacy Run fallback through `WorkflowForTrigger` for Runs without invocation identity.
- Lifecycle prompt kind may change for feedback, GitHub remediation, or post-merge, but the original pinned workflow steps/runner remain unchanged.
- Verify path containment, permissions, malformed/missing snapshot failure, workflow edit/delete after admission, retry/resume/post-merge behavior, and legacy fallback.

### 8. Add deterministic cron schedules and cursors

Files: new `internal/triggerscheduler/scheduler.go`, `store.go`, tests; `go.mod`, `go.sum`; wiring in `main.go`.

- Add `robfig/cron/v3` with the explicit standard five-field parser. Reject seconds, descriptors, `@every`, embedded timezone directives, and arbitrary durations; validate separate IANA timezone.
- Store schema-1 cursors separately by schedule ID and material revision/fingerprint. A new, re-enabled, or materially edited schedule begins strictly after edit time.
- Emit deterministic IDs from schedule ID, material revision, and scheduled UTC instant. Include reserved schedule ID/revision/scheduled-at attributes plus bounded non-conflicting context.
- Advance cursor only after successful coordinated publication. On restart publish only the oldest missed occurrence, record skipped count, and advance to the next future occurrence.
- Start scheduler only after coordinated catch-up and reconciliation readiness. Stop it cleanly with service context and leave no process behind.
- Test timezone/DST boundaries, edit/enable/disable, deterministic duplicates, one-oldest catch-up, cursor crash boundary, publication failure, no issue requirement, and ordinary rule routing.

### 9. Add authenticated trigger CRUD and operational status

Files: `internal/server/server.go`, `server_test.go`, response/request types or new trigger handler file if extraction improves clarity.

- Register exact canonical authenticated `GET /triggers`, `GET /api/triggers`, and `PUT /api/triggers`; reject trailing slash and cleaned aliases.
- GET returns registry revision, editable rules/schedules, enabled workflow choices, observed source suggestions, schedule last/next/skipped status, rule outstanding/rate status, recent invocation/suppression summaries, compatibility status, and synthesized protected routes.
- Never expose workflow snapshot files, raw payloads/comment bodies, secrets, commands, repository paths, credentials, or protected handler mutation controls.
- PUT requires same-origin JSON, bounded strict decoding, optimistic revision, whole-snapshot validation, fixed-target preflight, and coordinated registry/workflow checks. Return authoritative current snapshot on conflict and never partially mutate.
- Settings PUT uses the same coordinator for workflow cross-validation. Unauthenticated, cross-origin, oversized, malformed, stale, unsupported, or unroutable requests leave all stores unchanged.
- Keep protected routes read-only and clearly identify configured matches as additional invocations.

### 10. Build the `/triggers` frontend

Files: `frontend/src/index.tsx`, `frontend/src/styles.css`, frontend configuration/tests if present.

- Add Triggers between Agents and Settings in shared navigation and exact client routing.
- Render rule, schedule, protected route, and recent invocation sections with explicit loading, empty, dirty, validation, save, conflict, success, network failure, and readiness states.
- Rule editor supports name/enabled, free-text source with observed suggestions, type/action, nullable subject mode, exact attribute rows, workflow, fixed/subject/attribute target, hop/outstanding/rate controls, add/edit/clone, confirmed delete, and rule scope summary.
- Warn and reconfirm match-all, telemetry, lifecycle, agent, and derived-event rules without preventing them.
- Schedule editor supports five-field cron, IANA timezone, optional subject/context, enable/disable, status, add/edit, and confirmed delete. It has no workflow selector.
- Protected routes are read-only. Recent outcomes distinguish admitted/queued/claiming/claimed/succeeded/blocked/failed/rejected/suppressed and show safe reasons.
- Preserve keyboard access, visible focus, label/error associations, light/dark modes, and responsive desktop, 720-pixel, and 320-pixel layouts.

### 11. Update documentation and rollback guidance

Files: `README.md`, directly affected checked-in operational documentation.

- Document generic rule semantics, every-wire-event eligibility, nullable subject, metadata, causation, limits, target policies, workflow pinning, fan-out/serialization, cron production, protected additive routes, and bounded retention.
- Document authenticated API/page paths, private file ownership/modes, readiness, and observed-source compatibility marker.
- Preserve clean merged-main deployment, human-only merge, exact verified-head, receipt, health, and cleanup instructions.
- State that prior-binary activation is unavailable after any future source token is observed. Before that, require quiescence, zero pending records, zero nonterminal registry work/Runs, and valid legacy settings. Recovery otherwise is a forward corrective or revert commit on current `main`.

## Implementation sequence and commits

1. Event vocabulary, causation, and batch wire hook.
2. Registry plus bounded routing projection and admission tests.
3. Invocation/Run saga, pinned workflow execution, and startup reconciliation.
4. Cron scheduler and cursor persistence.
5. Authenticated API and frontend.
6. Documentation, integration/fault tests, and final simplification.

Use recent `Factory: ...` commit style. Keep each checkpoint buildable and run focused tests before pushing. Do not merge the PR.

## Verification before publication

### Focused automated checks

- Format every changed Go file with `gofmt`.
- Run focused packages while iterating: `go test ./internal/eventwire ./internal/triggerregistry ./internal/triggerrouter ./internal/triggerscheduler ./internal/agentrun ./internal/server ./internal/settings`.
- Run fault-injection and generated high-volume batch/retention tests separately with verbose output when diagnosing.
- Run frontend typecheck and frozen production build while iterating.
- Require `git diff --check` after every remediation round.

### Required repository checks

From the issue worktree, with repository-pinned Bun:

```bash
go test ./...
go test -race ./...
go vet ./...
export MISE_BUN_VERSION=1.3.11
bun install --cwd frontend --frozen-lockfile
bun run --cwd frontend typecheck
bun run --cwd frontend build
```

Record the exact clean verified head only after final review and CI remediation.

### Browser verification

- Check for an existing development server before starting one. If changed code requires an isolated server, use a temporary port and stop it before the turn ends.
- Verify authenticated `/triggers` at desktop, 720-pixel, and 320-pixel widths in light and dark modes.
- Exercise rule add/edit/disable/delete, match-all warning, nullable subject modes, attributes, workflow/target choices, limits, optimistic conflict, invalid fixed target, loading/empty/network failure, and keyboard focus.
- Exercise schedule add/edit/disable/delete, cron/timezone validation, status, and no workflow field.
- Confirm protected routes are visible but immutable and recent invocation/suppression status contains no secrets or private paths.
- Verify unauthenticated API/page behavior, cross-origin mutation rejection, trailing-slash/unknown `404`, no console errors, and no unexpected failed requests.
- Through focused local fixtures, verify unfamiliar source, telemetry, agent/lifecycle, sibling-derived, cycle-suppressed, multi-match, same-issue serialization, and different-issue fan-out states render correctly.

## Publication and green loop

- Commit implementation in the logical units above and push the issue branch.
- Update draft PR #9 with the approved scope, architecture, storage/claim invariants, migration/rollback boundary, screenshots if useful, and complete verification evidence; then mark it ready for review.
- Publish an implementation summary to ENG-40 and move it to In Review.
- Use `$FACTORY_AGENT_HELPER agent github-events` as the durable wake signal. After every event or timeout, refresh authoritative PR head, reviews, threads, checks, and merge state with `gh`.
- Use a Claude tmux review child first for bounded adversarial implementation review. On operational Claude failure only, run the identical prompt with a Codex tmux child. Address actionable in-scope findings.
- Re-run affected focused checks after each remediation and the complete required repository checks after the final change.
- Prove the local clean verified head, pushed PR head, reviewed head, and green-check head are identical. Write `$FACTORY_AGENT_HELPER agent checkpoint ready-for-merge` for that exact OID and stop with human merge authority intact.

## Post-merge deployment and cleanup

- Resume only after authoritative GitHub state proves PR #9 was human-merged and its merge result contains the exact verified head. Treat closed-unmerged or head mismatch through the typed Factory lifecycle.
- In primary checkout `/Users/tom/repos/tomnagengast/factory`, require clean `main`, fetch, and fast-forward to `origin/main`. Never deploy from the issue worktree or T9 mirror.
- Capture current deployment identity/receipt, then deploy only clean updated `main`:

```bash
bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
```

- Verify local and public health identity, launchd state, Factory tmux-session survival, authenticated `/triggers` readback, private registry/routing/cursor/workflow file modes, schedule and invocation persistence, deployment receipt identity, and exact merged ancestry.
- Verify GitHub branch auto-deletion and use Worktrunk for issue-worktree cleanup. Confirm primary `main` is clean and healthy.
- On deployment or identity/content failure, preserve evidence, use the documented network-app recovery path, verify the restored health/receipt, and report `deployment_failed` if forward recovery cannot succeed.

## Unresolved questions

None.
