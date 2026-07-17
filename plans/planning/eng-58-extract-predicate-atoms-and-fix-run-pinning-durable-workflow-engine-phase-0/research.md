# ENG-58 Phase 0 Research

> updated: 2026-07-17T00:06:11Z

## Research questions

1. What behavior do the current ready, pull-request, blocker, completion, deployment, and cleanup validators enforce, including ordering, errors, and side effects that a predicate extraction must preserve?
2. Which named atoms and contract profiles can express those validators without creating an import cycle or changing observable decisions?
3. Which run admission and continuation paths pin a workflow today, which do not, and what failures can late binding cause?
4. What is the smallest compatible pinning change, including persisted pre-fix runs and rollback-boundary handling?
5. What recorded fixtures and commands can prove exact parity and every ENG-58 acceptance criterion?
6. Does Phase 0 require another repository, a data migration, a deployment, or an owner decision?

## Scope and controlling decisions

ENG-58 is only Phase 0 of the locked durable-workflow design in `/Users/tom/notes/agent/plans/planning/2026-07-16-factory-durable-workflow-engine.md`. The complete 217-line plan was read before repository research. Its controlling requirements are:

- extract named evidence predicates and the `sdlc-deploy` and `sdlc-repo-only` contract profiles;
- re-express the existing mechanical validators as profile evaluations;
- pin every newly claimed run to an exact workflow;
- preserve all observable behavior;
- add predicate unit tests and recorded golden comparisons;
- do not implement the journal, spine, host calls, wake router, advisory contract, authoring changes, or later-phase migrations.

No issue comment, attachment, child issue, related issue, prior branch, pull request, or retained checkpoint adds scope. The Linear project routes to `tomnagengast/factory` at `/Users/tom/repos/tomnagengast/factory`; its normalized origin matches the Factory-managed checkout. No additional repository is implicated by any caller or interface traced below.

## 1. Current validator behavior

### Terminal decision choreography

`MechanicalCompletionValidator.Validate` is not one conjunction. Its order and short circuits are part of the contract (`internal/agentrun/completion.go:195-256`):

1. A process failure before a checkpoint is accepted as `failed`; the same failure after a checkpoint is rejected and reparked for recovery.
2. Without a ready checkpoint, only the four pre-checkpoint blocker types are accepted. Other blocked intents fail. A success intent first resolves task identity and branch prefix, then discovers matching PRs. Discovery errors are terminal rejections, not reparks; matching PRs produce a distinct count-bearing reason; otherwise success is rejected because no manager-validated checkpoint exists (`completion.go:258-287`).
3. With a checkpoint, authoritative PR refresh errors repark, except a correctly typed external-authentication blocker backed by the typed error class.
4. Any `OPEN` PR reparks before blocker or success evaluation.
5. A blocked intent follows the blocker-specific path. A success intent first validates merged PR identity, then reads system evidence, then evaluates completion requirements.

This ordering must remain in `agentrun`. Moving I/O into a general profile runner would add reads to current short paths and change retry behavior.

### Ready checkpoint shape and manager binding

`ReadyCheckpoint.Validate` checks these conditions in exact order (`internal/agentrun/ready.go:41-78`):

- contract version equals 1;
- run ID is present;
- an optional task identity is valid;
- repository is `owner/name`;
- PR number is positive;
- base and head branches are structurally valid;
- a nonzero task's head begins with its provider-isolated task prefix;
- verified head is a lowercase 40-character OID;
- creation time is present.

Read/write add a run-directory basename match and preserve their current error prefixes (`ready.go:84-112`). Manager validation then binds the shape-valid checkpoint to the current run's task, routed repository, base branch, and lifecycle segment start (`manager.go:378-400`).

An `OPEN` ready snapshot requires open state, non-draft, matching base, matching head branch, and exact verified head in that order (`manager.go:359-376`). A merged snapshot requires merged state, matching base, matching head branch and verified head, and a valid merge commit OID (`manager.go:409-422`). Closed/merged races in `parkReadyRun` intentionally validate only base/head identity before resuming the post-merge segment (`manager.go:314-347`); profile extraction must not tighten that race path.

### GitHub safeguards

`GitHubCLI.Snapshot` reads PR state, draft status, base/head identity, merge commit, review decision, reported checks, and update time (`internal/agentrun/github.go:54-123`). Safeguard regression is true when:

- review decision is `CHANGES_REQUESTED`;
- any reported check has a nonempty status other than `COMPLETED`; or
- any check outcome/state is not empty and is outside `NEUTRAL`, `SKIPPED`, or `SUCCESS`.

