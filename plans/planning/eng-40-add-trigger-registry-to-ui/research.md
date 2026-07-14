# ENG-40 Research: Generic trigger registry and durable workflow invocations

Linear: https://linear.app/nags-cloud/issue/ENG-40/add-trigger-registry-to-ui

## Research questions

1. What product contract does the new human feedback establish, and which prior conclusions remain valid?
2. How generic is the current event wire, and what must change so current and future normalized sources can be routed declaratively?
3. Where must rule matching become durable so retries and policy edits cannot change an already admitted event?
4. How can every matching rule retain distinct workflow intent without breaking Factory's one-issue lifecycle safeguards?
5. What execution-target policies fit the current issue-centric Run model?
6. How should cron remain independent from workflow routing while staying deterministic and restart-safe?
7. What persistence, workflow-version, concurrency, compatibility, and rollback safeguards are required?
8. How should authenticated CRUD and `/triggers` fit the existing API and UI?
9. Which acceptance criteria are observable, and how will each be verified and deployed?
10. Which events are eligible for generic admission, and how are recursion, queue growth, and admission authority bounded?

## Evidence-backed answers

### 1. Corrected product contract and retained work

Observed facts:

- The original issue asks for a `/triggers` registry that lists existing triggers and supports add, modify, and delete operations, including cron schedules.
- The new human feedback rejects the unpublished closed `linear-label | linear-comment | cron` abstraction. It explicitly requires generic declarative event-to-workflow rules, cron as an event producer, durable workflow invocations, multi-match fan-out, an open event vocabulary, and explicit execution targeting.
- The prior plan never reached publication or implementation. Draft PR #9 contains only the committed research, so there is no completed implementation to redo.
- The prior research remains correct about private versioned persistence, optimistic CRUD, repository allowlisting, issue-centric lifecycle gates, deterministic cron cursors, protected GitHub remediation/post-merge behavior, authenticated UI conventions, and exact-main deployment.
- Plan review round 3 found a valid safeguard gap: a wire record can be pending before a Run exists, so workflow identity needed by that admission must survive registry edits and must block unsafe prior-binary rollback. The human continuation feedback and prior Factory blocker comment preserve that finding.

Proposed direction:

- Replace the earlier research's typed trigger kinds with an authenticated registry of generic rules. Each rule contains an optional exact-match filter, a configured workflow, and a target-resolution policy.
- Treat rules, cron schedules, durable routing decisions, workflow invocations, and lifecycle Runs as separate concepts.
- Preserve protected lifecycle routes as system-owned behavior. Configured rules may independently match the same normalized event, but cannot replace, disable, or alter GitHub remediation, contextual feedback handling, human merge authority, verified-head validation, deployment, or cleanup.

### 2. Existing generic wire and open event vocabulary

Observed facts:

- `eventwire.Event` already normalizes `source`, `type`, `action`, optional `subject`, multi-valued attributes, channels, and receipt time (`internal/eventwire/event.go:26-35`).
- `eventwire.Filter` already implements the requested AND semantics: omitted scalar fields are wildcards, configured scalars compare exactly, every configured attribute key/value must be present, and extra event attributes do not prevent a match (`internal/eventwire/event.go:87-114`).
- `Wire` evaluates every matching registered route in registration order. It does not use first-match semantics (`internal/eventwire/wire.go:103-142`).
- `Event.Validate` currently rejects any source other than `linear`, `github`, or `factory`; `/api/wire` and `agent events` repeat that closed allowlist (`internal/eventwire/event.go:42-44`, `internal/server/server.go:372-393`, `agent_commands.go:207-221`).
- The wire preserves unknown future event types, but commit `54bad3b` did not open future source names.
- Trusted adapters already own ingress authentication. Linear verifies HMAC, replay time, delivery shape, actor/provenance facts, and newly applied labels before normalization. GitHub verifies its HMAC and delivery metadata before normalization (`internal/server/server.go:528-637`). There is no generic external publish API.
- Factory emits one `agent-record` event per collected agent output line and one `agent-run` event per lifecycle transition. The manager collects before and after each reconciliation, and service heartbeats add another recurring Factory event source (`internal/agentrun/collector.go:84-123`, `internal/agentrun/collector.go:187-244`, `internal/agentrun/manager.go:141-153`, `main.go:476-517`). Serialization alone cannot prevent a generic rule from recursively growing a queue from those events.
- The current Linear label path admits only the configured actor and compares the configured label case-insensitively. The comment path additionally excludes Factory-authored comments and requires a valid issue, while continuation claiming requires retained Run history (`internal/server/server.go:578-601`, `internal/server/server.go:829-855`, `internal/agentrun/store.go:257-259`). Ordinary match attributes must not accidentally weaken those authority gates.

