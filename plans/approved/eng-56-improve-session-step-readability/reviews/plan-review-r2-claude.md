I have gathered sufficient evidence. Let me write the review.

## Adversarial Review — ENG-56 Session Step Readability Plan

I read the full plan (`plan.md`, 207 lines) and its `research.md`, then opened every source file the plan touches: `internal/agentrun/observer.go`, `internal/agentrun/observer_test.go`, `frontend/src/index.tsx` (step-rendering at 2351-2539, contract at 155-161, keyed expansion at 2354-2374), `frontend/src/styles.css` (step classes present), `internal/server/server.go` (`writeAgent` passthrough at 540-553), and `README.md:211`.

### Verification of plan claims against the repo

- `StepView` (observer.go:59-65) exactly matches the plan's "only id/type/status/summary/payload" premise; the proposed additions are additive with `omitempty`, so the server passthrough (`writeAgent` → `writeJSON`) carries them with no server change. Plan's "unchanged passthrough" premise (line 72) is correct.
- Codex `item.started`/`item.completed` replacement by item ID (observer.go:390-407, `stepID` at 430-436) exists and supports the plan's stable-ID claim (decisions 3, 8).
- The line-based `agentSteps` dedup (`steps[index] = step`) is full-replacement today; the plan correctly identifies (decisions 2, 7; phase 2) that Claude `tool_result` must *merge* rather than replace, and that one record must yield a slice. This is a real, scoped refactor, not a hidden contradiction.
- Frontend expansion is keyed `${windowID}:${stepID}` (index.tsx:2361-2374), so preserving provider IDs on completion keeps open rows open across the 2s poll — plan's core UX claim holds.
- Verification commands resolve: `TestAuthenticatedAgentActivityAndReference` exists (server_test.go:485); `TestObserver*`/`TestAgentSteps*` match the `-run` regex; step CSS classes exist. README observer paragraph (line 211) exists and matches the "expand for raw payload" copy the plan updates.
- Existing observer tests remain satisfiable under additive changes: command "printf working" (test:72) has no shell wrapper so conservative unwrap is a no-op; file_change summary "main.go" (test:99) and agent_message text (test:102) stay as summaries/narrative.
- Codex `mcp_tool_call`/`web_search`/`error` field names rest on structure-only inspection of this run's real JSONL (research §1, 47) rather than repo fixtures — acceptable evidence given no contradicting fixture exists.

### P0 — none.

### P1 — none.

No plan-level defect makes the issue unachievable, unsafe, or likely-incorrect without a change. Redaction is routed through the existing `redact` at the observer boundary for every new field (decision 10, matching observer.go:575-580); read-only/auth invariants are untouched; rollback/deploy use the pinned `nags` command and forbid worktree deploys.

### P2 / P3 (non-blocking)

- **P2 — multi-block ID collision in the fallback path.** Decision 3 ties the disambiguating block index to "event UUID … when present," and other records fall back to a single line-derived hash (`stepID` today hashes the whole line). If one Claude `assistant` record carries multiple `text`/`tool_use` blocks *and* lacks a UUID, several steps from that one line would collide on the same fallback ID and the dedup map would silently drop all but the last. Claude stream-json records normally include `uuid`, so this is low-likelihood, but the safest correction is one clause: line-derived fallback IDs for multi-step records must always incorporate the block index, independent of UUID presence.
- **P3 — narrative-vs-tool discriminator.** The plan adds `action/detail/output/error` but no explicit "kind" field; the frontend must branch narrative vs tool off `type`. Workable, but worth stating so the mapping is intentional rather than incidental.
- **P3 — correlated-payload duplication.** A multi-`tool_use` assistant record will appear in full inside each correlated row's two-record payload array (whole record, not just the block). Consistent with "retain the complete redacted raw event," just noting it is intended, not lossless-per-block.

### Scope discipline

Non-goals are tight and honored; the documentation update is within the issue's stated acceptance criteria, not added scope. No test/infra inflation.

VERDICT: READY
