# ENG-42 Implementation Plan: Parallel review agents

> updated: 2026-07-14T15:46:50-07:00

## Linear issue context

- Issue: ENG-42, `Parallel review agents`
- URL: https://linear.app/nags-cloud/issue/ENG-42/parallel-review-agents
- Problem: Factory currently tells principals to run Claude first and launch Codex only after an operational Claude failure. ENG-42 requires every workflow adversarial review to run both providers while allowing one provider failure to be ignored when the other produces a usable review.
- Repository route: `tomnagengast/factory`, managed primary `/Users/tom/repos/tomnagengast/factory`, issue worktree `/Users/tom/repos/tomnagengast/factory/.worktrees/eng-42-parallel-review-agents`.

## Acceptance criteria

1. Every Factory adversarial review round instructs the principal to launch one Claude child and one Codex child with the identical rendered prompt before waiting for either result.
2. When both reviews are usable, a P0/P1 finding or `REVISE` verdict from either provider blocks progression; `READY` requires both usable reviews to be ready.
3. When exactly one provider terminates operationally or has no usable verdict, the surviving usable review determines the round and the failed provider is preserved as evidence without causing a fallback round.
4. When neither provider yields a usable review after safe retries, implementation does not begin and the existing `authority_unavailable` lifecycle blocker is used.
5. Factory terminal completion treats a failed child process with a valid terminal result as finished evidence, while missing, malformed, or unfinished child results continue to block cleanup/completion.
6. Operator documentation describes the same policy, and all repository-required verification passes.

## Research questions and evidence-backed answers

1. **Where is provider policy defined?** `principalPrompt` contains the Claude-first fallback contract (`internal/agentrun/execute.go:257-300`); Go does not otherwise orchestrate plan review providers.
2. **Can current mechanics launch both?** Yes. `SpawnChild` creates and returns a detached tmux window without waiting (`internal/agentrun/child.go:41-119`), so two calls can launch independently before either result is consumed.
3. **How are verdicts combined?** The existing review contract makes only P0/P1 blocking and emits `READY` or `REVISE`; the safe combination is the union of usable P0/P1 findings. A usable `REVISE` from either provider blocks the round.
4. **What conflicts with tolerated failures?** `completedChildResults` currently requires every terminal child result to be successful (`internal/agentrun/completion_system.go:144-171`), which mislabels a tolerated failed provider as unfinished work.
5. **What pins the behavior?** `internal/agentrun/execute_test.go:53-79`, `internal/agentrun/completion_system_test.go:152-178`, and `README.md:137-158` encode the current fallback and success-only semantics.
6. **What deployment applies?** Factory is a deployable service. `nags.toml`, the live `nags` CLI, and `README.md:228-280` establish commit-pinned deployment from clean merged primary `main`, exact identity-aware health checks, receipts, and rollback.

The full evidence record is `plans/planning/eng-42-parallel-review-agents/research.md`.

## Current behavior and root cause

`principalPrompt` tells the LLM principal to prefer Claude and spawn a Codex child only if Claude fails operationally. That policy prevents two independent reviews from running on every round. The child launcher itself is already asynchronous and provider-neutral, so the orchestration gap is instructional rather than infrastructural.

A separate completion bug makes the requested tolerance mechanically impossible at the terminal boundary: `completedChildResults` equates nonzero child exit status with incomplete work. A failed provider remains a real, finished child record and is useful evidence; whether the logical review had a usable surviving verdict is owned by the principal and gated before implementation. Terminal completion should therefore check that every launched child has stopped and written a valid result, not require every optional child process to succeed.

## Decisions

### Dual-review round contract

- Replace the sequential fallback paragraph in `principalPrompt` with explicit instructions to start both provider children from the same rendered review prompt before waiting.
- Require the principal to wait for and consume every child result.
- Treat two usable reviews as one logical round. `READY` requires both to be ready; any concrete P0/P1 or `REVISE` from either triggers only the smallest required plan revision and another dual-provider round.
- Treat one operational failure or unusable verdict as tolerated when the other review is usable. Preserve and report the failed result, but do not launch a fallback because the peer already ran.
- If neither result is usable after safe retries, stop before implementation through `authority_unavailable`. Do not add a new blocker enum or keep an LLM turn sleeping for provider limits to reset.

### Child completion semantics