Proposed matching contract:

- Keep `eventwire.Source` as a string type, but validate it as a bounded lowercase token rather than an enum. Constants for `linear`, `github`, and `factory` remain conveniences.
- Match source, type, action, subject, attribute keys, and values byte-for-byte and case-sensitively after source adapters normalize them. The matcher performs no trimming or case folding.
- Represent rule subject as a nullable value: omitted or `null` is wildcard, `""` exactly matches canonical subject absence, and a nonempty value matches exactly. `Event.Subject == ""` is the one canonical absent representation, so the journal format does not need to change. Present empty source/type/action values remain invalid. Attribute filters keep current membership semantics, including exact empty-string values.
- Every enabled matching rule fires. Evaluate and persist matches in stable rule-ID order; do not use rule list order as hidden priority.
- An empty filter is an explicit match-all rule. The API accepts it, while the UI gives a prominent broad-rule warning and requires confirmation before save.
- Opening source vocabulary does not open ingestion. Current and future source adapters must still authenticate and normalize their inputs before publishing. Source-specific facts such as Linear actor ID, Factory provenance, added label names, issue ID, and issue identifier become bounded normalized attributes that rules can match.
- Admission authority is immutable envelope metadata, not a match attribute. Each trusted adapter stamps producer, authority, actor kind and ID, provenance, eligibility and restriction reason. Direct roots also carry their own event ID as causal root; derived events additionally carry parent invocation, parent Run, hop, and the stable rule-ID path. These fields are validated and journaled with the event but are not editable by rule filters.
- Default generic eligibility preserves current authority: a configured-actor Linear label addition and eligible non-Factory Linear feedback are direct roots; cron is a schedule root; signed GitHub lifecycle events, project onboarding, service telemetry, legacy events without admission metadata, agent output, and Run transitions remain protected or restricted by default.
- Registry operators cannot override adapter authentication, repository allowlisting, protected lifecycle behavior, or the Factory-authored Linear-comment exclusion. They may deliberately widen an authenticated adapter's restricted actor/provenance class only through an explicit rule admission override containing exact allowed actor IDs or provenance classes, a nonempty audit reason, and a second UI confirmation. Workflow-derived Factory events additionally require an explicit `allowDerived` override. The pinned invocation records the override.
- Linear adapters emit every newly added label as both byte-exact ID and a trimmed, Unicode case-folded canonical name. Registry validation applies the identical canonicalizer only to the reserved canonical-label filter. This preserves the current case-insensitive label behavior while all other fields remain byte-exact.
- Keep raw payloads, comment bodies, secrets, commands, repository paths, and credentials out of rule filters and API responses.

### 3. Durable routing decisions before mutable policy can advance

Observed facts:

- `Wire.Publish` fsyncs the event before dispatch. If any handler returns a transient error, that record remains pending and ordered catch-up stops (`internal/eventwire/wire.go:49-72`, `internal/eventwire/wire.go:103-142`).
- HTTP starts before `recoverEventWire` completes, so settings mutation is possible while an earlier record is pending (`main.go:406-429`).
- Event IDs are deduplicated only inside the retained journal window. Durable invocation idempotency cannot depend on wire retention alone (`internal/eventwire/journal.go:144-225`, `internal/eventwire/journal.go:391-410`).
- Registering one dynamic wire route per rule would make CRUD ordering and partial multi-route failure unsafe. One broad admission route can evaluate one immutable registry snapshot and atomically persist the full outcome.

Proposed durability boundary:

