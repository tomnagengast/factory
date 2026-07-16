I've completed my validation against the repository. Here is my review.

---

**ENG-56 Adversarial Plan Review**

I read the full plan and research doc, and opened every source file and command the plan touches: `internal/agentrun/observer.go` (StepView, `agentEvent`, `agentStep`/`agentSteps`, redact threading), `frontend/src/index.tsx` (AgentStep type, step rendering, window+step expansion keys), `frontend/src/styles.css`, `README.md`, `internal/agentrun/observer_test.go`, `internal/server/server.go`, and the `nags`/`bun` verification tooling.

**Validation summary (claims confirmed against the repo):**
- Additive `StepView` fields land on the struct at `observer.go:59-65`; server passthrough is real — no `StepView`/`Steps` handling exists in `internal/server/server.go`, so additive `omitempty` JSON flows through unchanged (plan line 72 holds).
- `agentStep → []StepView` refactor is feasible; the dedup-by-ID mechanism (`stepIndexes`, `observer.go:401-407`) already supports started/completed replacement by `item:ID`.
- Research grounds the new decode fields: MCP `server/tool/arguments/result/error`, `web_search` `query/action`, `error.message`, and Claude block `id/name/input`, `tool_use_id`, result content/error (research.md:47,52-53). Struct extension is additive.
- Frontend expansion key is `${windowID}:${stepID}` (`index.tsx:2361-2373`), matching the "window-plus-step" stable-open claim (Decision 3, research point 4).
- Redaction is threaded as a closure into `agentSteps(value, redact)` (`observer.go:381,389`), so Decision 10 per-field redaction is reachable.
- Verification is runnable: `TestAuthenticatedAgentActivityAndReference` exists (`server_test.go:485`); `-run 'TestAgentSteps|TestObserver'` matches existing tests; `MISE_BUN_VERSION=1.3.11 bun ... --cwd frontend` is the repo convention (README.md:222-224); `nags deploy --expected-commit` and `nags rollback <name> --to` both exist in the CLI; health port 8092 matches prior deployments.
- Scope is disciplined; non-goals bound out grouping, markdown, new deps/frameworks, and shell/nav rework.

No P0 or P1 findings. The plan is technically grounded, the phases are independently testable (backend/tests, then frontend, then docs/publish), and it neither under- nor over-reaches the issue.

**P2 (non-blocking)**
- Merge-vs-replace ambiguity for Claude `tool_result`: the current dedup does a full overwrite (`steps[index] = step`, `observer.go:402-403`). The result record lacks the original `tool_use` command/description/input, so a literal reuse of that overwrite would wipe the row's action/summary and leave only output. Plan lines 35/94/97 clearly intend an in-place *merge* (carry forward the tool-use action/summary/detail, add output/error), and the phase-2 success criteria enforce it via tests — but the plan never states "merge, do not overwrite" explicitly. Worth one sentence so the implementer doesn't inherit the overwrite semantics.

**P3 (observations)**
- Multi-block ID collision on the no-UUID path: Decision 3 (line 36) resolves collisions with "event UUID plus block index when present" and tool-use IDs, which correctly anticipates that one assistant record emits several rows (the payload-hash fallback at `observer.go:433-435` would otherwise collide). The wording leaves the *no-UUID, multi-text-block* case implicit; stating that block index always disambiguates would remove the ambiguity.
- The "deterministic browser fixture" (matrix rows for 1440x900/390x844, keyboard/focus, live-refresh persistence) relies on a manual/existing harness not defined in-plan. Consistent with prior approved plans, but not specified here.

VERDICT: READY
