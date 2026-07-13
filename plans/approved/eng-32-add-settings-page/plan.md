# ENG-32 Implementation Plan: Add settings page

> updated: 2026-07-13T03:33:56Z

## Issue context and acceptance criteria

ENG-32 requests a `/settings` page that lets an operator change which events trigger an agent, which workflows those triggers run, the ordered steps in each workflow, and provider spawn settings such as model and reasoning level without a source-code edit and release.

The implementation is complete when:

1. An authenticated operator can load `/settings`, inspect the effective revision, edit triggers, workflow assignments and ordered steps, provider settings, retry count, and run concurrency, then save a validated revision.
2. Settings persist privately and atomically across process restart, while an absent settings file produces behavior identical to the current release.
3. The initial Linear-label and later human-comment trigger paths consult the live settings. Disabled triggers do not claim runs, but signed event ingestion and private activity retention still work.
4. A newly started run uses the selected built-in `$do` workflow and renders its ordered declarative steps into the principal prompt without weakening the mandatory Factory lifecycle contract.
5. Newly launched principals and children use the configured provider model and effort as distinct process arguments; principal retry count and manager concurrency honor validated settings.
6. Invalid, malformed, oversized, stale-revision, unauthenticated, and cross-origin writes fail without changing the last good snapshot.
7. Human-only merge authority, exact verified-head checks, repository routing, secret handling, deployment source restrictions, and post-merge cleanup remain non-editable and pass the existing lifecycle suites.

## Research questions and answers

The complete evidence is in `plans/planning/eng-32-add-settings-page/research.md`.

- **Where is configuration consumed?** Trigger policy is consumed by the webhook server, principal model/prompt/retries by a separate `agent-exec` process, child provider settings by separate `child-exec` processes, and concurrency by the long-running manager. A shared persistent store is required.
- **Which events are safely editable?** Initial Linear label application and later human comment continuation are external entry triggers. GitHub remediation and post-merge continuation are protected transitions for already-managed runs and remain mandatory.
- **How can workflows be editable without exposing shell execution?** Workflows use a fixed `do` runner and bounded declarative step text. No command, executable path, provider flag, secret, repository, merge, or deployment field is accepted. The hard-coded lifecycle contract is always appended after configurable workflow context.
- **Where do UI and APIs fit?** Add authenticated method-aware routes in `server.New`, an allowlisted OAuth return path, and a `/settings` branch in the existing Solid SPA.
- **How should persistence behave?** Follow the repository's versioned JSON, mutex, private-file, fsync, and atomic-rename store conventions. Use optimistic revisions to prevent silent overwrite.
- **What deploys and proves the change?** The Factory release is deployed only after human merge from the clean updated primary checkout, bound to its exact commit, then verified through local/public health identity, service state, tmux survival, authenticated settings readback, and a reversible persistence probe.

## Current behavior and root cause

`internal/server/server.go` hard-codes the `Factory` trigger label and fixed predicates. `internal/agentrun/execute.go` hard-codes the Codex and Claude models, common effort, principal retry count, and wrapper prompt. `main.go` reads concurrency once from `FACTORY_MAX_AGENTS`. The server, principal, and child are distinct processes, so replacing constants with a server-only variable would not satisfy the issue.

The root opportunity is to establish one validated durable settings contract that each boundary reads at the moment it can safely adopt a new snapshot. This avoids releases for ordinary policy changes while keeping durable runs and lifecycle authority stable.

## Decisions and alternatives

### Decisions

- Store settings at `~/.local/share/factory/data/settings.json` using schema version 1 and a monotonically increasing revision.
- Model two editable external triggers: `linear-label` with a bounded label name and `linear-comment`; both have enabled state and a workflow ID.
- Model up to eight workflows. Each has a stable slug ID, bounded display name, enabled state, fixed runner `do`, and 1-20 ordered bounded text steps. Referenced workflows cannot be disabled or removed.
- Model principal Codex, Codex child, and Claude child provider settings independently. Model identifiers use a non-shell bounded identifier pattern so future provider models do not require a release. Effort is an enum supported by the respective provider invocation. Principal attempts and maximum concurrent runs use bounded integers.
- Read trigger policy for each signed Linear dispatch, manager concurrency for each reconcile, principal settings once per lifecycle segment, and child settings once per child launch. Do not restart an in-flight provider process when settings change.
- Require the caller's current revision on PUT. Return `409 Conflict` for a stale revision and the current snapshot for recovery.
- Reject browser writes when an `Origin` header names a different host. Keep API authentication mandatory and continue to support authenticated non-browser automation with no Origin header.

### Alternatives considered

