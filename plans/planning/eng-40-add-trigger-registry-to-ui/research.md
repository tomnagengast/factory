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

Proposed matching contract:

- Keep `eventwire.Source` as a string type, but validate it as a bounded lowercase token rather than an enum. Constants for `linear`, `github`, and `factory` remain conveniences.
- Match source, type, action, subject, attribute keys, and values byte-for-byte and case-sensitively after source adapters normalize them. The matcher performs no trimming or case folding.
- Missing source/type/action/subject fields are wildcards. Present empty source/type/action values are invalid. A present empty subject means an exact subjectless match. Attribute filters use current membership semantics, including exact empty-string values.
- Every enabled matching rule fires. Evaluate and persist matches in stable rule-ID order; do not use rule list order as hidden priority.
- An empty filter is an explicit match-all rule. The API accepts it, while the UI gives a prominent broad-rule warning and requires confirmation before save.
- Opening source vocabulary does not open ingestion. Current and future source adapters must still authenticate and normalize their inputs before publishing. Source-specific facts such as Linear actor ID, Factory provenance, added label names, issue ID, and issue identifier become bounded normalized attributes that rules can match.
- Keep raw payloads, comment bodies, secrets, commands, repository paths, and credentials out of rule filters and API responses.

### 3. Durable routing decisions before mutable policy can advance

Observed facts:

- `Wire.Publish` fsyncs the event before dispatch. If any handler returns a transient error, that record remains pending and ordered catch-up stops (`internal/eventwire/wire.go:49-72`, `internal/eventwire/wire.go:103-142`).
- HTTP starts before `recoverEventWire` completes, so settings mutation is possible while an earlier record is pending (`main.go:406-429`).
- Event IDs are deduplicated only inside the retained journal window. Durable invocation idempotency cannot depend on wire retention alone (`internal/eventwire/journal.go:144-225`, `internal/eventwire/journal.go:391-410`).
- Registering one dynamic wire route per rule would make CRUD ordering and partial multi-route failure unsafe. One broad admission route can evaluate one immutable registry snapshot and atomically persist the full outcome.

Proposed durability boundary:

- Add a shared policy coordinator around trusted event publication, generic admission routing, registry mutation, and workflow mutation.
- Register one broad generic admission handler before the existing Linear and GitHub system handlers.
- Every trusted producer publishes through the coordinated wire wrapper. The wrapper holds the policy coordinator until the new record's generic admission decision is durable or publication fails.
- The admission handler evaluates all enabled rules against one registry and settings snapshot, then atomically fsyncs one decision containing the wire event ID and sequence plus all matching invocations. Persist an empty decision when no rule matches.
- Replays reuse the existing decision and never re-evaluate the event against a later registry revision.
- Registry/workflow mutations check every pending wire record under the same coordinator. If any pending record lacks a durable admission decision, reject the mutation until catch-up records that decision. This closes the prior review's pending-wire race without freezing policy for records whose decisions are already pinned.
- Source-specific handlers remain later routes. If a protected Linear or GitHub handler fails after admission, the wire record stays pending, but its generic decision and workflow snapshots are already durable and idempotent.

### 4. Durable invocation ledger, multi-match fan-out, and serialization

Observed facts:

- `agentrun.Store.claim` deduplicates by delivery ID and then coalesces every active admission for the same issue into the existing Run, incrementing `DuplicateTriggers`. It discards the incoming trigger's distinct intent (`internal/agentrun/store.go:203-255`).
- The manager assumes at most one active Run per issue. It uses an issue-derived tmux session name, and `awaiting_human_merge` remains nonterminal across GitHub remediation and post-merge continuation (`internal/agentrun/manager.go:155-198`, `internal/agentrun/manager.go:460-488`, `internal/agentrun/store.go:438-463`).
- Allowing multiple same-issue Runs to start directly would collide on session identity and could create competing PR, merge, deployment, and cleanup lifecycles.