- Keep returning complete when no child directory exists.
- For each child directory, continue to require a readable and decodable `result.json` with non-zero `finishedAt`.
- Stop requiring `status == succeeded` and `exitCode == 0` for the mechanical `ChildrenComplete` signal. A valid finished failure is complete; a missing or zero-finish result is incomplete.
- Keep malformed JSON and filesystem errors as errors so corrupt evidence cannot become success.

### Documentation

- Update the README child-agent section to describe parallel dual review, conservative verdict combination, one-provider tolerance, both-provider unavailability, and terminal-result retention.

## Alternatives considered

1. **Add a Go review scheduler.** Rejected because the repository currently delegates review content, round selection, plan remediation, and result interpretation to the `$do` principal. Rebuilding that state machine in Go adds duplicate lifecycle authority without being required for concurrency.
2. **Keep sequential fallback but always run Codex after Claude succeeds.** Rejected because it serializes provider latency and preserves misleading fallback semantics instead of spawning both before waiting.
3. **Ignore every child directory regardless of result.** Rejected because missing, corrupt, or still-running child evidence must continue to block terminal cleanup.
4. **Add a `review_unavailable` blocker type.** Rejected as unnecessary contract expansion. `authority_unavailable` is already a valid pre-PR blocker for unavailable required reviewers.
5. **Classify only specially named review children as optional.** Rejected because child names are agent-chosen slugs, not durable roles. The clean semantic boundary is finished versus unfinished child execution; logical review sufficiency remains with the principal.

## Assumptions

- Factory's generated principal prompt is the repository-owned instruction surface for this issue and can explicitly supersede the generic provider-installed skill's older fallback wording.
- A principal that has no usable review cannot honestly reach the plan gate or implementation phase, so terminal completion does not need to re-evaluate review text.
- Increased provider consumption is intentional scope: every adversarial review now runs both configured children.

## Non-goals

- Changing provider models, effort levels, retry counts, maximum concurrent Factory runs, settings schema, or tmux child mechanics.
- Changing review priority definitions, the three-round maximum, research/plan human gates, or Linear comment correlation.
- Adding provider-result synthesis APIs or parsing review content inside Go.
- Modifying the external provider-owned `$do` skill checkout or any T9 mirror.
- Changing human-only merge authority, exact verified-head validation, deployment safeguards, or cleanup policy.

## Impacted files and interfaces

- `internal/agentrun/execute.go`
  - `principalPrompt`: replace sequential fallback prose with the dual-launch, verdict-combination, failure-tolerance, and both-unavailable contract.
- `internal/agentrun/execute_test.go`
  - `TestPrincipalPromptGroupsChildAgentsInTmux`: assert both provider invocation forms, same-prompt parallel timing, wait/consume requirements, conservative `REVISE` handling, one-provider tolerance, and `authority_unavailable` behavior; assert stale fallback wording is absent.
- `internal/agentrun/completion_system.go`
  - `completedChildResults`: require terminal result evidence rather than successful exit status.
- `internal/agentrun/completion_system_test.go`
  - Rename and expand the child-result test to cover no children, missing result, zero `finishedAt`, terminal failed result, and terminal successful result.
- `README.md`
  - Child agents section: document the deployed dual-review policy and result semantics.

No public HTTP, storage schema, command-line, settings, or network interface changes.

## Implementation phases

### Phase 1: Encode the parallel review contract

1. Update `principalPrompt` so all generated Factory segments receive the same mandatory dual-review behavior.
2. Update the prompt test with positive assertions for both provider launches and the combination/failure rules plus a negative assertion against the old Claude-first fallback text.
3. Update the README child-agent contract to match.

Success criteria:

- Focused prompt tests pass.
- Generated prompt text unambiguously requires both children to start before waiting, retains every P0/P1 finding, tolerates exactly one provider failure, and blocks when neither provider is usable.

### Phase 2: Make terminal child evidence compatible with tolerated failures

1. Narrow `completedChildResults` to validate terminality: result file exists, JSON decodes, and `finishedAt` is non-zero.
2. Preserve error behavior for unreadable or malformed evidence.
3. Expand tests to prove terminal failed and successful results are complete while absent and unfinished results are not.

Success criteria:

- The focused completion tests pass.
- A finished failed child no longer causes `ChildrenComplete` to be false.
- Missing, malformed, or unfinished child evidence remains non-complete or errors as appropriate.

### Phase 3: Review the complete diff and run publication verification

1. Run focused tests after each phase.
2. Inspect the diff for stale fallback prose, accidental lifecycle changes, secrets, generated cache files, and unrelated churn.
3. Run every repository-required suite and frozen frontend build.
4. Record exact commands and the verified head in the PR and Linear implementation summary.

