## Adversarial Review: ENG-42 Parallel review agents

I read the full plan, `research.md` context, and opened every source file it touches. The plan's cited anchors are accurate: `principalPrompt`/fallback prose (`execute.go:257-300`, fallback at `:292`), `completedChildResults` success-only gate (`completion_system.go:167`), async `SpawnChild` (`child.go:41-114`), the two target tests (`execute_test.go:53-79`, `completion_system_test.go:152-178`), and README child-agent prose (`README.md:158`). The `authority_unavailable` blocker exists in the allowed enum (`execute.go:300`), and `ProcessResult` carries `Status`/`ExitCode`/`FinishedAt` (`launcher.go:51-58`), so the narrowed completion check compiles. The completion change's only production caller is `completion_system.go:83`, and its only behavior-sensitive test is the one the plan already rewrites (`completion_system_test.go:152`); `completion_test.go`/`manager_test.go` set `ChildrenComplete` directly and are unaffected. Phase verticality, scope discipline, and rollback/rollout behavior are sound.

### P0 / P1

None. No finding rises to catastrophic/irreversible harm or makes the issue impossible/unsafe to implement without a plan change.

### P2 (non-blocking)

- **Verification gate contradicts the negative-assertion test instruction.** Plan line 86 requires the prompt test to "assert stale fallback wording is absent," and the established pattern for that is a literal `strings.Contains(prompt, "<phrase>")` negative check (cf. `execute_test.go:76`, and the existing positive assertion `"Claude review child exits nonzero"` at `:67`). But the verification gate at plan line 158 runs `rg ... "prefers Claude|Claude review child exits nonzero|fallback for the same logical review" internal/agentrun README.md` and requires **no matches** across all of `internal/agentrun` — which includes `execute_test.go`. If the negative assertion reuses any of those three exact phrases, the gate matches the test file and fails, even though the prompt is correctly stripped. Evidence: all three phrases exist today in `execute.go:292`, `execute_test.go:67`, `README.md:158`. Smallest correction: either scope the gate to non-test surfaces (e.g. `execute.go README.md`) or specify that the negative assertion checks a phrase outside the three gate patterns.

### P3 (observations)

- **Deploy invocation form.** Plan line 167 uses `~/.local/bin/nags deploy factory --expected-commit ...` (app positional), while `README.md:230` shows `~/.local/bin/nags deploy --expected-commit ...` (no positional). Rollback/github-hook do take the app positional. The `nags` binary is external and unverifiable from this repo; confirm the deploy signature against the installed CLI before relying on the plan's form. Not blocking (post-merge, human-driven).
- `research.md:20` also contains the gate phrase, but the gate scope excludes `plans/`, so it is harmless — noted only to preempt confusion.

VERDICT: READY