- Add one `CoordinatedWire` around trusted event publication, recovery, generic admission routing, registry mutation, and workflow mutation. Keep the underlying `eventwire.Wire` private after handler registration so no producer or recovery path can bypass the wrapper.
- Register one broad generic admission handler before the existing Linear and GitHub system handlers.
- `CoordinatedWire.Publish`, `PublishBatch`, and `CatchUp` all acquire the same policy coordinator before entering the wire. The fixed lock order is policy coordinator, wire dispatch mutex, one journal or route mutex, then registry/settings/invocation/Run stores. No store method may publish or call back into the coordinator while holding a store lock.
- The admission handler runs with the policy coordinator already held and must never reacquire it or synchronously republish. It evaluates all enabled rules against one registry and settings snapshot, then appends and fsyncs one atomic decision frame containing the wire event ID and sequence plus every match outcome. Persist an explicit empty decision when no rule matches.
- Replays reuse the existing decision and never re-evaluate the event against a later registry revision.
- Registry/workflow mutations check every pending wire record under the same coordinator. If any pending record lacks a durable admission decision, reject the mutation until catch-up records that decision. This closes the prior review's pending-wire race without freezing policy for records whose decisions are already pinned.
- Source-specific handlers remain later routes. If a protected Linear or GitHub handler fails after admission, the wire record stays pending, but its generic decision and workflow snapshots are already durable and idempotent.
- Only ledger persistence failures are transient admission errors. Invalid targets, causal suppression, and rule queue/rate denials are durable outcomes, so they cannot stall protected handlers.
- Startup opens and strictly validates every store, registers admission before protected handlers, reconciles invocation/Run pairs, performs coordinated catch-up, reconciles pairs again, and only then enables mutating APIs, cron, the invocation promoter, and the Run manager. Health may listen earlier, but readiness-gated mutation and workers cannot overtake recovery.

### 4. Durable invocation ledger, multi-match fan-out, and serialization

Observed facts:

- `agentrun.Store.claim` deduplicates by delivery ID and then coalesces every active admission for the same issue into the existing Run, incrementing `DuplicateTriggers`. It discards the incoming trigger's distinct intent (`internal/agentrun/store.go:203-255`).
- The manager assumes at most one active Run per issue. It uses an issue-derived tmux session name, and `awaiting_human_merge` remains nonterminal across GitHub remediation and post-merge continuation (`internal/agentrun/manager.go:155-198`, `internal/agentrun/manager.go:460-488`, `internal/agentrun/store.go:438-463`).
- Allowing multiple same-issue Runs to start directly would collide on session identity and could create competing PR, merge, deployment, and cleanup lifecycles.

Proposed invocation model:

- Add a private schema-1 segmented append ledger with checksummed, length-bounded frames and a rebuildable exact in-memory index. Do not place the unbounded history in a copy-on-write JSON document. A decision frame atomically persists zero or more immutable invocation or suppression outcomes.
- Index the decision by event ID and persist a digest of the canonical routing content: source, type, action, subject, attributes, channels, and immutable admission/causation envelope, excluding transport receipt time. The first wire sequence is audit data, not part of decision identity. If the same event ID reappears after journal retention, reuse the exact prior decision; if its canonical digest differs, fail closed as an ID collision.
- Compute the idempotency key from `(eventID, ruleID, ruleRevision)`, domain-separated and hashed. Do not use the shared delivery ID or bounded Run/wire retention as authority.
- Each invocation snapshots the event ID/sequence, immutable admission and causation envelope, stable rule ID and per-rule revision, complete matched filter, admission override and target policy, complete validated workflow value, settings revision and digest, resolved issue identity, state, timestamps, retry detail, repository route when resolved, and linked Run ID when claimed.
- Store the complete `settings.Workflow` value, not only `WorkflowID`. A later workflow edit, disable, or delete therefore cannot change admitted executable steps. Provider/model settings remain safe-boundary settings read for each lifecycle segment; ENG-40 pins the workflow, not the entire agent configuration.
- Invocation states are `admitted`, `queued`, `claiming`, `claimed`, `succeeded`, `blocked`, `failed`, `rejected`, and `suppressed`. Target syntax failures become durable rejected matches. Cycle, hop, queue, and rate denials become durable suppressed matches. Transient repository resolution leaves an admitted invocation retryable instead of blocking the entire event wire.
- Direct eligible events begin at hop zero. A derived event inherits its root event ID, parent invocation and Run, increments hop, and extends a stable rule-ID path. The same stable rule ID may execute at most once in one causal root even after rule edits. Default maximum hop is one; an explicit derived-event override may raise it only to the global hard maximum of three.
- Each stable rule has durable safety controls shared across revisions: `maxOutstanding` and `maxAdmissionsPerHour` default to 10 and are configurable only from 1 through 100. Outstanding includes admitted, queued, claiming, and claimed. The hourly count includes gate-passing matches, including rejected targets, but excludes retries and replay of an existing decision. A global outstanding limit of 1,000 provides a second bound.
- Promote invocations deterministically by `(wire sequence, rule ID)`. Different issue targets can use normal global concurrency. For one issue, only the oldest invocation may own a nonterminal Run.
- Add a separate recoverable claim saga. Persist `claiming` with a deterministic Run ID derived from the invocation ID, call idempotent `RunStore.EnsureInvocationRun` with that exact Run and invocation identity, then append `claimed`. `EnsureInvocationRun` returns an identical existing pair, rejects collisions, refuses creation while another nonterminal Run owns the issue, and never uses legacy duplicate-trigger coalescing.
- The manager may start an invocation Run only after a coordinator-protected check confirms the ledger is durably `claimed` and linked to that Run. A crash after claim intent but before Run creation recreates the same Run; a crash after Run creation but before claim confirmation links the existing Run and never creates a second one.
- Multiple rules matching one event always produce distinct invocations, even when workflow and issue are equal. They serialize per issue rather than increasing `DuplicateTriggers` or silently coalescing.
- Protected contextual Linear feedback, GitHub remediation, and post-merge continue to resume the existing lifecycle Run. A configured rule that also matches the same event creates an additional queued invocation by design; it cannot replace the system-owned resume.
- Terminal Run state and completion evidence are reflected back to the invocation before the next same-issue invocation is promoted. The Run then receives an invocation-reflected receipt. Startup reconciliation repairs either interrupted write order, and the collector cannot acknowledge or permit pruning of the terminal transition before reflection is durable.

