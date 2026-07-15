# ENG-49 recovery research

> updated: 2026-07-14T23:40:51-07:00

## Research questions

1. What exactly happened to PR #12, and which post-merge contract rejected it?
2. Can the reviewed `8769363` implementation be recovered without weakening exact-head validation or changing its tree?
3. Why was a noncompliant GitHub merge method available at the human gate?
4. Which code seam can make every Factory-managed repository compatible with the exact-head contract?
5. Why did later Linear comments create additional Runs, and which part is a defect versus the approved additive-trigger policy?
6. What must remain unchanged?
7. What verification, rollout, deployment, and rollback evidence is required?

## 1. Incident reconstruction

Observed facts:

- PR #12 recorded the exact ready checkpoint head `876936375467c71c36d728ccfcaa441ed7be18a1` and GitHub still reports that value as `headRefOid`.
- GitHub reports `51fc2594848fff213e602186922418be887e20f5` as the merged result. The local `main` history contains 14 rewritten commits ending at that OID, so this was a rebase merge rather than one squash commit.
- Both tips resolve to tree `b457e11910d467632857d1f90e71d964ec631952`.
- `git merge-base --is-ancestor 876936375467c71c36d728ccfcaa441ed7be18a1 51fc2594848fff213e602186922418be887e20f5` exits `1`.
- Run `run-c2c7417ab81649af` retained PR #12, the exact checkpoint, and the reported merge OID. Its mechanical completion result accepted `verified_head_mismatch` and did not deploy or clean up.
- Local and public health still report the prior verified release `f383b179d890abab1378cb670ce4964c64013539`.

The rejection is intentional. `internal/agentrun/completion_system.go` proves the checkpointed head is an ancestor of the reported merge commit. `internal/agentrun/completion.go` requires that evidence for terminal success. `internal/agentrun/execute.go`, the compiled workflow, `README.md`, and repository instructions all prohibit deploying a squash or rebase replay even when its tree matches.

Conclusion: do not deploy `51fc259` under the original Run and do not weaken the ancestry gate.

## 2. Safe ancestry recovery

The retained original worktree is clean at `8769363`; the recovery worktree is independently based on current `origin/main` at `51fc259`.

`git merge-tree --write-tree 51fc2594848fff213e602186922418be887e20f5 876936375467c71c36d728ccfcaa441ed7be18a1` returns `b457e11910d467632857d1f90e71d964ec631952`, the same tree as both parents. Therefore a normal `--no-ff` merge of `8769363` into the recovery branch:

- records the original reviewed head as an ancestor;
- introduces no content change from PR #12;
- leaves the old blocked Run and checkpoint immutable;
- lets the recovery PR add narrowly scoped prevention changes on top; and
- gives a new reviewed PR head that a human merge commit can preserve exactly.

The recovery PR must itself be merged with GitHub's **Create a merge commit** method. Rebase or squash would reproduce the incident.

## 3. GitHub merge-policy mismatch

Current GitHub repository metadata reports all three pull-request merge methods enabled and automatic branch deletion enabled for each compiled Factory repository:

| Repository | Merge commit | Squash | Rebase | Auto-delete branch |
| --- | --- | --- | --- | --- |
| `tomnagengast/factory` | true | true | true | true |
| `tomnagengast/network` | true | true | true | true |
| `tomnagengast/notebook` | true | true | true | true |
| `tomnagengast/artifacts` | true | true | true | true |

`internal/agentrun/launcher.go` already owns idempotent GitHub repository creation and validation before onboarding or Run launch. It reads repository privacy/default-branch metadata but neither reads nor reconciles merge methods. GitHub CLI supports explicit idempotent flags:

```text
gh repo edit owner/repository \
  --enable-merge-commit=true \
  --enable-squash-merge=false \
  --enable-rebase-merge=false \
  --delete-branch-on-merge=true
```

The narrow mechanical fix is to extend the existing repository projection and preparation boundary. Preparation should return immediately when policy already matches; otherwise it should reconcile the four settings, re-read authoritative GitHub state, and fail closed unless the desired state is proven. This covers compiled repositories, dynamically admitted repositories, existing checkouts, and greenfield bootstrap through the same allowlisted repository path.

For this recovery, `tomnagengast/factory` must be reconciled before the new ready checkpoint because the fixed binary is not deployed yet. The other compiled repositories can be reconciled in the same explicit rollout so the first post-upgrade Run does not perform a surprise policy transition.