- **Edit provider `$do` skill files from the UI:** rejected because those files are outside this repository and changing them would bypass repository review and release ownership.
- **Accept arbitrary shell commands or provider flags:** rejected because providers run with elevated local permissions and shell-like configuration would create an unnecessary code-execution surface.
- **Keep settings only in memory:** rejected because principals and children are separate processes and settings would disappear on restart.
- **Apply changes immediately to active processes:** rejected because it would make durable lifecycle behavior non-reproducible and could change authority after a run began.
- **Use environment variables for every field:** rejected because the issue explicitly seeks operator edits without the environment refresh and release process, and environment values cannot represent workflow collections cleanly.

## Assumptions and non-goals

Assumptions:

- The authenticated operator is trusted to write declarative workflow guidance, but the service still validates shape and preserves hard safety instructions.
- New-segment semantics are acceptable: saved settings affect later webhook decisions, manager scheduling, provider launches, and children, not a provider command already running.
- The current environment-derived concurrency seeds defaults only when no persisted settings file exists. A persisted revision becomes authoritative.

Non-goals:

- Editing the provider-installed `$do` skill, lifecycle contract version, merge policy, deployment commands, or completion validator.
- Editing webhook secrets, Linear actor identity, OAuth allowlists, repository catalog, paths, branches, receipts, or health targets.
- Arbitrary commands, scripts, environment variables, model CLI flags, prompt templates, or executable workflow runners.
- Migrating existing run-store records or retroactively changing active and completed runs.
- Multi-user roles, a general secrets UI, or an audit-event history beyond revision and update timestamp.

## Impacted files and interfaces

### New settings domain

- `internal/settings/settings.go`
  - Define `Snapshot`, `Triggers`, `Trigger`, `Workflow`, `AgentSettings`, `ProviderSettings`, and `RuntimeSettings` JSON contracts.
  - Define current defaults matching `Factory`, `$do`, `gpt-5.6-sol`, `fable`, `high`, three attempts, and the startup concurrency default.
  - Validate schema version, revision, trigger references, workflow uniqueness, text bounds, provider identifier syntax, provider effort enums, attempts, and concurrency.
- `internal/settings/store.go`
  - Implement `Open(path, defaults)`, `Snapshot()`, and `Update(expectedRevision, candidate, now)` with `sync.RWMutex`, copied snapshots, private directories/files, temp-file fsync, and atomic rename.
  - Export a typed revision-conflict error and avoid replacing state on any failed validation or write.
- `internal/settings/settings_test.go` and `internal/settings/store_test.go`
  - Cover defaults, validation boundaries, workflow references, round-trip/reopen, private permissions, stale revisions, no-change-on-error, and concurrent readers/writers under the race detector.

### Runtime consumption

- `internal/server/server.go`
  - Add a narrow settings-reader/updater dependency to `Config` and `appServer`.
  - Add authenticated `GET /api/settings`, `PUT /api/settings`, and private `/settings` SPA routes.
  - Bound and strictly decode PUT bodies, validate same-origin browser requests, map conflicts to 409, and never return filesystem paths or secrets.
  - Replace the compile-time trigger label/toggles with a fresh settings snapshot during Linear normalization/dispatch while leaving GitHub and post-merge transitions fixed.
- `internal/server/server_test.go`
  - Extend the existing handler fixture with a temporary settings store.
  - Test private page/API authentication, read/write success, strict validation, body limit, origin rejection, stale revision, label rename/disable, comment disable, activity preservation, and unchanged protected lifecycle paths.
- `internal/agentrun/execute.go`
  - Replace provider constants and fixed retry loop limit with validated `PrincipalConfig` and `ChildConfig` provider settings.
  - Render the selected enabled workflow's ordered steps before the non-overridable Factory contract, preserving all trigger-specific openings and result markers.
  - Build provider argument slices directly with model and effort values; do not invoke a shell.
- `internal/agentrun/execute_test.go`
  - Verify default prompt parity, selected workflow/step ordering, mandatory contract placement, provider argument construction, configured attempts, and trigger-specific post-merge safeguards.
- `agent_commands.go`
  - Derive the settings path from the validated Factory run/output directory, load a snapshot for `agent-exec` and `child-exec`, select the trigger's workflow for a principal, and fail closed on invalid persisted settings.
- `agent_commands_test.go`
  - Cover settings-path derivation and default/custom snapshot loading without exposing arbitrary path flags.
- `internal/agentrun/manager.go`
  - Replace the fixed `maxConcurrent` value with a validated concurrency reader and snapshot it once per reconcile pass so scheduling decisions are internally consistent.
- `internal/agentrun/manager_test.go`
  - Prove a changed concurrency value affects later reconcile passes without interrupting active runs.
- `main.go`
  - Open the settings store beside existing data stores, seed its defaults from current startup defaults, pass it into server and manager, and preserve startup failure on a present invalid settings file.