Crash-recovery contract:

- A crash before event fsync leaves no event or invocation. After event fsync but before decision fsync, the record remains pending and coordinated catch-up creates exactly one decision under the still-guarded policy snapshot.
- A torn final ledger frame is truncated; any complete corrupt frame fails closed. A crash after decision fsync but before a later handler or wire acknowledgment reuses the byte-identical decision. Batch recovery has a decided prefix and undecided suffix, and policy mutation remains blocked until the suffix is decided.
- A crash after `claiming` but before Run replacement creates the deterministic Run once. A crash during Run replacement exposes the complete old or new projection. A crash after Run durability but before `claimed` links that exact Run, and the manager cannot launch it early.
- A crash after `claimed` but before notification is recovered by polling. Existing `starting` and `running` reconciliation remains responsible for launcher-boundary recovery.
- A crash after terminal Run durability but before invocation reflection reflects the same completion evidence and keeps the next same-issue invocation blocked. A crash after reflection but before the Run receipt adds the receipt exactly once. A crash before collector acknowledgment or next promotion retries without admitting a second successor.
- Fault-injection tests exercise every boundary above for succeeded, blocked, and failed outcomes with two same-issue invocations and one different-issue invocation. Global invariants are one immutable decision per event, one invocation per event/rule/revision, at most one Run per invocation, at most one nonterminal Run per issue, and no terminal transition pruning before reflection.

### 5. Explicit execution target and repository routing

Observed facts:

- Every current Factory Run requires a canonical Linear issue identifier. Repository resolution reads that issue's current Linear project metadata and accepts only an allowlisted repository/path pair (`internal/agentrun/store.go:213-221`, `internal/agentrun/repository.go:130-183`, `internal/agentrun/repository.go:193-247`).
- The complete allowlisted repository route is copied into a Run before execution (`internal/server/server.go:751-768`, `internal/agentrun/store.go:265-281`).
- Supporting workflows without any Linear issue would require a different run, approval, PR, merge, deployment, and completion model. The feedback identifies that as a larger change.

Proposed target policies:

- `fixed`: the rule stores one canonical Linear issue identifier. Registry writes preflight it through `RepositoryResolver`.
- `event-subject`: the normalized event subject must be exactly one canonical Linear issue identifier.
- `event-attribute`: a configured attribute key must contain exactly one canonical Linear issue identifier.
- Persist the resolved issue identifier in the invocation at match time. Preserve a normalized Linear issue UUID when the source provides it for audit identity, but keep the canonical identifier as the existing routing input.
- Resolve current project metadata after durable admission and immediately before moving the invocation to queued. Pin the resulting allowlisted route into the invocation and Run. A transient Linear failure retries; a missing issue/project or non-allowlisted route rejects only that invocation.
- ENG-40 remains issue-centric. Non-Linear-issue workflows and configurable repositories, paths, branches, commands, providers, merge behavior, or deployment behavior are non-goals.

### 6. Cron as an independent event producer

Observed facts:

- Factory has no cron parser, schedule registry, or durable schedule cursor. Existing timers only drive reconciliation and service telemetry (`main.go:476-517`).
- The event-wire ready callback is the safe scheduler start boundary because handlers and pending records are recovered before manager work begins (`main.go:415-429`).
- The prior research verified `github.com/robfig/cron/v3` v3.0.1 as an available parser whose explicit parser can support standard minute-first fields and timezone-aware `Next` calculation.

Proposed schedule contract:

