# ENG-40 Research: Add trigger registry to UI

Linear: https://linear.app/nags-cloud/issue/ENG-40/add-trigger-registry-to-ui

## Research questions

1. What Factory behaviors are triggers today, and which of them may safely be edited?
2. What runtime path turns an external event into a repository-routed agent run?
3. What must a cron trigger target, and what scheduling semantics fit the existing lifecycle?
4. Where should trigger definitions and schedule cursors persist without breaking rollback?
5. How should the `/triggers` page and API fit existing authentication, routing, mutation, and UI conventions?
6. Which acceptance criteria are observable, and how will each be verified?
7. What data, security, compatibility, rollout, rollback, and deployment risks must the plan address?

## Evidence-backed answers

### 1. Current trigger inventory and authority boundaries

Observed facts:

- The only operator-editable run admission paths are a newly applied Linear label and an eligible Linear human comment. They are two fixed fields in settings schema 1, not a registry (`internal/settings/settings.go:31-55`, `internal/settings/settings.go:85-123`).
- A Linear label starts a run only after HMAC verification, a one-minute replay check, delivery validation, actor allowlisting, current-label inspection, and proof that the matching label was absent before the update (`internal/server/server.go:528-575`, `internal/server/server.go:829-859`).
- A Linear comment is recorded as a body-free wake only for the configured actor and only when its final line is not Factory provenance. Disabling comment admission does not disable the journal. A comment can create a continuation only after retained Factory history exists (`internal/server/server.go:578-602`, `internal/server/server.go:714-732`, `internal/agentrun/store.go:203-278`).
- Signed GitHub events schedule reconciliation only for already parked runs. Authoritative open-PR changes resume remediation as `github-update`; merged or closed PRs resume the same run as `post-merge`. These are protected lifecycle transitions, not independent run-admission triggers (`internal/server/server.go:604-637`, `internal/server/server.go:770-802`, `internal/agentrun/manager.go:399-448`).
- Project create/update events drive repository onboarding, while service start/heartbeat/stop events are telemetry. Neither is an operator-selected `$do` workflow admission (`internal/server/server.go:560-565`, `internal/server/server.go:695-712`, `main.go:476-516`).
- There is no cron expression model, parser, scheduler, durable schedule cursor, or cron run path. The only current `Cron*` strings deny Claude child scheduling tools (`internal/agentrun/execute.go:19-21`).
- ENG-32 deliberately made only Linear label and comment admission editable while keeping GitHub remediation and post-merge continuation mandatory (`plans/planning/eng-32-add-settings-page/research.md:32-45`).

Decision:

- ENG-40 will turn the two existing admission behaviors into a typed editable registry and add a typed cron admission behavior.
- The initial editable event vocabulary is closed: at most one `linear-label` definition and at most one `linear-comment` definition. Operators may add, modify, disable, or delete either definition; signed events continue to enter the wire and private journals when admission is absent or disabled.
- Multiple cron definitions are allowed. Arbitrary source/type/action expressions, webhook registration, commands, scripts, repository paths, secrets, and lifecycle-transition edits are not part of this issue.
- `/triggers` will also show synthesized read-only entries for GitHub remediation and authoritative merge/post-merge continuation so the page explains all agent lifecycle trigger kinds without making safeguards deletable. Project onboarding and service heartbeat telemetry are not workflow triggers and remain outside this registry.

### 2. Existing event-to-run path

Observed facts:

- Every issue run requires a valid Linear issue identifier. `agentrun.Store.Claim` rejects anything else, deduplicates delivery IDs, and coalesces new admissions into the one active run for the issue (`internal/agentrun/store.go:53-64`, `internal/agentrun/store.go:203-278`).
- Before claim, `RepositoryResolver.Resolve` reads the issue's Linear project metadata and matches it against the allowlisted repository catalog. The trigger cannot provide a repository or path directly (`internal/server/server.go:751-768`, `internal/agentrun/repository.go:130-183`, `internal/agentrun/repository.go:193-247`).
- Linear and GitHub input is normalized into the ordered, fsynced event wire before downstream effects. Duplicate event IDs are stable, transient dispatch errors remain pending, and typed permanent routing failures are rejected and acknowledged so later records can proceed (`internal/eventwire/wire.go:49-72`, `internal/eventwire/wire.go:103-142`, `internal/eventwire/journal.go:144-176`).
- The claimed run persists its trigger kind and repository route. The manager starts a repository-specific tmux principal and passes the trigger kind into `agent-exec` (`internal/agentrun/store.go:78-108`, `internal/agentrun/manager.go:141-194`, `internal/agentrun/launcher.go:488-534`).
- `agent-exec` reloads settings and currently maps every non-comment trigger kind to the label workflow. Trigger kind alone cannot select among multiple registry entries (`agent_commands.go:42-73`, `internal/settings/settings.go:146-155`).

