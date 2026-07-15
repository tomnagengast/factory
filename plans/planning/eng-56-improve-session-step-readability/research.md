# ENG-56 Session Step Readability Research

> updated: 2026-07-15T16:26:40-07:00

## Research questions

1. What current behavior and readability failure are proven by the issue references, repository, and live provider records?
2. Which provider event shapes, observer interfaces, frontend components, tests, and callers participate?
3. Which information can explain what ran and why without exposing new private data or coupling the browser to provider JSON?
4. Which visual, responsive, accessibility, compatibility, and security invariants must remain unchanged?
5. What observable acceptance criteria and exact verification cover the change?
6. What post-merge deployment, identity, health, rollback, and recovery commands apply?
7. Which conclusions are facts, which are inferences, and which decisions remain unresolved?

## Issue and routing context

- Linear issue: [ENG-56](https://linear.app/nags-cloud/issue/ENG-56/improve-session-step-readability), “Improve session step readability.” The request says a step should immediately explain what ran and why, and explicitly asks Factory to draw from the supplied Claude and Codex examples.
- Linear project metadata specifies `GitHub Repo: https://github.com/tomnagengast/factory` and `Local Path: /Users/tom/repos/tomnagengast/factory`.
- Factory supplied `/Users/tom/repos/tomnagengast/factory` as the managed mutable repository. Its normalized `origin` is `tomnagengast/factory`, exactly matching the allowlisted project metadata. The repository root is the subsystem scope.
- Worktrunk created `eng-56-improve-session-step-readability` from fetched `origin/main` at `92f36186f0c7aeec07ddb1509a1ed883559c5a77`.
- Intake found no prior ENG-56 branch, worktree, pull request, checkpoint, Linear gate comment, attachment record, parent, or child issue. The issue has `Factory` but not `Yolo`; each later gate must refresh labels independently.
- `nr` relatedness discovery was run before exact search. Its local index produced no usable ranked output, so every candidate below was verified through exact search, complete source reads, history, and provider-record structure.
- One bounded read-only Claude research audit completed successfully in the Factory tmux session. It independently reached the same coordinated backend/frontend conclusion, identified Claude tool input/result loss and non-core Codex fallback loss, and confirmed the redaction, stable-ID, authenticated API, responsive, deployment, and rollback constraints recorded below.

## 1. Current behavior and root cause

### The current card leads with transport metadata, not the action

Observed facts:

- The issue's current Factory screenshot shows each collapsed row with a large repeated `COMPLETED` label, a competing `COMMAND EXECUTION` type label, and a command beginning `/bin/zsh -lc ...` truncated before the meaningful target is fully visible.
- `frontend/src/index.tsx:2498-2526` renders every event with the same `<details>` card. Status is first, `summary` is a single-line ellipsized middle column, and the normalized event type is last. Expansion reveals only `step.payload`.
- `frontend/src/styles.css:646-742` gives status and type persistent uppercase columns, forces the summary to one line, and presents the raw JSON payload as the only detail surface. The mobile rules at `frontend/src/styles.css:1401-1427` remove the disclosure glyph but retain status, type, and summary as three competing pieces of information.
- The current help copy in `frontend/src/index.tsx:2690-2697` and `README.md:211` describes the interaction as “expand for raw payload,” confirming that the existing product model is event inspection rather than action comprehension.

Root cause:

The observer exposes provider transport fields (`type`, `status`, one lossy `summary`, and raw payload), and the UI gives those fields equal visual weight. Meaningful provider fields are discarded before rendering, so CSS alone cannot make several step kinds clear.

### Provider normalization loses the fields that explain the work

Observed Codex facts:

- `internal/agentrun/observer.go:49-66` defines `StepView` with only `ID`, `Type`, `Status`, `Summary`, and `Payload`.
- `agentStep` at `internal/agentrun/observer.go:407-427` pretty-prints and redacts the complete record, but `agentEvent` at `internal/agentrun/observer.go:439-459` decodes only item ID/type/status/text/command/aggregated output/file paths plus minimal Claude content fields.
- `stepSummary` at `internal/agentrun/observer.go:477-515` understands only command, text, first changed path, Claude text/name, generic user result, and final result. Unknown item kinds fall back to their machine type.
- A structure-only inspection of this run's private Codex JSONL found `command_execution`, `agent_message`, `mcp_tool_call`, `web_search`, and `error` items. Real `mcp_tool_call` records contain `server`, `tool`, `arguments`, `result`, `error`, and status; `web_search` contains `query` and `action`; `error` contains `message`. The current decoder ignores all of those meaningful fields, making their summaries “mcp tool call,” “web search,” or “error.” No field values or secrets were copied into this artifact.
- Stable Codex item IDs already let `agentSteps` replace `item.started` with `item.completed` in place (`internal/agentrun/observer.go:390-405`). Redaction is applied both to raw payload and formatted terminal output (`internal/agentrun/observer.go:369-383`).

Observed Claude facts:

- A structure-only inspection of the bounded Claude research child's event file found `assistant` content blocks of `thinking`, `text`, and `tool_use`, and `user` blocks of `tool_result`. Tool-use blocks include `id`, `name`, and `input`; observed `Bash` input contains `command` and `description`, while `Read` contains `file_path`. Tool results carry the matching `tool_use_id`, content, and optional error state.
- The current `agentEvent.Message.Content` decoder ignores `id`, `input`, `thinking`, `tool_use_id`, result content, and error state. `agentStep` emits at most one step per JSONL record, even though a Claude message can contain multiple content blocks. A `user` record becomes the generic “Tool result” instead of completing the earlier tool step.
- Retained provider detection in `internal/agentrun/observer.go:276-287` and the shared live/history parse path at `internal/agentrun/observer.go:236-256` and `369-383` mean one normalization fix can serve both live tmux capture and immutable retained history.

Inference:

ENG-56 needs coordinated observer and UI changes. A frontend-only parser would duplicate provider schemas in TypeScript, parse an already-redacted JSON string, and still lack a reliable way to correlate Claude tool use/results. A backend-only summary improvement would leave raw JSON as the only expanded content and preserve the status/type-heavy hierarchy.

## 2. Reference-derived product direction

Observed facts from the five issue images:

- Claude's collapsed presentation interleaves explanatory prose with compact action rows. Rows use a recognizable tool glyph, a human verb such as `Ran`, an operator-written description when available, and a disclosure affordance. A grouped command sheet then labels `Command` and `Output` separately.
- Codex's collapsed presentation also uses an icon plus a human verb (`Ran`, `Spawned`) and the most useful object or command. Expansion shows a labeled tool card (`Shell` in the example), readable command/output content, and an outcome such as `Success`.
- Both references put provider taxonomy and repeated completed status behind the action. Both keep technical detail available on demand.

Chosen direction:

- Evolve the existing Factory console rather than replace its visual system. Keep its dark console surface, window tabs, page hierarchy, native `<details>` interaction, read-only contract, and redacted raw fallback.
- Make the organizing idea “action first, evidence second”: each tool row begins with a small type glyph and plain verb, followed by the clearest available object/description. Completion becomes quiet metadata; in-progress and failed states remain conspicuous.
- Render agent text/reasoning summaries as readable narrative entries in sequence so the surrounding “why” is visible instead of looking like another tool transport event.
- On expansion, show normalized labeled sections such as `Command`, `Input`, `Output`, or `Error` when available, then retain the complete redacted event under a lower-priority `Raw event` disclosure/fallback. Do not discard operator-grade diagnostics.
- Do not group adjacent tools in v1. Claude groups commands while Codex keeps individual actions; stable one-event rows preserve current chronology and retry identity with less implementation risk.
- Reuse inline SVG/CSS primitives and current dependencies. Do not add an icon package, component library, markdown renderer, or frontend test framework for this bounded change.

## 3. Required observer contract

The smallest provider-neutral additive contract is:

- Preserve `id`, `type`, `status`, `summary`, and `payload` for compatibility and diagnostics.
- Add a human action label (for example `Ran`, `Read`, `Searched`, `Updated`, `Used`, `Reported`, or `Responded`).
- Add optional normalized detail and output/error text. All new strings must pass through the same redaction function before leaving `agentrun.Observer`.
- Allow one provider record to yield zero, one, or several steps. Claude `thinking` remains excluded from readable output; text becomes narrative, each `tool_use` becomes a tool step, and a matching `tool_result` updates that step in place by tool-use ID.
- Keep Codex `item.started`/`item.completed` correlation by item ID. Normalize commands, MCP server/tool/arguments/result/error, web queries, file paths, agent messages/reasoning summaries, and explicit error messages.
- Prefer provider descriptions as the collapsed summary when they state intent, but keep the actual command/input in expanded detail. Where no description exists, use the command, query, path, or `server · tool` identity itself.
- Unknown future event types must continue to produce an inspectable fallback with a humanized type and the redacted raw event. Plain non-JSON terminal panes must continue using `WindowView.Output`.

This is an additive authenticated API change. `internal/server/server.go:516-554` passes the observer view through without persistence or a second schema, and `internal/server/server_test.go:488-531` proves the canonical authenticated route. No migration or durable data rewrite applies.

## 4. Invariants and risks

### Privacy and security

- `/api/agents`, canonical observer pages, raw prompts/logs, issue identifiers, paths, and session names remain authenticated (`README.md:292`). This issue does not change routing or auth.
- `Observer.redact` must process every new summary/detail/output field, not only raw payload. Tests must place a secret in each added surface and prove it becomes `[REDACTED]`.
- Do not parse, render, or execute provider content as HTML. Narrative and detail remain Solid text nodes and `<code>`/`<pre>` content.
- Keep the observer read-only. No terminal input, provider mutation, or new API mutation is in scope.

### Compatibility and lifecycle

- Stable IDs and in-place transition from started to completed must remain unchanged for live polling. Expanded state is keyed by window plus step ID in `frontend/src/index.tsx:2354-2377`; changing an ID on completion would collapse the user's open row.
- Live pane capture remains bounded to 128 KiB while retained JSONL history remains complete (`internal/agentrun/observer.go:24-29`, `236-256`, and `369-383`).
- `WindowView.Output` remains the fallback when a pane has no structured steps. Unknown JSON remains readable rather than disappearing.
- Existing clients that only understand the old StepView fields continue to work because the new fields are additive. The repository deploys frontend and Go server atomically, but the fallback is still cheap and explicit.
- No data migration, settings revision, wire behavior, workflow policy, merge authority, deployment gate, or cleanup validator changes.

### Responsive and accessibility

- Keep semantic native `<details>/<summary>`, keyboard toggling, visible `:focus-visible`, and a disclosure affordance at desktop and mobile widths.
- Icons are decorative (`aria-hidden`); action, summary, and non-success status remain text. Do not communicate failed/in-progress state through color alone.
- Long commands and paths must wrap or clamp without forcing horizontal page overflow, while expansion exposes the complete normalized detail. Raw code panes may scroll internally.
- Preserve scroll position and expanded rows across the existing two-second live refresh through stable keyed IDs.
- Loading, no-window, unstructured-output, observer-error/offline, active success, in-progress, failed, and unknown-event fallbacks remain legible. Conflict state does not apply because the observer is read-only and has no optimistic mutation.

## 5. Observable acceptance criteria

1. A Codex command step reads as `Ran <meaningful command>` without leading `/bin/zsh -lc` transport noise when a shell wrapper can be safely unwrapped. Expansion separately exposes the complete command and output plus raw event.
2. Codex MCP, web-search, file-change, message/reasoning, and error steps use their real tool, query, path, text, or message rather than generic machine type fallbacks.
3. Claude text and tool actions appear in chronological order; a Bash/Read tool uses its description or target immediately, and its later result updates the same stable row with output/error state rather than creating “Tool result.”
4. Completed status is visually quiet, while running and failed states remain explicit text and color. Unknown events remain inspectable.
5. All normalized and raw fields are redacted, authenticated, read-only, and rendered as text.
6. Desktop and 390 px mobile layouts show icon, action, useful summary, disclosure, and state without page overflow. Summary is operable with keyboard, retains visible focus, and keeps expanded state during live refresh.
7. Existing empty/loading/error/unstructured-pane behavior and live/retained history continue to work.
8. Operator documentation describes action-first normalized details and the retained raw event fallback.

## 6. Verification evidence and exact commands

Baseline evidence on `92f3618`:

- `go test ./internal/agentrun` passed.
- `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck` passed.
- `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` passed.
- A managed Factory server was already listening on `127.0.0.1:8092`; no temporary server was started during research.

Focused implementation checks:

```text
gofmt -w internal/agentrun/observer.go internal/agentrun/observer_test.go
go test ./internal/agentrun -run 'TestAgentSteps|TestObserver'
go test -race ./internal/agentrun -run 'TestAgentSteps|TestObserver'
go test ./internal/server -run TestAuthenticatedAgentActivityAndReference
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build
```

Browser verification uses deterministic mock Codex and Claude observer responses against the built UI, without touching the running managed service. Inspect at 1440×900 and 390×844; exercise mouse and keyboard disclosure, long command/path wrapping, narrative, command, MCP, search, file change, success, in-progress, failure, unknown event, empty windows, unstructured output, loading, and request failure/offline. Confirm no console error, failed asset request, horizontal page overflow, missing focus indicator, or expansion-state regression. Conflict is explicitly not applicable to this read-only route.

Mandatory Factory publication suite:

```text
go test ./...
go test -race ./...
go vet ./...
MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build
```

## 7. Deployment, health, and recovery

Deployment applies because the change modifies the embedded Factory frontend and Go observer. The pinned workflow requires deployment only from the clean, updated primary `main` checkout after a human merge that contains the exact verified PR head:

```text
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
```

Post-deploy identity and content probes from the primary checkout:

```text
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/home | jq .agentRuns
jq . ~/.local/share/factory/deployments/current.json
tmux -L factory-agents list-sessions
```

Require local and public health plus `current.json` to agree on commit, tree, build ID, deployment ID, and contract version; require both health commits to equal the deployed primary `HEAD`; require the ENG-56 tmux session to survive the service restart. After browser authentication, open this run's canonical observer and verify that the deployed content shows the action-first presentation.

The deployment provider automatically restores the previous release when new-release verification fails and records the failed receipt. If later manual recovery is required, inspect local/public health and receipts first, identify a known successful deployment ID, then run:

```text
~/.local/bin/nags rollback factory --to <deployment-id>
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

Do not stash, reset, deploy from the issue worktree, remove a live deployment lock, or rewrite Factory state to recover. A forward corrective commit deployed from clean merged `main` remains the normal recovery when rollback identity cannot be proven.

## Evidence classification, contradictions, and unresolved questions

- Observed facts are tied above to issue images, current source, repository history, tests, baseline commands, or structure-only inspection of this run's provider JSONL.
- The proposed provider-neutral fields, action vocabulary, narrative treatment, and no-grouping choice are inferences chosen from that evidence. They preserve current contracts while solving both provider families.
- The pinned workflow names `~/.local/bin/nags deploy --expected-commit ...` as the authoritative self-deploy command, while the repository README shows `bin/network-app deploy factory --expected-commit ...`. `bin/network-app` is a compatibility wrapper that execs the same provider after removing the `factory` positional argument. The plan must use the pinned direct command and may retain the documented wrapper only as compatibility context.
- Unresolved questions: None.
