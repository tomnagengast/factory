# FAC-1 Native rollout canary checklist implementation plan

> updated: 2026-07-16T06:32:42Z

## Task context

FAC-1 is the independently gated native Factory canary required after ENG-46. It asks for one concise checklist in `README.md` under `## Native tasks and provider authority`. The change must document human-gated research and plan approval, a checkpointed PR, human merge authority, exact merged-main deployment, cleanup, and provider-neutral completion evidence. Runtime behavior, configuration, dependencies, generated files, and public interfaces must remain unchanged.

The human scope confirmation is stricter than a generic documentation-only change: the final merge diff must contain only the intended `README.md` edit. Required workflow research, plan, and review artifacts will remain durable in pushed commit history and commit-specific task links, but will be removed from the final PR tree after implementation.

## Acceptance criteria

- `README.md` contains a concise `### Native rollout canary checklist` in the native provider-authority section, before native storage/recovery documentation.
- The checklist explicitly covers separate human approvals for research and the reviewed plan.
- It requires one locally verified, checkpointed PR head and preserves human-only merge authority through **Create a merge commit**.
- It requires deployment of the exact merge from updated clean `main`, never from the task worktree.
- It requires GitHub remote-branch deletion and Worktrunk local branch/worktree cleanup after deployment verification.
- It requires provider-neutral task completion evidence for the PR, verified head, human merge, deployment/health, cleanup, completed child work, approved gates, and no unanswered human feedback.
- The final branch diff against the pre-task `origin/main` contains only `README.md`.
- Factory's required Go, race, vet, frozen Bun install, frontend typecheck, and frontend build checks pass before publication.
- After human merge, the exact checkpointed head is an ancestor of the merge commit, updated clean primary `main` is deployed, local/public health and the deployment receipt match that merged-main commit, cleanup completes, and FAC-1 reaches mechanically validated completion.

## Research evidence

The approved research is at `plans/planning/factory-task-61f08eab1543d34c-native-rollout-canary-checklist/research.md` in commit `edec1e35d404d2f5213dd5eed8bd619fdf8b1ace` and is linked from FAC-1.

- `README.md:76-110` establishes native/Linear authority, the dark rollout, the scoped native helper, and mechanical completion. `README.md:112` begins storage/recovery, proving the intended insertion point.
- `README.md:96` already states the canary lifecycle in prose. The requested checklist should operationalize, not redefine, that lifecycle.
- `README.md:227-235` establishes exact-head merge ancestry, clean-main deployment, immutable release health verification, and cleanup ordering.
- `internal/workflow/defaults/full-sdlc-provider-neutral.md:19-90` establishes artifact gates, dual-provider plan review, final verification, checkpointing, human merge, deployment, and cleanup.
- `internal/agentrun/completion.go:353-382` mechanically requires deployment/health, clean updated main, merge and verified-head containment, no safeguard regression, absent branch/worktree, provider completion, and completed children.
- Runtime probes found healthy local and public Factory service identity at commit `712b62605f65f6c91b2e51ec8464612a2a3c6847`, deployment `20260716T031735Z-712b62605f65-27994`, contract version `1`, with a drained wire and a healthy native task store.

## Current behavior and root cause

The README already names all required canary boundaries, but the information is distributed across narrative paragraphs in the native authority, run lifecycle, human merge/deployment, and deploy/recovery sections. An operator cannot scan one bounded checklist to confirm that the canary crossed the provider-neutral lifecycle in order. FAC-1 closes this documentation gap without changing any enforcement.

## Decisions and alternatives

### Decision: add one six-item checklist

Add a third-level heading immediately after the native completion paragraph and before storage/recovery. Use six ordered task-list items for:

1. human approval of research and reviewed plan gates;
2. local verification and checkpointing of one exact PR head;
3. human merge with **Create a merge commit** and exact-head ancestry proof;
4. fast-forwarded clean `main` deployment and receipt/health identity;
5. automatic remote deletion and Worktrunk cleanup;
6. provider-neutral terminal evidence, completed children, approved gates, and no unanswered feedback.

This is concise, ordered, and reuses existing authority terms.

### Decision: keep workflow artifacts out of the final tree

Commit, push, review, link, and approve the required artifacts. Move the plan/reviews to the approved location before touching `README.md`. After implementing and verifying the checklist, delete only this task's workflow artifact directories in a dedicated scope-cleanup commit. Commit-specific GitHub URLs and PR history preserve the evidence, while the final tree diff satisfies the explicit README-only constraint.