- Store schedules separately from rules in the registry snapshot. A schedule has stable ID, display name, enabled flag, five-field expression, IANA timezone, optional subject, and bounded context attributes. It contains no workflow or rule reference.
- Emit `source=factory`, `type=cron`, `action=due`, `subject=<configured subject or schedule ID>`, with reserved `scheduleId`, `scheduleRevision`, and `scheduledAt` attributes plus non-conflicting configured context.
- An ordinary rule matches that event by source/type/action/schedule ID and selects the workflow and target policy. Scheduling and routing remain independent.
- Use a separate private schema-1 cursor store keyed by schedule ID and material revision/fingerprint. A new, re-enabled, or materially edited schedule begins strictly after the edit time.
- Event IDs include schedule ID, material revision, and scheduled UTC instant. Advance the cursor only after successful durable wire publication.
- After downtime, publish at most the oldest missed occurrence once, record the skipped count, and then advance to the first future instant. Do not perform an unbounded backfill.
- Accept exactly standard five-field minute/hour/day-of-month/month/day-of-week syntax plus a separate IANA timezone. Reject seconds, descriptors, `@every`, embedded timezone directives, and arbitrary durations.
- A schedule does not require an issue identifier. Target context may carry one for an extracting rule, or the matching rule may use a fixed issue.

### 7. Persistence, workflow compatibility, and rollback

Observed facts:

- Current `settings.json` is strict schema 1. The deployed reader rejects unknown fields and other schema values, so adding the registry inside settings would make prior-binary rollback fail at startup (`internal/settings/store.go:25-54`, `internal/settings/store.go:83-93`).
- Existing stores use private directories, `0600` files, fsync, copied snapshots, and atomic same-directory replacement (`internal/settings/store.go:19-80`, `internal/settings/store.go:96-123`, `internal/agentrun/store.go:761-799`).
- Workflows have stable IDs but no version, and `agent-exec` currently reloads current settings and maps only trigger kind to a workflow (`internal/settings/settings.go:136-155`, `agent_commands.go:42-73`).
- The launcher passes only `TriggerKind`; lifecycle resumes replace that kind with `github-update` or `post-merge` (`internal/agentrun/launcher.go:488-534`, `internal/agentrun/store.go:438-463`).
- Journal open validates every retained event, including acknowledged records, and compaction retains the newest configured window. A retained event with a newly opened source token therefore prevents the prior binary from starting even when the pending count is zero (`internal/eventwire/journal.go:391-410`, `internal/eventwire/journal.go:465-539`).
- Perfect permanent idempotency for infinitely many unique events cannot consume finite storage. The product must choose exact tombstones with a hard admission ceiling rather than silent expiry or probabilistic duplicate suppression.

Proposed persistence and execution safeguards:

- Add independent private schema-1 registry and cursor stores plus the segmented invocation ledger. Keep legacy `settings.json` fields and schema unchanged for prior-binary readability.
- On an absent registry only, seed generic rules equivalent to the current label and comment settings using normalized actor/provenance/added-label attributes and the configured workflows. Opening defaults does not write a file.
- Remove legacy trigger editing from `/settings`, but continue round-tripping those values unchanged and reserving their workflow references for prior-binary validation.
- Materialize the invocation's immutable workflow snapshot into the private Run directory and pass its path explicitly to `agent-exec`. Existing Runs without invocation identity keep the current trigger-kind fallback.
- Lifecycle prompt kind may change for feedback, GitHub remediation, or post-merge, but the original invocation workflow snapshot never changes.
- A registry candidate may reference only an existing enabled workflow. Admitted invocations do not require the live workflow to remain enabled because their complete workflow is pinned.
- Ledger recovery accepts only an incomplete final frame as truncatable. Complete-frame checksum failure, duplicate identity, illegal transition, nonmonotonic sequence, or index disagreement fails closed. It keeps complete state for nonterminal invocations, compacts terminal invocations and completed or empty decisions to minimal exact tombstones, and never silently expires those tombstones under the permanent replay contract.
- Use 16 MiB ledger segments, a 512 MiB committed-ledger ceiling, and a separate 32 MiB compaction reserve that new admission cannot consume, for a 544 MiB application-enforced directory bound. Add a 256 MiB wire-byte ceiling and 1,000-pending-record ceiling alongside the existing retained-record limit. Global outstanding invocations are capped at 1,000, and each rule has its queue/rate controls. Compact one segment at a time at high water. A wire ceiling rejects publication before adding another record. A ledger ceiling appends nothing, leaves the already-journaled undecided record pending, and causes ordered catch-up to reject later publication before further journal growth. Either opens a visible fail-closed circuit, stops cron cursor advancement, and blocks registry/workflow mutation. Terminal reflection, audited compaction, and capacity repair remain available; coordinated catch-up resumes after capacity is restored.
- Every file replacement, including the existing Run store path touched by the claim saga, must fsync the temporary file, rename, and fsync the parent directory. Ledger segment rotation and compaction use the same durability boundary with a manifest that points only to complete segments.
- Prior-binary rollback is allowed only after Factory is stopped and an exclusive offline preflight proves: every store is strictly readable; global and channel wire acknowledgments equal their totals; no admitted, queued, claiming, or claimed invocation remains; no nonterminal Run has registry/invocation identity; and legacy settings validate with enabled legacy workflow references.
- The preflight hashes all locked state and runs the exact target binary's journal reader against a byte-identical snapshot. Every retained event must pass that target's source validator. A zero pending count is never sufficient.
- If only fully acknowledged incompatible-source records prevent rollback, a separately authorized current-binary preparation may archive and hash the original journal, drop only globally and channel-acknowledged records, preserve all counters and receipts, and rerun the target-reader probe. Pending incompatible records or any other failed condition require forward correction. Deployment rechecks the locked hashes before consuming the single-use rollback receipt.

