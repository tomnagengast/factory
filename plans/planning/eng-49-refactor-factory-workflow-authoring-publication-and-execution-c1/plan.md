# ENG-49 exact-head recovery plan

> updated: 2026-07-14T23:44:00-07:00

## Linear issue context

ENG-49 owns Factory's published Markdown workflow refactor. PR #12 implemented the approved scope and was human rebase-merged, rewriting the exact verified head out of the reported merge ancestry. Factory correctly accepted `verified_head_mismatch`, retained the clean original worktree and checkpoint, and left production on the previous verified release.

This continuation repairs publication without weakening the exact-head contract. It creates a new reviewed ancestry-preserving checkpoint, removes incompatible merge choices from every Factory-managed GitHub repository, and makes the compiled workflow's Linear provenance instructions self-contained.

Issue: https://linear.app/nags-cloud/issue/ENG-49/refactor-factory-workflow-authoring-publication-and-execution

Original PR: https://github.com/tomnagengast/factory/pull/12

Recovery PR: https://github.com/tomnagengast/factory/pull/14

Research: `plans/planning/eng-49-refactor-factory-workflow-authoring-publication-and-execution-c1/research.md`

## Recovery acceptance criteria

- [ ] The new recovery branch and final verified PR head contain original checkpoint head `876936375467c71c36d728ccfcaa441ed7be18a1` as an ancestor.
- [ ] The ancestry merge itself introduces no file-content change relative to its first parent.
- [ ] Every Factory-managed GitHub repository preparation idempotently requires merge commits, disables squash and rebase merges, and requires automatic head-branch deletion.
- [ ] Repository policy reconciliation re-reads authoritative GitHub state and fails closed when the desired policy is not proven.
- [ ] Existing compliant repositories incur no mutation.
- [ ] The compiled `full-sdlc` workflow requires exact reserved Linear comment footers and explicitly tells the human to use **Create a merge commit** because squash/rebase blocks deployment.
- [ ] The existing protected feedback binding, independent additive generic trigger, exact-head validator, original blocked Run, and schema-2 workflow behavior remain unchanged.
- [ ] Focused tests, all Go tests, race tests, vet, frozen Bun install, frontend typecheck, and frontend production build pass.
- [ ] Before checkpointing, the four compiled GitHub repositories report merge-only policy and automatic branch deletion.
- [ ] A human merges the exact verified recovery head using a merge commit.
- [ ] The merged Factory `main` deploys through the existing exact-commit command and local/public health plus schema-2 workflow probes pass before cleanup.

## Evidence-backed current behavior

1. GitHub reports PR #12 head `8769363` and merged result `51fc259`; both trees equal `b457e1`, but the former is not an ancestor of the latter.
2. `internal/agentrun/completion_system.go` mechanically tests exact ancestry. `internal/agentrun/completion.go` requires it for completion. No tree-equality exception exists or should be added.
3. `internal/agentrun/launcher.go` owns repository creation and validation for compiled, dynamic, and bootstrap routes. Its GitHub projection currently reads only identity, privacy, and default branch.
4. All four compiled repositories currently allow merge commits, squash merges, and rebase merges, even though only the first can satisfy Factory's gate. They already enable branch auto-deletion.
5. `internal/linearhook/event.go` classifies a final standalone `🐘` or exact inline-code `codex-do` footer as Factory provenance. The compiled workflow does not currently teach this protocol.
6. The ready comment on PR #12 omitted inline-code formatting around its marker, so it was normalized as human. The earlier product-decision comment was genuinely human. The generic rule's additional serialized invocations are explicitly additive and remain in scope as expected behavior, not a routing defect.
7. A normal merge-tree calculation between `51fc259` and `8769363` returns the existing tree. The recovery can add ancestry with no code-content replay or trust-policy relaxation.

## Decisions

### Preserve exact ancestry

After plan approval and before implementation edits, merge retained commit `8769363` into the continuation branch with a normal `--no-ff` merge. Do not use an `ours` strategy, cherry-pick, rebase, reset, replace ref, graft, or tree-equivalence bypass. Require:

```text
git merge --no-ff 876936375467c71c36d728ccfcaa441ed7be18a1 -m "Factory: restore verified workflow ancestry"
git diff --exit-code HEAD^1 HEAD
git merge-base --is-ancestor 876936375467c71c36d728ccfcaa441ed7be18a1 HEAD
```

The empty first-parent diff proves the merge commit changed ancestry only. Later prevention commits become part of the new reviewed head.

### Reconcile repository policy at the existing trust boundary