An empty check outcome does not regress. `MatchingIssuePullRequests` filters the first 100 all-state PRs solely by head-prefix and does not fetch safeguards (`github.go:125-171`). Authentication is represented by `externalAuthenticationError`; string markers are only classification inputs (`completion.go:139-169`).

### System evidence and side effects

`SystemCompletionEvidence` performs ordered external reads and produces the legacy `CompletionEvidence` shape (`internal/agentrun/completion_system.go:71-148`):

- child result files are read first. No children is complete; a missing, malformed, or unfinished result is incomplete/error; a finished failed child still counts complete (`completion_system.go:150-181`).
- deployable evidence may classify a sufficiently recent matching failed pending receipt, then reads the current receipt and live health. A classified deployment failure can be returned without a current receipt (`completion_system.go:111-148`).
- repository evidence begins with `git fetch --prune origin`, then reads status, head, branch, upstream, origin main, origin URL, ancestry, remote head, and Worktrunk inventory (`completion_system.go:191-277`). This fetch is a deliberate side effect and remains outside the pure predicate package.
- `completionAncestor` maps every nonzero exit, including operational errors, to `false` (`completion_system.go:411-417`). Phase 0 must preserve that arguably imperfect reject-vs-repark behavior.
- Linear task completion is a read. Factory-native task completion can mutate task state through `TaskCompletion.Complete`, but only after all prerequisite completion evidence passes (`completion_system.go:352-379`). This remains ordered after repository evidence and outside pure predicates.

The current `HealthMatches` conjunction has eight checks: status, app, commit, tree, build ID, deployment ID, contract version, and health start time (`completion_system.go:128-132`). Deployable `SourceValid` combines the five checkout/source checks with receipt-on-main and thirteen receipt identity/shape/time checks (`completion_system.go:237-247`). Repository-only source validity is only the five checkout/source checks (`completion_system.go:237-250`). These are the large conjunctions that Phase 0 must decompose.

`CompletionEvidence.ExternalAuthenticationFailure` is never set by a production reader. Real authentication acceptance uses typed errors from GitHub or Linear. The boolean's blocker disjunct is therefore vestigial but must remain behaviorally compatible for injected/test evidence until a later contract migration.

### Completion contracts and blocker backing

The existing ordered post-merge requirements are (`completion.go:353-383`):

- for deployable repositories only: successful receipt, then matching live health;
- clean updated-main source;
- merge contained;
- verified head contained in the merge;
- no safeguard regression;
- remote issue branch absent;
- issue worktree absent;
- task complete;
- all child work complete.

Failures are joined in that exact order with `; `. Those strings are persisted in `Run.Detail`, `TerminalRejection`, and `CompletionValidation` and rendered by observers (`store.go:723-767`, `store.go:844-886`). They are observable compatibility data, not internal-only wording.

Repository-only evidence selects the same profile minus receipt and health. Selection already happens through per-repository reader construction from `RepositoryConfig.DeploymentRequired` (`project_setup.go:93-122`, `internal/agentrun/repository.go:44-46`).

Post-ready blocker backing has three no-evidence fast paths: closed-unmerged from PR state, verified-head mismatch from snapshot head, and safeguard regression from snapshot checks/reviews (`completion.go:297-314`). Only a merged PR reaches system evidence for the remaining backing. The cleanup blocker is supported when any of remote branch, worktree, task completion, or child completion is false (`completion.go:328-339`). A profile implementation therefore needs both all-of contract profiles and an any-of cleanup-blocker profile, without changing which evidence is read.

## 2. Predicate boundary and profiles

### Package boundary

`internal/agentrun` must import `internal/predicate`; `predicate` therefore cannot import `agentrun` types without a cycle. The behavior-preserving boundary is a source-neutral predicate vocabulary and evaluator:

- stable atom names;
- a `Fact` containing atom, pass/fail state, and the legacy failure text;
- an `EvidenceSource` interface evaluated with context and parameters;
- ordered `Profile` requirements with all-of or any-of mode;
- an evaluation result retaining ordered facts and failures;
- fail-closed handling for a missing or duplicate required fact.

`agentrun` adapters construct facts from its existing checkpoints, snapshots, receipts, health identities, git results, task/child results, and completion evidence. This keeps source I/O, mutation, error classification, and retry choreography where they are today while extracting the reusable decision vocabulary required by later engine phases.

### Named atom groups

The evidence supports these groups. Exact constant spelling is an implementation-plan decision, but semantics are fixed:

- checkpoint shape: contract version, run ID, task validity, repository validity, PR validity, branch validity, task-prefix isolation, verified-head format, creation time, run-directory identity;
- manager binding: task match, routed repository match, base match, segment freshness;
- ready/merged PR: state, draft state, base, head branch, verified head, merge-commit format;
- safeguards: review clear, checks terminal, check outcomes accepted;
- health identity: status, app, commit, tree, build, deployment, contract, start time;
- checkout/source: clean status, base branch, tracked upstream, head contained in origin main, origin allowlisted;
- receipt: source commit on main, status, app, branch, tree, contract, commit/tree/hash formats, deployment/build IDs, repository, checkpoint time, finish ordering;
- lifecycle completion: merge contained, verified head contained, remote branch absent, worktree absent, task complete, children complete;
- blocker backing: authentication failure, deployment failure, plus negated/any-of requirements over the lifecycle atoms.

The public contract profiles are `sdlc-deploy` and `sdlc-repo-only`. Internal profiles compose the ready checkpoint, manager binding, ready PR, merged PR, safeguards, health identity, checkout/source, deployable receipt/source, and typed blockers. The two public profiles preserve the exact existing completion failure order and text.

### What remains unchanged

- `CompletionEvidence`, `ReadyCheckpoint`, `PullRequestSnapshot`, receipt, and health JSON shapes remain compatible.
- External commands, HTTP/Linear reads, task mutation, and sequencing remain in `agentrun`.
- Authentication keeps typed-error classification.
- Current fast paths do not acquire new evidence reads.
- Current error/repark decisions and exact reason strings remain unchanged.
- No advisory profile is added in Phase 0; it belongs to Phase 5.

## 3. Workflow pinning asymmetry

### Already pinned paths

- Generic trigger rules, including the production Linear-label rule, pin full workflow ID/revision/name/enabled/Markdown, digest, and policy revision during durable admission (`internal/triggerrouter/coordinator.go:294-315`, `internal/triggerrouter/admission.go:78-113`, `internal/triggerrouter/manager.go:162-187`). Production hardcodes generic triggers on (`main.go:502-530`).
- Native task start and native feedback admissions pin the selected provider-neutral workflow (`internal/taskservice/service.go:254-290`, `service.go:314-332`, `internal/triggerrouter/native.go:42-89`).
- Protected Linear comment continuations pin and digest the protected feedback workflow before `ClaimContinuation`; active feedback coalesces into the existing run without replacing its pin (`internal/server/server.go:976-1007`, `internal/agentrun/store.go:427-510`).
- GitHub, remediation, comment, and post-merge wakes resume the same run and retain the stored pin (`store.go:667-681`, `manager.go:424-472`).

Pinned launch validates the stored digest, writes private `workflow.json`, and supplies `--workflow-file` on every segment (`internal/agentrun/launcher.go:556-629`). Snapshot reading revalidates path, permissions, size, structure, and digest (`internal/agentrun/execute.go:132-162`).

### Unpinned paths

`Store.Claim` passes an empty pin, digest, and policy revision; the private claim method only validates and copies a pin when `requireHistory` is true (`internal/agentrun/store.go:253-258`, `store.go:427-510`). Two production-code callers use it:

1. the legacy Linear-label server fallback (`internal/server/server.go:1010-1021`), disabled by current production's `GenericTriggers: true` but retained and tested;
2. project-provider bootstrap (`project_setup.go:191-218`), which is production-reachable from project provisioning and was not named in the issue description (`main.go:452-476`).

An unpinned launch writes no workflow snapshot. `agent-exec` reloads live settings and resolves `WorkflowForTrigger` at process start (`agent_commands.go:89-105`). Observable consequences are:

- a queued run can execute a workflow published or rebound after claim;
- a later Linear-comment segment can switch to the current protected feedback binding;
- a GitHub/post-merge segment can switch again to the current legacy label binding;
- removal or disabling of the live binding can end the tmux process without a result, eventually surfacing as a lost process.

The current production store contains no nonterminal unpinned run. It does retain old terminal unpinned history, which is never relaunched.

### Minimal compatible correction

- Replace the public unpinned claim input with a claim object that always carries trigger, full pin, digest, and policy revision. Keep continuation's history requirement as a separate option/method.
- Perform delivery deduplication and active-run coalescing before validating a candidate new pin, so retries continue returning the original run and never replace its pin.
- Validate and clone the pin for every newly created run.
- Select/pin/digest the label workflow in both legacy server admission and project-provider bootstrap. Inject the narrow settings/policy dependency into the latter and preserve the existing Markdown rollback-incompatibility boundary.
- Retain the launch-time live-settings fallback only for already persisted pre-fix unpinned runs. Removing it would be a migration and could strand a historical nonterminal record during a deployment race; backfilling with restart-time settings could not reconstruct admission intent. New unpinned claims become impossible.
- No run-store schema bump is needed because the fields already exist and remain optional for retained history.