### 8. Authenticated API and `/triggers` UI

Observed facts:

- Protected pages and APIs already use separate `ViewerAuth.Page` and `ViewerAuth.API` wrappers. Canonical routing rejects trailing slashes and cleaned-path aliases (`internal/server/server.go:305-328`, `internal/server/server.go:902-921`).
- Settings PUT provides the repository's proven browser mutation contract: same-origin credentials, `application/json`, strict bounded decoding, optimistic revision, authoritative conflict response, and no mutation on failure (`internal/server/server.go:466-526`, `frontend/src/index.tsx:284-323`).
- The Solid UI already has shared navigation and accessible loading, error, dirty, saving, success, conflict, and failure patterns (`frontend/src/index.tsx:374-427`, `frontend/src/index.tsx:886-1254`).
- Frozen built assets are required at service startup and are not committed (`.gitignore:1-3`, `main.go:75-78`).

Proposed surface:

- Add canonical authenticated `GET /triggers`, `GET /api/triggers`, and whole-snapshot `PUT /api/triggers`.
- Return rules, schedules, enabled workflow choices, observed-source suggestions, schedule last/next due data, and synthesized read-only system routes. Do not return secrets, raw payloads, repository paths, commands, or private workflow execution files.
- Add Triggers between Agents and Settings in shared navigation.
- The page has separate routing-rule, schedule, and protected-system-route sections. Rules provide structured optional source/type/action/subject fields, attribute rows, workflow selection, target-policy controls, enable/disable, edit, and confirmed delete. Schedules provide cron/timezone/subject/context controls without a workflow selector.
- Show protected contextual feedback, GitHub remediation, and post-merge routes as read-only. Configured matches against those events remain additional invocations.
- Show admission eligibility, causal depth, queue/rate controls, outstanding counts, suppression reasons, and circuit status. Restricted actor/provenance or derived-event overrides require explicit selectors, an audit reason, and a second confirmation; the UI cannot offer an override for authentication, repository, protected lifecycle, or Factory-authored-comment boundaries.
- Use free-text source entry with observed suggestions, not a closed select. Keep structured exact-match data and do not add scripts, expressions, regexes, arbitrary JSON predicates, or executable fields.
- Preserve explicit loading, empty, validation, save, conflict, and network failure states; keyboard operation; light/dark modes; and desktop, 720-pixel, and 320-pixel layouts.

### 9. Observable acceptance, verification, and deployment

ENG-40 is complete when:

1. An authenticated operator can open `/triggers` and inspect generic editable rules, independent cron schedules, and protected system routes.
2. The operator can add, modify, enable/disable, and delete rules with optional exact filters, workflow selection, and fixed or extracted issue targets.
3. Current and future normalized source tokens are valid without weakening immutable adapter-assigned eligibility, source authenticity, actor/provenance controls, or adding a generic publish endpoint.
4. Every eligible enabled match produces one durable invocation keyed by event/rule/revision, including multiple matches for one event and durable rejected or suppressed matches for invalid targets, causal guards, and queue/rate limits.
5. Each invocation runs the pinned workflow snapshot selected at admission even after workflow or rule edits.
6. Multiple invocations for one issue serialize through distinct Runs; different issues may fan out concurrently; no distinct workflow intent is recorded as `DuplicateTriggers` or discarded.
7. Contextual feedback, GitHub remediation, human merge, exact verified-head deployment, and cleanup remain system-owned lifecycle behavior on the original Run.
8. Cron schedules emit deterministic `factory/cron/due` events with context; ordinary rules route them; cursor recovery is timezone-aware, restart-safe, and bounded to one catch-up.
9. Match-all, self-cycle, multi-rule cycle, heartbeat, agent-record, and agent-run cases remain bounded by immutable eligibility, stable causal paths, hard hop limits, and durable per-rule/global admission ceilings across restart and rule revision.
10. Registry/settings edits cannot overtake an undecided pending wire record. Startup reconciliation converges every interrupted claim and terminal-reflection boundary before workers start.
11. Ledger and wire disk ceilings trip a visible fail-closed breaker without silently pruning exact tombstones or advancing cron, and recovery cannot create a duplicate invocation or Run.
12. Prior-binary rollback validates every retained journal record with the exact target reader and fails closed while incompatible records, active invocations, or registry-era Runs could be lost or mis-executed.
13. Unauthenticated, cross-origin, malformed, oversized, invalid, stale, and unroutable mutations fail without changing the last good stores.

Verification will include focused matcher, admission classification, causation, registry, ledger, scheduler, event-wire, run-store, manager, launcher, settings, server, auth, and target-reader rollback tests; self-cycle, A-to-B-to-A, hard-hop, heartbeat/agent-event default denial, restart-stable rate/queue limits, multi-match, and same-issue serialization integration tests; fault injection before and after event append, decision append, claim intent, Run replacement, claim confirmation, terminal Run write, invocation reflection, Run receipt, and collector acknowledgment; the complete Go test/race/vet suites; frozen Bun install/typecheck/build; authenticated desktop/mobile browser flows; and exact-commit post-merge deployment probes.

After human merge, deploy only the clean updated primary checkout with:

```text
bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
```

Verify local and public health identity, launchd state, Factory tmux-session survival, authenticated registry readback, private file modes, schedule/invocation persistence, deployment receipt identity, GitHub auto-deletion, and Worktrunk cleanup. Recovery is a forward corrective or revert commit on current `main`; prior-binary activation additionally requires the quiesced offline preflight above.

## Alternatives considered

- **Closed label/comment/cron kinds:** rejected by the clarified product contract because future event vocabulary would require schema redesign.
- **One dynamic wire route per rule:** rejected because CRUD order and partial multi-route retries would not provide one atomic admission decision.
- **Store only trigger and workflow IDs on Run:** rejected because both definitions are mutable between admission and launch.
- **Create one Run directly per match:** rejected because same-issue Runs would collide with the issue-owned tmux, PR, merge, deployment, and cleanup lifecycle.
- **Keep current active-Run coalescing for generic rules:** rejected because it silently discards distinct workflow intent.
- **Treat serialization as recursion control:** rejected because queued workflow-derived and telemetry events can grow without bound even when only one Run executes at a time.
- **Let ordinary attributes override actor/provenance admission:** rejected because a match-all rule would bypass current Linear and protected-lifecycle authority boundaries.
- **Two file replacements under one mutex:** rejected because a process crash can still leave the invocation and Run stores at different durable points. Deterministic identities and startup reconciliation are required.
- **One growing `trigger-invocations.json`:** rejected because permanent decisions and high-volume telemetry produce unbounded rewrite amplification and memory usage.
- **Expire or probabilistically summarize idempotency tombstones:** rejected under the current permanent replay contract because it can rerun an old invocation or suppress a legitimate one. Exact tombstones instead stop at a hard capacity ceiling.
- **Cron definitions that directly select workflows:** rejected because scheduling and routing would remain coupled and future producers would need special admission paths.
- **Arbitrary expressions, regexes, scripts, or commands:** rejected because exact declarative matching already satisfies the requested generality without adding code execution.
- **Change settings schema in place:** rejected because the current release would fail to start after rollback.
- **Permit prior-binary rollback with selected pending-event exceptions:** rejected because the old binary cannot interpret generic decisions, workflow snapshots, or invocation queues safely.

## Security, data, compatibility, rollout, and rollback risks