Proposed invocation model:

- Add a private schema-1 `trigger-invocations.json` store. A routing decision persists zero or more immutable invocations plus permanent idempotency tombstones.
- Compute the idempotency key from `(eventID, ruleID, ruleRevision)`, domain-separated and hashed. Do not use the shared delivery ID or bounded Run/wire retention as authority.
- Each invocation snapshots the event ID/sequence, stable rule ID and per-rule revision, complete matched filter and target policy, complete validated workflow value, settings revision and digest, resolved issue identity, state, timestamps, retry detail, repository route when resolved, and linked Run ID when claimed.
- Store the complete `settings.Workflow` value, not only `WorkflowID`. A later workflow edit, disable, or delete therefore cannot change admitted executable steps. Provider/model settings remain safe-boundary settings read for each lifecycle segment; ENG-40 pins the workflow, not the entire agent configuration.
- Invocation states are `admitted`, `queued`, `claimed`, `succeeded`, `blocked`, `failed`, and `rejected`. Target syntax failures become durable rejected matches. Transient repository resolution leaves an admitted invocation retryable instead of blocking the entire event wire.
- Promote invocations deterministically by `(wire sequence, rule ID)`. Different issue targets can use normal global concurrency. For one issue, only the oldest invocation may own a nonterminal Run.
- Add a separate `ClaimInvocation` path. If the issue already has any nonterminal Run, leave the invocation queued without modifying that Run. Otherwise create one Run keyed to the invocation and persist the invocation ID and pinned identities.
- Multiple rules matching one event always produce distinct invocations, even when workflow and issue are equal. They serialize per issue rather than increasing `DuplicateTriggers` or silently coalescing.
- Protected contextual Linear feedback, GitHub remediation, and post-merge continue to resume the existing lifecycle Run. A configured rule that also matches the same event creates an additional queued invocation by design; it cannot replace the system-owned resume.
- Terminal Run state is reflected back to the invocation before the next same-issue invocation is promoted.

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

Proposed persistence and execution safeguards:

- Add independent private schema-1 registry, cursor, and invocation stores. Keep legacy `settings.json` fields and schema unchanged for prior-binary readability.
- On an absent registry only, seed generic rules equivalent to the current label and comment settings using normalized actor/provenance/added-label attributes and the configured workflows. Opening defaults does not write a file.
- Remove legacy trigger editing from `/settings`, but continue round-tripping those values unchanged and reserving their workflow references for prior-binary validation.
- Materialize the invocation's immutable workflow snapshot into the private Run directory and pass its path explicitly to `agent-exec`. Existing Runs without invocation identity keep the current trigger-kind fallback.
- Lifecycle prompt kind may change for feedback, GitHub remediation, or post-merge, but the original invocation workflow snapshot never changes.
- A registry candidate may reference only an existing enabled workflow. Admitted invocations do not require the live workflow to remain enabled because their complete workflow is pinned.
- Prior-binary rollback is allowed only after Factory is quiesced and the current binary's offline preflight proves: all stores are readable; no admitted, queued, or claimed invocation remains; no nonterminal Run has registry/invocation identity; the event wire has zero pending records; and legacy settings still validate with enabled legacy workflow references.
- Any failed rollback preflight requires forward correction. The previous binary must never be started merely because the new files are syntactically ignorable.

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
- Use free-text source entry with observed suggestions, not a closed select. Keep structured exact-match data and do not add scripts, expressions, regexes, arbitrary JSON predicates, or executable fields.
- Preserve explicit loading, empty, validation, save, conflict, and network failure states; keyboard operation; light/dark modes; and desktop, 720-pixel, and 320-pixel layouts.

### 9. Observable acceptance, verification, and deployment

ENG-40 is complete when:

1. An authenticated operator can open `/triggers` and inspect generic editable rules, independent cron schedules, and protected system routes.
2. The operator can add, modify, enable/disable, and delete rules with optional exact filters, workflow selection, and fixed or extracted issue targets.
3. Current and future normalized source tokens are valid without weakening source-adapter authenticity or adding a generic publish endpoint.
4. Every enabled matching rule creates one durable invocation keyed by event/rule/revision, including multiple matches for one event and durable rejected matches for invalid extracted targets.
5. Each invocation runs the pinned workflow snapshot selected at admission even after workflow or rule edits.
6. Multiple invocations for one issue serialize through distinct Runs; different issues may fan out concurrently; no distinct workflow intent is recorded as `DuplicateTriggers` or discarded.
7. Contextual feedback, GitHub remediation, human merge, exact verified-head deployment, and cleanup remain system-owned lifecycle behavior on the original Run.
8. Cron schedules emit deterministic `factory/cron/due` events with context; ordinary rules route them; cursor recovery is timezone-aware, restart-safe, and bounded to one catch-up.
9. Registry/settings edits cannot overtake an undecided pending wire record. Prior-binary rollback fails closed while any pending wire record, invocation, or registry-era Run could be lost or mis-executed.
10. Unauthenticated, cross-origin, malformed, oversized, invalid, stale, and unroutable mutations fail without changing the last good stores.

Verification will include focused matcher, registry, invocation, scheduler, event-wire, run-store, manager, launcher, settings, server, auth, and rollback tests; multi-match and same-issue serialization integration tests; the complete Go test/race/vet suites; frozen Bun install/typecheck/build; authenticated desktop/mobile browser flows; and exact-commit post-merge deployment probes.

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
- Match-all rules and high-frequency telemetry matches can create large queues. The UI warns before saving a broad rule, and per-issue serialization plus global concurrency prevents overlapping execution, but the operator remains responsible for intentional fan-out.
- Invocation idempotency tombstones are not pruned in schema 1. This favors correctness over bounded metadata growth; a future compaction schema must preserve replay identity explicitly.
- Additive optional Run fields preserve existing records. Legacy Runs continue using current trigger-kind workflow fallback.
- Scheduler startup waits for wire catch-up. Deployment and prior-binary rollback follow the exact-main and fail-closed procedures above.

## External and delegated evidence

- The existing generic filter and wire, run coalescing, settings, lifecycle, UI, and rollback boundaries were independently traced by two read-only Codex tmux research children after both first-choice Claude children failed operationally on account rate limits:
  - `/Users/tom/.local/share/factory/runs/run-fa164a89d91c0fd6/children/generic-rules-research-codex-47c57fd0/`
  - `/Users/tom/.local/share/factory/runs/run-fa164a89d91c0fd6/children/invocation-research-codex-5b899fe9/`
- Their useful conclusions were reconciled rather than copied blindly. In particular, the final design rejects preserving active-Run coalescing for generic rules and uses a separate serialized invocation queue.
- Cron parser availability and explicit five-field/timezone behavior remain based on the upstream [robfig/cron v3](https://github.com/robfig/cron) documentation and local module discovery for v3.0.1.

## Assumptions proposed for approval

- All matching rules execute; there is no first-match or priority mode.
- Exact AND filters are sufficient. Empty filters are allowed as explicit match-all rules with a UI warning.
- Factory remains issue-centric. Target policies are fixed issue, event subject, or one event attribute; workflows without a Linear issue are outside ENG-40.
- Eligible contextual Linear feedback keeps its system-owned resume behavior. A configured generic rule matching the same comment intentionally creates an additional serialized invocation.
- Only the workflow value is pinned. Provider/model settings continue to apply at lifecycle segment boundaries.
- Cron uses five fields, a separate IANA timezone, and one oldest missed occurrence after downtime. Schedules do not require an issue because routing rules own target resolution.
- Invocation idempotency tombstones remain durable and unpruned in schema 1.

## Unresolved questions

The assumptions above require owner approval at the revised research gate. No additional repository-discoverable question blocks that decision.
