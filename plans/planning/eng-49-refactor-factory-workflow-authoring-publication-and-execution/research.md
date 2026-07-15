# ENG-49 Workflow Authoring, Publication, and Execution Research

> updated: 2026-07-14T22:11:45-07:00

## Research questions

1. What current behavior and failure mode are proven by the repository?
2. Which domain types, persistence stores, APIs, UI routes, admission paths, prompt paths, and lifecycle safeguards participate?
3. Which existing seams should the implementation extend rather than replace?
4. What compatibility, security, concurrency, migration, and rollback constraints are mechanically required?
5. How is each acceptance criterion observable and verifiable?
6. What exact post-merge deployment, health, content, and recovery procedures apply?
7. Which statements are observed facts, which are inferences, and which decisions still require an owner?

## Issue and routing context

- Linear issue: [ENG-49](https://linear.app/nags-cloud/issue/ENG-49/refactor-factory-workflow-authoring-publication-and-execution), “Refactor Factory workflow authoring, publication, and execution.”
- Linear project metadata specifies `GitHub Repo: https://github.com/tomnagengast/factory` and `Local Path: /Users/tom/repos/tomnagengast/factory`.
- Factory supplied the managed clone `/Users/tom/repos/tomnagengast/factory`; its normalized origin is `tomnagengast/factory`, so routing is exact and the repository root is the subsystem scope.
- The deterministic issue branch is `eng-49-refactor-factory-workflow-authoring-publication-and-execution`, created by Worktrunk from fetched `origin/main` at `f383b179d890abab1378cb670ce4964c64013539`.
- The issue currently carries the `Yolo` label. That is intake context only until labels are refreshed independently at each Linear gate.
- The issue references the 495-line source design `/Users/tom/notes/agent/plans/planning/2026-07-14-factory-workflows.md`. That document was read in full and is evidence for product decisions, but it is not the lifecycle plan artifact.
- A later human reply on 2026-07-14 resolved the final plan-review decision: protected feedback uses a dedicated published-policy workflow binding, while the configurable generic `linear-comment` rule remains separate and additive. The reply also requires a schema-1-backup versus retained-trigger-registry rollback preflight and authorizes one fresh dual-provider plan-review cycle limited to those two P1 corrections.

## 1. Current behavior and root cause

### Published workflow policy is a short-step settings record

Observed facts:

- `internal/settings/settings.go` defines schema 1 and owns `Workflow` as `id`, `name`, `enabled`, fixed `runner: "do"`, and 1–20 single-line steps. Validation restricts IDs to lowercase slugs, names to 80 bytes, steps to 240 bytes, and the collection to eight workflows.
- `settings.Defaults` seeds one enabled `full-sdlc` workflow with seven advisory steps. `Snapshot.Validate` also requires the two legacy Linear triggers to reference enabled workflows.
- `internal/settings/store.go` persists the whole settings snapshot as strict bounded JSON with a mutex, optimistic revision, `0600` mode, temporary-file fsync, and rename. The current bound is 1 MiB, schema 2 and migration do not exist, and there is no draft store.
- `frontend/src/index.tsx` mirrors the Go shape in `WorkflowSettings` and edits workflow cards, runner text, and ordered steps inside `SettingsPage`. There is no `/workflows` route or nav item.
- `internal/server/server.go` returns and accepts the entire settings snapshot at `/api/settings`. A stale settings page can therefore overwrite a concurrent workflow change.
- `internal/server/triggers.go` returns complete `settings.Workflow` objects through `/api/triggers`, so the trigger selector currently receives step bodies rather than a summary-only contract.

Root cause:

The settings workflow is not executable policy. It is short advisory context attached to an external procedural policy. Authoring, publication, pinning, and execution authority are split across settings, the provider-owned `$do` skill, skill references, the Factory prompt wrapper, and Go lifecycle validators. This creates drift and prevents note-like authoring or exact immutable publication.

### Factory tells the principal to invoke external `$do`

Observed facts:

- `internal/agentrun/execute.go:principalPrompt` opens new work with `Use $do`, renders the selected settings steps as declarative context, says `/do` owns the lifecycle, and references `.agents/skills/do/scripts/linear_graphql.py` plus `/do` review/wait behavior.
- The current dual-provider review rule, ready checkpoint protocol, human-only merge prohibition, verified-head rule, and typed result vocabulary are also embedded in this wrapper.
- Provider-process retry text says `Resume the Factory /do run`.
- `.agents/skills/do` is not part of this repository. The provider-installed skill is therefore a separate release boundary and cannot supply immutable repository-owned workflow revisions.

The failure mode is proven: operator-edited steps do not become the procedural workflow, while the real lifecycle policy is duplicated and can drift. ENG-49 must make published Markdown the direct procedural input without moving mechanical trust boundaries into editable text.

## 2. Existing seams and data flow

### Admission is already the immutable pinning seam

Observed facts:

- `internal/triggerrouter/admission.go:ApplyDecisionBatch` evaluates a registry and settings snapshot together while recording one durable decision per wire event.
- For each admitted rule, `newInvocation` resolves the configured workflow from that same settings snapshot, clones it, serializes it, hashes the serialized bytes with SHA-256, and stores the full workflow plus digest on the durable `Invocation`.
- `internal/triggerrouter/model.go` includes the selected rule revision, settings revision, workflow value, and workflow digest in durable routing state.
- `internal/triggerrouter/manager.go` passes that pinned workflow into `agentrun.InvocationClaim`.
- `internal/agentrun/store.go` copies it into `Run.PinnedWorkflow`, preserves it across retries and segments, and enforces one nonterminal Run per issue.
- `internal/agentrun/launcher.go` writes a contained private `workflow.json` file in the Run directory and passes its exact path to `agent-exec`.
- `internal/agentrun/execute.go:ReadWorkflowSnapshot` requires an absolute contained regular file with `0600` permissions, bounded strict JSON, a valid enabled workflow, and no trailing data.

Inference supported by those facts:

ENG-49 should replace the pinned value and prompt composition, not build a step interpreter, separate execution queue, or second pinning mechanism. The invocation, Run, private snapshot, retry, feedback, remediation, and post-merge paths already form the correct immutable chain.

### CoordinatedWire is already the publication boundary

Observed facts:

- `internal/triggerrouter/coordinator.go` owns one `policy` mutex around wire publication, catch-up, registry writes, and settings writes.
- Before a policy mutation it checks every dispatched-but-undecided record has a durable routing decision. Mutations return `ErrPolicyPending` rather than overtaking admission.
- Registry and settings snapshots are cross-validated before persistence.
- `triggerregistry.Snapshot.Validate` rejects an enabled rule whose workflow is missing or disabled.

Inference supported by those facts:

Published workflows should remain in the atomic settings policy checkpoint. Exact-draft publish and live delete should extend `CoordinatedWire`, inheriting the policy lock and pending-decision gate. Autosaved drafts have different authority and should stay in a separate private store that admission never reads.

### Mechanical safeguards remain outside Markdown

Observed facts:

- Repository routing and managed-root/origin checks are in `internal/agentrun/repository.go`, `launcher.go`, and completion validation.
- Run ownership is enforced in `internal/agentrun/store.go`.
- Contract-v1 ready checkpoints are written by `factory agent checkpoint ready-for-merge` and validated before awaiting merge.
- Human merge, exact verified-head ancestry, merged-main deployment receipts, completion evidence, and clean Worktrunk cleanup are validated in `internal/agentrun/ready.go`, `completion.go`, `completion_system.go`, and their tests.
- The latest base commit `f383b17` strengthened verified merge ancestry. ENG-49 must not weaken or relocate that logic.

### Protected feedback needs its own published-policy binding

Observed facts:

- `internal/server/server.go` currently decides whether a human comment carries the protected continuation marker by reading `settings.Triggers.LinearComment.Enabled`, then calls `RunStore.ClaimContinuation` without a workflow value.
- `internal/agentrun/store.go:ClaimContinuation` coalesces feedback into an active Run or creates a fresh Run without `PinnedWorkflow`; only generic routed invocations currently create a Run with a pinned workflow.
- `internal/server/triggers.go` exposes `linear-feedback` as metadata-only protected-route text. The independently configurable `linear-comment` registry rule is returned separately and is seeded from the same legacy settings field only when a retained registry does not already exist.
- `settings.Snapshot.Validate` protects the legacy Linear-comment workflow reference, while `triggerregistry.Snapshot.Validate` independently protects enabled generic-rule references. Neither contract currently represents the dedicated protected binding selected by the owner.

Required behavior from the human decision:

- Schema-2 published policy owns a dedicated, always-enabled protected feedback binding. `/triggers` exposes its workflow selector as a protected entry that may be repointed but not disabled or deleted.
- Repointing the protected binding is a `CoordinatedWire` settings-policy mutation. It requires an enabled published target and increments policy revision without rewriting the independent generic `linear-comment` rule.
- Workflow disable/delete validation counts both the protected binding and generic rules. Workflow A cannot be retired until neither source references it.
- A fresh post-terminal feedback Run pins the binding's current workflow body, workflow revision, digest, and policy revision when claimed. Feedback joining an active Run preserves that Run's existing pin. Repointing A to B affects only later fresh Runs, including when an A continuation is already queued.
- New continuation routing stops consulting the legacy settings trigger field. The protected route remains non-disableable, and a configured generic `linear-comment` rule is explicitly additive.

## 3. Required target contracts

The issue and full source design resolve the following product decisions:

- Create `internal/workflow` with a published Markdown definition, a separate draft model, canonical validation, revision helpers, a compiled `full-sdlc` default, strict legacy decoding, and migration helpers.
- Keep published definitions in schema-2 `settings.json`; use a separate schema-versioned private `workflow-drafts.json` for non-executable autosave state.
- Published definitions carry stable ID, server-owned workflow revision, name, enabled state, Markdown, and update time. Drafts carry their own revision and a server-owned base workflow revision.
- Canonicalize CRLF and lone CR to LF before validation, comparison, persistence, or hashing. Markdown is literal UTF-8, nonblank, NUL-free, at most 128 KiB, and not interpreted by the server.
- Keep at most eight workflow IDs across published definitions and never-published drafts. Cap published and draft aggregate Markdown independently at 768 KiB. Raise the settings checkpoint bound to 2 MiB and workflow request bounds to 1 MiB.
- Create a disabled starter draft immediately with a server-assigned stable ID. Autosave, discard, publish, and delete serialize by workflow ID. Unrelated workflows may save concurrently.
- Autosave changes only the draft revision and never acquires the policy coordinator. Publish takes the keyed authoring lock before the coordinator lock and revalidates exact draft, base workflow, policy, pending-wire, and rule-reference state.
- Successful publish promotes the exact persisted draft, increments live workflow and policy revisions only for material published changes, and advances the draft base plus draft revision so stale tabs conflict.
- If the process stops after the live settings write but before the draft-base write, authoring reconciliation compares canonical content and advances draft metadata idempotently without creating a second live revision.
- Draft absence is an empty store. Draft corruption or I/O failure preserves diagnosis and disables authoring while published admission, execution, and readiness continue against the last good policy.
- `GET /api/workflows` returns published and draft authoring state, explicit availability, and rule references. Draft create/autosave/discard/publish and confirmed published delete use strict authenticated same-origin JSON contracts with bounded bodies and optimistic conflicts.
- `/api/settings` becomes a narrow agent/runtime DTO and cannot overwrite workflows. `/api/triggers` returns published workflow summaries only and never returns Markdown.
- A referenced workflow cannot be deleted. A workflow referenced by an enabled rule cannot be disabled.
- The principal prompt becomes a code-owned segment header, verbatim pinned Markdown between explicit delimiters, and a compact code-owned runtime footer containing only parseable/mechanical protocol.
- Add `factory agent linear-graphql`, reading request JSON from stdin and `LINEAR_API_KEY` only from the inherited environment. New workflow Markdown must not depend on provider skill files.
- Build a protected `/workflows` list/detail source editor with approximately 750 ms debounced serialized autosave, save coalescing, exact acknowledged-revision publish, explicit discard/delete, conflict preservation, local-dirty-only navigation warnings, keyboard/focus support, and desktop/mobile layouts.
- `/settings` retains only agent launch policy and capacity and links to `/workflows`.

## 4. Compatibility and migration risks

### Schema-1 settings migration

Observed facts:

- The current reader strictly accepts schema 1 and rejects unknown fields.
- Existing workflow records contain `runner` and `steps`; provider, attempt, concurrency, trigger, revision, and timestamp values are operator state that must be preserved.

Required behavior:

- Decode schema 1 and schema 2 explicitly.
- Before replacing schema-1 settings, write one private fsynced `settings.schema1.backup.json` if absent.
- Migrate every workflow to revision 1 using the self-contained compiled Full SDLC Markdown and append its legacy steps under a labeled migrated-guidance section.
- Preserve IDs, names, enablement, trigger references, agent settings, attempts, concurrency, policy revision, and update time.
- A valid schema-2 checkpoint wins. Invalid, oversized, conflicting-backup, or partially migrated state fails closed without replacing last good data.

### Prior-binary rollback must preflight the retained registry

Observed facts:

- `main.go` opens `settings.json` first and then opens `triggers.json` with `triggerregistry.Open(..., settingsStore.Snapshot())`.
- `triggerregistry.Open` strictly decodes the retained registry and validates enabled rules against the workflows in the active settings snapshot.
- Therefore a schema-2 workflow can be published and selected by an enabled retained rule before any admission sets the monotonic compatibility marker. Restoring the schema-1 backup would remove that workflow, and the prior binary would fail during registry startup validation.

Required behavior:

- Before restoring `settings.schema1.backup.json` or invoking a prior-binary rollback, a read-only schema-2-aware preflight must strictly decode the backup and retained registry and run the prior release's registry-versus-settings validation.
- Any incompatibility forbids backup restoration and requires schema-2-aware forward recovery, even when the admission compatibility marker is still false.
- A committed no-admission fixture must cover a schema-2-only workflow referenced by an enabled retained rule and prove the preflight rejects rollback without mutating either file.

### Retained routing and Run records

Observed facts:

- `trigger-routing.jsonl` uses strict unknown-field decoding.
- Retained `Invocation.Workflow`, `Run.PinnedWorkflow`, and `workflow.json` use the legacy settings type today.
- Current terminal compaction keeps only workflow ID, while the target requires ID, revision, and digest.

Risk and requirement:

Replacing the JSON shape without an explicit legacy decoder would make retained routing and Run data unreplayable. New API writes must reject legacy fields, while retained internal records use a narrow legacy read path. Nonterminal legacy pins must keep their original `$do` semantics during the compatibility window rather than being silently reinterpreted as new Markdown.

### Snapshot integrity

Observed fact:

`ReadWorkflowSnapshot` validates containment, mode, size, strict JSON, and workflow validity but not the admission digest.

Requirement:

Persist a pinned wrapper containing definition and digest, recompute the digest over canonical execution fields on read, and reject mismatch. Terminal compaction drops Markdown but retains workflow ID, revision, and digest.

## 5. Security, data, and concurrency constraints

- Workflow and draft Markdown are authenticated operator policy and sensitive even when a draft is not executable.
- Do not render Markdown as HTML, interpolate it into shell arguments, environment values, filenames, URLs, or templates, or parse fenced blocks as commands.
- Every authoring route reuses Google-session auth, same-origin mutation checks, JSON media-type checks, bounded request bodies, strict decoding, no trailing content, and no-partial-mutation behavior from the settings/trigger APIs.
- Add `/workflows` to the OAuth return-path allowlist. Repository evidence also shows `/triggers` is currently missing from that allowlist despite being protected; fixing both in the same route test is adjacent correctness, not scope expansion.
- Never expose Run files, prompts, repository paths, credentials, raw trigger payloads, or drafts from `/api/triggers`.
- Fixed lock order is per-workflow authoring lock, coordinator policy lock, settings/draft store internals. Draft mutation must not call back while locks are held in reverse order.
- Published policy remains valid if draft authoring is unavailable. Invalid schema-2 policy or unreplayable legacy state fails startup closed.

## 6. Acceptance-to-verification mapping

| Acceptance or risk | Exact evidence to produce |
| --- | --- |
| Definition, draft, canonicalization, limits, defaults | `go test ./internal/workflow -count=1` with validation, canonicalization, clone/equality, aggregate, default, and legacy cases |
| Schema-1 migration and private backup | `go test ./internal/settings -count=1` using production-shaped schema-1 fixtures, operator overrides, idempotence, backup mode, conflicts, corruption, and bounds |
| Draft durability and isolation | `go test -race ./internal/workflow ./internal/settings -count=1`; create/save/reopen/discard, absent file, `0600`, concurrent distinct drafts, stale revision, corrupt/write failure, unchanged live policy bytes/revision |
| Exact publish and crash reconciliation | Focused server/coordinator tests that publish acknowledged draft N, exclude unacknowledged N+1, reject stale draft/base/policy, inject failure after settings write, reopen, and prove one workflow/policy increment |
| Pending-wire and reference safety | `go test ./internal/triggerrouter ./internal/triggerregistry -run 'Test.*(Workflow|Policy|Admission)' -count=1` |
| Auth, origin, strict JSON, bounds, conflicts | `go test ./internal/server ./internal/viewerauth -run 'Test.*(Workflow|Settings|Private|Return)' -count=1`, covering 401/redirect, 403, 400, 409, 413, and 415 |
| Slim settings and trigger summary | Server tests prove stale settings cannot overwrite a published workflow and `/api/triggers` response bytes exclude a unique Markdown sentinel |
| Immutable pinning and direct execution | `go test ./internal/agentrun ./internal/triggerrouter -run 'Test.*(Workflow|PrincipalPrompt|PostMerge|Continuation|Admission)' -count=1`; publish v2 after admitting v1 and prove all v1 segments retain v1 while only new admission sees v2 |
| Snapshot containment and digest | Tests reject wrong mode, symlink, relative/outside path, malformed/unknown JSON, missing body, and digest mismatch |
| Factory-owned Linear helper | `go test . -run 'Test.*LinearGraphQL' -count=1` with `httptest`, missing key, network error, GraphQL error, mutation failure, and no credential leakage |
| Frontend type/build | `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck`; `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile`; `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` |
| Editor behavior | Authenticated browser flow for create, multiline/code-fence edit, debounce/coalescing, navigate/restart recovery, exact publish, discard, cross-tab conflict, reload/duplicate, reference blocks, offline recovery, keyboard/focus, desktop/mobile, console/network |
| Stale external-policy coupling | `rg -n 'Use \$do|The /do skill owns|runner.*do|Ordered declarative steps|\.agents/skills/do' internal frontend/src README.md --glob '!frontend/dist/**'` returns only intentional legacy compatibility text |
| Repository-required correctness | `go test ./...`; `go test -race ./...`; `go vet ./...`; frozen Bun build; `git diff --check`; clean status after commits |

Baseline observation on `f383b17`: focused tests for `internal/settings`, `internal/triggerregistry`, `internal/triggerrouter`, `internal/agentrun`, `internal/server`, and `internal/viewerauth` all pass.

## 7. Deployment, health, and recovery

### Pre-deploy gate from updated clean primary `main`

```bash
curl -fsS http://127.0.0.1:8092/api/healthz | jq -e '.status == "ok" and .wire.pending == 0'
jq -e '[.runs[] | select(.state == "pending" or .state == "post_merge_pending" or .state == "starting" or .state == "running" or .state == "awaiting_human_merge")] | length == 0' \
  "$HOME/.local/share/factory/data/agent-runs.json"
cp -p "$HOME/.local/share/factory/data/settings.json" \
  "$HOME/.local/share/factory/data/settings.pre-workflows-deploy.json"
```

Also run the release candidate’s retained-routing migration test and use authenticated `/api/triggers` to require the sum of `ruleStatus[].outstanding` to be zero.

### Deployment

Repository evidence in `README.md` and the installed CLI establishes the current command:

```bash
test "$(git branch --show-current)" = main
test -z "$(git status --porcelain)"
git fetch --prune origin
git merge --ff-only origin/main
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
```

This must run only after a human merge, from the Worktrunk-resolved managed primary checkout, never from the issue worktree or T9 mirror.

### Post-deploy verification

```bash
COMMIT=$(git rev-parse HEAD)
curl -fsS http://127.0.0.1:8092/api/healthz | jq -e --arg commit "$COMMIT" '.status == "ok" and .commit == $commit and .wire.pending == 0'
curl -fsS https://factory.nags.cloud/api/healthz | jq -e --arg commit "$COMMIT" '.status == "ok" and .commit == $commit and .wire.pending == 0'
jq -e '.schema == 2' "$HOME/.local/share/factory/data/settings.json"
test "$(stat -f '%Lp' "$HOME/.local/share/factory/data/settings.json")" = 600
test "$(stat -f '%Lp' "$HOME/.local/share/factory/data/settings.schema1.backup.json")" = 600
launchctl print "gui/$(id -u)/com.nags.factory" | rg 'state = running|pid = [0-9]+'
tmux -L factory-agents list-sessions
```

Use an authenticated browser/API session to prove `/api/workflows` exposes published Markdown plus revisions, `/api/settings` omits workflows, `/api/triggers` contains summaries only, and a create/autosave/read/discard draft canary changes neither settings bytes nor policy revision. Confirm `workflow-drafts.json` is `0600`, wire health remains caught up, receipt identity matches, and the Factory issue tmux session survived service restart.

### Recovery

- Before any prior-binary rollback, stop the service, preserve schema-2 state, require the workflow compatibility marker to remain false, and run the release's read-only schema-1-backup versus retained-registry preflight. Restore the private schema-1 backup and use `~/.local/bin/nags rollback factory --to <deployment-id>` only when that preflight passes.
- After the first durable new-shape admission, or whenever the retained registry fails the schema-1-backup preflight, recovery must use a schema-2-aware release or a corrective commit merged and deployed from clean `main`; never silently translate Markdown back to steps.
- Never delete or truncate wire/routing journals. Preserve corrupt draft state and repair authoring separately while published admission/execution continue.
- If a published workflow causes bad behavior, disable affected trigger rules, preserve pinned evidence, publish a corrected revision, and explicitly handle already-admitted invocations.

## Contradictions and assumptions

- Neuron’s first index refresh completed without usable ranked output, so exact source search, full design reading, history, blame, focused tests, and two independent research children supplied the evidence instead. No conclusion depends on a low-seed graph result.
- Scout’s first directory-summary run did not finish within a bounded research interval and was interrupted cleanly. No Scout child process remains.
- The source design noted a prior live production observation at commit `f383b17`, schema-1 settings revision 5, wire caught up, and no nonterminal Runs. That is historical observation, not a premise. Deployment must re-run every quiescence check against fresh state.
- The installed provider `$do` text contains a stale Factory deployment form, while this repository’s current runbook and installed `nags` CLI use `~/.local/bin/nags deploy ... --expected-commit`. ENG-49 explicitly removes that drift by compiling the repository-owned workflow from current evidence.
- Implementation may add focused fixtures and test helpers needed to prove migration and compatibility, but it must not implement native tasks, change lifecycle contract v1, change provider/capacity semantics, or remove interactive `$do` support outside Factory.

## Unresolved questions

None. The issue description, full source design, current repository, history, baseline tests, and independent research agree on the source-first editor, private autosaved drafts, deliberate exact-draft publication into coordinated settings policy, direct pinned Markdown execution, compatibility window, deployment path, and unchanged Go-enforced safeguards.
