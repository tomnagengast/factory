# ENG-42 Research: Parallel review agents

## Research questions

1. Where is the current Claude-first, Codex-fallback adversarial review behavior defined, and what executes it?
2. Can Factory already launch both providers concurrently in the same run without new orchestration primitives?
3. How should two usable verdicts, one provider failure, and two provider failures affect a logical review round?
4. What completion safeguard would currently reject a run containing a tolerated provider failure?
5. Which tests and operator documentation pin the current behavior?
6. What must remain unchanged, and how will the acceptance criteria and regression risks be verified?
7. What exact post-merge deployment, health verification, and recovery procedure applies?

## Evidence-backed answers

### 1. Current behavior is principal-prompt policy, not a provider scheduler

Observed facts:

- `principalPrompt` tells every Factory principal to use Claude first, launch Codex only after an operational Claude failure, and treat the replacement as the same review round (`internal/agentrun/execute.go:257-300`, especially line 292).
- `TestPrincipalPromptGroupsChildAgentsInTmux` pins that fallback prose through the substrings `Claude review child exits nonzero`, `--provider codex`, and `exact same prompt` (`internal/agentrun/execute_test.go:53-79`).
- The configured workflow only declares `Create and adversarially review the implementation plan`; it does not encode provider selection (`internal/settings/settings.go:100-114`).
- Git history identifies commit `6ba55c0` (`Factory: fall back to Codex reviews`) as the change that introduced the current prompt, test assertions, and README wording.
- The provider-owned `$do` skill installed outside this repository also describes the fallback. That package is not an allowlisted ENG-42 repository surface, so this change must make Factory's generated principal instruction explicit enough to override the older generic skill policy without mutating an external checkout.

Inference:

- The smallest repository-owned control point is the generated principal prompt. No Go scheduler currently decides which provider reviews a plan or combines review text.

### 2. Existing child mechanics already support parallel launches

Observed facts:

- `SpawnChild` accepts either `codex` or `claude`, creates a distinct output directory, and starts a detached tmux window immediately (`internal/agentrun/child.go:41-113`).
- The helper returns the window and durable output paths to the principal; it does not wait for the child (`internal/agentrun/child.go:105-119`).
- `ExecuteChild` writes each child's terminal `result.json` with status, exit code, and finish time (`internal/agentrun/execute.go:124-177`).
- The run-level provider settings already define independent Claude and Codex child models (`internal/settings/settings.go:115-122`).

Inference:

- A principal can spawn one Claude child and one Codex child with the identical rendered prompt before waiting for either. No tmux, child-execution, or settings schema change is needed.

### 3. A logical round should conservatively combine usable reviews

The issue requires both providers for every adversarial review while tolerating provider failure. The existing adversarial review contract makes only P0/P1 findings blocking and gives each provider a terminal `VERDICT: READY` or `VERDICT: REVISE`.

Decision supported by those constraints:

- Spawn both providers for the same logical round with the identical prompt before waiting.
- If both reviews are usable, the round is ready only when both are ready. Any P0/P1 finding or `REVISE` verdict from either provider requires the smallest plan correction, and the next round again launches both providers.
- If exactly one provider fails operationally or produces no usable verdict, preserve the failed result as evidence and use the surviving provider's review. The failure does not consume a second round and does not cause a fallback launch because both were already launched.
- If neither provider produces a usable review, do not implement. Reuse the existing pre-PR `authority_unavailable` blocker after safe retries rather than inventing a new lifecycle-contract blocker type. A later Factory retry can resume once provider capacity or authentication is restored.

This is a conservative union: one provider's `READY` cannot suppress the other's concrete P0/P1 finding.

### 4. Terminal completion currently mistakes a finished failed child for unfinished work

Observed facts:

- `completedChildResults` currently requires every child to have `status == succeeded`, `exitCode == 0`, and a non-zero `finishedAt` (`internal/agentrun/completion_system.go:144-171`).
- The corresponding test explicitly expects a finished failed child to make completion false (`internal/agentrun/completion_system_test.go:152-178`).
- Completion reports this false value as `child work remains incomplete` (`internal/agentrun/completion.go:350-363`).
- Durable memory from ENG-33 records the same production failure mode: a failed Claude attempt remained beside a successful Codex replacement and caused terminal completion to reject an otherwise completed lifecycle.

Inference and required correction:

- ENG-42 cannot honestly tolerate one failed review provider while this completion condition remains. Completion should continue to require every launched child directory to contain a decodable terminal result with a non-zero finish time, but it should treat a finished failed process as completed evidence. The principal and review policy decide whether the surviving review is usable; the mechanical terminal check should detect unfinished child processes, not reinterpret their outcome.
- Missing, malformed, or unfinished child results must remain blocking.

### 5. Tests and docs that must change

Observed facts:

- `internal/agentrun/execute_test.go:53-79` pins the fallback prompt and should instead pin parallel launch, identical prompts, conservative verdict combination, one-provider tolerance, and both-provider failure handling.
- `internal/agentrun/completion_system_test.go:152-178` pins failure-as-incomplete and should instead distinguish a terminal failed result from an absent or unfinished result.
- `README.md:137-158` documents Claude preference and sequential Codex fallback. It should document dual parallel review, combined verdicts, and terminal-result handling.

### 6. Acceptance criteria, invariants, and verification

Acceptance criteria derived from the issue:

1. Every Factory adversarial review round instructs the principal to launch both Claude and Codex with the same prompt before waiting.
2. Concrete blocking findings from either usable review are not discarded.
3. One provider's terminal operational failure is tolerated when the other provider returns a usable review.
4. Two unusable provider results stop progression under existing authority-unavailable handling.
5. A finished failed child no longer makes post-merge completion evidence falsely report unfinished child work; missing, malformed, or unfinished results still do.

Must remain unchanged:

- Human-only merge authority, exact verified-head checkpointing, Linear research and plan gates, review-round limit, repository routing, post-merge deployment source, and cleanup rules.
- Provider model/effort settings, tmux isolation, child output retention, and observer visibility.
- Only P0/P1 findings block implementation; P2/P3 remain non-blocking.

Verification:

- Focused: `go test ./internal/agentrun`
- Required publication suite: `go test ./...`
- Required race suite: `go test -race ./...`
- Required static analysis: `go vet ./...`
- Required frozen frontend build: `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile && MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build`
- Diff-level prompt inspection must prove the old sequential-fallback wording is absent and both provider invocations and combination semantics are present.

Baseline observation: `go test ./internal/agentrun` passes at commit `8a6bf5082dc5b622a0b8e0dc5e77248ad1a7bab9` before ENG-42 changes.

### 7. Deployment, health, and recovery

Observed facts:

- Factory is a deployable service. `nags.toml` defines a frozen Bun frontend build followed by a Go binary build and an identity-aware `/api/healthz` probe.
- Repository documentation exposes the current deployment interface as `~/.local/bin/nags deploy factory --expected-commit "$(git rev-parse HEAD)"`; `nags deploy --help` confirms the optional provider-app argument and required expected commit.
- Deployment must run only from the clean, updated primary `main` checkout at `/Users/tom/repos/tomnagengast/factory`, never from this issue worktree or the T9 mirror.
- Success requires local and public health plus the current deployment receipt to agree on commit, tree, build ID, deployment ID, and lifecycle contract version (`README.md:228-254`).
- Recovery uses the prior verified deployment ID with `~/.local/bin/nags rollback factory --to <deployment-id>`, followed by the same local and public health probes (`README.md:268-274`).

Exact post-merge procedure established for the plan:

1. Capture the current successful deployment receipt and deployment ID.
2. From clean updated primary `main`, run `~/.local/bin/nags deploy factory --expected-commit "$(git rev-parse HEAD)"`.
3. Verify `curl -fsS http://127.0.0.1:8092/api/healthz | jq .`, `curl -fsS https://factory.nags.cloud/api/healthz | jq .`, and `jq . ~/.local/share/factory/deployments/current.json` all identify the deployed merged-main commit and the same release identity.
4. Verify the Factory tmux session survived the service restart and refresh authoritative GitHub state before cleanup.
5. On deployment or identity mismatch, preserve the failed receipt, run `~/.local/bin/nags rollback factory --to <captured-deployment-id>`, and prove both health endpoints recovered before reporting `deployment_failed`.

## Sources and corroboration

- Linear issue ENG-42, `Parallel review agents`, read 2026-07-14.
- Repository source and history at baseline `8a6bf5082dc5b622a0b8e0dc5e77248ad1a7bab9`.
- Memoryd fact `factory-child-fallback-completion-bug`, sourced from `internal/agentrun/completion_system.go:145` and ENG-33 completion evidence.
- Read-only Claude research child `research-review-flow-94f99ff9`, completed successfully at 2026-07-14T22:44:48Z. Its prompt-orchestration findings were corroborated locally; its conclusion that no completion change was required was superseded by direct completion-code and durable-memory evidence above.

## Contradictions

- The provider-owned `$do` skill still describes sequential fallback, while ENG-42 requires parallel review. Factory's repository-owned generated prompt is the deployable authority available in this issue and must state the new policy explicitly.
- The generic lifecycle skill text names `bin/network-app deploy factory`, but that command is absent from this managed checkout and host. The repository's README, live `nags` CLI, and `nags.toml` establish `~/.local/bin/nags deploy factory --expected-commit ...` as the executable Factory deployment path.

## Assumptions

- `authority_unavailable` is the correct existing pre-merge blocker when neither provider can produce a usable review after safe retries.
- A child process result may be failed yet complete. Review sufficiency remains the principal's responsibility; the terminal completion reader only proves every launched child stopped and wrote durable result evidence.

## Unresolved questions

None.