### Alternatives considered

- Expand the existing prose paragraph instead of adding a checklist. Rejected because FAC-1 explicitly asks for a checklist and the prose is already dense.
- Add the checklist under `## Human merge and deployment`. Rejected because FAC-1 explicitly locates it under native tasks/provider authority and the checklist begins before merge.
- Repeat commands in every checklist item. Rejected because the README already owns detailed commands and FAC-1 asks for a concise operator checklist.
- Retain plan/review files in the merged tree. Rejected because the human explicitly limited the change to `README.md`; immutable commit links preserve workflow evidence without widening the final diff.

## Assumptions

- Markdown task-list syntax is acceptable in the repository README and requires no generated table of contents.
- The existing merge policy remains merge-commit only with automatic branch deletion.
- The provider-neutral task UI records both research and plan gate decisions, and mechanical completion remains the authority for FAC-1's terminal state.

## Non-goals

- No Go, TypeScript, CSS, HTML, workflow-default, test, configuration, dependency, manifest, or generated-output change.
- No alteration to native task creation, gate semantics, completion validation, deployment tooling, repository policy, or dark-rollout control.
- No Network-provider workflow work or broader native-project enablement.
- No new operational command or duplicate recovery runbook.

## Impacted files and interfaces

- `README.md`
  - Insert one third-level heading and six checklist items between the native completion paragraph and `### Native task storage and recovery`.
  - Documentation only. No runtime or API interface changes.
- `plans/planning/factory-task-61f08eab1543d34c-native-rollout-canary-checklist/research.md`
  - Retained during the lifecycle at its immutable commit URL, then removed from the final branch tree.
- `plans/planning/factory-task-61f08eab1543d34c-native-rollout-canary-checklist/plan.md` and `reviews/`
  - Reviewed in planning, moved to `plans/approved/...` after gate approval, then removed from the final branch tree while immutable commit links retain the evidence.

No code symbol, request schema, CLI contract, storage schema, deployment manifest, or public route changes.

## Vertical implementation phases

### Phase 1: approve the reviewed plan

1. Run one dual-provider adversarial review round with identical read-only prompts.
2. Correct only supported P0/P1 plan findings and repeat a complete dual-provider round if needed.
3. Commit and push both usable review outputs and the reviewed plan.
4. Attach immutable plan/review URLs to FAC-1 and open the native plan gate.
5. Wait for authoritative human approval.
6. Move the plan and reviews to `plans/approved/<branch>/` and commit/push the move before editing `README.md`.

Success: FAC-1 records an approved plan gate for the exact dual-reviewed plan, and the approved files are committed before implementation.

### Phase 2: add the checklist

1. Re-read the approved plan, current FAC-1 messages, and complete `README.md`.
2. Insert the six-item checklist at the researched location, reusing existing exact-head, clean-main, Worktrunk, and completion terminology.
3. Inspect the focused README diff and run `git diff --check` plus the checklist content query.
4. Commit the documentation change in repository history style.

Success: the README has one concise, ordered native canary checklist and no runtime file changed.

### Phase 3: enforce final README-only scope

1. Remove only this task's planning and approved artifact directories after their immutable URLs are recorded.
2. Commit the artifact cleanup.
3. Require `git diff --name-only origin/main...HEAD` to output exactly `README.md` and inspect the complete final diff.

Success: the final PR tree changes only the intended README content while artifact evidence remains accessible from commit-specific links and PR history.

### Phase 4: final verification and ready boundary

1. Run the complete verification matrix from the clean task worktree.
2. Push the implementation, update the PR body with decisions, risks, exact results, immutable approved-plan/review links, and the exact locally verified head.
3. Mark PR #17 ready, publish the implementation summary to FAC-1, and refresh all GitHub checks, comments, reviews, threads, mergeability, and task messages.
4. Address only actionable in-scope feedback and rerun affected verification after any new commit.
5. When the complete ready predicate passes and GitHub head equals local head, write the Factory ready checkpoint and instruct the human to use **Create a merge commit**.

Success: one open, non-draft, mergeable, green PR has no actionable feedback and has an exact checkpointed locally verified head.

### Phase 5: post-merge deployment and cleanup