- All event producers remain authenticated internal adapters. Opening the source token and rule vocabulary never creates an unauthenticated ingestion endpoint.
- Rule and schedule data is bounded declarative text. No value is evaluated by a shell or used to choose a repository, filesystem path, executable, secret, merge action, or deployment command.
- Every runnable invocation resolves a Linear issue through current allowlisted project metadata before its repository route is pinned.
- Durable decision receipts plus the shared coordinator prevent policy edits from changing pending admission semantics.
- Workflow snapshots preserve admitted behavior, while protected lifecycle code keeps human-only merge and exact-head deployment outside registry authority.
- Match-all rules cannot widen immutable eligibility. Derived-event overrides remain bounded by a stable causal rule path, hard hop ceiling, and durable queue/rate controls; adapter authentication and Factory-authored-comment restrictions are never overridable.
- Exact tombstones remain permanent in schema 1 but are compacted to minimal indexed records. The 512 MiB committed-ledger ceiling plus 32 MiB compaction reserve makes storage finite by failing closed before correctness would require eviction. Raising capacity or changing replay retention requires an explicit audited migration.
- The claim saga permits temporary durable disagreement between ledger and Run projection at documented crash points. Deterministic IDs, manager start checks, terminal reflection receipts, and startup reconciliation make every permitted disagreement convergent; any other pairing is corruption and blocks readiness.
- Additive optional Run fields preserve existing records. Legacy Runs continue using current trigger-kind workflow fallback.
- Scheduler startup waits for wire catch-up. Deployment and prior-binary rollback follow the exact-main and fail-closed procedures above.

## External and delegated evidence

- The existing generic filter and wire, run coalescing, settings, lifecycle, UI, and rollback boundaries were independently traced by two read-only Codex tmux research children after both first-choice Claude children failed operationally on account rate limits:
  - `/Users/tom/.local/share/factory/runs/run-fa164a89d91c0fd6/children/generic-rules-research-codex-47c57fd0/`
  - `/Users/tom/.local/share/factory/runs/run-fa164a89d91c0fd6/children/invocation-research-codex-5b899fe9/`
- Their useful conclusions were reconciled rather than copied blindly. In particular, the final design rejects preserving active-Run coalescing for generic rules and uses a separate serialized invocation queue.
- Three additional read-only Codex tmux research children independently traced the revision-3 recursion/admission, coordinator/claim, and ledger/rollback blockers. Their useful findings are incorporated into the immutable eligibility contract, recoverable claim saga, bounded append ledger, fault-injection matrix, and exact target-reader rollback preflight:
  - `/Users/tom/.local/share/factory/runs/run-fa164a89d91c0fd6/children/routing-safety-r3-b813025b/`
  - `/Users/tom/.local/share/factory/runs/run-fa164a89d91c0fd6/children/durability-r3-1ed9ed1b/`
  - `/Users/tom/.local/share/factory/runs/run-fa164a89d91c0fd6/children/ledger-rollback-r3-54d6beb2/`
- Cron parser availability and explicit five-field/timezone behavior remain based on the upstream [robfig/cron v3](https://github.com/robfig/cron) documentation and local module discovery for v3.0.1.

## Assumptions proposed for approval

- All matching rules execute; there is no first-match or priority mode.
- Exact AND filters are sufficient. A nullable subject distinguishes wildcard from exact absence. Empty filters are allowed as explicit match-all rules, but immutable eligibility still applies.
- Adapter authentication, Factory-authored Linear-comment exclusion, repository allowlisting, and protected lifecycle behavior are never overridable. Authenticated operators may explicitly widen only enumerated restricted actor/provenance classes with exact selectors, an audit reason, and confirmation; workflow-derived events also require `allowDerived`.
- Stable rule IDs may execute only once per causal root. Default maximum hop is one and the hard maximum is three. Per-rule outstanding and hourly admission limits default to 10 and are configurable from 1 through 100; global outstanding is capped at 1,000.
- Factory remains issue-centric. Target policies are fixed issue, event subject, or one event attribute; workflows without a Linear issue are outside ENG-40.
- Eligible contextual Linear feedback keeps its system-owned resume behavior. A configured generic rule matching the same comment intentionally creates an additional serialized invocation.
- Only the workflow value is pinned. Provider/model settings continue to apply at lifecycle segment boundaries.
- Cron uses five fields, a separate IANA timezone, and one oldest missed occurrence after downtime. Schedules do not require an issue because routing rules own target resolution.
- Exact decision and invocation tombstones remain durable and unexpired in schema 1, compact to minimal records, and fail closed at a 512 MiB committed-ledger ceiling with a separate 32 MiB compaction reserve rather than silently weakening idempotency. The wire adds a 256 MiB byte ceiling and 1,000 pending-record ceiling.
- Prior-binary rollback may proceed only with an exclusive, hash-bound receipt proving the exact target reader accepts every retained journal event. Fully acknowledged incompatible records require an explicit current-binary archive/compaction preparation first.

## Unresolved questions

The assumptions above require owner approval at the revised research gate. No additional repository-discoverable question blocks that decision.