Decision:

- Each registry admission will carry a stable trigger ID and selected workflow ID into the normalized wire decision, `agentrun.Trigger`, and durable `Run`.
- New additive run fields preserve the workflow selected at admission across queueing and later lifecycle segments. Existing records without those fields retain the current safe workflow fallback.
- Cron due occurrences will publish deterministic Factory wire events and use the same repository resolver, run store, coalescing rule, manager notifier, and lifecycle contract. The scheduler will never launch a provider directly.

### 3. Cron target and scheduling semantics

Observed facts:

- The current lifecycle is issue-centric from session naming through Linear gates, PR correlation, completion evidence, and cleanup. A schedule with only a workflow or repository cannot create a valid run without inventing a new authority model (`internal/agentrun/store.go:203-278`, `internal/agentrun/manager.go:488-530`, `internal/agentrun/completion.go:341-438`).
- The Go module currently has no dependencies. `go list -m -versions github.com/robfig/cron/v3` reports `v3.0.0-rc1`, `v3.0.0`, and `v3.0.1`; `go mod download -json github.com/robfig/cron/v3@v3.0.1` verifies the module and checksum without changing this repository.
- The upstream v3 documentation states that its configurable parser supports standard minute-first cron fields, timezone-aware schedules, and schedule `Next(time.Time)` calculation. Source: [robfig/cron v3](https://github.com/robfig/cron).

Decision:

- A cron definition requires an existing Linear issue identifier. The authenticated registry write preflights that issue through the current repository resolver; every due occurrence resolves it again so stale or changed project metadata cannot bypass allowlisting.
- The accepted dialect is exactly five fields: minute, hour, day of month, month, and day of week. Seconds, `@every`, arbitrary durations, and embedded timezone prefixes are rejected.
- Timezone is a separate required IANA location, defaulted to `UTC` for a new UI entry and validated with `time.LoadLocation`. The scheduler library handles calendar and DST transitions.
- The scheduler stores a configuration fingerprint and `nextFireAt` per cron trigger in a private versioned cursor file. New or materially edited triggers schedule their first occurrence strictly after the edit time instead of firing immediately.
- After downtime, at most one overdue occurrence is published per trigger, then the cursor advances to the first future occurrence. This avoids an unbounded backfill storm. If the target issue already has an active run, the existing claim path records a duplicate/coalesced admission rather than launching an overlapping session.
- The due event ID includes trigger ID and scheduled UTC instant. Cursor advancement happens only after successful wire publication. Retrying the same occurrence therefore reuses the deterministic event ID.

### 4. Persistence and rollback compatibility

Observed facts:

- Production currently has a private persisted `~/.local/share/factory/data/settings.json` at schema 1 and revision 1. The existing decoder rejects unknown fields and any schema other than 1 (`internal/settings/store.go:25-54`, `internal/settings/store.go:83-93`).
- Replacing the trigger object inside settings and bumping its schema would make the current release unable to start after a deployment rollback.
- Repository stores use mutex-protected copy-on-write snapshots, versioned JSON, `0600` files, file fsync, and atomic same-directory rename (`internal/settings/store.go:19-80`, `internal/settings/store.go:96-123`, `internal/agentrun/store.go:761-799`).

Decision:

- Add a dedicated `internal/triggerregistry` domain with `~/.local/share/factory/data/triggers.json` schema 1 and an independent optimistic revision. The new release treats it as the only editable admission authority.
- On first start when `triggers.json` is absent, seed the in-memory registry from the current schema-1 settings label/comment values. The first successful registry update persists the new file privately and atomically.
- Keep the legacy trigger fields in `settings.json` unchanged for rollback compatibility, but remove their editor from `/settings` and stop consulting them for new admissions in the new release. The old release can still start and safely falls back to its last legacy label/comment policy if an operational rollback is required.
- Add `trigger-schedules.json` for scheduler cursor state using the same private atomic store convention. This state contains only trigger IDs, fingerprints, and schedule instants.
- Trigger definitions reference workflows stored in settings. Server-side writes for both stores share a coordinator lock and cross-validate enabled workflow references to prevent a trigger update racing a workflow disable/delete.

### 5. API, authentication, routing, and UI integration

Observed facts:

- Protected pages and APIs use separate `ViewerAuth.Page` and `ViewerAuth.API` wrappers. OAuth return destinations are separately allowlisted, and canonical routing deliberately returns 404 for trailing slashes and cleaned paths (`internal/server/server.go:305-328`, `internal/viewerauth/auth.go:408-432`, `internal/server/server.go:902-921`).
- The only current browser mutation pattern is whole-snapshot settings `PUT`: same-origin credentials, `application/json`, bounded strict decoding, optimistic revision conflicts, and authoritative response replacement (`frontend/src/index.tsx:284-323`, `internal/server/server.go:470-526`).
- The Solid frontend uses explicit pathname dispatch and a shared header. It already has accessible loading, error, empty, dirty, saving, saved, conflict, and failure patterns (`frontend/src/index.tsx:374-427`, `frontend/src/index.tsx:886-1000`, `frontend/src/index.tsx:1801-1833`).
- Built frontend assets under `frontend/dist` are ignored and must not be committed. Production refuses startup without a frozen frontend build (`.gitignore:1-3`, `main.go:75-78`, `nags.toml:15-16`).

Decision:

- Add canonical authenticated `GET /triggers`, `GET /api/triggers`, and `PUT /api/triggers`. The API returns the editable registry snapshot, enabled workflow choices, computed next fire times, and synthesized read-only lifecycle entries. It returns no secrets, repository paths, payload bodies, or private run data.
- Reuse whole-snapshot optimistic updates instead of inventing item-level REST semantics. The UI still provides create, edit, enable/disable, and delete operations locally, then saves one validated revision. Conflicts replace the draft with the authoritative snapshot and require review before another save.
- Add Triggers between Agents and Settings in shared navigation. `/settings` replaces its old trigger controls with a link to the single registry authority.
- Use native form controls and real buttons, inline create/edit, and a two-step inline delete confirmation. Loading, empty, load error, validation error, save success, conflict, and network error remain explicit live-region states. Desktop, 720-pixel, and 320-pixel layouts must remain keyboard usable in light and dark modes.

### 6. Observable acceptance and verification

ENG-40 is complete when:

1. An authenticated operator can open `/triggers` and see both editable admission definitions and read-only protected lifecycle triggers.
2. The operator can create, modify, enable/disable, and delete typed Linear-label, Linear-comment, and cron entries, with unique-kind and workflow-reference validation.
3. Registry updates survive restart with private atomic persistence, stale revisions return `409`, and rollback can still start the current schema-1 release.
4. Label and comment events retain all current authenticity, journaling, history, routing, and coalescing behavior while consulting the registry.
5. A due cron occurrence targets its configured Linear issue, resolves allowlisted repository metadata, enters the durable wire and claim path once, and never overlaps an active issue run.
6. Cron state is restart-safe, deterministic, timezone-aware, and bounded to one catch-up occurrence after downtime.
7. The selected trigger/workflow identity survives queueing and later lifecycle segments without making GitHub remediation, human merge, verified-head deployment, or cleanup configurable.
8. Unauthenticated, cross-origin, malformed, oversized, invalid, stale, and unroutable mutations fail without changing either store.

Verification will use focused registry/scheduler/store/server/run/auth tests; the complete Go test, race, and vet suites; frozen frontend install/typecheck/build; authenticated browser flows at desktop and mobile sizes; and exact-commit post-merge deployment probes.

Baseline evidence on `8a6bf5082dc5b622a0b8e0dc5e77248ad1a7bab9`:

```text
go test ./internal/settings ./internal/server ./internal/agentrun
PASS

MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile
bun run --cwd frontend typecheck
bun run --cwd frontend build
PASS; worktree remained clean
```

### 7. Security, compatibility, rollout, rollback, and deployment

- Webhook authentication, replay protection, actor allowlisting, Factory-comment exclusion, repository allowlisting, one-active-run coalescing, human-only merge, exact verified-head validation, clean-main deployment, receipt validation, and Worktrunk cleanup remain unchanged.
- Registry fields are typed declarative data. They never accept shell commands, executable paths, environment variables, secrets, repositories, branches, merge behavior, or deployment commands.
- Additive trigger/workflow fields on persisted runs are backward-readable because the existing run store uses ordinary JSON decoding. Existing runs without the fields use the current fallback.
- The independent trigger store keeps `settings.json` readable by the prior release. Rollback intentionally restores the prior release's legacy label/comment behavior; it cannot execute cron definitions because that code does not exist in the prior binary.
- After human merge, deploy only from the clean updated primary checkout with `bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"`. Verify local and public health identity, launchd state, tmux-session survival, authenticated registry readback, cursor persistence, and the deployment receipt. Recovery uses a corrective or revert commit on `main` and the same exact-commit deployment path.

## Alternatives considered

- **Replace the trigger fields inside settings schema 1:** rejected because a structural change without a schema bump is dishonest and a schema bump makes the current release fail startup during rollback.
- **Maintain both settings triggers and registry triggers as active authorities:** rejected because two editable sources would race and make admission behavior ambiguous.
- **Use item-level POST/PATCH/DELETE APIs:** rejected for the initial implementation because the repository's proven mutation contract is a complete revisioned snapshot. UI CRUD does not require a new concurrency protocol.
- **Allow arbitrary event predicates:** rejected because no event vocabulary, precedence, or fan-out semantics exists and it would substantially expand security and verification scope.
- **Let cron select a repository or command directly:** rejected because it bypasses issue-centric Linear gates and repository allowlisting.
- **Use manager polling or event-wire retention as the only cron cursor:** rejected because neither is a durable long-term occurrence ledger.
- **Make GitHub and post-merge entries editable:** rejected because they are mandatory safeguards for already-authorized runs.

## Assumptions

- “All existing triggers” means all agent admission and lifecycle trigger kinds, not telemetry timers or project onboarding. Protected lifecycle kinds are visible but read-only.
- “Event-based” means the two existing safe admission predicates. New arbitrary webhook/event languages are a later issue.
- A cron trigger is an authenticated recurring admission for an existing Linear issue. This preserves all current gates and routing.
- Standard five-field cron plus a separate IANA timezone is sufficient for the first version.
- A single catch-up occurrence is preferable to unbounded backfill after downtime.

## External and delegated evidence

- The upstream cron parser and timezone behavior were checked against [robfig/cron v3](https://github.com/robfig/cron); local module discovery confirmed v3.0.1 is available.
- The first Claude backend and UI research windows failed operationally because the Claude account had reached its weekly limit. Their durable diagnostics are under:
  - `/Users/tom/.local/share/factory/runs/run-673fd65811103e72/children/trigger-backend-research-6680512b/`
  - `/Users/tom/.local/share/factory/runs/run-673fd65811103e72/children/trigger-ui-research-41bee546/`
- A Codex backend child independently traced the trigger, wire, routing, run, lifecycle, persistence, and deployment boundaries and completed successfully:
  - `/Users/tom/.local/share/factory/runs/run-673fd65811103e72/children/trigger-backend-research-codex-0ab77421/`
- A Codex UI child independently traced routes, auth, mutation patterns, accessibility, responsive behavior, build assets, and frontend risks and completed successfully:
  - `/Users/tom/.local/share/factory/runs/run-673fd65811103e72/children/trigger-ui-research-codex-b5e67985/`

## Contradictions and resolved ambiguities

- ENG-32 put trigger controls on `/settings`; ENG-40 requests a dedicated registry. The dedicated page becomes the sole new-release editing authority, while legacy settings fields remain only for rollback compatibility.
- The issue says triggers are “mostly event-based,” but the code has only two editable admission events and several mandatory lifecycle wakes. The UI will distinguish editable admission from read-only lifecycle triggers.
- A cron expression alone cannot satisfy the current lifecycle. Requiring a Linear issue target is the smallest design that preserves routing, approval gates, PR correlation, and completion evidence.

## Unresolved questions

None. The assumptions above are highlighted in the Linear research gate so the owner can revise them before planning.