1. In the Factory post-merge continuation, fresh-read PR #17 and FAC-1 and reconstruct the ready checkpoint.
2. Require GitHub to report a merge commit whose ancestry contains the exact checkpointed head; repeat all safeguard checks.
3. Resolve the one primary Worktrunk checkout at `/Users/tom/repos/tomnagengast/factory`, require it to be clean except registered worktree bookkeeping, fetch/prune, and fast-forward `main` to `origin/main`.
4. Prove the PR diff is limited to `README.md`, then deploy the exact updated-main commit.
5. Verify receipt plus local/public health identity, then prove GitHub auto-deleted the remote task branch and prune the tracking ref.
6. Ensure both review child windows are finished and consumed. Remove the clean integrated task worktree/branch with Worktrunk, never force.
7. Recheck health, receipt ancestry, GitHub, clean primary state, and FAC-1 feedback. Publish merge, deployment, cleanup, and terminal evidence so Factory can mechanically complete FAC-1.

Success: the exact human-merged verified head is deployed from clean updated main, cleanup is complete, and FAC-1 records provider-neutral terminal evidence.

## Data, security, compatibility, migration, and rollout

- Data: no stored data, schema, journal, or migration changes.
- Security: no trust boundary, credential, authentication, authorization, or secret handling changes. The checklist preserves human-only merge and scoped provider authority.
- Compatibility: README-only final diff; no binary, API, CLI, workflow, or frontend compatibility impact.
- Migration: none.
- Rollout: the normal Factory immutable self-deployment still runs because FAC-1 is the native lifecycle canary and completion requires deployment evidence, even though runtime code is unchanged.

## Deployment, verification, and recovery

From the updated clean primary checkout after exact-head merge validation:

```bash
git fetch --prune origin main
git merge --ff-only origin/main
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
```

Post-deploy probes:

```bash
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
```

Require local health, public health, and `current.json` to agree on `status=ok/success`, app `factory`, the deployed updated-main commit and tree, build ID, deployment ID, and lifecycle contract version `1`. Confirm the Factory tmux session survived the service restart and authoritative GitHub/task reads still work.

If deployment fails, do not clean up the worktree. Inspect the failed receipt and verify automatic restoration of the previous release. If explicit recovery is required:

```bash
~/.local/bin/nags rollback factory --to <previous-successful-deployment-id>
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

Report `deployment_failed` with the merge commit, attempted deployment ID, receipt, recovered health, and exact next action. A documentation error discovered after merge requires a new reviewed corrective PR; never rewrite merged history or bypass exact-head validation.

## Verification matrix

| Criterion or risk | Verification |
| --- | --- |
| Checklist is in the correct section | `rg -n "Native rollout canary checklist|Native task storage and recovery" README.md` and inspect their order |
| All requested lifecycle stages are named | `rg -n "research|plan|checkpoint|Create a merge commit|clean.*main|Worktrunk|completion evidence" README.md` plus full diff review |
| Final scope is README-only | `test "$(git diff --name-only origin/main...HEAD)" = README.md` |
| Markdown patch is clean | `git diff --check origin/main...HEAD` |
| No accidental runtime behavior change | `git diff --stat origin/main...HEAD` and `git diff --no-ext-diff origin/main...HEAD -- README.md` |
| Go behavior remains green | `go test ./...` |
| Concurrent Go behavior remains green | `go test -race ./...` |
| Static Go analysis remains green | `go vet ./...` |
| Frontend lockfile/install remains reproducible | `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile` |
| Frontend types remain green | `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck` |
| Frontend production build remains green | `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` |
| Exact verified PR head | local `git rev-parse HEAD` equals GitHub `headRefOid`; PR is non-draft, mergeable, green, and feedback-clean before checkpoint |
| Human merge ancestry | `git merge-base --is-ancestor "$VERIFIED_HEAD" "$MERGE_COMMIT"` after authoritative GitHub `MERGED` snapshot |
| Exact merged-main documentation scope | `gh pr diff 17 --name-only` equals `README.md`; updated primary `main` contains the merge and verified head |
| Deployment identity | local/public `/api/healthz` and `current.json` match the updated-main commit/tree/build/deployment/contract |
| Cleanup | GitHub head ref is 404, pruned remote ref absent, `wt list --format=json --branches` lacks the task worktree/branch, and local branch ref is absent |
| Provider-neutral completion | FAC-1 shows approved research and plan gates, PR/merge/deployment/cleanup links or messages, completed children, no unanswered human feedback, and mechanically recorded terminal completion |

## Unresolved questions

None.
