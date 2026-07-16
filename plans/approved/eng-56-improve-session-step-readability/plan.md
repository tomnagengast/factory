# ENG-56 Session Step Readability Implementation Plan

> updated: 2026-07-16T12:21:33-07:00

## Issue context and acceptance criteria

- Issue: [ENG-56, Improve session step readability](https://linear.app/nags-cloud/issue/ENG-56/improve-session-step-readability).
- Make each observed agent step immediately explain what ran and why, using the supplied Claude and Codex references as product evidence while preserving Factory's existing console identity.
- Show agent narrative in sequence and show tool work as action-first rows whose collapsed text names the useful command, tool, query, path, description, or error.
- Put normalized technical evidence such as command, input, output, and error behind native disclosure, and retain the complete redacted raw event as a lower-priority diagnostic fallback.
- Support real Codex command, MCP, web search, file change, message/reasoning, and error events plus real Claude text, tool-use, and tool-result events.
- Preserve stable IDs across started/completed or tool-use/result updates so an expanded row remains expanded during live polling.
- Keep completion visually quiet while running and failed states remain explicit in text and color.
- Preserve authentication, redaction, read-only behavior, unknown-event fallback, unstructured pane output, live and retained history, keyboard access, visible focus, mobile layout, and page overflow safety.
- Update operator documentation for the action-first presentation and raw-event fallback.
- Research approval: Linear reaction `3d008d01-f75b-4ad9-a1fc-ccd8a1da6467` (`white_check_mark`) on comment `df1322c2-ccdc-4573-ad00-ce6475d17bc6`, observed in the authoritative refresh at 2026-07-15T17:53:21-07:00.

## Evidence-backed research and root cause

The complete research is in `plans/planning/eng-56-improve-session-step-readability/research.md`.

1. `StepView` currently exports only `id`, `type`, `status`, one lossy `summary`, and raw `payload`. The UI gives status and provider type persistent weight, forces the summary to one line, and exposes only raw JSON.
2. Codex records in the current run include `command_execution`, `agent_message`, `mcp_tool_call`, `web_search`, and `error`. The decoder drops MCP server/tool/arguments/result/error, search query/action, and explicit error message fields, so meaningful events collapse to machine-type names.
3. Claude records can contain several `thinking`, `text`, and `tool_use` blocks, followed by `tool_result` blocks keyed by `tool_use_id`. The current decoder ignores tool IDs, inputs, result content, and error state, and emits at most one step per record. Results therefore become generic separate rows instead of completing the original tool action.
4. Codex already supplies stable item IDs, and Claude supplies stable tool-use IDs. The observer's in-place step replacement and the frontend's window-plus-step expansion keys can preserve open disclosure if those provider IDs remain authoritative.
5. Both supplied products organize the experience around a small glyph, a human action, the useful object or intent, and progressive disclosure. Provider taxonomy and repeated completed status are secondary.
6. Live tmux capture and retained JSONL history share the same observer parsing path. A provider-neutral backend normalization serves both without duplicating provider schemas in TypeScript.
7. The observer already redacts raw payload and formatted output, the agent route is authenticated, Solid renders text nodes safely, and `WindowView.Output` handles unstructured panes. New normalized strings must join these existing boundaries rather than bypass them.

Root cause: Factory normalizes provider transport metadata but discards the provider fields that convey action, target, intent, result, and failure. The frontend then treats the remaining status/type/summary fields as peers. Neither a CSS-only restyle nor a backend-only summary tweak can satisfy the issue across both providers.

## Decisions

1. **Add a provider-neutral presentation contract.** Preserve every existing `StepView` field and add `action`, optional `detail`, optional `output`, and optional `error`. Existing clients remain compatible, and raw `payload` remains the complete diagnostic record.
2. **Return several normalized steps from one provider record.** Refactor record parsing from a single optional step to a slice. Claude `thinking` produces no visible row, `text` produces narrative, each `tool_use` produces a tool row, and `tool_result` merges output/error into its matching tool-use row without replacing the original action, summary, or detail. Codex keeps one step per item event and started/completed replacement by item ID.
3. **Use provider IDs before deterministic fallbacks.** Codex item IDs and Claude tool-use IDs are the stable row IDs. Claude narrative blocks use event UUID plus block index when present; other records retain deterministic line-derived fallback IDs. Completion/result updates never change the original tool row ID.
4. **Normalize meaning on the server.** Decode only the extra provider fields needed for readable presentation. Select a plain action and the clearest summary from description, command, server/tool, query, path, text, or error in that order appropriate to the step kind. The browser never parses provider JSON.
5. **Unwrap shell transport conservatively.** For collapsed Codex commands only, remove a leading `/bin/zsh -lc` or equivalent recognized shell wrapper when its quoted argument can be parsed losslessly. Keep the complete original command in `detail`; malformed or unfamiliar wrappers stay unchanged.
6. **Treat narrative separately from tools.** Agent text/reasoning uses a narrative row with no decorative status/type competition. Tool rows use an inline decorative SVG selected from the normalized type/action plus readable action and summary text.
7. **Use progressive evidence disclosure.** A tool row expansion renders labeled normalized sections in the order `Command`/`Input`, `Output`, and `Error` as applicable. A nested or trailing `Raw event` disclosure retains the redacted payload. For an uncorrelated row, `payload` remains one pretty-printed raw JSON object. For a Claude tool row completed by a later `tool_result`, `payload` becomes one pretty-printed JSON array containing the original assistant/tool-use source record followed by the user/tool-result source record; both records are redacted before serialization and remain in source order. Unknown events with no normalized detail remain directly inspectable through raw payload.
8. **Make state hierarchy intentional.** Completed is quiet metadata and may be omitted from the collapsed visual label. Running and failed remain explicit text, styled state, and accessible content. State is never communicated by color alone.
9. **Keep native semantics and existing visual language.** Retain `<details>/<summary>`, keyed expansion state, current console surface, inline SVG/CSS, text rendering, and current dependency set. Add no component, icon, markdown, or test framework.
10. **Redact at the observer boundary.** Apply `Observer.redact` independently to every new action, summary, detail, output, and error string before returning `AgentView`; retain the existing raw-payload redaction. Tests place secret material in every surface.
11. **Keep v1 rows independent.** Do not group adjacent tools. Individual stable rows preserve provider chronology, retries, updates, and expansion ownership with less risk.

## Alternatives considered

- **Frontend-only parsing of raw payload:** rejected because it duplicates provider schemas, operates on serialized redacted JSON, cannot reliably correlate Claude tool results, and spreads the trust boundary into the browser.
- **Only improve the existing summary string:** rejected because expanded content would remain raw JSON and narrative, command/input, output, and error would still compete in one field.
- **Replace the console with a new chat transcript:** rejected because the issue asks for step clarity, not a navigation or product-shell rewrite, and Factory's window tabs and operator-grade diagnostics remain useful.
- **Group adjacent commands like Claude:** deferred because provider behaviors differ and grouping complicates stable identity, chronology, retries, live replacement, and disclosure state.
- **Render provider markdown or HTML:** rejected because it adds dependencies and expands the content-security surface. Plain text and code blocks satisfy the evidence.
- **Add a frontend test framework:** rejected for this bounded view change. Type checking, production build, deterministic browser verification, and backend contract tests cover the changed surface without new infrastructure.

## Non-goals

- Changing workflow instructions, routing, one-run ownership, human merge authority, checkpoints, completion, deployment-source, or cleanup validation.
- Changing agent provider execution, tmux capture limits, retention, event storage, authentication, terminal input, or any mutating agent API.
- Grouping steps, adding filtering/search, syntax highlighting, markdown rendering, copy buttons, timestamps, duration metrics, or retry controls.
- Reworking the overall Factory page shell, navigation, window tabs, colors, typography system, or unrelated responsive surfaces.
- Migrating retained JSONL, rewriting historical records, changing a persistent schema, or bumping the lifecycle contract.

## Impacted files and interfaces

- `internal/agentrun/observer.go`: additive `StepView` presentation fields; provider event decoder extensions; record-to-steps parsing; Claude correlation; Codex/Claude action, summary, detail, output, error normalization; conservative shell-wrapper display helper; redaction of every presentation field; unknown/unstructured fallback preservation.
- `internal/agentrun/observer_test.go`: realistic Codex and Claude fixtures covering multiple content blocks, started/completed and tool-use/result replacement, stable IDs, action selection, shell wrapper handling, MCP/search/file/error fields, unknown events, retained/live behavior, unstructured output, and all-field redaction.
- `frontend/src/index.tsx`: additive `AgentStep` fields; narrative and tool presentation components; inline decorative step icons; labeled normalized evidence; raw-event fallback; quiet success and explicit non-success states; existing keyed expansion behavior.
- `frontend/src/styles.css`: action-first hierarchy, narrative/tool rows, icon/state/detail styles, focus-visible behavior, long-line handling, nested raw disclosure, and 390 px responsive rules.
- `README.md`: update the complete current observer documentation to describe readable normalized actions/details and the retained redacted raw event.
- PR body and Linear comments: reviewed-plan, implementation, exact verification, safeguards, and verified-head evidence.

`internal/server/server.go` remains an unchanged passthrough unless implementation evidence disproves that premise. The authenticated `/api/agents` route and persistent stores do not change.

## Vertical implementation phases

### 1. Normalize readable Codex steps without changing existing fallbacks

Files: `internal/agentrun/observer.go`, `internal/agentrun/observer_test.go`.

- Add the presentation fields and a shared constructor that redacts all normalized strings.
- Decode and normalize command, message/reasoning, MCP call, web search, file change, and explicit error records.
- Preserve stable item IDs and started/completed replacement.
- Add conservative shell-wrapper display unwrapping while retaining the full command detail.
- Keep unknown JSON inspectable and plain terminal panes on `WindowView.Output`.

Success criteria: realistic Codex fixtures produce action-first rows and labeled evidence; started/completed rows retain ID/order; malformed wrappers and unknown types safely fall back; secrets are redacted from every field.

### 2. Normalize chronological Claude narrative and correlated tools

Files: `internal/agentrun/observer.go`, `internal/agentrun/observer_test.go`.

- Decode message UUID, content block IDs, names, inputs, descriptions, paths, tool-result IDs/content/error, and text.
- Emit multiple steps per record, skip private thinking blocks, and preserve narrative/tool chronology.
- Use tool-use ID for the action row and merge the matching result into it without discarding the tool-use presentation fields. Replace its single-record payload with the ordered two-record redacted JSON array defined above.
- Preserve a readable generic fallback for unmatched or future content while retaining its raw redacted event.

Success criteria: a mixed Claude fixture renders text, Bash, Read, and later results in order; results update stable tool rows rather than producing generic duplicates; the correlated row preserves distinct markers from both raw source records in source order; failure and unmatched-result cases are explicit and inspectable; all surfaces are redacted.

### 3. Render the action-first transcript and progressive evidence

Files: `frontend/src/index.tsx`, `frontend/src/styles.css`.

- Extend the TypeScript contract additively.
- Render narrative and tool rows with decorative inline icons, plain actions, useful summaries, and explicit running/failed text.
- Render normalized detail/output/error sections on expansion, followed by the raw redacted event fallback.
- Preserve native summary keyboard behavior, visible focus, keyed expansion state, window switching, loading/empty/error/unstructured states, and internal code-pane scrolling.
- Ensure long commands and paths wrap or clamp without horizontal page overflow at desktop and 390 px widths.

Success criteria: the deterministic browser fixture shows every supported step kind and fallback with no console/asset/overflow error; keyboard disclosure and focus work; an expanded stable-ID row stays open across refreshed data.

### 4. Document, integrate, and publish

Files: `README.md`, plan/PR/Linear evidence.

- Re-read and update the full README observer section without changing unrelated operator contracts.
- Run focused, interactive, and mandatory Factory verification from the issue worktree.
- Review the complete diff for secrets, debug artifacts, generated output, stale comments, accidental churn, and unrelated changes.
- Push the verified implementation, update and ready the PR, then enter the GitHub and Linear green loop.

Success criteria: documentation matches the shipped UI and API; all verification is recorded; the PR head exactly matches the local verified head; every check, review, thread, PR comment, and Linear comment is clear before checkpointing.

## Data, migration, compatibility, rollout, and rollback

### Data and migration

- No persistent data, event, settings, repository routing, receipt, or lifecycle schema changes.
- Retained provider JSONL remains the source of truth and is normalized only when read. Existing retained runs gain the presentation when observed after deployment.
- `StepView` changes are additive JSON fields with `omitempty` for optional evidence. Existing field consumers continue to work.
- No migration, backfill, or contract-version bump applies.

### Security and compatibility

- The observer remains read-only and the canonical route remains authenticated.
- All provider content remains Solid text or code content, never HTML execution or markdown rendering.
- Every normalized string is independently redacted before serialization, and the raw payload retains its existing redaction.
- Live capture remains bounded, retained history remains complete, unknown JSON remains inspectable, and unstructured pane output remains available.
- Stable provider IDs remain authoritative so polling does not reorder steps or collapse open disclosure.

### Rollout

- Backend and embedded frontend ship atomically in the Factory binary.
- Before publication, validate with provider-shaped fixtures and a deterministic local browser fixture rather than mutating the managed running service.
- After human merge, deploy only from the updated clean primary `main` checkout and prove the merge contains the exact checkpointed head.

### Rollback and recovery

- Before merge, correct or revert the issue branch normally.
- After merge, prefer a corrective or revert commit merged to `main` and deploy that exact commit.
- If immediate recovery is required, inspect current and retained receipts, choose a previously successful deployment ID, and run the pinned rollback command. Never deploy from the issue worktree, rewrite retained events, remove a live lock, stash around a dirty primary checkout, or destroy evidence.

## Verification matrix

| Acceptance criterion or risk | Verification |
| --- | --- |
| Codex command, MCP, search, file, narrative, and error normalization | Table-driven realistic fixtures in `internal/agentrun/observer_test.go`; `go test ./internal/agentrun -run 'TestAgentSteps|TestObserver'` |
| Claude multi-block chronology and tool-result correlation | Mixed assistant/user fixture with text, Bash, Read, success, failure, and unmatched result; assert order, stable IDs, merged presentation fields and output/error, and distinct raw markers from both source records in the ordered correlated payload |
| Stable live transition and retained history | Existing plus focused started/completed and retained-log tests; `go test -race ./internal/agentrun -run 'TestAgentSteps|TestObserver'` |
| Shell wrapper safety | Cases for valid quoted wrapper, spaces/newlines, malformed quote, unfamiliar shell/flags, and no wrapper; collapsed summary may unwrap only when lossless while detail remains exact |
| Unknown JSON and unstructured pane fallback | Unknown item fixture retains humanized summary/raw payload; non-JSON pane retains `WindowView.Output` |
| Redaction and text safety | Seed sentinel secrets into action, summary, detail, output, error, and payload; assert no sentinel in serialized view; frontend renders only text/code nodes |
| Authenticated API regression | `go test ./internal/server -run TestAuthenticatedAgentActivityAndReference` plus full server suite |
| Type contract and embedded production bundle | `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck`; `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` |
| Desktop/mobile visual hierarchy | Deterministic mock observer data at 1440x900 and 390x844 covering narrative, command, MCP, search, file, success, running, failure, unknown, empty, and unstructured states |
| Keyboard/focus/live refresh | Tab to each summary, toggle with keyboard, verify visible focus and disclosure; refresh fixture with same IDs and assert open row persists |
| Loading/offline/error and overflow | Delay/fail observer request; inspect console/network; assert readable fallback and no page-level horizontal overflow with long commands/paths |
| Complete Factory regression | `go test ./...`; `go test -race ./...`; `go vet ./...` |
| Frozen frontend publication | `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile`; typecheck; build |
| Diff and process hygiene | `git diff --check origin/main...HEAD`; full diff/status inspection; confirm no temporary server/browser/helper process remains |
| PR and Linear safeguards | Fresh GitHub PR/check/review/comment/thread state after each wake; fresh complete Linear conversation after each feedback wake; answer all actionable feedback |
| Exact verified head | Local `git rev-parse HEAD` equals GitHub `headRefOid` immediately before `factory agent checkpoint ready-for-merge` |

Conflict-state verification is not applicable because the observer is read-only and has no optimistic mutation.

## Exact post-merge deployment and recovery

After GitHub reports a human merge commit that contains the exact checkpointed head, resolve the single managed primary checkout at `/Users/tom/repos/tomnagengast/factory`, fetch/prune, fast-forward tracked `main`, and require clean local `main` to equal upstream. Deploy only there:

```bash
bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
```

Post-deploy health, identity, content, receipt, and session probes:

```bash
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/home | jq .agentRuns
jq . ~/.local/share/factory/deployments/current.json
tmux -L factory-agents list-sessions
```

Require local health, public health, and the current receipt to agree on commit, tree, build ID, deployment ID, and lifecycle contract; require both health commits to equal deployed primary `HEAD`; require the ENG-56 session to survive restart. In the authenticated deployed observer, confirm this run shows action-first rows, normalized evidence, and raw fallback.

If the deployment provider's automatic restoration does not recover a failed release, inspect local/public health and retained receipts, identify a known successful deployment ID, then run:

```bash
bin/network-app rollback factory --to <deployment-id>
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

After successful deployment, verify GitHub auto-deleted the remote issue branch, fetch/prune, consume all child windows, remove the clean integrated issue checkout through Worktrunk without force, re-run health, refresh GitHub and Linear, move ENG-56 to Done, and publish merge/deployment/cleanup evidence.

## Unresolved questions

None.