## 4. Linear provenance and additional invocations

Two later generic invocations were observed:

- The 05:06 UTC product-decision reply was an actual human comment. It correctly resumed the protected active lifecycle and also admitted the independent generic `linear-comment` rule.
- The 06:05 UTC ready comment ended with `🐘 codex-do:ENG-49:ready-for-merge:8769363`, without the required inline-code marker. `internal/linearhook/event.go` recognizes only a final line equal to `🐘` or matching `🐘 \`codex-do:...\``. The malformed footer was therefore classified as human and admitted another generic invocation.

`README.md` and the approved ENG-49 product decision explicitly say the generic rule is independent and additive. Its serialized fresh Run after the protected lifecycle became terminal is therefore not an invocation-consumption defect. Changing that behavior would reverse an explicit product decision and is out of scope.

There is a real workflow-authoring gap: `internal/workflow/defaults/full-sdlc.md` does not state the reserved signature protocol, even though the webhook classifier depends on it and Factory/human comments can share one Linear identity. The compiled workflow should require every Factory-authored Linear comment to end with `🐘` alone or the exact inline-code coordination marker. Existing parser and webhook tests already prove those two forms are excluded; a workflow/default prompt assertion should prevent the instruction from disappearing.

## 5. Invariants and non-goals

The recovery must preserve:

- human-only PR merge authority;
- exact checkpoint-head ancestry, not tree-equivalence substitution;
- allowlisted Linear project repository routing;
- immutable original Run, checkpoint, and deployment evidence;
- deployment only from clean, merged, updated Factory `main`;
- existing protected feedback binding and independent additive generic rule;
- current workflow publication, migration, draft, and compatibility semantics; and
- automatic remote branch deletion plus Worktrunk cleanup after verified deployment.

Non-goals:

- accepting squash or rebase merges through patch-ID or tree equivalence;
- rewriting PR #12 or its terminal Run;
- changing generic trigger serialization or coalescing;
- changing the workflow authoring UI/API; or
- deploying before the recovery PR passes a new exact-head checkpoint and human merge.

## 6. Verification, rollout, and recovery

Focused verification:

- launcher tests for already-compliant policy, one-time reconciliation, authoritative re-read, and fail-closed non-convergence;
- workflow/default tests for the exact merge-method warning and reserved Linear signature;
- ancestry checks proving the recovery branch contains `8769363` while its merge commit retains the original tree before prevention edits;
- `go test ./internal/agentrun ./internal/workflow ./internal/linearhook`.

Required publication verification:

```text
go test ./...
go test -race ./...
go vet ./...
MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build
```

Pre-checkpoint rollout proof:

- GitHub reports merge commits enabled, squash/rebase disabled, and branch auto-delete enabled for the four compiled repositories.
- The recovery PR is open, non-draft, mergeable, and its exact head is locally verified.
- The ready summary explicitly requires **Create a merge commit**.

Post-human-merge deployment from `/Users/tom/repos/tomnagengast/factory`:

```text
git fetch --prune origin
git merge --ff-only origin/main
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
git merge-base --is-ancestor <verified-recovery-head> <reported-recovery-merge-commit>
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

The health identities, current deployment receipt, expected commit/tree/build/deployment/contract values, workflow/schema-2 API state, wire catch-up, and tmux sessions must all pass the original approved ENG-49 deployment probes before cleanup.

If merge-policy reconciliation causes an unexpected repository workflow problem before merge, restore the previously observed settings with `gh repo edit ... --enable-merge-commit=true --enable-squash-merge=true --enable-rebase-merge=true --delete-branch-on-merge=true` and revise the plan. After the recovery PR merges, do not roll back to an ancestry-incompatible or schema-1 binary outside the existing schema compatibility preflight. Use the last verified release only when that preflight still permits it; otherwise use a forward schema-2-aware correction.

## Contradictions and assumptions

- The blocker comment calls the merge a squash replay, while GitHub/local history prove a rebase merge. Both violate the same ancestry contract, so the correction does not alter the blocker.
- Worktrunk classifies the old worktree as integrated because its tree matches `main`; Factory intentionally requires stronger commit ancestry. Factory's gate remains authoritative.
- One older durable memory recommends content equivalence after rewritten merges. That conflicts with the repository's current P0 instructions and commit `f383b17`; it is not applicable to this recovery.

## Unresolved questions

None.