### Authentication and UI

- `internal/viewerauth/auth.go` and `internal/viewerauth/auth_test.go`
  - Add `/settings` to protected OAuth return destinations and verify unsafe external returns still fall back.
- `frontend/src/index.tsx`
  - Add typed settings API clients, `/settings` routing, and an authenticated settings editor with trigger toggles, label/workflow selectors, workflow add/remove/reorder controls, provider model/effort controls, attempts/concurrency inputs, revision display, save state, validation messaging, and conflict reload.
  - Add a Settings navigation destination without exposing it on the public home page as an unauthenticated data source.
- `frontend/src/styles.css`
  - Add responsive form, workflow-card, step-list, status, focus, invalid, saving, and mobile layouts consistent with the existing visual system.
- `README.md`
  - Document settings location, defaults, scope, new-segment behavior, protected non-editable boundaries, authenticated API/page, recovery from an invalid file, and verification/deployment probes.

## Implementation phases

### Phase 1: Durable validated settings contract

1. Add the settings types, defaults, clone helpers, and complete validation.
2. Add the atomic store and typed revision conflict.
3. Add focused store/domain tests and run `go test ./internal/settings` plus `go test -race ./internal/settings`.

Success criteria: a missing file yields current behavior; valid updates survive reopen with `0600` permissions; invalid and stale writes leave the last good revision unchanged.

### Phase 2: Authenticated API and trigger consumption

1. Wire the store in `main.go` and `server.Config`.
2. Add GET/PUT settings handlers, body/origin/revision protections, and private page routes.
3. Make initial label and human comment eligibility consult one fresh snapshot per signed Linear delivery.
4. Add server and auth tests, then run focused packages.

Success criteria: authenticated round-trip works, all unsafe writes fail closed, renamed/disabled triggers affect only new claims, activity ingestion remains durable, and protected lifecycle transitions are untouched.

### Phase 3: Runtime workflow, provider, retry, and concurrency settings

1. Derive and load the shared settings store from validated run directories in principal/child commands.
2. Pass typed provider values to executor configs and construct direct provider argument vectors.
3. Render the selected workflow's steps while keeping the Factory lifecycle contract final and mandatory.
4. Read concurrency once per manager reconcile pass.
5. Add focused executor, command, and manager tests.

Success criteria: new principal segments/children use configured values, attempts/concurrency obey bounds, in-flight commands are not restarted, and existing lifecycle prompt assertions still pass.

### Phase 4: Settings UI and operator documentation

1. Add the typed Solid resource/form state and `/settings` route.
2. Implement accessible structured editing, workflow/step operations, optimistic save, conflict handling, and mobile layout.
3. Update README with scope, persistence, new-segment semantics, recovery, and API behavior.
4. Run frontend typecheck/frozen build and inspect desktop/mobile interactions against a temporary local server after confirming port availability; stop every temporary process.

Success criteria: every named setting is visible and editable without raw JSON, keyboard/focus behavior is usable, loading/error/success/conflict states are clear, and the built assets compile reproducibly.

### Phase 5: Full verification and publication evidence

1. Review the complete base diff for accidental churn, secrets, unsafe fields, and lifecycle regressions.
2. Run all focused and repository-required commands from a clean worktree.
3. Update the PR body with the exact verified head and evidence, mark ready, post the Linear implementation summary, and enter the durable PR green loop.

Success criteria: all acceptance criteria map to passing evidence, the PR ready predicate is freshly satisfied, and the contract-v1 ready checkpoint records the exact local head.

## Data, security, compatibility, migration, rollout, and rollback

### Data and migration

- New state is one versioned private JSON file. No existing files or run records change shape.
- Missing state uses defaults and is not written until the first successful operator save.
- Present unknown schema versions or invalid content fail startup visibly. Recovery is to restore a known-good private file or move the invalid file aside deliberately, then restart; the service never silently discards it.

### Security

- Page and APIs require existing Google/session or break-glass authentication.
- Browser mutations require a same-host Origin when Origin is present. JSON size and field decoding are strict.
- No settings value is interpreted by a shell. Model identifiers and workflow IDs are syntax-bounded; numeric and enum values are allowlisted.
- Workflow text is bounded declarative context. Mandatory human-merge, verified-head, repository-routing, deployment-source, and typed-result instructions remain hard-coded after it and cannot be removed through settings.
- Settings responses contain no secrets or host filesystem paths.

### Compatibility and rollout

- Defaults reproduce current behavior, including the `Factory` label, enabled comment continuations, `$do`, provider models/effort, three attempts, and startup concurrency.
- Existing active processes keep their launch snapshot. Later external events, reconcile passes, lifecycle segments, and child launches read the latest revision at their boundary.
- The deployment includes backend and frozen frontend assets in one immutable release.