Extend the private `githubRepository` projection in `internal/agentrun/launcher.go` with GitHub CLI's `mergeCommitAllowed`, `squashMergeAllowed`, `rebaseMergeAllowed`, and `deleteBranchOnMerge` fields.

Add one repository-policy reconciliation method called during `TmuxLauncher.Prepare` after origin, base, and default-branch identity are established:

1. Read authoritative repository metadata through the configured GitHub CLI.
2. Return without mutation when merge commits are enabled, squash/rebase are disabled, and branch auto-delete is enabled.
3. Otherwise invoke `gh repo edit` with all four explicit desired values.
4. Re-read authoritative metadata.
5. Return a precise error unless the read proves the complete policy.

Use the configured repository identity and GitHub binary only. Do not derive a repository from the working directory, accept webhook metadata, or change visibility/default branch. The existing `repositoryPrepareLocks` lock serializes this reconciliation with clone and workspace preparation for the same canonical path.

### Make comment provenance and merge method explicit in the compiled workflow

Update `internal/workflow/defaults/full-sdlc.md` so every Factory-authored Linear comment ends with exactly one of:

```text
🐘
```

or

```text
🐘 `codex-do:TEAM-123:phase:r1`
```

The text must say that emoji or marker prose elsewhere is not a signature and that no prose follows the footer.

At the ready boundary, require the implementation summary to name the exact verified head and tell the human to choose **Create a merge commit**. State that squash and rebase preserve neither the checkpointed OID nor deployability.

Add focused default-workflow assertions rather than changing the existing parser. `internal/linearhook/event_test.go` and `internal/server/server_test.go` already cover parser/webhook behavior.

### Reconcile existing repositories before the recovery checkpoint

The currently deployed binary cannot perform the new reconciliation. After the reviewed plan is approved, apply the same explicit policy to the four compiled repositories:

```text
gh repo edit tomnagengast/factory --enable-merge-commit=true --enable-squash-merge=false --enable-rebase-merge=false --delete-branch-on-merge=true
gh repo edit tomnagengast/network --enable-merge-commit=true --enable-squash-merge=false --enable-rebase-merge=false --delete-branch-on-merge=true
gh repo edit tomnagengast/notebook --enable-merge-commit=true --enable-squash-merge=false --enable-rebase-merge=false --delete-branch-on-merge=true
gh repo edit tomnagengast/artifacts --enable-merge-commit=true --enable-squash-merge=false --enable-rebase-merge=false --delete-branch-on-merge=true
```

Re-read all four through GitHub's repository API and require exact desired values. This is an authorized recovery mutation, not a merge. It makes the human merge UI compatible with the already-mandatory Factory contract before PR #14 reaches the ready gate.

## Alternatives considered

### Accept the identical tree

Rejected. It would change the P0 exact-head trust contract and does not generalize safely when base changes or conflict resolutions alter the merged tree.

### Re-push the original branch directly

Rejected as the final design. It could add the old head through a merge-only PR, but it would re-present the full prior diff and would not prevent another incompatible merge. The continuation branch records the old head with a no-content merge and presents only the prevention diff against `main`.

### Document the correct merge button without mechanical repository policy

Rejected. PR #12 already described an exact head, yet the GitHub UI exposed two invalid choices. Documentation alone does not align the available action surface with the gate.

### Reject noncompliant repository policy without reconciling it

Rejected. Factory already owns idempotent repository onboarding and has administrative GitHub authority. A fail-only check would leave new repositories and all four existing compiled repositories unusable until a separate manual operation. Reconciliation followed by authoritative proof is narrower and self-healing.

### Coalesce or suppress the generic `linear-comment` invocation

Rejected as out of scope and contrary to the explicit ENG-49 product decision that protected feedback and generic rules are independent and additive.

## Non-goals

- No modification to exact-head completion evidence or blocker classification.
- No mutation or reinterpretation of original Run `run-c2c7417ab81649af`.
- No PR merge, auto-merge, administrator bypass, force push, or manual deployment from a worktree.
- No trigger registry, feedback binding, workflow editor, settings schema, migration, draft-store, or frontend behavior change.
- No branch protection or paid GitHub ruleset work. This private repository has no branch protection and GitHub rulesets are unavailable on the current plan; merge-method settings are sufficient for the scoped incident.
- No attempt to turn the earlier generic invocations into the original post-merge Run.

## Impacted files and interfaces

### `internal/agentrun/launcher.go`

- Extend `githubRepository` JSON projection with merge-policy fields.
- Expand `readGitHubRepository`'s `gh repo view --json` field list.
- Add policy predicate/reconciliation helpers.
- Call reconciliation within `Prepare` after repository/default-branch validation and before workspace synchronization.
- Preserve current missing-repository, privacy, identity, default-branch, clone, and checkout behavior.

