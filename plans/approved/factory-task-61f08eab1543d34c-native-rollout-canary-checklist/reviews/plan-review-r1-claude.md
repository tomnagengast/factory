# FAC-1 plan review round 1 - Claude

## Summary

FAC-1 is a documentation-only task: insert one six-item `### Native rollout canary checklist` into `README.md` under `## Native tasks and provider authority`, before `### Native task storage and recovery`. The reviewer confirmed every structural, code, and command claim the plan depends on.

Verified against the tree:

- The insertion point is exact: the native completion paragraph ends at `README.md:110`; `### Native task storage and recovery` is at `README.md:112` (plan lines 27, 80, 113). The section header `## Native tasks and provider authority` is at `README.md:76`; canary lifecycle prose is at `README.md:96` (plan line 28).
- Human merge/deployment ancestry, clean-main, health, and cleanup-ordering references at `README.md:227-235` match plan lines 29 and 133-137.
- `internal/agentrun/completion.go:353-382` (`completionProblems`) mechanically gates on deployment/health, clean updated main, merge and verified-head containment, no safeguard regression, remote branch/worktree absence, task completion, and child completion, exactly as plan line 31 asserts. The `DeploymentRequired` conditional at `completion.go:361` supports plan line 147's self-deployment requirement.
- `internal/workflow/defaults/full-sdlc-provider-neutral.md` exists and its research/plan/review lifecycle matches plan lines 30 and 93-98. The gate validates the recorded approval decision, not file persistence, so Phase 3 artifact deletion does not violate the mechanical gate.
- Verification commands are repository conventions: `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile`, typecheck, and build match `nags.toml:15` and `README.md:301-303`; frontend `typecheck` and `build` scripts exist in `frontend/package.json`.
- Deployment and recovery commands (`~/.local/bin/nags deploy --expected-commit`, health probes on port 8092, `~/.local/share/factory/deployments/current.json`, contract version 1) match `README.md:233,405,409` and prior approved plans.
- Artifact deletion is sound: `research.md` and `plan.md` are already committed at `edec1e3` and `693dcbf`, so removing them in a later commit preserves immutable history while netting the final tree diff to `README.md` only. `origin/main` is at merge base `712b626`, so `git diff --name-only origin/main...HEAD` resolves as planned.

No P0/P1 findings. The change is minimal, low-risk, internally consistent, and does not add unrelated scope.

## P2/P3 findings (non-blocking)

- **P3 - divergence from repository convention.** Sibling tasks retain their plan under `plans/approved/<branch>/`. Phase 3 uniquely deletes this task's plan, research, and reviews from the final tree. This is justified by the explicit human README-only constraint and immutable commit URLs, but leaves no in-tree plan record.
- **P3 - brittle scope assertion.** `test "$(git diff --name-only origin/main...HEAD)" = README.md` returns nonzero for multiple files but may report an argument-count error. The verification still fails safely, so this is cosmetic.
- **P2 - checklist wording deferred.** The plan specifies six items by intent rather than exact prose. The mapping covers every acceptance criterion one-to-one, so implementation should preserve that mapping.

VERDICT: READY