### Rollback and recovery

- Before merge, revert the feature commits normally if verification fails.
- After merge, a code regression requires a corrective or revert commit on `main` followed by the same exact-commit Factory deployment. Do not deploy an older issue-worktree binary.
- A bad operator setting is recoverable through the authenticated UI/API if the service remains healthy. If validation blocks startup, restore the last known-good `0600` settings file or deliberately remove the invalid file to re-enable compiled defaults, then redeploy/restart and verify both health identities.
- Persistent deployment failure is reported with receipts and logs intact; cleanup does not conceal it.

## Exact post-merge deployment and verification

From the clean primary checkout resolved by Worktrunk after fetching and fast-forwarding `origin/main`:

```bash
/Users/tom/.local/share/nags/provider/bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
curl -fsS http://127.0.0.1:8092/api/healthz | jq -e --arg commit "$(git rev-parse HEAD)" '.status == "ok" and .app == "factory" and .commit == $commit and .contractVersion == "1"'
curl -fsS https://factory.nags.cloud/api/healthz | jq -e --arg commit "$(git rev-parse HEAD)" '.status == "ok" and .app == "factory" and .commit == $commit and .contractVersion == "1"'
launchctl print "gui/$(id -u)/com.nags.factory" | rg 'state = running|pid = [0-9]+'
tmux -L "$FACTORY_TMUX_SOCKET" has-session -t "$FACTORY_TMUX_SESSION"
```

Then use an authenticated API request without printing credentials to read `/api/settings`, save an unchanged candidate with its current revision, read it back at revision + 1, and restore any intentionally changed probe value. Confirm `~/.local/share/factory/data/settings.json` is mode `0600`. Re-read both health endpoints and the current deployment receipt. Factory lifecycle cleanup then verifies the receipt contains the merge commit, GitHub auto-deleted the head branch, and Worktrunk removed the clean integrated issue branch/worktree.

Recovery after a failed probe is to inspect `~/Library/Logs/factory.err` and the pending/current deployment receipts, make a corrective or revert commit on `main`, rerun the same expected-commit deployment, and repeat every probe. Never deploy from the issue worktree or manually mutate merge authority.

## Verification matrix

| Acceptance criterion or risk | Exact verification |
| --- | --- |
| Defaults preserve current behavior | `go test ./internal/settings ./internal/server ./internal/agentrun` with missing-file fixtures and existing prompt/trigger assertions |
| Atomic private persistence and restart | `go test -race ./internal/settings -run 'Test(Store|Concurrent)'`; assert reopen equality and `0600` mode |
| Authenticated settings page/API | `go test ./internal/server ./internal/viewerauth -run 'Test.*Settings'`; unauthenticated page redirects and API challenges |
| Mutation validation, CSRF, body limit, conflict | `go test ./internal/server -run 'TestSettingsAPI'`; assert 400/409/413/403 paths and unchanged revision |
| Label/comment trigger enable, rename, workflow selection | `go test ./internal/server -run 'TestLinear.*Settings|TestLinearFactoryLabel|TestLinearComment'` |
| Protected GitHub/post-merge transitions unchanged | `go test ./internal/server ./internal/agentrun -run 'TestGitHub|Test.*Merge|Test.*Checkpoint|Test.*Completion'` |
| Workflow ordered steps plus mandatory safety contract | `go test ./internal/agentrun -run 'TestPrincipalPrompt|TestWorkflow'` and assert configurable steps precede immutable contract text |
| Provider model/effort and retries use typed direct args | `go test ./internal/agentrun -run 'Test.*Arguments|Test.*Attempts'`; no shell invocation in diff |
| Runtime concurrency affects later reconcile only | `go test ./internal/agentrun -run 'TestManager.*Concurrency'` |
| Frontend types and frozen dependency graph | `export MISE_BUN_VERSION=1.3.11; bun install --cwd frontend --frozen-lockfile && bun run --cwd frontend typecheck && bun run --cwd frontend build` |
| Desktop/mobile UI, keyboard, loading/error/success/conflict | Check existing server first; if needed run a tracked temporary server, inspect `/settings` at desktop and mobile widths with authenticated browser tooling, exercise keyboard controls/save/conflict, check console/network, then stop the process |
| Complete Go correctness | `go test ./...`; `go test -race ./...`; `go vet ./...` |
| Diff hygiene and secret safety | `git diff --check`; `git status --short`; inspect `git diff origin/main...HEAD`; search changed files for credentials/debug output |
| Post-merge deploy identity and survivability | Run the exact deployment and local/public health, launchd, tmux, settings readback/persistence, receipt, GitHub, Linear, and Worktrunk probes listed above |

## Unresolved questions

None.