Success criteria:

- All verification matrix commands pass from a clean issue worktree.
- The draft PR is updated, marked ready, and reaches the complete ready-for-human-merge predicate before checkpointing.

## Data, security, compatibility, migration, rollout, and rollback

- **Data:** No persistent schema or migration. Existing child `result.json` files remain unchanged.
- **Security:** Child isolation, provider allowlist, private file permissions, secret filtering, and read-only review prompts remain unchanged. Requiring a terminal result prevents abandoned children from being hidden.
- **Compatibility:** Previously successful child results still count as complete. Existing finished failed child evidence becomes correctly classifiable as complete. No serialized format changes.
- **Migration:** None. The completion behavior applies when old retained runs are evaluated, fixing their false-incomplete state without rewriting artifacts.
- **Rollout:** Merge through the exact verified-head gate and deploy the Factory service from updated clean primary `main`.
- **Code rollback:** Revert the merged commit on `main`, repeat the required verification, and deploy the resulting clean updated `main` commit. Do not deploy a worktree or unmerged revert.
- **Runtime recovery:** Capture the currently successful deployment ID before deployment. If the new release or identity probes fail, preserve failed receipts and restore that prior release with `~/.local/bin/nags rollback factory --to <deployment-id>`, then verify local and public health.

## Verification matrix

| Acceptance criterion or risk | Verification |
| --- | --- |
| Both providers are launched from the same prompt before waiting | `go test ./internal/agentrun -run TestPrincipalPromptGroupsChildAgentsInTmux -count=1`; inspect generated prompt assertions for `--provider claude`, `--provider codex`, identical prompt, and launch-before-wait wording |
| Either usable P0/P1 or `REVISE` blocks the round | Same focused prompt test asserts conservative combination semantics |
| One operational failure is tolerated | Same focused prompt test asserts the surviving usable review determines the round and no fallback round is created |
| Neither usable review prevents implementation | Same focused prompt test asserts `authority_unavailable` behavior |
| Finished failed child is complete evidence | `go test ./internal/agentrun -run TestCompletedChildResults -count=1` |
| Missing or unfinished child remains incomplete | `go test ./internal/agentrun -run TestCompletedChildResults -count=1` |
| Malformed child evidence still fails closed | Add malformed JSON case to `TestCompletedChildResults`; run the same focused command |
| Existing agent lifecycle behavior remains green | `go test ./...` |
| Concurrency safety | `go test -race ./...` |
| Static correctness | `go vet ./...` |
| Frozen frontend and integrated build inputs remain valid | `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile && MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` |
| No stale fallback docs or prompt remain in repository-owned surfaces | `rg -n "prefers Claude|Claude review child exits nonzero|fallback for the same logical review" internal/agentrun README.md` must return no matches |

## Post-merge deployment, verification, and recovery

Precondition: GitHub authoritatively reports a human merged the exact locally verified PR head, every final check/review/comment/thread/Linear safeguard still passes, and the primary checkout resolved by Worktrunk is clean.

1. From `/Users/tom/repos/tomnagengast/factory`, fetch/prune `origin`, fast-forward `main`, and prove local `HEAD == origin/main` before deployment.
2. Capture the current receipt and deployment ID:
   - `jq . ~/.local/share/factory/deployments/current.json`
3. Deploy the exact updated-main commit:
   - `~/.local/bin/nags deploy factory --expected-commit "$(git rev-parse HEAD)"`
4. Verify the immutable release identity and health:
   - `curl -fsS http://127.0.0.1:8092/api/healthz | jq .`
   - `curl -fsS https://factory.nags.cloud/api/healthz | jq .`
   - `jq . ~/.local/share/factory/deployments/current.json`
   - Confirm commit, tree, build ID, deployment ID, application, and lifecycle contract agree and identify the deployed merged-main source.
5. Verify the issue tmux session survived the service restart and refresh the authoritative GitHub/Linear state before branch cleanup.
6. On failure, preserve pending/failed receipts, run `~/.local/bin/nags rollback factory --to <captured-deployment-id>`, and prove both health endpoints recovered. Report `deployment_failed`; do not claim success or hide the failure with cleanup.

After successful deployment, follow the mandatory bounded GitHub auto-deletion retries, fetch/prune verification, clean integrated Worktrunk removal, final health/receipt ancestry check, Linear completion, and final feedback refresh.

## Unresolved questions

None.
