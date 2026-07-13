# ENG-32 Research: Add settings page

Linear: https://linear.app/nags-cloud/issue/ENG-32/add-settings-page

## Research questions

1. What is configurable today, where is it defined, and which process consumes it?
2. Which webhook events currently start or resume agents, and what behavior must remain invariant?
3. What is the smallest settings model that supports triggers, workflows, workflow steps, and spawn settings without turning the UI into an unsafe command editor?
4. Where do authenticated settings APIs and a `/settings` page fit in the existing server and frontend?
5. How should settings persist across service restarts and remain consistent across concurrent server, principal, and child processes?
6. Which acceptance criteria are observable, and how will each be verified?
7. What compatibility, security, rollout, rollback, and deployment constraints apply?

## Evidence-backed answers

### 1. Current configuration and process boundaries

Observed facts:

- The Linear label name is the compile-time `triggerLabel = "Factory"`, and `agentTrigger` accepts only an allowed actor's `Issue/update` that newly adds that label (`internal/server/server.go:29-35`, `internal/server/server.go:642-669`).
- Human comments are wake events only when they come from the configured actor and are not Factory-authored. They create or coalesce a continuation through `dispatchLinear` and `Store.ClaimContinuation` (`internal/server/server.go:424-448`, `internal/server/server.go:519-548`).
- A claimed run flows through `Manager.reconcile` into `TmuxLauncher.Start`, which starts `factory agent-exec` in a separate tmux process (`internal/agentrun/manager.go:137-190`, `internal/agentrun/launcher.go:204-248`). Child agents are additional `factory child-exec` processes (`agent_commands.go:63-81`).
- Codex model `gpt-5.6-sol`, Claude model `fable`, effort `high`, and three principal attempts are compile-time constants (`internal/agentrun/execute.go:17-22`). `runCodex` and `ExecuteChild` place those values directly on provider command lines (`internal/agentrun/execute.go:145-167`, `internal/agentrun/execute.go:195-242`).
- The principal always receives a hard-coded lifecycle-contract prompt selected by trigger kind. This prompt preserves Factory's human-merge boundary and typed terminal results (`internal/agentrun/execute.go:245-306`).
- Maximum concurrent runs is currently an environment-derived startup setting, defaulting to three (`main.go:31-35`, `main.go:250-272`, `main.go:408-418`). Repository routing, deployment receipts, and health targets are an explicit allowlisted catalog (`main.go:162-218`).

Inference:

- In-memory server configuration cannot reach already-separated `agent-exec` and `child-exec` processes. A persisted store at a stable Factory data path is required, with each newly claimed or launched process reading a validated snapshot. Existing runs should not be silently reinterpreted mid-segment.

### 2. Trigger and lifecycle invariants

Observed facts:

- External run creation currently has two entry paths: a new `Factory` label application creates an initial run, while an eligible later human comment can resume an active run or create a focused continuation after retained history (`internal/server/server.go:370-401`, `internal/server/server.go:424-448`, `internal/server/server.go:519-548`; `README.md:3-38`, `README.md:100-113`).
- GitHub events and authoritative merge reconciliation wake or resume already-managed runs. They are not independent opt-in entry points (`internal/server/server.go:574-640`; `internal/agentrun/manager.go:263-448`).
- Repository routing is resolved from Linear project metadata against an allowlisted catalog before a run is claimed (`internal/server/server.go:550-564`, `internal/agentrun/repository.go:1-183`).
- Project instructions require human-only merge authority, exact verified-head deployment gates, isolated repository state, and deployment only from clean merged `main`.

Decision:

- The editable trigger surface will cover initial Linear-label runs and human-comment continuations. GitHub remediation and post-merge continuation remain protected lifecycle transitions because disabling them could strand an already-authorized run or bypass cleanup/deployment checks.
- The label name may be configured as a bounded non-empty value because the predicate already discovers the label by name from each signed payload. Actor identity, webhook secrets, repository routing, merge authority, and deployment gates remain non-editable.

### 3. Safe workflow and spawn settings model

Observed facts:

- The only current workflow is the provider-installed `$do` SDLC, while the repository owns the Factory wrapper prompt and trigger-specific opening (`README.md:21`, `README.md:174`; `internal/agentrun/execute.go:245-282`).
- Provider processes run with bypassed local approval/sandbox flags, making arbitrary commands, paths, or command-line fragments in settings a high-risk execution surface (`internal/agentrun/execute.go:145-167`, `internal/agentrun/execute.go:210-238`).
- The child environment deliberately excludes service secrets other than the credentials needed for the workflow (`internal/agentrun/launcher.go:270-294`).

Decision:

- Settings will define workflows as declarative records with stable IDs, names, enabled state, a fixed built-in runner (`do`), and bounded ordered text steps. Triggers select a workflow ID. The steps become an explicit checklist in the principal prompt, while the hard-coded Factory lifecycle contract remains mandatory and is rendered after configurable content. The UI will not accept commands, executable paths, provider flags, repository paths, secrets, or an alternative merge/deploy authority model.
- Spawn settings cover the principal Codex model/effort/attempt limit and the Codex and Claude child model/effort. Models are bounded provider identifiers, efforts are enumerated, and attempts are range-limited. Values are passed as distinct CLI arguments rather than shell text.
- Settings apply to new run segments and newly spawned children. An in-flight provider process is not restarted when settings change.

### 4. HTTP and frontend integration

Observed facts:

- `server.New` uses method-aware `http.ServeMux` patterns. Authenticated APIs are wrapped by `viewerAuth.API`; authenticated SPA pages are wrapped by `viewerAuth.Page` (`internal/server/server.go:250-270`).
- The frontend is one SolidJS entry point with explicit pathname dispatch at `frontend/src/index.tsx:1177-1202`, and Vite proxies `/api` in development (`frontend/vite.config.ts:1-11`).
- OAuth redirect destinations are allowlisted. A new private page must be recognized by `protectedPagePath` or login falls back to `/activity` (`internal/viewerauth/auth.go:404-425`).
- Existing settings-free private-route tests provide patterns for both page redirects and API challenges (`internal/server/server_test.go:85-119`).

Decision:

- Add authenticated `GET /api/settings` and `PUT /api/settings`, plus authenticated `GET /settings` and `/settings/` SPA routes. The PUT endpoint uses a bounded body, strict JSON decoding, optimistic revision matching, and same-origin request validation. The frontend loads the current revision, edits structured controls, and reports saved, conflict, validation, and network states accessibly.

### 5. Persistence and concurrency

Observed facts:

- Existing stores use a versioned JSON envelope, a `sync.RWMutex`, copy-on-write updates, and atomic same-directory temp-file replacement with private permissions (`internal/agentrun/store.go:161-195`, `internal/agentrun/store.go:764-793`).
- Factory state is rooted under `~/.local/share/factory`, with durable data under `data/` (`main.go:112-139`). Child helpers derive shared state from Factory run paths rather than accepting arbitrary external paths (`agent_commands.go:219-227`).

Decision:

- Add a dedicated versioned settings store at `~/.local/share/factory/data/settings.json`. Missing state yields defaults exactly matching current behavior. Writes validate the complete candidate, require the caller's revision, increment revision, fsync, and atomically rename a `0600` file. Reads return copies.
- Server trigger evaluation and process launch read current snapshots. The active `Run` retains its trigger kind and durable lifecycle data; changing a trigger or workflow affects later claims/segments only and cannot mutate a running provider command.

### 6. Observable acceptance and verification

The issue description implies these acceptance criteria:

1. An authenticated operator can open `/settings`, see the effective trigger, workflow, step, and agent spawn configuration, edit it, save it, and see the persisted revision.
2. Settings survive store reopen/service restart with private atomic persistence.
3. Disabling an external trigger prevents a new run while signed webhook ingestion and activity retention continue.
4. A trigger's selected workflow and ordered steps appear in the next principal prompt without replacing Factory's mandatory safety contract.
5. Principal and child provider commands use the configured model and reasoning/effort values as distinct arguments, and the configured attempt limit is honored.
6. Unauthenticated, cross-origin, stale-revision, malformed, oversized, and invalid settings mutations fail without changing persisted state.
7. Existing default behavior and all lifecycle safeguards remain unchanged when no settings file exists.

Verification will use focused settings-store, server, prompt, executor, launcher, and auth tests; the complete Go test/race/vet suites; frontend typecheck and frozen build; and authenticated browser inspection at desktop and mobile sizes against a temporary local server when implementation is complete.

### 7. Compatibility, rollout, rollback, and deployment

Observed facts:

- Server startup refuses a missing built frontend (`main.go:69-72`). The frozen release build is declared in `nags.toml:15-16`.
- Repository publication requires `go test ./...`, `go test -race ./...`, `go vet ./...`, and the frozen Bun frontend build (`AGENTS.md`).
- Factory self-deployment must run from the clean updated primary checkout and bind the release to that exact commit. The lifecycle contract specifies `bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"`; the installed provider command is `/Users/tom/.local/share/nags/provider/bin/network-app` and the current local/public health endpoints agree on commit `b5537cb892fcbb529933159c0ee9c2971a5d4fd2`.

Decisions and recovery:

- Rollout is backward-compatible: absent settings state produces today's label, trigger, workflow, models, effort, attempts, and concurrency. Existing run-store JSON is unchanged.
- A settings validation or persistence failure fails the mutation and retains the last good snapshot. Startup fails closed only for a present but invalid settings file so corruption is visible rather than silently resetting policy.
- After human merge, deploy from the updated primary checkout with `/Users/tom/.local/share/nags/provider/bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"`. Verify local and public `/api/healthz` identity, launchd service state, the Factory tmux session, authenticated settings readback, and a saved-settings persistence probe that restores its original value.
- If deployment or health verification fails, preserve receipts and worktree state, inspect `~/Library/Logs/factory.err`, correct or revert on `main`, and rerun the same expected-commit deployment. Do not deploy from the issue worktree or manually weaken lifecycle safeguards.

## Contradictions and resolved assumptions

- The issue asks to configure workflow steps, but the actual `$do` skill lives outside this repository. This implementation will configure a safe, declarative checklist around the fixed built-in `$do` runner rather than editing provider skill files or accepting arbitrary executable workflow code.
- The issue says "things like," not an exhaustive schema. The initial scope covers every named category while deliberately excluding secrets, routing, executable commands, and protected merge/deployment transitions.
- `FACTORY_MAX_AGENTS` is an existing environment override. The settings store will default from that startup value only when no persisted settings file exists; once persisted, the operator-visible setting is authoritative for future scheduling.

## External and delegated evidence

- A read-only Claude child independently traced the same three-process boundary and store/auth conventions. Durable output: `/Users/tom/.local/share/factory/runs/run-c05c264010010c2e/children/settings-research-aaeec5e9/events.jsonl`.
- Live probes on 2026-07-12 PDT confirmed both `http://127.0.0.1:8092/api/healthz` and `https://factory.nags.cloud/api/healthz` report the same healthy Factory deployment identity.

## Unresolved questions

None. The ambiguous workflow wording is resolved with the bounded declarative model above; materially broader executable workflow authoring is a non-goal for this issue.
