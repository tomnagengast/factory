# ENG-58 Predicate Extraction and Run Pinning Plan

> updated: 2026-07-17T00:24:41Z

## Issue context and acceptance criteria

- Issue: [ENG-58, Extract predicate atoms and fix run pinning (durable workflow engine, Phase 0)](https://linear.app/nags-cloud/issue/ENG-58/extract-predicate-atoms-and-fix-run-pinning-durable-workflow-engine).
- Implement only Phase 0 of the locked durable-workflow design: extract reusable named evidence predicates and the `sdlc-deploy` and `sdlc-repo-only` profiles, re-express current mechanical validation through those profiles, and pin every newly claimed run to the exact workflow selected at admission.
- Preserve current decisions, short-circuit and evidence-read ordering, retry versus terminal behavior, typed blocker handling, reason text and ordering, JSON compatibility, trust boundaries, and deployment contract.
- Prove parity with table-driven predicate tests and committed recorded golden cases that cover every atom, both public profiles, internal validation profiles, supported blockers, multi-failure order, and retry/terminal outcomes at profile boundaries.
- Prove initial claims persist an immutable cloned workflow pin, digest, and policy revision; deduplicated or coalesced claims retain the original pin; all continuation segments retain it; and only retained pre-fix unpinned records use the compatibility fallback.
- Do not implement journal/spine state, host calls, wake routing, advisory profiles, authoring changes, later-phase schema changes, or unrelated cleanup.
- Research authorization: Linear comment `f498b42f-8bbe-4127-a4e9-5871d195eca2` received Tom's `white_check_mark` reaction. The authoritative refresh at 2026-07-17T00:12:00Z also showed the current `Yolo` label, authorizing progression through the complete reviewed plan.

## Repository, branch, pull request, and sequencing

The Linear project routes ENG-58 to `tomnagengast/factory` at `/Users/tom/repos/tomnagengast/factory`, whose normalized origin matches the managed repository. No caller or interface requires another admitted repository.

- Repository: `tomnagengast/factory`.
- Branch: `eng-58-extract-predicate-atoms-and-fix-run-pinning-durable-workflow-engine-phase-0`.
- Draft PR: [#20](https://github.com/tomnagengast/factory/pull/20).
- Merge sequence: the single PR is human-merged with **Create a merge commit** only after its exact verified head is checkpointed.
- Deployment sequence: prove the GitHub merge commit contains that checkpointed head, update the one clean managed `main` checkout, then deploy Factory from that checkout. Never deploy from the issue worktree or T9 mirror.

## Evidence-backed research and root cause

Complete evidence is in `plans/planning/eng-58-extract-predicate-atoms-and-fix-run-pinning-durable-workflow-engine-phase-0/research.md`.

### Mechanical validation

1. `MechanicalCompletionValidator.Validate` is an ordered state machine, not a single boolean expression. It distinguishes pre-checkpoint process failure, allowed pre-checkpoint blockers, missing ready checkpoints, authoritative PR refresh failures, open PRs, post-ready blockers, merged identity, system evidence, and completion requirements. Moving evidence reads into a generic evaluator would change retries and side effects.
2. Ready-checkpoint shape, run-directory identity, manager binding, open-ready PR identity, merged PR identity, safeguard state, health identity, deployment receipt identity, repository source identity, completion state, and blocker backing are currently spread across `ready.go`, `manager.go`, `github.go`, `completion_system.go`, and `completion.go`.
3. `SystemCompletionEvidence` deliberately performs I/O in order, including child-file reads, deployment receipt and health reads, `git fetch --prune`, repository inspection, Linear reads, and a possible Factory-native task-completion mutation. These effects and their error classification must remain in `agentrun`.
4. Completion problem strings and their ordering are persisted and observable. Deployable completion checks receipt, health, source, merge ancestry, verified-head ancestry, safeguards, remote branch cleanup, worktree cleanup, task completion, and child completion. Repository-only completion omits receipt and health but retains the remaining order.
5. Cleanup blocker backing is any-of four failed cleanup facts; completion profiles are all-of. Typed GitHub/Linear authentication errors remain authoritative. The compatibility boolean in injected `CompletionEvidence` must not silently change.

### Run pinning

1. Generic trigger rules, native task admissions, and protected Linear comment continuations already select, digest, persist, validate, and launch a full workflow snapshot. Wakes resume the same stored run and pin.
2. Public `Store.Claim` supplies no pin, and private claim validation only requires one for history-based continuations. The production-reachable project-provider bootstrap and retained legacy Linear-label server path therefore create unpinned runs.
3. An unpinned run resolves live settings at process launch, so a queued run or later segment can execute a workflow published, rebound, disabled, or removed after admission. That violates durable run identity.
4. The store already persists all pin fields. Current production has no nonterminal unpinned run, so preventing new unpinned records needs no schema migration or backfill. A narrow fallback remains necessary for a retained pre-fix record that could exist during a rolling deployment.

Root cause: Factory has stable mechanical evidence and workflow snapshot machinery, but evidence decisions are encoded as local conjunctions and validation branches rather than a reusable source-neutral vocabulary, while one public admission API makes the workflow snapshot optional. Those two asymmetries block the later durable engine without requiring later-engine scope in ENG-58.

## Decisions

### Predicate package and evaluation contract

1. **Create a source-neutral `internal/predicate` package.** It owns stable atom identifiers, ordered profile requirements, all-of/any-of mode, facts, results, and a deterministic evaluator. It does not import `agentrun`, execute commands, read files or APIs, mutate tasks, or classify transport errors.
2. **Use stable string atom identifiers with explicit parameters.** An atom identifies one indivisible claim such as `checkout.clean` or `health.commit_matches`; parameters carry comparison context without importing lifecycle types. Constants make spelling reviewable and future journal data stable.
3. **Evaluate through an evidence-source interface.** A profile asks a source for one fact at a time in declared order. A fact records atom, pass/fail, and the existing failure text. Evaluation preserves ordered facts and ordered failures. Missing facts, duplicate/conflicting facts, unsupported atoms, and source errors fail closed with explicit errors rather than manufacturing success.
4. **Support both conjunction and disjunction.** Public completion profiles use ordered all-of semantics. Cleanup blocker backing uses ordered any-of semantics. An all-of evaluation visits all requirements when the legacy caller accumulates every problem; an any-of evaluation can stop on its first supporting fact because only the support decision is observable.
5. **Keep orchestration and effects in `agentrun`.** Adapters expose already-read checkpoint, snapshot, health, receipt, repository, task, child, and completion evidence as facts. Existing caller choreography decides when to collect evidence, how to map read errors, and whether to reject, accept, or repark.

### Atom and profile boundaries

6. **Extract the complete Phase 0 atom vocabulary.** Named groups cover checkpoint shape and run-directory identity; manager/checkpoint binding; ready and merged PR identity; review/check safeguards; health identity; checkout/source identity; deployment receipt identity; merge and verified-head ancestry; remote branch, worktree, task, and child cleanup; typed authentication/deployment support; and cleanup-blocker disjunction.
7. **Keep two public contract profiles.** `sdlc-deploy` contains deploy receipt, health identity, clean updated main, ancestry, safeguard, cleanup, task, and child requirements in current failure order. `sdlc-repo-only` contains the same sequence without receipt and health. Internal ordered profiles cover checkpoint shape, binding, ready PR, merged PR, safeguards, health, checkout, receipt, and each typed blocker.
8. **Preserve legacy text at the adapter boundary.** Profile requirements carry the exact existing user-facing failures. Callers retain their current contextual prefixes and joins. This avoids exposing evaluator-internal wording in `Run.Detail`, terminal rejection, completion validation, API views, or Linear evidence.
9. **Preserve intentional validation variants.** The closed/merged race path continues checking only base/head identity; it does not reuse the stricter final merged profile. Fast blocker paths do not gain system-evidence reads. `completionAncestor` continues treating all nonzero exits as false for Phase 0 parity.
10. **Record parity as data.** Add committed JSON golden cases containing compact input facts and expected ordered atom outcomes, decisions, problems, blocker support, and retry/terminal result. Tests load every case through production profiles. A frozen test-only legacy evaluator compares the complete completion boolean matrix; production has one decision implementation, the profiles.

### Admission-time workflow pinning

11. **Replace unpinned initial claim input with an explicit candidate claim.** Add an initial `Claim` value containing `Trigger` plus a workflow candidate: full `WorkflowPin`, `WorkflowDigest`, opaque `PolicyRevision`, and an optional resolution error. `Store.Claim` accepts that value; `ContinuationClaim` remains a distinct history-based type. A caller whose current workflow selection fails still submits the trigger and the error-carrying candidate, so an idempotent retry can reach durable deduplication rather than failing early. No production or test caller can create a new run through the public API without a successfully resolved workflow snapshot.
12. **Validate only when creation is necessary.** Under the store lock, delivery deduplication and active task coalescing run before the candidate resolution error or pin metadata is examined and return the original run unchanged. A genuinely new run rejects a carried resolution error, then requires a complete enabled pin and matching digest before deep-cloning the pin and opaque policy revision into durable state. Policy revision is an exact settings-snapshot coordinate, not a store-verifiable validity claim; zero is allowed and preserved.
13. **Pin every admission path at selection time without defeating retries.** Generic and native paths adapt to the candidate claim without changing selection. The legacy server label fallback and project-provider bootstrap attempt `WorkflowForTrigger`, digest the result, capture the settings revision, and mark rollback incompatibility. If selection, digesting, or boundary marking fails, they carry that error in the candidate and still call `Store.Claim`; the store ignores it only for a duplicate/coalesced run and otherwise returns it without creating state. Project-provider bootstrap receives the narrow settings/policy dependency from `main`.
14. **Do not re-resolve on continuation.** Feedback, GitHub, remediation, and post-merge segments retain the stored pin. Tests change live settings after claim and prove subsequent preparation still writes the original `workflow.json` and `--workflow-file`.
15. **Keep a documented compatibility fallback.** Launcher/`agent-exec` live-settings resolution remains only when loading an already persisted pre-fix run whose pin is empty. A fixture demonstrates that compatibility. All newly claimed runs take the snapshot path. Removing the fallback or changing the store schema is deferred to a later migration.

## Alternatives considered

- **Move I/O into predicate sources:** rejected because generic evaluation would change evidence-read order, `git fetch` timing, task mutation timing, and retry classification.
- **Make predicates functions over `agentrun` structs:** rejected because it couples the reusable package to current orchestration types and creates an import-cycle boundary for callers that must import `predicate`.
- **Expose only the two completion conjunctions:** rejected because ENG-58 explicitly requires atoms and re-expression of existing validation, and later phases need stable named evidence across ready, safeguard, source, blocker, and completion boundaries.
- **Return only a boolean from evaluation:** rejected because exact ordered failures, auditability, golden parity, and later evidence recording require per-atom outcomes.
- **Short-circuit every profile:** rejected because completion currently accumulates every failing reason in order. Profile mode and caller-visible behavior determine traversal.
- **Auto-pin a default inside `Store.Claim`:** rejected because the store has no authoritative settings/routing context, and late default lookup recreates the admission/launch race.
- **Resolve or reject a pin before calling the store:** rejected because an idempotent retry must reach durable dedupe/coalescing and return its original run even if today's workflow binding is missing or different. The error-carrying candidate preserves the selection failure for genuinely new runs without resolving settings while holding the run-store lock.
- **Backfill old unpinned records from current settings:** rejected because current settings cannot reconstruct the workflow selected when the record was admitted.
- **Remove the legacy server or project bootstrap paths:** rejected because both remain tested or production-reachable and the acceptance criterion is every new run, not only today's dominant trigger route.
- **Begin journal, wake, authoring, or advisory work:** deferred to their locked later phases; none is necessary to satisfy Phase 0.

## Non-goals

- Changing workflow Markdown, workflow selection semantics, trigger routing, provider choice, one-run ownership, human merge authority, exact-head checkpointing, or deployment-source policy.
- Adding journal entries, durable spine state, host calls, wake queues, advisory contracts, authoring validation, code generation, migrations, or a new lifecycle schema/version.
- Correcting legacy `completionAncestor` error classification, removing `ExternalAuthenticationFailure`, rewriting stored completion reason text, or broadening supported blocker types.
- Tightening the closed/merged race path, adding evidence reads to blocker fast paths, changing GitHub's 100-PR discovery limit, or changing check/review acceptance.
- Reworking settings storage, repository routing, deployment receipt or health formats, task providers, UI, API shapes, authentication, or unrelated tests.
- Backfilling terminal history or deleting the pre-fix compatibility fallback.

## Impacted files and interfaces

- `internal/predicate/` new package: atom constants, fact/evidence-source types, ordered profile model, evaluator, focused unit tests, and predicate fixtures.
- `internal/agentrun/predicates.go` or equivalently bounded adapter files: Factory atom sources and profile declarations without external I/O.
- `internal/agentrun/ready.go`: route ready-checkpoint shape and run-directory identity through internal profiles while retaining exact errors and file boundaries.
- `internal/agentrun/manager.go`: route checkpoint binding and ready/merged snapshot decisions through profiles; preserve the reduced race validation path.
- `internal/agentrun/github.go`: express review/check safeguard requirements as facts while preserving GitHub CLI parsing and current aggregate field compatibility.
- `internal/agentrun/completion_system.go`: compute granular health, checkout, receipt, ancestry, and cleanup facts after existing ordered reads; preserve side effects, mutation, and `CompletionEvidence` compatibility.
- `internal/agentrun/completion.go`: select `sdlc-deploy` or `sdlc-repo-only`, use ordered profile failures and blocker-support profiles, and preserve terminal/repark choreography.
- `internal/agentrun/testdata/predicate_parity.json` and predicate/agentrun tests: recorded exact parity across atoms, profiles, blockers, failures, and retry decisions.
- `internal/agentrun/store.go`: required initial-claim type, pin validation after dedupe/coalescing, durable clone, and retained continuation semantics.
- `internal/agentrun/launcher.go`, `agent_commands.go`: demonstrate pinned launch for every new run and isolate/document the retained pre-fix fallback.
- `internal/server/server.go` and tests: select/pin the workflow in the legacy label path and adapt claim interfaces.
- `project_setup.go`, `main.go`, and tests: inject the narrow workflow/settings policy dependency, pin project-provider bootstrap, and adapt construction.
- `internal/triggerrouter/`, `internal/taskservice/`, observers, collectors, routing, migration, manager, server, and task tests: construct explicit pinned claims in fixtures and assert original-pin retention rather than relying on an impossible unpinned public claim.
- Plan, PR body, and Linear comments: review, implementation, verification, exact-head, merge, deployment, and cleanup evidence.

No frontend production file, external API, repository configuration, persistent JSON field, database schema, or additional repository changes.

## Vertical implementation phases

### 1. Establish the pure predicate vocabulary and frozen parity corpus

Files: `internal/predicate/*`, `internal/agentrun/testdata/predicate_parity.json`, focused test helpers.

- Implement stable atoms, facts, source parameters, ordered all-of/any-of profiles, deterministic results, and fail-closed evaluator behavior.
- Define the Phase 0 atom vocabulary and the two public completion profile names plus internal profile names.
- Record golden cases for passing and failing atoms, missing/duplicate evidence, all-of failure ordering, any-of cleanup support, deploy/repository profile differences, and exact legacy messages.
- Add a test-only frozen legacy completion evaluator and exhaustive boolean comparisons before changing production validator call sites.

Success criteria: the package has no `agentrun` dependency or I/O; evaluator tests pass under race; every public profile requirement and atom appears in committed parity data; the frozen comparison demonstrates current completion outcomes and ordered text.

### 2. Re-express checkpoint, PR, safeguard, and completion validation

Files: `internal/agentrun/predicates.go`, `ready.go`, `manager.go`, `github.go`, `completion_system.go`, `completion.go`, relevant tests and golden records.

- Add adapters from current lifecycle structs and collected evidence to named facts.
- Route checkpoint shape/directory, manager binding, open-ready, final merged, safeguards, health, receipt/source, completion, and blocker backing through their profiles.
- Keep command/API/file reads, task mutation, typed transport error classification, and short-circuit/repark choreography in their existing callers.
- Retain compatibility aggregate booleans where external/test construction depends on them, while deriving production decisions from atom results.
- Run focused tests after each boundary and compare exact acceptance, repark, terminal status, details, and problem order against goldens.

Success criteria: no production validation conjunction covered by Phase 0 remains duplicated outside its profile; current fast paths perform the same reads; all legacy focused tests and golden parity cases pass unchanged in observable output.

### 3. Make admission-time pinning mandatory

Files: `internal/agentrun/store.go`, `launcher.go`, `agent_commands.go`, `internal/server/server.go`, `project_setup.go`, `main.go`, `internal/triggerrouter/*`, `internal/taskservice/*`, and impacted tests.

- Introduce the explicit initial-claim value with a pinned or error-carrying workflow candidate and adapt every caller/fixture.
- Reorder private claim logic so delivery dedupe and active coalescing precede validation, then require and deep-clone a valid workflow snapshot for every created run.
- Select and pin the configured workflow in legacy server label admission and provider bootstrap, wiring only the needed settings/policy dependency.
- Prove durable reopen, live-settings changes, all continuation kinds, launcher snapshot preparation, rollback-boundary marking, retained pre-fix fallback behavior, and caller-level retries after the binding is removed or disabled.

Success criteria: code search finds no unpinned production creation path; invalid pins create no record; retry/coalesce retain the first pin; all newly admitted runs prepare from their immutable snapshot; the compatibility fixture is the only unpinned launch case.

### 4. Integrate, verify, and publish

Files: test evidence, approved plan, PR, and Linear lifecycle records.

- Run the complete focused and mandatory Factory verification matrix in a hermetic Git-config environment.
- Inspect the full diff for accidental churn, generated files, secrets, debug output, stale compatibility comments, duplicate predicates, and unrelated changes.
- Push the exact verified head, update the draft PR with problem, decisions, risks, non-goals, plan path, and exact evidence, mark it ready, move Linear to In Review, and enter the GitHub/Linear green loop.
- Answer every actionable review/thread/comment, re-run affected checks after fixes, and checkpoint only when local and remote heads and all safeguards agree.

Success criteria: all checks pass; the PR is open, non-draft, mergeable, review-clear, thread-clear, and comment-clear; Linear has no unanswered feedback; local `HEAD` equals GitHub's head before the ready checkpoint.

## Data, migration, compatibility, rollout, and rollback

### Data and migration

- Existing `Run` JSON already contains workflow pin, digest, and policy revision fields. No schema or contract-version bump applies.
- Newly created runs always populate those fields. Terminal unpinned history remains readable. A retained nonterminal pre-fix unpinned record remains launchable through the isolated compatibility path.
- Predicate fixtures are test data, not runtime state. Existing ready checkpoint, completion evidence, deployment receipt, health, settings, task, API, and Linear/GitHub data shapes remain unchanged.

### Compatibility and trust boundaries

- The predicate package consumes supplied evidence only. It cannot shell out, read arbitrary paths, call GitHub/Linear, mutate a task, or decide retry policy.
- Existing adapters preserve authoritative typed errors, repository allowlisting, task-provider identity, branch isolation, OID checks, ancestry, receipt/health identity, human merge, and exact verified-head gates.
- Exact reason strings and order remain compatibility requirements. Missing or conflicting predicate evidence fails closed and is covered by tests.
- Workflow snapshots retain current permission, size, structure, digest, enabled-state, and rollback-boundary validation. Policy revision remains an opaque coordinate and may be zero. Candidate claims and carried resolution errors never overwrite or invalidate the pin on an existing run.

### Rollout

- Backend and tests ship in one Factory binary. There is no feature flag because new admissions must not remain unpinned.
- Deploy only after a human merge whose merge commit contains the exact checkpointed PR head.
- During rollout, an already persisted unpinned record can use the compatibility fallback; any run admitted by the new binary must have a durable snapshot.

### Rollback and recovery

- Before merge, correct or revert the issue branch normally.
- After merge, prefer a schema-2-aware corrective or revert commit on `main` and deploy that exact merged commit. No persisted data rewrite is required.
- An older binary can read newly populated optional pin fields. However, rollback preflight remains authoritative because Markdown-bearing runs may have crossed the existing schema-2 rollback boundary.
- Never reset, stash around, or deploy from a dirty/divergent managed main checkout, the issue worktree, or T9.

## Verification matrix

| Acceptance criterion or risk | Exact verification |
| --- | --- |
| Predicate evaluator determinism and failure closure | `go test ./internal/predicate`; cases for all/any order, unsupported/missing/duplicate facts, source errors, parameters, and result retention |
| Exact legacy completion parity | Load `internal/agentrun/testdata/predicate_parity.json`; exhaustive frozen legacy versus profile comparison; assert exact ordered problems and deploy/repository differences |
| Ready checkpoint and directory identity | Existing plus table-driven `ready_test.go` cases for every shape atom, provider-isolated prefix, and run-directory binding |
| Manager binding and PR identity | Focused `manager_test.go` cases for task/repository/base/freshness, ready state/draft/base/head/OID, merged state/base/head/OID/merge commit, and intentionally reduced race validation |
| Safeguard semantics | `github_test.go` matrix for requested changes, pending checks, accepted/failed outcomes, and empty outcome; compare aggregate compatibility field |
| Health, checkout, receipt, ancestry, and cleanup atoms | `completion_system_test.go` fixtures for every individual identity field, time/format/ancestry boundary, repository-only mode, and existing I/O/mutation order |
| Typed blockers and retry/terminal choreography | `completion_test.go` goldens for all pre/post-checkpoint blockers, external auth typed errors, open PR/repark, refresh/read errors, cleanup any-of support, and unsupported blockers |
| Required initial pin persistence | Store claim/reopen test asserts deep-equal pin, digest, opaque policy revision including zero, rollback marker, and no aliasing after caller mutation |
| Invalid initial pin cannot create state | Resolution error, missing/incomplete/disabled pin, and mismatched digest tests assert error plus unchanged durable run count |
| Dedupe and active coalescing retain first pin | Store and caller-level retries with a different pin or a removed/disabled current binding carry the candidate error through `Store.Claim`, then return the original ID and original snapshot without replacement |
| Every production admission is pinned | Legacy server label, generic rule, native task, feedback continuation, and project-provider bootstrap tests freeze settings after admission and assert stored snapshot |
| Every later segment retains original pin | Comment, GitHub, remediation, and post-merge resume/preparation tests mutate live binding then assert original `workflow.json`, digest, and `--workflow-file` |
| Pre-fix compatibility is isolated | Persist an unpinned legacy fixture without using public new-claim API; prove fallback launch works and new admissions cannot reach it |
| Focused package regression | `GIT_CONFIG_GLOBAL=/dev/null go test ./internal/predicate ./internal/agentrun ./internal/server ./internal/triggerrouter ./internal/taskservice`; `GIT_CONFIG_GLOBAL=/dev/null go test -race ./internal/predicate ./internal/agentrun` |
| Complete Factory Go regression | `GIT_CONFIG_GLOBAL=/dev/null go test ./...`; `GIT_CONFIG_GLOBAL=/dev/null go test -race ./...`; `GIT_CONFIG_GLOBAL=/dev/null go vet ./...` |
| Frozen frontend publication | `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile`; `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck`; `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` |
| Diff and process hygiene | `git diff --check origin/main...HEAD`; complete diff/status review; confirm no secrets/debug/generated/unrelated files and no temporary process or child window remains |
| GitHub and Linear safeguards | Fresh authoritative PR checks, merge state, review decision, reviews, PR comments, inline comments, unresolved threads, and full Linear conversation after every durable wake |
| Exact verified head | Local `git rev-parse HEAD` equals GitHub `headRefOid` immediately before `factory agent checkpoint ready-for-merge` |

The `GIT_CONFIG_GLOBAL=/dev/null` prefix isolates final Go verification from Tom's global `.worktrees` ignore, which causes a pre-existing launcher test failure without changing Factory behavior or user configuration. No browser, mobile, keyboard, conflict, offline, or frontend visual exercise is applicable because ENG-58 changes no interactive surface.

## Exact post-merge deployment, probes, and recovery

After GitHub authoritatively reports a human merge commit and `git merge-base --is-ancestor <checkpointed-head> <merge-commit>` succeeds, resolve the single managed primary checkout at `/Users/tom/repos/tomnagengast/factory`. Require it clean, fetch and prune origin, fast-forward tracked `main`, and require local `main` to equal fetched upstream. Deploy only there:

```bash
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
```

Then verify both health identities and the current receipt:

```bash
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
tmux -L factory-agents list-sessions
```

Require local health, public health, and receipt to agree on commit, tree, build ID, deployment ID, and contract version; both health commits must equal deployed managed `HEAD`; the ENG-58 run/session must survive restart; and no wire item may remain undispatched. Confirm new admissions expose a nonempty stored workflow pin without creating a synthetic run solely for probing.

If deployment fails, inspect the failed receipt and confirm automatic restoration. Use rollback only if Factory's workflow rollback preflight accepts the target:

```bash
bin/network-app rollback factory --to <deployment-id>
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

Because current Markdown admissions have crossed the schema-2 rollback-incompatibility boundary, an older binary may be unsafe; use a schema-2-aware known-good release or a forward fix when preflight rejects rollback.

After successful deployment, verify GitHub auto-deleted the remote issue branch, fetch/prune, ensure every child window is finished and consumed, remove the clean integrated issue checkout through Worktrunk without force, repeat health and authoritative GitHub/Linear reads, move ENG-58 to Done, and publish merge, deployment, and cleanup evidence.

## Unresolved questions

None.