### `internal/agentrun/launcher_test.go`

- Update greenfield GitHub fakes to emit policy metadata.
- Prove already-compliant preparation makes no `repo edit` call.
- Prove a noncompliant repository is edited once and a second preparation is idempotent.
- Prove non-converging authoritative state fails preparation.

### `internal/workflow/defaults/full-sdlc.md`

- Add the exact reserved Linear signature protocol.
- Add the explicit merge-commit-only human instruction at the ready boundary.
- Preserve all existing full-SDLC phases and mechanical authority language.

### `internal/workflow/defaults_test.go`

- Assert the compiled default includes both valid footer forms and the merge-method warning.
- Assert it still contains the exact-head and human-only merge contract.

### `README.md`

- Document repository merge-policy reconciliation in onboarding and Run preparation.
- State that the ready UI must offer/use **Create a merge commit** only.
- Document fail-closed reconciliation and operator recovery/verification commands.
- Preserve the existing additive generic trigger explanation.

### Planning artifacts and Git history

- Retain this research, plan, and adversarial review under the continuation branch path.
- Add one ancestry-only merge commit whose first-parent diff is empty.
- Preserve the original branch/worktree until successful recovery deployment and final cleanup.

## Implementation phases

### Phase 1: Record reviewed ancestry

1. Re-read this approved plan and branch status.
2. Merge `8769363` with a normal `--no-ff` merge.
3. Prove the merge's first-parent diff is empty, its second parent is the exact original head, and the head is now an ancestor.
4. Push the ancestry commit and confirm PR #14 remains the single recovery PR.

Success: the continuation history contains the original reviewed head without a content change.

### Phase 2: Reconcile GitHub merge policy

1. Extend repository metadata and implement the idempotent reconcile/re-read/fail-closed flow.
2. Update launcher fakes and add focused policy tests.
3. Run `gofmt` and focused launcher tests.
4. Commit the coherent backend change.

Success: compliant state is a no-op; drift is repaired once; false success is rejected.

### Phase 3: Close workflow provenance and merge-instruction gaps

1. Add exact Linear footer rules and merge-commit instruction to the compiled workflow.
2. Add focused compiled-default tests.
3. Update the full README contract and recovery guidance.
4. Run focused workflow, Linear hook, server, and prompt tests.
5. Commit the coherent workflow/documentation change.

Success: a principal has self-contained instructions to avoid both the malformed footer and invalid human merge choice.

### Phase 4: Reconcile live GitHub settings

1. Apply the explicit four-repository `gh repo edit` commands.
2. Re-read all four repositories through authoritative GitHub API state.
3. If any fails to converge, restore the previous values for repositories already changed, preserve code work, and stop before checkpointing.

Success: the recovery PR can only be merged through an ancestry-preserving merge commit, and all currently managed repositories match the deployment contract.

### Phase 5: Full verification and publication

1. Review the complete `origin/main...HEAD` diff, first-parent ancestry merge, commit history, and worktree status.
2. Use the code-simplifier workflow on changed Go code without altering behavior.
3. Run every focused and required publication command.
4. Push, update PR #14 with exact evidence, mark it ready, and publish a valid signed Linear implementation summary.
5. Refresh GitHub checks, merge state, reviews, comments, threads, and Linear feedback until the complete ready predicate passes.
6. Record the exact verified PR head. Tell the human explicitly to choose **Create a merge commit**.

Success: one open, non-draft, mergeable recovery PR is ready at an exact locally verified head with no unresolved safeguard.

### Phase 6: Human merge, deployment, and cleanup

1. Wait for a human merge. Do not merge, enable auto-merge, or bypass protections.
2. Fresh-read GitHub and require the reported merge commit to contain the exact recovery head.
3. Repeat final check/review/comment/thread/Linear safeguards.
4. Resolve the primary checkout through Worktrunk, require clean tracked state, fetch/prune, and fast-forward `main` to `origin/main`.
5. Run the exact deployment and post-deploy probes below.
6. Verify remote auto-deletion, prune, then remove the clean recovery and original ENG-49 worktrees/branches with Worktrunk only after deployment evidence succeeds.
7. Move ENG-49 to the completed state and publish merge/deployment/cleanup evidence with a valid Factory signature.

Success: production runs the exact human-merged recovery commit, schema-2 ENG-49 behavior is healthy, and no ENG-49 branch/worktree remains.

## Data, security, compatibility, rollout, and rollback

### Data and compatibility

