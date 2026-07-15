# ENG-49 Factory Workflows Implementation Plan

> updated: 2026-07-14T22:11:45-07:00

## Linear issue context

- Issue: [ENG-49 - Refactor Factory workflow authoring, publication, and execution](https://linear.app/nags-cloud/issue/ENG-49/refactor-factory-workflow-authoring-publication-and-execution)
- Project routing: `tomnagengast/factory`, managed clone `/Users/tom/repos/tomnagengast/factory`, repository root as subsystem scope.
- Research artifact: `plans/planning/eng-49-refactor-factory-workflow-authoring-publication-and-execution/research.md`.
- Draft PR: https://github.com/tomnagengast/factory/pull/12.
- Research gate: crossed on 2026-07-14 under a fresh Linear `Yolo` label read after the committed research and draft PR were published.

## Research questions and answers

1. **What fails today?** Settings workflow steps are advisory `$do` context rather than executable policy, so authoring and lifecycle authority are split across settings, a provider-owned skill, references, prompt text, and Go validators.
2. **Where should immutable publication attach?** `triggerrouter.ApplyDecisionBatch` already serializes, hashes, and pins the selected workflow at admission; this remains the pinning seam.
3. **Where should live publication serialize?** `CoordinatedWire` already owns the policy mutex, pending-decision gate, and cross-store validation; workflow publish/delete extend this boundary while draft autosave remains outside it.
4. **How does execution retain identity?** Invocations flow into Runs, private `workflow.json`, retries, feedback, remediation, and post-merge segments. A dedicated protected feedback binding in published policy selects the definition for a fresh post-terminal continuation; the new Run pins that definition when claimed, while feedback joining an active Run preserves its existing pin. The generic `linear-comment` rule remains independent and additive.
5. **What must remain mechanical?** Repository routing, one-run ownership, human-only merge, checkpoint validation, exact verified-head ancestry, merged-main deployment, completion evidence, and cleanup remain enforced in Go.
6. **What is the dominant compatibility risk?** Strict retained routing and Run JSON embeds the legacy `runner`/`steps` shape, requiring explicit dual-shape decoding and fixtures before the new definition replaces it.
7. **How is production changed and recovered?** Deploy the human-merged exact verified head only from updated clean managed `main` with `~/.local/bin/nags deploy --expected-commit`, verify schema/health/content/receipt state, and allow prior-binary rollback only before any new-shape workflow admission is durable and after the retained trigger registry validates against the schema-1 backup. After either boundary fails, use schema-2-aware recovery or a forward corrective commit.

## Outcome

Replace the workflow step editor embedded in `/settings` with an authenticated `/workflows` workspace where operators create and edit versioned Markdown instructions. Edits autosave as private, non-executable draft revisions. An explicit Publish action promotes one exact saved draft into live policy. Each admitted invocation pins the complete published workflow revision and executes that Markdown directly as the principal agent's procedural instructions. Factory no longer tells principals to invoke `$do` for new work.

Keep Factory's trust boundaries outside editable Markdown: allowlisted repository resolution, one-run ownership, private run isolation, human-only merge authority, ready-checkpoint validation, exact verified-head validation, merged-main deployment, terminal completion evidence, and cleanup remain implemented and enforced in Go.

This is one Factory change. The provider-installed `$do` skill remains available during the compatibility and rollback window, but it is not a dependency of new Factory invocations.

## Acceptance criteria

1. An authenticated operator can open `/workflows`, list workflows, create a disabled draft with a server-assigned stable ID, edit its metadata and Markdown body in a document-like editor, return after navigation or restart without losing the saved draft, and deliberately publish the exact saved draft.
2. The editor autosaves after a short debounce and distinguishes local edits, saving draft, draft saved, unpublished changes, published, autosave failure, validation failure, and draft conflict. It warns before discarding only edits that have not reached the draft store, never silently replaces local Markdown, and remains usable at desktop and mobile widths.
3. `/settings` contains only agent launch policy and capacity. It no longer renders workflow cards, runner fields, or ordered steps. Shared navigation includes Workflows as a first-class destination.
4. `/triggers` reads enabled workflow summaries only from the published coordinated policy snapshot. It exposes a dedicated protected feedback workflow selector that can be repointed but cannot be disabled or deleted; the configurable generic `linear-comment` rule remains independent and additive. Draft autosaves never acquire the policy lock and never affect trigger choices. Publishing, deleting a published workflow, protected-binding changes, and generic trigger saves cannot overtake an undecided wire event. A published workflow cannot be disabled while an enabled rule or protected binding references it or deleted while any rule or protected binding references it.
5. Every newly admitted invocation records the selected published workflow ID, workflow revision, complete Markdown body, digest, rule revision, and policy/settings revision before promotion. Unpublished drafts and later published edits do not change queued, running, retry, feedback, GitHub remediation, or post-merge segments of that Run.
6. A new principal receives the pinned Markdown directly inside a small Factory runtime envelope. Generated prompts contain no `Use $do`, `$do` invocation, `The /do skill owns`, or repo-relative `.agents/skills/do` dependency.
7. The compiled `full-sdlc` workflow preserves current intake, research, human gates, dual-provider plan review, implementation, PR remediation, ready checkpoint, human merge, deployment, and cleanup behavior. It is self-contained and uses Factory-owned helper commands rather than skill-local scripts or reference files.
8. Existing schema-1 settings, retained trigger-routing journal entries, and retained Run records open safely. Migration preserves workflow IDs, names, enablement, legacy step guidance, trigger references, provider settings, attempts, and concurrency. An absent draft store is a valid empty state.
9. Unauthenticated, cross-origin, malformed, oversized, stale-draft-revision, stale-base-workflow-revision, stale-policy-revision, invalid-body, illegal-ID-change, referenced-disable, and referenced-delete requests fail without changing durable published policy. A draft-store failure cannot stop admission or execution of already published workflows.
10. Existing lifecycle contract v1, ready checkpoint shape, human-only merge behavior, exact verified-head checks, deployment source restrictions, completion validator, and repository routing continue to pass unchanged.
11. A prior-binary rollback is permitted only when the monotonic compatibility marker is false and a read-only preflight proves the retained trigger registry is valid against the exact schema-1 backup. A no-admission schema-2-only workflow reference fails that preflight without modifying either file.

## Research findings

### Current source of workflow behavior

- `internal/settings/settings.go` defines a workflow as `id`, `name`, `enabled`, fixed `runner: "do"`, and 1 through 20 single-line steps of at most 240 bytes.
- `frontend/src/index.tsx` edits that collection inside `/settings` as workflow cards and ordered text inputs. `/triggers` receives enabled workflow choices through `GET /api/triggers`.
- `internal/agentrun/execute.go` does not execute those steps as a workflow. It renders them as advisory context, tells the principal to `Use $do`, and adds a long Factory-specific wrapper prompt.
- The real procedural lifecycle is split across the provider-owned `.agents/skills/do/SKILL.md`, three referenced Markdown files, a skill-local Linear GraphQL script, and the Factory wrapper. The main skill and references are about 66 KiB before removing duplication.
- This split has already drifted. The installed `$do` skill still describes a sequential review fallback and a stale Factory deployment command, while Factory's wrapper and tests require parallel Claude and Codex review children and the live repository runbook uses `~/.local/bin/nags deploy --expected-commit ...`.

### Existing pinning is the correct execution seam

- `internal/triggerrouter/admission.go` resolves the selected enabled workflow while holding one registry/settings snapshot, serializes the complete value, hashes it, and stores it on the durable invocation.
- `internal/agentrun/store.go` copies that pinned value onto the Run. `internal/agentrun/launcher.go` writes private `workflow.json` in the Run directory and passes its contained path to `agent-exec`.
- The same Run and pinned value survive retries, feedback, GitHub remediation, and the post-merge segment. Terminal reflection compacts the body while retaining identity.
- Therefore the change should replace the pinned value and prompt composition, not add another runner, queue, or step interpreter.

### Policy coordination and persistence

- Workflows currently live in the versioned, private, atomic `settings.json` checkpoint. Trigger registry mutation and settings mutation already share `triggerrouter.CoordinatedWire`, which blocks writes until every dispatched wire record has a durable decision and cross-validates enabled workflow references.
- A separate `workflows.json` would require a new three-store consistency protocol without providing a user-visible benefit for this scope. Keep workflows in the atomic policy checkpoint, but extract their Go domain, API, and UI from settings.
- Autosaved authoring state has different consistency requirements from executable policy. Store it separately in a private `workflow-drafts.json`: a draft write is recoverable authoring state only, never appears in trigger summaries, and never participates in admission. Publish remains the single bridge into `settings.json` under `CoordinatedWire`.
- Use a settings schema migration because the persisted workflow shape changes. Retain explicit legacy decoding for journal and Run records because `trigger-routing.jsonl` uses strict unknown-field decoding.

### Runtime state observed during planning

- The deployed Factory was healthy at commit `f383b179d890abab1378cb670ce4964c64013539`, with a fully dispatched wire and no nonterminal Runs.
- Persisted settings were schema 1, revision 5, with the `full-sdlc` workflow, principal and child provider overrides, three attempts, and concurrency 10. Migration must preserve operator values rather than reseed compiled defaults.
- The approved native-tasks plan currently states that `$do` remains the single lifecycle policy. That premise becomes invalid after this feature and must be rebaselined before native task implementation.

## Target architecture

```text
/workflows editor
        |
        v
authenticated draft API ----> workflow-drafts.json
        |                         private, non-executable
        |
        +---- explicit Publish of exact saved draft
        |
        v
CoordinatedWire policy lock ---- /triggers published summaries
        |
        v
settings.json published policy checkpoint
        |
        v
event admission -> pinned published revision/body/digest -> Run workflow.json
                                                               |
                                                               v
                                             Factory envelope + Markdown -> Codex
                                                               |
                                                               v
                             mechanical ready/merge/deploy/completion validators
```

### Workflow model

Create `internal/workflow` and move workflow-specific validation, cloning, equality, defaults, and migration helpers into it.

The authoritative published definition is:

```go
type Definition struct {
    ID        string    `json:"id"`
    Revision  uint64    `json:"revision"`
    Name      string    `json:"name"`
    Enabled   bool      `json:"enabled"`
    Markdown  string    `json:"markdown"`
    UpdatedAt time.Time `json:"updatedAt,omitempty"`
}
```

Schema-2 published policy also owns the protected workflow binding:

```go
type ProtectedWorkflowBindings struct {
    LinearFeedback WorkflowBinding `json:"linearFeedback"`
}

type WorkflowBinding struct {
    WorkflowID string `json:"workflowId"`
}
```

The binding is always active. Operators may repoint it to another enabled published workflow from `/triggers`, but cannot disable or delete the protected route. It is separate from every generic registry rule, including the configurable `linear-comment` rule.

Persist authoring state separately:

```go
type Draft struct {
    WorkflowID           string    `json:"workflowId"`
    Revision             uint64    `json:"revision"`
    BaseWorkflowRevision uint64   `json:"baseWorkflowRevision"`
    Name                 string    `json:"name"`
    Enabled              bool      `json:"enabled"`
    Markdown             string    `json:"markdown"`
    UpdatedAt            time.Time `json:"updatedAt"`
}
```

`Revision` is the server-owned draft revision used for autosave concurrency. `BaseWorkflowRevision` is the published revision from which the draft was derived, or zero for a never-published workflow. Draft revisions do not consume workflow or policy revisions.

Contract decisions:

- IDs retain the current lowercase slug shape and 48-byte limit. A persisted ID is immutable.
- Names retain the current trimmed printable 80-byte limit.
- Markdown is literal UTF-8 source. It must be nonblank, at most 128 KiB, and contain no NUL. Canonicalize CRLF or lone CR to LF before validation, revision comparison, persistence, or hashing. Preserve headings, lists, code fences, tabs, and other ordinary Markdown source.
- Markdown has no server-side variable substitution, frontmatter semantics, include directive, shell interpretation, or executable runner field. The runtime envelope supplies task and segment context separately.
- Keep at most eight workflow IDs across the union of published definitions and never-published drafts. Cap published and draft aggregate Markdown independently at 768 KiB. Raise the private settings checkpoint limit to 2 MiB and bound each workflow API request at 1 MiB.
- Publishing increments the existing settings/policy revision and assigns the workflow a server-owned monotonically increasing revision; new published workflows start at revision 1. Name, body, or enabled-state changes all increment the published workflow revision only when promoted.
- New drafts receive a server-assigned stable workflow ID, start disabled with a small editable Markdown starter, and are persisted immediately. They become selectable in `/triggers` only after an enabled revision is successfully published.
- A workflow referenced by any rule or protected binding cannot be deleted. A workflow referenced by an enabled rule or the always-active protected binding cannot be disabled. Existing invocations and continuation Runs do not count as live references because they own complete pinned copies.
- Terminal routing and Run compaction retain workflow ID, workflow revision, and digest but remove Markdown.

### Draft and publication API

Add a strict authenticated workflow authoring API:

- `GET /api/workflows` returns the current policy revision, published definitions, current drafts, draft/publication status, draft-store availability, and per-workflow rule references. For a published workflow without a stored draft, synthesize an editor document equal to the published definition without writing it. If the draft store is unavailable, still return published definitions read-only with an explicit authoring error. It does not return pinned Run files, repository paths, commands, prompts from prior Runs, credentials, or raw trigger payloads.
- `POST /api/workflow-drafts` allocates a stable server-generated workflow ID and immediately persists a disabled starter draft. The response includes draft revision 1 and base workflow revision 0.
- `PUT /api/workflow-drafts/{id}` autosaves one document with an expected draft revision. It validates and canonicalizes the complete draft, assigns the next draft revision, and atomically replaces only that draft. For a synthesized editor document, expected draft revision 0 plus the exact current published workflow revision materializes draft revision 1. If neither a matching published definition nor an existing draft remains, return `409 Conflict` rather than recreating a discarded or deleted workflow. The draft base is server-owned and cannot be changed by the client.
- `DELETE /api/workflow-drafts/{id}` discards authoring state only when the expected draft revision and base workflow revision still match. For a published workflow, the editor falls back to the published definition. For a never-published workflow, the document disappears. A stale cross-tab discard returns `409 Conflict` and cannot remove a newer saved draft.
- `POST /api/workflow-drafts/{id}/publish` accepts the exact expected draft revision, base workflow revision, and policy revision. It promotes that persisted draft, not unacknowledged browser state.
- `DELETE /api/workflows/{id}` is a separate confirmed live-policy action with expected workflow and policy revisions and remains available when draft authoring is unavailable. A retained draft whose nonzero base points at a deleted live workflow is stale deletion residue and must be removed or quarantined during reconciliation, never treated as a new workflow.
- `409 Conflict` returns authoritative revision metadata and the current server draft or published base as appropriate. The UI preserves local text and offers explicit reload, duplicate, or manual reconciliation rather than force-overwriting either side.
- Use the existing same-origin, JSON content-type, body-limit, strict decoding, authentication, and no-partial-mutation conventions. Draft endpoints report draft-store availability independently from live service readiness.

Implement `workflow-drafts.json` as a schema-versioned, mutex-protected private store with `0600` mode, bounded strict decoding, fsync, temporary-file rename, and directory sync, following the settings store's durability pattern. Its absence means no saved drafts. Corruption is preserved for diagnosis and makes authoring endpoints unavailable, but does not degrade trigger admission, agent execution, or health of the last good published policy.

Autosave does not acquire `CoordinatedWire`. Serialize autosave, discard, publish, and delete for the same workflow ID with a keyed authoring lock while allowing unrelated drafts to save concurrently. Publish takes the keyed authoring lock before the coordinator lock, confirms that the requested draft revision is durably saved, waits for the pending-decision gate, and revalidates the draft revision, base workflow revision, policy revision, and trigger references. Only then does it write that exact document into the live workflow collection. Keep this lock order everywhere and never call back into draft mutation while holding the locks in reverse order. No admission path reads the draft store.

After a successful live write, atomically advance the saved draft's base to the new published workflow revision, increment the draft revision so stale tabs conflict, and keep it as the editor's working copy. If the process stops between those writes, reconcile before serving authoring requests by comparing canonical content: when the live definition exactly matches the saved draft, advance the base and draft revision idempotently without creating another policy revision. Mark authoring unavailable if that repair cannot be persisted. An identical publish retry returns the already-published result rather than minting a second workflow revision. Publishing a draft identical to its current published base is a no-op that returns synchronized revision metadata.

Change `GET/PUT /api/settings` to a dedicated public DTO containing only schema/revision metadata, `agents`, and `runtime`. A settings PUT clones the current internal snapshot and replaces only those fields, so a stale settings page cannot erase a workflow revision.

Change `GET /api/triggers` to return only workflow summaries needed by selectors: ID, revision, name, and enabled state. Never return workflow Markdown through the trigger endpoint.

Include the protected `linear-feedback` entry in that response with its current workflow ID and published revision. Add a strict same-origin `PUT /api/triggers/protected/linear-feedback` mutation containing the expected settings/policy revision and replacement workflow ID. The mutation calls a dedicated `CoordinatedWire` settings-policy method, requires an enabled published target, increments the policy revision, and never edits the generic trigger registry. Generic `PUT /api/triggers` remains independent. Both mutations use the same pending-decision gate and return authoritative conflict state.

### Direct agent execution

Replace `principalPrompt`'s `$do` call and duplicated procedural policy with three explicit layers:

1. A code-owned segment header identifies the task/issue, trigger kind, Run, workflow ID/revision/digest, repository context already resolved by Factory, and whether this is initial, feedback, remediation, or post-merge work.
2. The complete pinned Markdown appears verbatim between unambiguous workflow delimiters.
3. A compact code-owned runtime footer defines only the process protocol that Factory must parse or mechanically validate: helper location, private run scope, child-window containment, ready-checkpoint command, human-only merge prohibition, allowed terminal markers/blocker types, and the fact that Factory's mechanical validators are authoritative.

Procedural choices such as research method, plan gates, review rounds, implementation flow, and PR remediation belong in the editable default workflow. Trust invariants and machine protocol remain in the envelope and Go validators.

Additional execution decisions:

- The first attempt in a segment receives the full header, pinned Markdown, and footer. A provider-process retry resumes the same Codex thread with `Resume the Factory workflow from durable state` and does not substitute a live workflow revision.
- A new post-merge process receives the same original pinned Markdown plus fresh post-merge segment context.
- Every newly created Run receives an immutable pin before it can launch. Routed invocations use the admission pin. A fresh human-feedback continuation reads the dedicated protected binding and matching definition from one coordinated published-policy snapshot, then passes that definition, digest, and policy revision into `ClaimContinuation`; feedback coalesced into an existing nonterminal Run keeps that Run's original pin. Repointing A to B affects only later fresh continuations, and publication, repointing, or deletion after claim cannot change a queued A continuation. The launcher writes `workflow.json` whenever a new-shape pin is present, not only when `InvocationID` is nonempty. New continuations never consult the legacy settings trigger field; the generic `linear-comment` rule continues to evaluate the same event independently through routed admission.
- `workflow.json` remains the private strict snapshot file. Define a pinned wrapper containing the complete definition plus its digest, validate the digest over canonical execution fields when reading, and keep `prompt.txt` as the rendered execution record.
- Add `factory agent linear-graphql`, reading JSON from stdin and using the existing `LINEAR_API_KEY`, so the default workflow no longer references `.agents/skills/do/scripts/linear_graphql.py`. Keep the existing secret-filtered process environment and do not add arbitrary URL or credential flags.
- Inline the current human-gate, adversarial-review, and PR-green-loop behavior into the compiled default Markdown after reconciling it with Factory's current parallel-review prompt and current deployment runbook. Do not leave runtime includes pointing at provider skill files.
- Do not parse Markdown into steps or execute fenced blocks. The principal agent interprets the document agentically.

### Editor experience

Build `/workflows` as a source-first note workspace, not another settings form:

- A workflow list shows name, stable ID, published revision, draft status, enabled state, updated time, and trigger-reference count. Never-published documents have a Draft badge rather than a fake live revision.
- Selecting a workflow opens a document editor with a title input, immutable server-assigned ID, enabled toggle, large line-wrapped Markdown textarea, byte count, draft and published revision context, and a short reminder that only publishing affects later admissions.
- Create persists a disabled starter draft before opening it. Deleting a never-published item discards its draft. Deleting a published workflow requires a second confirmed live-policy action and explains blocking rule references.
- Debounce autosave by approximately 750 ms. Serialize requests per workflow, coalesce newer local edits behind the in-flight request, and never run parallel saves for one draft. Surface `Editing locally`, `Saving draft`, `Draft saved`, `Unpublished changes`, `Published`, `Autosave failed`, and `Conflict` states.
- Enable Publish only when the visible content is the exact acknowledged draft revision, validation passes, and no save or conflict is pending. Publish failure leaves the saved draft intact. `Discard draft` explicitly resets a published workflow to its live definition or removes a never-published draft.
- Add `beforeunload` protection and in-app navigation confirmation only while local edits have not been acknowledged by the draft store. A saved unpublished draft survives navigation and restart without a warning.
- On offline, server, or cross-tab conflict, preserve the local text in memory and stop automatic retry loops that could reorder edits. Let the operator retry, reload the server draft, duplicate into a new draft, or reconcile explicitly. Never silently replace or force-publish local Markdown.
- Preserve keyboard focus, labels, error association, and visible draft/publication state. Use a native textarea and existing Solid/CSS stack. Do not add CodeMirror, a Markdown renderer, or HTML sanitizer in v1.
- `/settings` links to `/workflows` with a short separation-of-concerns note but contains no workflow editor.

## Migration and compatibility

### Settings schema 1 to schema 2

Add an explicit decoder for both schemas. The schema-2 internal checkpoint continues to contain legacy trigger fields for old Run fallback plus `[]workflow.Definition`, agent settings, and runtime settings.

The draft store is new, optional authoring state and does not participate in settings migration. On first authoring write, create its parent/file with private permissions. Do not seed drafts for every migrated workflow; `GET /api/workflows` can synthesize an editor document from the live definition until the operator changes it.

For each schema-1 workflow:

1. Preserve ID, name, and enabled state.
2. Start workflow revision at 1.
3. Seed the new body from the self-contained compiled Full SDLC Markdown.
4. Append a clearly labeled `Migrated operator guidance` section containing the old ordered steps in their original order. This preserves the old semantics, which were always `$do` plus advisory steps.
5. Preserve legacy trigger workflow IDs, agent models/effort, attempts, concurrency, settings revision, and updated time.

Seed the schema-2 protected feedback binding from the schema-1 `triggers.linearComment.workflowId`. This is migration input only. If no retained generic registry exists, its default `linear-comment` rule may also seed from the legacy value as today, but after migration the protected binding and generic rule are distinct policy controls and neither rewrites the other.

Before replacing `settings.json`, write one private fsynced `settings.schema1.backup.json` if it does not already exist. The migration is idempotent: a valid schema-2 checkpoint wins; a valid schema-1 checkpoint plus identical backup migrates; conflicting or invalid inputs fail startup closed.

### Retained routing and Run records

- Implement backward-compatible workflow JSON decoding for the legacy `runner: "do"` plus `steps` shape because trigger routing replay uses `DisallowUnknownFields`.
- Accept legacy workflow values only when reading retained internal records. The workflow API and settings schema 2 must reject legacy runner/step fields.
- Nonterminal legacy pinned records, if any exist during development or recovery, continue through a narrow legacy prompt path that invokes `$do`; do not silently reinterpret an already admitted workflow. New admissions always pin Markdown definitions.
- Add a dedicated non-prunable `workflowRollbackIncompatible` compatibility marker to the schema-2 settings checkpoint. It starts false during migration and is set monotonically, no later than the first schema-2 workflow admission or fresh new-shape continuation claim, before the corresponding routing or Run record is persisted. A conservative marker write without a completed admission is safe; clearing it is never allowed. Admission, terminal compaction, Run/routing pruning, restart, and ordinary settings/workflow updates preserve it. Use this marker, not retained prunable records, for preflight and rollback decisions.
- Before production activation, require a quiescence preflight: service ready, wire pending count zero, routing projection healthy, and no nonterminal Run or invocation with a legacy workflow shape. The observed planning state already met this condition, but deployment must recheck it.
- Keep the provider-installed `$do` skill in place through the rollback window. New Factory prompts do not use it. Removing the global skill or its Network source is a separate decision because it still supports direct interactive `$do ISSUE-NNN` use outside Factory.

### Rollback

- Add a read-only `factory workflow-rollback-preflight --settings-backup <path> --trigger-registry <path>` command. It strictly decodes the schema-1 backup and retained registry with bounded input, applies the prior release's registry-versus-settings validation, reports no secrets or file contents, and never writes either file.
- Before any schema-2 workflow admission or fresh new-shape continuation is durably recorded, a stopped and quiescent service can roll back to the prior binary only while the dedicated compatibility marker remains false, no new-shape routing or Run record exists, and the rollback-preflight command proves the retained trigger registry is valid against the exact schema-1 backup.
- If the retained registry references a schema-2-only workflow that the backup does not contain, the preflight fails even when no admission occurred and the marker is false. Do not restore the backup; use a schema-2-aware verified release or a forward corrective commit.
- After the first schema-2 admission, an older binary cannot replay the strict routing/Run state even if no operator has published an edit. Recovery must use a schema-2-aware previously verified release or a forward corrective commit deployed from clean merged Factory `main`; never promise an ordinary prior-binary or plain code-revert recovery, and never silently translate the new body back to short steps.
- Preserve the backup and workflow revisions for diagnosis. Do not rewrite trigger-routing or Run journals during rollback.

## Implementation phases

### Phase 1: Extract the workflow domain and implement migration

Primary files:

- New `internal/workflow/model.go`, `draft.go`, `draft_store.go`, `defaults.go`, `defaults/full-sdlc.md`, and focused tests.
- `internal/settings/settings.go`, `store.go`, and tests.
- Compatibility fixtures covering current schema-1 `settings.json`, legacy routing operations, and legacy pinned Runs.

Tasks:

1. Define the Markdown workflow and draft models, clone/equality helpers, editable and pinned validation modes, independent draft/published revision rules, body/aggregate limits, embedded default, and the non-prunable schema-2 workflow rollback-compatibility marker.
2. Reconcile the current `$do` skill, its three references, `principalPrompt`, ENG-42 parallel-review behavior, and the current Factory runbook into one self-contained compiled Full SDLC Markdown document.
3. Change settings to schema 2 and implement strict schema-1 migration with private backup, operator-value preservation, protected-feedback binding seeding, appended legacy guidance, idempotence, and fail-closed corruption handling.
4. Retain strict legacy workflow decoding for internal routing/Run replay without accepting legacy fields from new API writes.
5. Implement the private versioned draft store with per-document optimistic revisions, atomic durability, missing-file behavior, strict bounds, corruption preservation, and no dependency from admission or execution paths.
6. Add the read-only prior-binary rollback preflight and a no-admission fixture where a retained enabled rule references a schema-2-only workflow absent from the schema-1 backup; prove preflight rejection leaves both files byte-identical.
7. Run focused domain/store tests under the race detector.

Success criteria:

- The current production-shaped schema-1 fixture migrates byte-for-byte for non-workflow settings and preserves every workflow reference.
- The compiled workflow contains the complete current behavioral contract but no repo-relative skill include or stale deployment command.
- A malformed, oversized, conflicting-backup, or partially migrated checkpoint fails startup without replacing the last good file.
- New editable definitions cannot contain legacy runner/steps.
- Draft writes survive reopen and concurrent access; a missing or unavailable draft store never changes or blocks the last good published policy.

Suggested commit: `Factory: define Markdown workflows`

### Phase 2: Add draft, publication, and slim settings APIs

Primary files:

- `internal/triggerrouter/coordinator.go` and tests.
- `internal/triggerregistry/model.go`, `defaults.go`, and tests.
- New `internal/server/workflows.go` and tests.
- `internal/server/server.go`, `triggers.go`, `server_test.go`.
- `internal/viewerauth/auth.go` and tests.
- `main.go` wiring.

Tasks:

1. Add coordinator methods to snapshot, publish, and delete live workflows and to repoint the protected feedback binding under the existing policy lock and pending-decision gate.
2. Validate generic registry and protected-binding references against the proposed workflow collection. Reject disable/delete conflicts without mutating either snapshot.
3. Add authenticated `GET /api/workflows`, draft create/autosave/discard, exact-draft publish, confirmed live delete, and protected `GET /workflows` routing.
4. Make updates to an existing draft conditional on its draft revision; revision-zero materialization also requires the exact published workflow revision. Make discard conditional on draft and base workflow revisions. Make publish conditional on draft revision, base workflow revision, and policy revision, with idempotent reconciliation after a publish/draft-store crash boundary. Missing or stale state returns `409` instead of recreating or deleting another tab's work.
5. Return reference metadata for enabled and disabled rules plus the protected feedback binding. Return only published workflow summaries from `/api/triggers`, expose the protected binding as non-disableable, and add its dedicated optimistic repoint mutation without coupling it to generic registry updates.
6. Replace settings API serialization with the narrow agent/runtime DTO and preserve the live workflow collection during settings writes.
7. Add `/workflows` to OAuth return-path allowlisting and canonical-route tests.

Success criteria:

- Draft create/edit survives navigation without changing policy or waiting on wire state.
- Workflow publish/enable/disable and live delete are atomic and optimistic. Publish promotes exactly the acknowledged draft revision.
- Workflow publication and trigger saves serialize against pending wire decisions and each other.
- A settings save cannot overwrite workflows, and a trigger response cannot leak Markdown.
- Every rejected publication leaves live workflow and policy revisions unchanged. A failed publication leaves the saved draft available for retry.

Suggested commit: `Factory: add workflow policy API`

### Phase 3: Execute pinned Markdown without `$do`

Primary files:

- `internal/agentrun/execute.go`, `execute_test.go`.
- `internal/agentrun/launcher.go`, `store.go`, `invocation_test.go`, and related tests.
- `internal/triggerrouter/admission.go`, `model.go`, `store.go`, and tests.
- `agent_commands.go` and `agent_commands_test.go`.

Tasks:

1. Change invocation and Run types to pin the new workflow definition, revision, digest, and applicable policy revision while preserving legacy JSON decoding and terminal compaction. Extend fresh continuation claims to accept the definition selected by the dedicated protected feedback binding, preserve the pin when feedback resumes an active Run, and write the compatibility marker before persisting the first new-shape routing or continuation record.
2. Keep contained `0600` workflow snapshot validation and reject missing, malformed, mismatched-digest, symlinked, or out-of-Run paths.
3. Replace the principal prompt with the header, verbatim Markdown, and compact runtime footer. Update retry text to say Factory workflow, not `/do` run.
4. Preserve initial, feedback, GitHub remediation, and post-merge segment context without replacing the pinned body. Make the launcher write the contained snapshot for every new-shape pinned Run regardless of whether it originated from a generic invocation or fresh feedback continuation; restrict live-settings fallback to identifiable retained schema-1 Runs and remove new-continuation reads of the legacy settings trigger field.
5. Add the stdin-only `factory agent linear-graphql` helper and update the default workflow to use it.
6. Remove all new-run prompt dependence on `$do`, `.agents/skills/do`, and skill reference files. Keep only the internal legacy replay branch.

Success criteria:

- Prompt tests prove exact Markdown preservation, delimiters, workflow identity, segment context, terminal protocol, checkpoint command, human-only merge prohibition, and absence of `$do` wording for new definitions.
- Editing or deleting the live workflow after admission does not change the invocation, Run, workflow file, retry, or post-merge prompt.
- Terminal compaction drops Markdown but retains ID, revision, and digest.
- Legacy retained fixtures still open; new definitions never take the legacy execution branch.

Suggested commit: `Factory: execute pinned workflow prompts`

### Phase 4: Build the `/workflows` note editor and simplify `/settings`

Primary files:

- `frontend/src/index.tsx`.
- `frontend/src/styles.css`.
- Frontend lockfile only if an existing build tool changes it; no new runtime dependency is planned.

Tasks:

1. Add workflow API types, clients, route dispatch, shared navigation item, loading/error states, and the list/detail workspace.
2. Implement disabled-by-default create, immutable persisted ID, name/body editing, enablement, confirmed delete, reference explanations, byte counts, and validation messaging.
3. Implement debounced serialized autosave, coalescing, exact-revision publish, discard, failure recovery, and conflict handling without silent draft replacement.
4. Remove workflow CRUD functions and step-list styles from Settings. Keep provider and capacity controls and add a link to Workflows.
5. Update trigger selector types to consume summaries without Markdown.
6. Verify responsive, keyboard, focus, local-dirty, saved-draft, restart recovery, publish, conflict, offline, empty, and server-error behavior in the running app after first checking whether a server already exists. Stop any temporary process started for verification.

Success criteria:

- Workflow creation and editing feel like editing a Markdown note rather than manipulating step rows.
- Draft edits save automatically and survive navigation. Operational changes require explicit publication and clearly state that only later admissions use them.
- `/settings`, `/workflows`, and `/triggers` each have one coherent responsibility at desktop and mobile widths.

Suggested commit: `Factory: add workflow authoring workspace`

### Phase 5: Documentation, compatibility audit, and publication

Primary files:

- `README.md`.
- Existing approved/planning docs only where they state current runtime behavior.
- `/Users/tom/notes/agent/plans/planning/2026-07-14-factory-native-tasks.md` before ENG-46 implementation begins.

Tasks:

1. Document `/workflows`, draft/publication API shape, autosave semantics, Markdown execution, deliberate publication, pinning, migration backup, compatibility boundary, and recovery.
2. Remove README statements that Factory principals run `$do` or depend on provider-installed workflow assets. Preserve documentation for direct interactive `$do` only in its owning Network repository if that use remains supported.
3. Rebaseline the native-tasks plan so provider-neutral task execution uses pinned Factory Markdown rather than a provider-owned `$do` skill. This is a planning correction, not native-task implementation.
4. Search all current code and operator docs for stale `Use $do`, `runner: do`, ordered workflow steps, repo-relative skill helpers, sequential reviewer fallback, and obsolete deploy commands.
5. Run the complete verification matrix, inspect the full diff, and publish through the normal human-only merge gate.

Success criteria:

- Current documentation has one source of truth for Factory workflow execution.
- No new Factory path requires the external skill, while the documented rollback window remains honest.
- All required repository verification passes from a clean implementation worktree.

Suggested commit: `Factory: document workflow execution`

## Impacted interfaces and file-level handoff

| Area | Required change |
| --- | --- |
| `internal/workflow` | New Markdown definition and draft models, independent revisioning, private draft store, validation, compiled default, legacy decoder, and migration helpers. |
| `internal/settings` | Schema 2, workflow-domain composition, protected feedback binding, schema-1 backup/migration and rollback preflight input, narrow agent/runtime update support, larger private checkpoint bound. |
| `internal/triggerregistry` | Validate and default against workflow definitions rather than settings-owned step records; enforce generic-rule reference behavior and reusable prior-binary backup validation. |
| `internal/triggerrouter` | Coordinate live workflow publication/deletion and protected-binding repoints, pin published revision/body/digest, retain policy revision, compact safely, replay legacy journal values. |
| `internal/agentrun` | Store/read new pinned definitions, compose direct prompts, preserve same body across segments, keep mechanical lifecycle gates unchanged. |
| `agent_commands.go` | Load new snapshots and add the Factory-owned Linear GraphQL helper used by the compiled workflow. |
| `internal/server` | Add combined workflow read, draft autosave/discard, exact-draft publication and live-delete handlers; pin protected feedback continuations; expose the protected binding separately from generic triggers; slim settings DTO; summary-only trigger choices; private page route. |
| `internal/viewerauth` | Allow `/workflows` as a protected return destination. |
| `frontend/src/index.tsx` | Add route/nav/API/list/editor, serialized autosave and publish state machine, remove settings workflow cards, update trigger summaries. |
| `frontend/src/styles.css` | Add note workspace and remove obsolete step-editor rules after verifying no shared selectors depend on them. |
| `README.md` | Describe the new authority split, migration, API, recovery, and direct execution. |
| Native tasks plan | Replace its `$do`-as-single-policy premise before implementation starts. |

## Non-goals

- A visual Markdown renderer, WYSIWYG editor, CodeMirror integration, attachments, includes, reusable workflow fragments, or workflow import/export.
- Template variables, secrets in workflow bodies, shell runners, arbitrary provider flags, conditional step engines, DAGs, or server-side Markdown interpretation.
- Per-project workflow catalogs, permissions beyond the current authenticated operator boundary, collaborative cursor editing, or browsing/restoring historical draft or published revisions. The current autosaved draft and current published revision are both in scope.
- Changing provider models/effort, principal attempts, manager concurrency semantics, trigger filter semantics, schedule semantics, repository routing, task identity, or native-task storage.
- Changing lifecycle contract v1, ready checkpoint schema, allowed blocker types, human merge authority, exact verified-head validation, deployment receipts, or completion evidence.
- Deleting the provider-owned `$do` skill or removing direct interactive `$do ISSUE-NNN` support from Network in the same Factory PR.

## Security and failure handling

- Workflow Markdown is trusted operator-authored execution policy and is sent to an agent that already has elevated repository permissions. Drafts are equally sensitive even though they are non-executable. Authenticate every read/write and make draft, published revision, and digest identity visible where applicable.
- Do not render Markdown as HTML in v1. This avoids a new stored-XSS and sanitizer boundary.
- Do not interpolate Markdown into a shell, CLI flag, filename, environment variable, or URL. Feed the composed prompt through stdin as today.
- Keep secrets out of workflow responses, prompt templates, logs, browser state, and error bodies. `linear-graphql` reads credentials only from the inherited environment.
- A workflow instruction cannot waive Go-enforced routing, run ownership, human merge, checkpoint, exact-head, deployment-source, completion, or cleanup validation.
- Startup fails closed on invalid schema-2 published policy or unreplayable legacy records. Published mutation failures retain the prior in-memory snapshot and private file.
- A missing draft file is an empty store. Draft corruption or I/O failure is preserved and surfaced as authoring unavailable without changing readiness, admission, or agent execution against published policy. Never silently recreate over a corrupt draft file.
- Draft autosave is never a policy mutation, never takes the wire coordinator lock, and never changes a published workflow, settings revision, trigger selector, or admitted Run.
- Body and aggregate limits bound browser requests, draft/settings JSON files, routing journals, Run snapshots, prompt size, and memory copying.

## Verification matrix

| Acceptance criterion or risk | Exact verification |
| --- | --- |
| Workflow model/default/migration | `go test ./internal/workflow ./internal/settings -count=1` with schema-1 production-shaped fixtures, custom steps, operator overrides, backup, reopen, corruption, bounds, and idempotence cases. |
| Draft durability and isolation | Workflow store tests cover create, serialized revision updates, reopen, discard, absent file, `0600`, bounds, stale revision, corruption preservation, write failure, and concurrent distinct drafts; assert every operation leaves live policy bytes/revision unchanged. |
| Race-safe persistence | `go test -race ./internal/workflow ./internal/settings -count=1`. |
| Exact saved-draft publication | Server/coordinator tests autosave draft revisions 1 through N, publish expected N, and prove the live body equals N while unsaved N+1 is excluded. Reject stale draft, base, and policy revisions without live mutation; reject stale autosave after live deletion and stale cross-tab discard without recreating or deleting state. |
| Publish crash idempotence | Inject failure after the settings write but before draft-base update; reopen/reconcile, retry the same publish, and prove one published workflow revision and one policy revision were created. |
| Coordinated publish/disable/delete | `go test ./internal/triggerrouter ./internal/triggerregistry -run 'Test.*(Workflow|Policy|Admission)' -count=1`. |
| No publish overtakes pending admission | Coordinator tests hold one undecided wire record, allow independent draft autosaves, reject publish/trigger/live-delete mutations, catch up, then publish exactly one later revision. |
| Trigger reference protection | Server/coordinator tests for protected-binding and enabled-rule disable, protected-binding and any-rule delete, independent generic-rule and protected-binding repoints, disabled unreferenced delete, and no mutation on rejection. |
| Auth, origin, strict JSON, size, conflict | `go test ./internal/server ./internal/viewerauth -run 'Test.*(Workflow|Settings|Private|Return)' -count=1`; assert 401/redirect, 403, 400, 409, 413, and 415 paths. |
| Settings cannot overwrite workflows | Open settings and workflow editor snapshots, publish a workflow edit, submit stale settings, verify 409 and unchanged workflow body/revision. |
| Trigger API does not leak Markdown | Decode `GET /api/triggers`, assert summary fields only, and search response bytes for a unique workflow-body sentinel. |
| Direct prompt execution | `go test ./internal/agentrun -run 'Test.*(Workflow|PrincipalPrompt|PostMerge|Continuation)' -count=1`; assert literal Markdown, ID/revision/digest, delimiter order, segment context, footer, and no new-run `$do` text. |
| Pinned behavior across edits | Admit revision 1, publish revision 2, launch/retry/resume/post-merge revision 1, then admit a new event and prove only it receives revision 2. Bind protected feedback to A, claim a fresh A continuation, repoint to B, and prove queued/active A Runs retain A while only a later fresh continuation pins B. Prove A cannot be disabled/deleted until neither the binding nor generic rules reference it, and prove the generic `linear-comment` rule remains additive and independently configurable. |
| Monotonic rollback boundary | Focused settings/router/Run tests set the compatibility marker before the first schema-2 admission and fresh continuation, compact and prune terminal routing and Run records, reopen every store, and prove the marker remains true and cannot be cleared by later settings/workflow writes. |
| Pre-admission rollback compatibility | A production-shaped schema-1 backup plus retained registry passes the read-only preflight. A no-admission fixture with an enabled retained rule referencing a schema-2-only workflow fails preflight, leaves both files byte-identical, and selects schema-2-aware recovery. |
| Legacy journal and Run compatibility | Replay committed legacy JSON fixtures through strict trigger-routing open and Run open; exercise the legacy execution branch only for legacy pinned values. |
| Private contained snapshot | Tests reject wrong mode, symlink, relative/outside path, malformed JSON, digest mismatch, unknown fields, and missing body. |
| Linear helper portability | `go test . -run 'Test.*LinearGraphQL' -count=1` using `httptest`; verify stdin JSON, GraphQL errors, mutation failure, missing key, network error, and no credential in args/output. |
| Frontend type safety | `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck`. |
| Frozen frontend build | `export MISE_BUN_VERSION=1.3.11; bun install --cwd frontend --frozen-lockfile && bun run --cwd frontend build`. |
| Editor UX | In the authenticated running app, exercise load, empty, immediate draft create, multiline/code-fence edits, debounce/coalescing, navigate/restart recovery, publish gating, exact saved-draft publish, discard, cross-tab conflict, explicit reload/duplicate, referenced disable/delete, offline recovery, keyboard focus, and desktop/mobile widths; check console/network and stop temporary processes. |
| Stale implementation text | `rg -n 'Use \$do|The /do skill owns|runner.*do|Ordered declarative steps|\.agents/skills/do' internal frontend/src README.md --glob '!frontend/dist/**'` returns only intentional legacy compatibility text. |
| Complete Go correctness | `go test ./...`; `go test -race ./...`; `go vet ./...`. |
| Diff and secret hygiene | `git diff --check`; `git status --short`; inspect `git diff origin/main...HEAD`; search changed files for credentials, absolute private paths, debug output, and bundled prompt artifacts. |

## Rollout, deployment, and recovery

### Pre-merge

1. Capture the exact schema-1 and retained-record fixtures in tests before changing types.
2. Keep implementation in one issue-scoped Worktrunk worktree and commit by the logical phases above.
3. Verify no unrelated `.worktrees/` or user-owned changes enter the diff.
4. Record the exact verified PR head only after every command in the verification matrix passes and browser behavior has been inspected.

### Pre-deploy migration gate

From clean updated primary `main`, before invoking deployment:

```bash
curl -fsS http://127.0.0.1:8092/api/healthz | jq -e '.status == "ok" and .wire.pending == 0'
jq -e '[.runs[] | select(.state == "pending" or .state == "post_merge_pending" or .state == "starting" or .state == "running" or .state == "awaiting_human_merge")] | length == 0' \
  "$HOME/.local/share/factory/data/agent-runs.json"
cp -p "$HOME/.local/share/factory/data/settings.json" \
  "$HOME/.local/share/factory/data/settings.pre-workflows-deploy.json"
```

Also verify the trigger-routing store opens through the release candidate's focused migration test. Do not manually edit or truncate live JSON or journals.

Through an authenticated `GET /api/triggers`, require the sum of `ruleStatus[].outstanding` to be zero. This closes the gap where a queued invocation exists before its Run is created.

### Exact deployment

After a human merges the exact verified head, use Worktrunk to resolve the primary checkout, fetch/prune, and fast-forward clean `main` to `origin/main`. From `/Users/tom/repos/tomnagengast/factory`, require clean status and exact upstream equality, then run the current documented CLI:

```bash
test "$(git branch --show-current)" = main
test -z "$(git status --porcelain)"
git fetch --prune origin
git merge --ff-only origin/main
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
```

Never deploy from the T9 mirror or an issue worktree.

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

Use an authenticated browser/API session to read `/api/workflows`, confirm `full-sdlc` has a Markdown body and published revision, confirm `/api/settings` omits workflows, and confirm `/api/triggers` returns published summaries only. To canary authoring, record the current settings/policy revision, create a never-published disabled draft, autosave a unique body, navigate away and read it back, then discard it. Verify the settings file and policy revision never changed, the draft file is `0600`, and wire dispatch remained caught up. Do not publish a canary edit to the production default workflow merely to test persistence.

Finally confirm the current deployment receipt, public/local build identity, clean primary checkout, human-merged verified head ancestry, remote branch auto-deletion, and Worktrunk cleanup.

### Recovery

- If migration prevents startup before any schema-2 workflow admission or fresh new-shape continuation is durably recorded, stop the service, preserve the invalid schema-2 file, require the dedicated compatibility marker to be false, prove no new-shape routing or Run record exists, and run `factory workflow-rollback-preflight --settings-backup "$HOME/.local/share/factory/data/settings.schema1.backup.json" --trigger-registry "$HOME/.local/share/factory/data/triggers.json"`. Only after it passes may recovery restore the private schema-1 backup and reactivate the previously verified release through `~/.local/bin/nags rollback factory --to <deployment-id>`. If it fails, preserve both files and use schema-2-aware forward recovery. Recheck local/public identity and wire health.
- If any schema-2 workflow admission is durable, do not restore the old schema or use a prior binary that cannot decode it. Use a schema-2-aware verified release or make a corrective commit on Factory `main`, deploy that exact merged commit, and preserve current settings/routing/Run evidence.
- If health is degraded because a wire record is pending, inspect health, `system-events.jsonl`, `trigger-routing.jsonl`, settings, and service logs. Correct the forward policy or decoder and allow ordered catch-up. Never delete or truncate the wire or routing journal.
- If draft authoring is unavailable, preserve `workflow-drafts.json` and logs, keep published triggers and Runs operating, and repair or recover the draft store separately. Never replace corrupt drafts with an empty file or promote browser-local content as part of recovery.
- If the new default workflow produces an incorrect agent behavior, disable affected trigger rules first through the authenticated policy surface, preserve the admitted pinned evidence, correct the workflow revision, and re-enable only after validation. Existing admitted invocations remain pinned and must be handled explicitly rather than silently changed.

## Alternatives considered

1. **Only move the existing cards to `/workflows`.** Rejected because it preserves the real problem: steps remain advisory while procedural authority is duplicated in `$do` and the Factory wrapper.
2. **Persist a separate `workflows.json`.** Rejected for this scope because the current settings checkpoint and coordinator already provide the required atomic publication boundary. A separate file adds crash consistency and migration work without improving the editor or pinning contract.
3. **Edit provider skill files from the browser.** Rejected because those files belong to another repository/release boundary and do not give admitted Runs immutable revision identity.
4. **Render Markdown preview in v1.** Rejected because source editing satisfies the note-like authoring goal without adding a Markdown dependency, stored-HTML surface, sanitizer policy, or edit/preview fidelity problem.
5. **Autosave directly into published policy.** Rejected because each keystroke would change executable policy for later admissions and contend with the wire gate. Autosave instead targets private non-executable drafts; deliberate exact-draft publication remains the auditable policy transition.
6. **Build a step/DAG engine.** Rejected because the user explicitly wants agentic execution of a skill-like prompt, and the existing agent already owns procedural interpretation.
7. **Put all safeguards in the editable default workflow.** Rejected because prompt text cannot enforce human merge, verified head, deployment source, routing, or terminal evidence. Those controls already have mechanical Go implementations and remain authoritative.
8. **Delete `$do` immediately.** Rejected because retained legacy records and emergency schema-1 rollback need the old behavior during rollout, and direct interactive `$do` outside Factory is a separate product decision.

## Unresolved questions

None. The implementation can proceed with the source-first editor, private autosaved draft revisions, deliberate exact-draft publication into the atomic settings-backed workflow collection, direct pinned Markdown execution, compatibility window, and unchanged mechanical lifecycle gates described above.