## 4. Verification and recorded parity fixtures

### Golden fixtures

The repository currently has no golden/testdata convention for these validators. Phase 0 should add committed JSON fixtures under `internal/agentrun/testdata` and/or `internal/predicate/testdata` that record inputs plus exact expected ordered results for:

- ready checkpoint shape failures, run-directory binding, manager binding, ready PR, and merged PR;
- safeguard review/check combinations;
- all health identity atoms;
- all checkout and receipt atoms, including time and ancestry boundaries;
- every single completion failure and representative multi-failure ordering for both deployable and repository-only contracts;
- every supported and unsupported typed blocker, including the cleanup any-of profile;
- external read errors and the existing repark/non-repark decisions around each profile boundary.

Tests should evaluate those records through the extracted profiles and compare exact atom outcomes, decision state, accepted/repark flags, detail, and validation reason. A small test-only frozen legacy evaluator can additionally compare the complete boolean matrix during the extraction; production must use only the new profiles.

### Pinning tests

- initial claim persists an exact cloned pin, digest, and policy revision across store reopen;
- missing, incomplete, disabled, or digest-mismatched pins cannot create a run;
- duplicate delivery and active coalescing retain the first run and first pin;
- legacy label admission and project-provider bootstrap capture admission-time content and do not change after a settings update;
- comment/GitHub/post-merge resumes retain the original pin;
- a newly pinned non-invocation launch writes `workflow.json` and passes `--workflow-file`;
- a retained pre-fix unpinned fixture still uses the compatibility fallback.

### Required commands

Focused checks:

```text
go test ./internal/predicate ./internal/agentrun ./internal/server ./internal/triggerrouter ./internal/taskservice
go test -race ./internal/predicate ./internal/agentrun
```

Final publication checks required by repository policy:

```text
go test ./...
go test -race ./...
go vet ./...
MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build
```

Baseline focused testing passed for server, trigger-router, and task-service. `internal/agentrun` has one pre-existing environment-dependent failure: `TestTmuxLauncherPrepareAllowsRegisteredWorktreeOnly` expects `.worktrees/stray.txt` to be visible, while Tom's global excludes file ignores `.worktrees`. The test passes with `GIT_CONFIG_GLOBAL=/dev/null`. Final Go verification should run in that hermetic Git-config environment without changing the user's global configuration; this is not an ENG-58 product behavior change.

## 5. Deployment, recovery, and compatibility

Factory is deployable. After a human merge preserving the exact verified head, deployment must run only from the clean updated managed main checkout:

```text
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
```

Post-deploy identity checks are:

```text
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
```

Local health, public health, and the current receipt must agree on commit, tree, build ID, deployment ID, and contract version. Current local/public health is `ok` at main commit `7b464322f7319f4af7fc3404befa5aff99cfa6f0` with a fully dispatched wire.

The deployment provider rejects dirty, non-main, divergent, or unexpected commits. If the release fails, inspect the failed receipt and verify automatic recovery. Rollback is only allowed when Factory's workflow rollback preflight passes:

```text
bin/network-app rollback factory --to <deployment-id>
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

Because current schema-2 Markdown admissions have crossed the rollback-incompatibility boundary, practical recovery may require a schema-2-aware known-good release or a forward fix rather than an older binary. Phase 0 adds no schema, journal, API, or cross-repository migration.

## Contradictions and resolved assumptions

- The issue cites the legacy server label path but omits production-reachable project-provider bootstrap. Caller enumeration proves both must be fixed for “every run pins.”
- The locked architecture describes source-backed atom evaluation. Phase 0 can establish that interface without moving existing side effects or mutations; doing so would violate the zero-behavior requirement.
- `ExternalAuthenticationFailure` appears to be evidence, but no real reader populates it. Preserve compatibility while keeping typed errors authoritative.
- The neuron query produced no result because the current index lacks usable embeddings/edges. Exact search, complete file reads, history, blame, memoryd, and runtime inspection supplied the evidence instead.

## Unresolved questions

None. The locked design plus repository/runtime evidence resolves Phase 0 scope, architecture, verification, deployment, and recovery without an owner decision.

## Research activity record

Two read-only Factory tmux activities were launched and consumed. The Codex pinning activity completed successfully. The Claude predicate activity read the full plan and relevant validator surface but hit a provider session limit before its final report; its durable findings and operational failure were preserved. No child edited repository or external state.

Durable memory searched included prior workflow-authoring and pinning decisions. The material Phase 0 constraints were reflected under `factory-eng58-phase0-predicate-research` for resume safety.