No JSON schema or durable record shape changes. Original Run, checkpoint, trigger routing, schema-2 settings, migration backup, and draft state remain byte-compatible. Repository metadata is read live from GitHub and not persisted.

### Security and authority

Policy mutation is restricted to the repository identity already resolved from allowlisted Linear project metadata and passed through `LauncherConfig`. The configured GitHub CLI and token are the existing administrative authority. Reconciliation never enables auto-merge or performs a PR merge.

Removing squash/rebase aligns GitHub's human action surface with Factory's existing ancestry invariant. Human merge authority remains intact because Factory only constrains which human merge strategy is available.

### Rollout

Before the recovery checkpoint, reconcile the four known repositories and verify them. The code then keeps compiled, dynamically admitted, and greenfield repositories converged on every normal preparation boundary. A compliant repository takes only the existing metadata read and no edit.

### Rollback

Before PR merge, the prior merge-method settings can be restored explicitly if the product decision is reversed. Do not deploy while they are restored because that would recreate the incident surface.

After merge, use the original ENG-49 schema rollback preflight before activating a schema-1 release. If the compatibility marker or retained registry forbids prior-binary rollback, recover forward with another schema-2-aware commit. Never rewrite run evidence or deploy `51fc259` under the blocked checkpoint.

## Verification matrix

| Acceptance criterion or risk | Verification |
| --- | --- |
| Original reviewed head becomes an ancestor | `git merge-base --is-ancestor 8769363 HEAD` |
| Ancestry merge has no content delta | `git diff --exit-code <merge>^1 <merge>` and exact second-parent check |
| Compliant repo is a no-op | focused launcher unit test with no edit log |
| Drift reconciles once | focused launcher unit test with stateful fake GitHub CLI and two `Prepare`/policy calls |
| False convergence fails closed | focused launcher unit test whose fake ignores `repo edit` |
| Greenfield path gets the same policy | updated bootstrap fixture plus policy assertions |
| Workflow carries exact signatures | compiled-default test checks standalone `🐘` and inline-code marker instructions |
| Workflow prohibits replay merge | compiled-default test checks **Create a merge commit** plus squash/rebase blocker language |
| Parser behavior unchanged | `go test ./internal/linearhook ./internal/server` |
| Exact-head mechanical gate unchanged | `go test ./internal/agentrun` and diff review of completion files |
| All backend behavior | `go test ./...` |
| Concurrency safety | `go test -race ./...` |
| Static correctness | `go vet ./...` |
| Frozen frontend dependency graph | `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile` |
| Frontend types and production bundle | `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck` and `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` |
| Live merge policy | `gh api repos/<owner>/<repo>` for all four repositories |
| PR safeguard state | fresh `gh pr view`, checks, comments, reviews, and unresolved-thread queries |
| Deployment identity | local/public `/api/healthz`, `current.json`, current symlink, receipt, expected commit/tree/build/deployment/contract equality |
| ENG-49 schema-2 behavior | authenticated `/api/workflows`, `/api/settings`, and `/api/triggers` probes from the original approved plan |
| Cleanup | GitHub ref `404`, fetched remote ref absent, Worktrunk list and local branch refs absent |

## Exact post-merge deployment and verification

From the Worktrunk-resolved primary `/Users/tom/repos/tomnagengast/factory` checkout only:

```text
git fetch --prune origin main
git merge --ff-only origin/main
test "$(git branch --show-current)" = main
test -z "$(git status --porcelain --untracked-files=normal -- . ':(exclude,literal).worktrees')"
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
git merge-base --is-ancestor <verified-recovery-head> <reported-recovery-merge-commit>
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
```

Then require:

```text
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
launchctl print "gui/$(id -u)/com.nags.factory"
tmux -L factory-agents list-sessions
```

The local/public health and receipt must agree on the exact deployed commit, source tree, build ID, deployment ID, and lifecycle contract. Require wire pending `0`, no failed project setup, schema-2 workflow publication healthy, slim settings output, summary-only trigger output, correct protected feedback binding, and no draft publication caused by the probes.

After successful deployment only, verify GitHub auto-deleted the recovery branch, fetch/prune, and use:

```text
wt -C /Users/tom/repos/tomnagengast/factory remove --foreground --no-hooks -y eng-49-refactor-factory-workflow-authoring-publication-and-execution-c1
wt -C /Users/tom/repos/tomnagengast/factory remove --foreground --no-hooks -y eng-49-refactor-factory-workflow-authoring-publication-and-execution
```

Do not force either removal. If Worktrunk cannot prove clean integration, report `cleanup_failed` and preserve evidence.

## Unresolved questions

None.
