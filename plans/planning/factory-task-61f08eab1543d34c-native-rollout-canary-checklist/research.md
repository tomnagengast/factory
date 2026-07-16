# FAC-1 Native rollout canary checklist research

## Research questions

1. Where should the checklist live, and what existing documentation must it complement rather than repeat?
2. Which lifecycle steps and authority boundaries must the checklist name to satisfy FAC-1 without changing runtime behavior?
3. How can the workflow publish durable research and plan evidence while ensuring the final merge diff is limited to `README.md`?
4. What verification proves both the documentation change and the native canary lifecycle?
5. What exact post-merge deployment, health, cleanup, and recovery procedures apply to Factory?

## Evidence-backed answers

### 1. Placement and current gap

- FAC-1 explicitly requests a concise checklist under the Native tasks/provider authority documentation. `README.md:76-110` is the matching section. It currently describes separate Factory and Linear authority, dark rollout, the one independently gated native canary, scoped helper commands, and mechanical completion, but it expresses the canary requirements across prose rather than as an operator checklist.
- `README.md:112` begins the storage and recovery subsection. The narrowest placement is therefore after the native completion paragraph at `README.md:110` and before `### Native task storage and recovery`, keeping rollout operation separate from storage recovery.
- Commit `ffeb9e8237ebff7a6261b08995db13388a7aae2c` introduced this section and the source-neutral terminology. No later commit changes that authority model, so the new text should reuse its vocabulary rather than define a parallel process.

### 2. Required lifecycle and authority boundaries

- FAC-1 and its two human messages require a documentation-only change limited to `README.md`, independent human research and plan approvals, and preserved human merge authority.
- The pinned provider-neutral workflow requires research and plan gates, a reviewed plan, a locally verified exact PR head, a human merge, deployment only from updated clean main, branch/worktree cleanup, and final provider evidence (`internal/workflow/defaults/full-sdlc-provider-neutral.md:19-35`, `:62-90`).
- Existing README authority is consistent: the canary must independently cross research and plan gates and complete a checkpointed PR, human merge, deployment, and cleanup (`README.md:96`); the human must use **Create a merge commit** because squash/rebase breaks exact-head ancestry (`README.md:227-235`).
- Provider-neutral completion is not a manually editable task state. `README.md:110` and `internal/agentrun/completion.go:353-382` show that completion requires a successful deployment receipt and matching health, clean updated main containing the merge and verified head, no safeguard regression, absent remote branch and local worktree, provider task completion, and completed child work.
- The checklist should therefore use six short ordered checkboxes: approve research and plan gates; verify and checkpoint one PR head; human merge that exact head with a merge commit; update clean main and deploy the merge; verify cleanup; record source-neutral completion evidence and no unanswered feedback.

### 3. Documentation-only final tree

- The task description says the final deployed main commit must contain only the intended README change, and the latest human message says to keep the change limited to `README.md`.
- The pinned workflow independently requires committed research, plan, and review artifacts. Those artifacts will be committed and pushed for gate/review durability, linked by commit-specific GitHub URLs, and moved to the approved path before implementation as required.
- Before final publication, the workflow-only `plans/.../factory-task-61f08eab1543d34c-native-rollout-canary-checklist/` files will be removed in a dedicated scope-cleanup commit. Their immutable commit-specific GitHub links and PR history retain the evidence, while `git diff --name-only origin/main...HEAD` at the verified head must print only `README.md`.
- This approach changes no runtime source, configuration, generated output, dependency, or test behavior.

### 4. Verification

- Focused documentation checks:
  - `git diff --check origin/main...HEAD`
  - `git diff --name-only origin/main...HEAD` must equal `README.md`
  - `git diff -- README.md` must show one concise checklist in the intended section
  - `rg -n "Native rollout canary checklist|research|plan|checkpoint|Create a merge commit|clean.*main|cleanup|completion evidence" README.md`
- Repository-required publication checks, even for a documentation-only change:
  - `go test ./...`
  - `go test -race ./...`
  - `go vet ./...`
  - `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile`
  - `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck`
  - `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build`
- PR checks must be passing or legitimately skipped, the PR must be non-draft and mergeable, no review requests or unresolved threads may remain, task messages must have no unanswered feedback, and GitHub's head OID must equal the locally verified OID before checkpointing.
- Post-merge verification must prove the merge contains the checkpointed head, updated `main` differs from the pre-task base only in `README.md` for this PR, the deployment receipt and local/public health identify the deployed merged-main commit, GitHub auto-deleted the head branch, Worktrunk removed the local branch/worktree, and FAC-1 records terminal evidence.

### 5. Deployment and recovery

- The pinned workflow's exact self-deployment command is `~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"`, run only from the updated clean primary checkout (`internal/workflow/defaults/full-sdlc-provider-neutral.md:76-90`). The helper accepts a repository-root deployment with auto-detected provider app, and Factory's README states deployment fails closed unless the checkout is clean, on `main`, tracking the official origin, equal to `origin/main`, and equal to the expected commit (`README.md:388`).
- Verify both `http://127.0.0.1:8092/api/healthz` and `https://factory.nags.cloud/api/healthz`, plus `~/.local/share/factory/deployments/current.json`. All must agree on commit, tree, build ID, deployment ID, and contract version (`README.md:400-409`). The pre-change baseline is healthy at commit `712b62605f65f6c91b2e51ec8464612a2a3c6847`, deployment `20260716T031735Z-712b62605f65-27994`, contract version `1`, with a drained wire and one healthy native task.
- If deployment verification fails, the immutable deployment flow restores the prior release and writes failed evidence (`README.md:233`). Inspect the failed receipt and recovered health. If explicit recovery is needed, use `~/.local/bin/nags rollback factory --to <previous-successful-deployment-id>` and recheck both health endpoints. Do not clean up the task worktree until successful deployment verification.

## Contradictions and resolutions

- The workflow requires repository artifacts, while FAC-1 requires a final diff limited to `README.md`. The artifact-history strategy above satisfies both: artifacts exist, are reviewed, committed, pushed, and linked during the lifecycle, but are absent from the final merge tree.
- The older installed `do` skill describes Linear-specific signatures and a direct `bin/network-app` deployment form. This Run is pinned to the newer provider-neutral workflow, whose scoped task helper and exact `~/.local/bin/nags deploy --expected-commit ...` command are authoritative.

## Assumptions

- No markdown formatter is configured for README files; `git diff --check` and exact content inspection are the focused documentation checks.
- GitHub repository policy remains merge-commit only with automatic branch deletion. Intake verified `mergeCommitAllowed=true`, `squashMergeAllowed=false`, `rebaseMergeAllowed=false`, and `deleteBranchOnMerge=true`.

## Unresolved questions

None.
