## Adversarial review: ENG-32 settings page plan

### Evidence-checked claims (all confirmed)

- Root-cause analysis (plan line 32) is accurate: the trigger label is a compile-time constant (`internal/server/server.go:33`, consumed at `server.go:648`), provider models/effort/retries are constants (`internal/agentrun/execute.go:17-23`), and concurrency is read once from `FACTORY_MAX_AGENTS` (`main.go:264`, `envInt` at `main.go:408`). Server, principal (`agent-exec`), and children (`child-exec`) are genuinely separate processes (`agent_commands.go:29-32`, `internal/agentrun/launcher.go:234-241`, `internal/agentrun/child.go:93-99`), so the durable-file design is required, not gold-plating.
- Declared defaults (plan lines 79, 189) exactly match current constants: `Factory`, `$do`, `gpt-5.6-sol`, `fable`, `high`, 3 attempts, concurrency 3 (`main.go:34`).
- Settings path `~/.local/share/factory/data/settings.json` (plan line 40) matches the existing data root (`main.go:115-116`), and deriving it from the validated run directory works: the established `FACTORY_RUN_DIR` validation and state-root derivation pattern already exists (`agent_commands.go:219-227`), and children inherit `FACTORY_RUN_DIR` (`child.go:90`).
- The store conventions Phase 1 promises to follow (mutex, private dirs/files, temp-file `Sync`, atomic rename) exist verbatim in `internal/agentrun/store.go:765-789` and `internal/activity/store.go:350-374`.
- `/settings` OAuth return requires touching `protectedPagePath` (`internal/viewerauth/auth.go:418-425`) - the plan names the right file and behavior (plan lines 115-116).
- The SPA is plain path-branch routing (`frontend/src/index.tsx:1177-1201`); adding a `/settings` branch is exactly as described. `typecheck`/`build` scripts and the `MISE_BUN_VERSION=1.3.11` frozen-lockfile command match `frontend/package.json` and `nags.toml [build]`.
- Verification matrix `-run` patterns resolve to real existing tests (`TestLinearFactoryLabelStartsOneRunPerActiveIssue` at `server_test.go:370`, `TestLinearComment*` at 575-688, `TestManagerHonorsConcurrencyLimit` at `manager_test.go:198`, `TestPrincipalPrompt*` at `execute_test.go:49`); the rest are tests the plan itself adds.
- Deployment probe fields match `healthResponse`/`BuildIdentity` JSON keys (`server.go:116-145`), `buildContractVersion = "1"` matches `LifecycleContractVersion = 1` (`ready.go:15`), and `/Users/tom/.local/share/nags/provider/bin/network-app` exists.
- Security posture is sound: providers are already invoked via `exec.CommandContext` argument vectors with no shell (`execute.go:148-171`), the plan keeps it that way with syntax-bounded identifiers; session cookies are `SameSite=Lax` (`auth.go:436`) so the Origin check is correct defense-in-depth; the hard-coded lifecycle contract stays appended after configurable steps.
- Scope matches the Linear issue; phases are vertical (contract -> API/trigger -> runtime -> UI -> verification) with runnable per-phase commands; rollback and invalid-file recovery are explicit and fail-closed.

### P0/P1 findings

None.

### P2/P3 findings (non-blocking)

- **P2 - Workflow selection for continuation segments is unspecified.** Editable triggers (`linear-label`, `linear-comment`) map to workflow IDs (plan line 41), but later lifecycle segments run with trigger kinds `postmerge`/`github` (`manager.go:308,413,426`; passed via `--trigger-kind` at `launcher.go:237`), which have no settings mapping. Plan line 103 says "select the trigger's workflow for a principal" without saying what a post-merge/GitHub segment selects. Smallest fix: one sentence declaring the fallback (e.g., default workflow, or original trigger's workflow persisted on the run).
- **P3 - Mid-run workflow drift across segments.** Because settings are re-read per lifecycle segment (plan line 44), a run claimed under one workflow can resume a later segment under edited steps. Covered generally by the new-segment assumption (plan line 61); worth a note only.
- **P3 - Deployment probes** at plan lines 208-209 (`$FACTORY_TMUX_SOCKET`/`$FACTORY_TMUX_SESSION`) only resolve inside a Factory run environment, not an operator shell; and the settings readback probe (line 212) intentionally increments the production revision. Both acceptable as written since the lifecycle agent executes them, but stating the execution context would remove ambiguity.

VERDICT: READY
