# ENG-48 Management CLI Implementation Plan

> updated: 2026-07-15T06:28:45Z

## Linear context and acceptance criteria

- Issue: [ENG-48, Add management cli](https://linear.app/nags-cloud/issue/ENG-48/add-management-cli).
- Add public `factory --help`/`-h`, `factory --version`/`-v`, `factory start`, `factory status`, `factory stop`, and `factory doctor` behavior to the existing binary.
- Preserve bare `factory`, explicit `factory serve`, and every internal agent command used by Factory's lifecycle.
- Work without Tom's private network-app provider. An unmanaged user can start Factory locally with no launchd, immutable release, receipt, Caddy, DNS, or `nags` installation.
- Default standalone startup to `127.0.0.1:8092`; accept validated `--host` and `--port` overrides when needed.
- Preserve the managed production launchd/OAuth/receipt/deployment contract and the separate `factory-agents` tmux server.
- Research gate approval: Linear reaction `c2c06ccb-8617-489e-8869-38319677dceb` (`white_check_mark`) on `codex-do:ENG-48:research-gate:r3` at 2026-07-15T06:18:00.616Z.

## Research questions and evidence-backed answers

The complete evidence is in `plans/planning/eng-48-add-management-cli/research.md`.

1. **What fails today?** `runAgentCommand` recognizes only `serve` and internal lifecycle helpers. Every requested management flag or verb exits 2 as unknown (`agent_commands.go:23-40`).
2. **Where should the CLI live?** In the current binary dispatcher. Existing handlers already use standard-library `flag.FlagSet` parsing and integer exit codes; no framework is required (`agent_commands.go:42-55`, `go.mod`).
3. **Why is launchd-only insufficient?** The process listens on loopback, but startup, OAuth, provider onboarding, launchd artifacts, receipts, and deployment assume Tom's managed environment (`main.go:74-112,193-241,355-381,438-450`). A provider-only management layer cannot serve open-source users.
4. **What existing contracts can be reused?** `/api/healthz` already returns status, build identity, start time, wire state, and project setup state with 200/503 semantics (`internal/server/server.go:166-205,375-395`). `signal.NotifyContext` and bounded HTTP shutdown already provide graceful termination (`main.go:54-56,469-481`).
5. **How can local trust remain safe?** Select it only for an explicit unmanaged `start` whose effective host is loopback, then require every protected request's `Host` and `Origin` to match the exact configured loopback authority. Explicit `serve` and any non-loopback local bind retain Google OAuth and fail closed on incomplete configuration.
6. **How are host/port overrides safe?** Validate a bind host without URL syntax and a decimal port from 1 through 65535. Store the selected address in private runtime metadata. Explicit later-command flags override metadata, which overrides the loopback default. Off-loopback never enables local trust.
7. **How can local stop avoid PID reuse?** Before signaling, match private runtime metadata to bounded health using start time and the complete build identity. Never signal on missing, stale, unreachable, or mismatched evidence.
8. **What deployment applies?** Factory remains deployable only from clean human-merged primary `main`, through `bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"`, followed by local/public health, receipt, release, launchd, tmux, and CLI identity checks.

## Current behavior and root cause

- Command dispatch treats no arguments and `serve` as server startup, preserves three internal command families, and rejects everything else.
- `serve` combines configuration selection, managed-only OAuth construction, store/catalog/provider setup, HTTP listener creation, readiness recovery, and shutdown in one function.
- The listener address is hard-coded to `127.0.0.1` plus `${PORT:-8092}` and the OAuth redirect is hard-coded to Factory's public hostname.
- The server accepts a concrete `*viewerauth.Authenticator`, preventing an explicit local-loopback policy without weakening that production implementation.
- There is no durable standalone runtime identity, so a later CLI process cannot safely distinguish the Factory instance it should stop.
- The repository contract requires `bin/network-app` for self-deployment, but the standalone repository does not currently track that compatibility entry point.

## Decisions

1. **One binary, one dispatcher.** Add management handlers beside the existing internal dispatcher, in a dedicated `management_commands.go` file to keep the public surface separate from agent plumbing.
2. **Fail-closed mode selection.** If any managed marker exists (plist, generated wrapper, active release, or current receipt), classify the installation as managed. A partial/broken managed installation reports its missing artifact; it never falls back to local trust.
3. **Attached local start.** Unmanaged `factory start` runs in the foreground, prints the effective URL, and exits with the server. Ctrl-C is the simplest stop path; `factory stop` exists for another terminal. No daemonization or service generation is added.
4. **Address configuration.** `start`, `status`, `stop`, and `doctor` accept `--host` and `--port`. `start` defaults to `127.0.0.1` and `${PORT:-8092}`. Later commands use explicit flags, then matching local runtime metadata, then the same default. Managed `start` rejects explicit address overrides because launchd owns its environment.
5. **Authorization split.** Replace the server's concrete viewer-auth dependency with the smallest interface its routes use. Add a local implementation that authorizes only requests received by a loopback-bound unmanaged server and rejects any protected request whose normalized `Host` is not the exact configured loopback host/port or whose `Origin`, when present, is not the matching `http` origin. This closes DNS-rebinding and hostile-Host paths before existing same-origin mutation checks. Explicit managed `serve` retains the current Google implementation. Non-loopback unmanaged `start` also uses Google and requires `FACTORY_GOOGLE_REDIRECT_URL` to be an HTTPS URL.
6. **Private runtime record.** Write schema-versioned `~/.local/share/factory/local-runtime.json` atomically at mode `0600` after the listener is acquired. Record PID, UTC start time, host, port, executable, mode, and complete build identity. Remove it only when the exiting process still owns the exact record.
7. **Health-verified local stop.** Read the record, apply explicit probe-address overrides, fetch health with a bounded client, require matching start time/build identity, signal the recorded process with SIGTERM, and wait boundedly for the matching health identity to disappear. Never delete persistent app data or signal on ambiguous evidence.
8. **Managed lifecycle remains provider-scoped.** The exact artifacts are `$HOME/Library/LaunchAgents/com.nags.factory.plist`, `$HOME/.local/bin/factory-run`, `$HOME/.local/share/factory/current/factory`, and `$HOME/.local/share/factory/deployments/current.json`; the live plist confirms label `com.nags.factory`, `RunAtLoad=true`, `KeepAlive=true`, and the fixed wrapper program. Managed `start` validates those artifacts and the successful receipt, checks `launchctl print gui/<uid>/com.nags.factory`, uses `launchctl bootstrap gui/<uid> <plist>` when unloaded or `launchctl kickstart -k gui/<uid>/com.nags.factory` when loaded but unhealthy, then requires bounded health to match the current receipt. Managed `stop` checks loaded state and performs `launchctl bootout gui/<uid>/com.nags.factory`. Command exit status is used only as loaded/unloaded evidence; output is not parsed.
9. **Mode-aware doctor.** Common checks cover address/config validation, build identity, frontend, required application environment names without values, required executable discovery, state-root access, runtime record, and bounded health. Managed checks add plist, wrapper, release, receipt identity, and launchd. Provider absence is informational in standalone mode.
10. **Deployment compatibility entry point.** Add `bin/network-app` as a narrow compatibility translator to `$HOME/.local/bin/nags`, with an actionable failure when unavailable. For the mandated `deploy factory ...` form it removes the legacy `factory` positional and invokes `nags deploy ...`, matching the current repository runbook; for `rollback factory ...` it retains the required app positional and invokes `nags rollback factory ...`. Unsupported compatibility commands fail clearly. It never installs/downloads the provider and is not invoked by standalone runtime commands.
11. **Managed identity gate.** Both the already-healthy short circuit and post-launch readiness require a current receipt with `status=success`, `app=factory`, lifecycle contract 1, and a health response with `status=ok`, the same app/source commit/source tree/build ID/deployment ID/contract, and `health.startedAt >= receipt.startedAt`, matching `internal/agentrun/completion_system.go:112-128`. A healthy but stale or unrelated process never makes managed `start` succeed.

## Alternatives considered

- **Launchd-only lifecycle:** rejected because it directly violates the open-source feedback.
- **Silently disable OAuth whenever credentials are missing:** rejected because a production misconfiguration could become an authorization bypass.
- **Explicit `--local` mode flag:** rejected because provider-free users should get the intended default. Managed-marker detection remains fail-closed, and non-loopback still requires OAuth.
- **Detached daemon plus PID file:** rejected because detach semantics are platform-specific and add hidden process/log ownership. Attached startup is observable and smaller.
- **Unauthenticated HTTP shutdown:** rejected because a non-loopback bind would expose process control.
- **Bearer-token HTTP shutdown:** rejected because plain HTTP to a non-loopback host could disclose the control token.
- **Generate launchd/systemd definitions:** rejected because it turns start into install and expands the issue into cross-platform packaging.
- **Add restart/log commands:** rejected because they are not requested and managed equivalents already exist.
- **Install or fetch `nags`:** rejected because standalone operation must not depend on the private provider.

## Non-goals

- Packaging, release downloads, PATH alias publication, Homebrew, setup/install commands, background daemonization, or OS service installation.
- Automatic public exposure, TLS termination, DNS, Caddy, Cloudflare, or a `--public-url` CLI.
- Generalizing Factory's repository catalog, Linear trigger policy, or single-owner lifecycle beyond what is needed to boot and manage the existing app locally.
- Replacing Google OAuth, provider receipts, immutable releases, rollback, or exact verified-head deployment for the managed host.
- Rewriting provider-owned webhook registration or Cloud URL provisioning. In standalone mode those optional operations continue to fail clearly if the private provider is unavailable; absence does not prevent startup.
- Changing persistent application schemas or event/run/workflow semantics.

## Impacted files and interfaces

- `agent_commands.go`: extend root dispatch; preserve every existing internal handler and `requiredCommand` behavior.
- New `management_commands.go`: public parsers, usage/version output, mode detection, address resolution, health client, managed launchctl runner, doctor report, runtime record, signaling/wait logic, and injectable seams.
- New `management_commands_test.go`: dispatcher, address, mode, status, runtime record, local stop, launchctl, doctor, and secret-hygiene tests.
- `main.go`: introduce `serveOptions`; keep no-argument and explicit `serve` on managed defaults; let unmanaged `start` call the same server with local options; construct listener before writing runtime metadata; parameterize bind host/port and OAuth redirect selection; preserve recovery/manager/service logic.
- `lifecycle_test.go`: assert production `serve` remains fail-closed, local loopback mode boots without OAuth, non-loopback local mode requires complete OAuth plus HTTPS redirect, and listener/runtime cleanup behavior is bounded.
- `internal/server/server.go`: replace concrete viewer auth with a narrow interface only.
- New `internal/viewerauth/local.go`: explicit local policy implementing the same interface without OAuth redirects, with canonical loopback Host/Origin enforcement.
- `internal/viewerauth/auth_test.go` and/or new `local_test.go`: prove local pass-through behavior is confined to its explicit implementation and managed Google behavior is unchanged.
- `README.md`: document build prerequisites, required application environment, local start/status/stop/doctor flows, host/port precedence, non-loopback OAuth requirement, managed behavior, and recovery.
- New `bin/network-app`: narrow deploy/rollback argument translator required by repository policy.
- PR body and Linear comments: durable reviewed-plan, implementation, validation, and exact-head evidence.

No persistent API response or data-store schema changes are planned. The new local runtime JSON is private operator metadata with its own schema and ownership checks.

## Implementation phases

### 1. Add the public read-only command surface

Files: `agent_commands.go`, new `management_commands.go`, tests.

- Add deterministic help/version output and parsers for `status` and `doctor` with `--host`, `--port`, and `--json`.
- Add validated address resolution and a bounded health client that preserves the health body for JSON output.
- Keep internal commands hidden from public help and unchanged in behavior.

Success criteria: aliases return 0; invalid args return 2; health outcomes return the documented 0/1 codes; existing internal command tests pass unchanged.

### 2. Separate managed and local authorization/runtime configuration

Files: `main.go`, `internal/server/server.go`, `internal/viewerauth/local.go`, tests.

- Add a narrow server viewer-auth interface.
- Parameterize server host, port, mode, and OAuth redirect without changing managed defaults.
- Select local viewer policy only for unmanaged loopback `start`; require the existing Google settings plus explicit HTTPS redirect for non-loopback local start.
- Give the local viewer policy the one canonical loopback authority and reject protected requests with missing/alternate/attacker-controlled Host or mismatched Origin before routing to pages/APIs.
- Keep no-argument and explicit `serve` managed and fail-closed.

Success criteria: managed startup validation is unchanged; local loopback can boot with required application credentials but no provider/OAuth; canonical protected requests work; hostile Host/Origin and DNS-rebinding cases fail; non-loopback cannot boot without OAuth/HTTPS redirect; all listeners bind exactly the requested validated address.

### 3. Add private standalone lifecycle identity and stop

Files: `management_commands.go`, `main.go`, tests.

- Acquire the listener before publishing the runtime record; write it atomically at `0600`.
- Refuse ambiguous live/stale record replacement.
- Verify health start time/build identity before SIGTERM and during bounded shutdown wait.
- Remove only the exact record owned by the exiting process.

Success criteria: a real built-binary temporary-HOME flow starts attached on a temporary port, reaches health, is stopped from a second CLI process, drains, and leaves no runtime record; mismatch cases never signal.

### 4. Add managed launchd lifecycle and mode-aware doctor

Files: `management_commands.go`, tests.

- Implement fail-closed managed-marker detection.
- Validate the exact plist/wrapper/release/receipt paths, add the fixed print/bootstrap/kickstart/bootout operations, and cover a stopped-then-started sequence.
- Reuse the existing completion identity rules for both healthy short-circuit and post-launch readiness; receipt mismatch is a managed start failure.
- Add structured human/JSON doctor reports without credential values.
- Treat provider artifacts as managed requirements only after managed classification.

Success criteria: fake-runner fixtures prove every exact path/argument, stopped-then-started flow, and idempotent branch; old/unrelated health cannot satisfy the receipt gate; partial managed installs fail rather than selecting local trust; doctor covers both modes and secret-hygiene assertions.

### 5. Document local operation and satisfy deployment entry-point policy

Files: `README.md`, new `bin/network-app`, tests/probes.

- Document build and configuration prerequisites and all command examples.
- Document local trust versus off-loopback OAuth requirements and host/port precedence.
- Add the non-installing provider translator and verify exact `deploy factory` to `nags deploy` and `rollback factory` to `nags rollback factory` argv with a fake provider executable.
- Preserve existing deploy/recovery documentation and exact-main safeguards.

Success criteria: README commands match actual help; translator fails clearly without provider, removes only the legacy deploy app positional, retains the rollback app positional, and rejects unsupported commands; no standalone command invokes it.

### 6. Final integration and publication

- Run the complete verification matrix from a clean worktree.
- Update the draft PR with decisions, risks, exact results, and the verified head; mark it ready only after all local checks pass.
- Address GitHub review, checks, threads, and later Linear feedback with the smallest in-scope revisions.
- Write the Factory ready-for-merge checkpoint only when the fresh complete safeguard predicate passes.

Success criteria: exact local head equals PR head, every reported/required check is pass or legitimate skip, no actionable request/thread/Linear feedback remains, and Factory accepts the checkpoint.

## Data, security, compatibility, migration, rollout, and rollback

### Data

- Existing stores are untouched. `local-runtime.json` is a new schema-versioned `0600` operational record under the existing private state root.
- Use same-directory temporary write, file fsync, rename, and directory fsync following repository store conventions. Cleanup compares the complete record before removal.
- No migration is required. Unknown runtime-record schema fails closed with doctor guidance; it is never interpreted or overwritten while a matching process could be alive.

### Security

- Local trust is constructed only for an unmanaged loopback-bound server. Its protected wrappers require the exact configured loopback Host and matching HTTP Origin, preventing DNS rebinding through attacker-controlled authorities. It is impossible to select from explicit `serve` or a non-loopback bind.
- Non-loopback start requires complete Google configuration and an explicit HTTPS redirect URL.
- Doctor and errors name missing variables/artifacts but never include their values.
- Local stop requires matching private record, health start time, and complete build identity before signaling.
- User-supplied host/port are passed as typed listener/probe values, never shell text. Launchctl label/domain/path are fixed and executed without a shell.
- Provider translation is a fixed executable handoff with allowlisted deploy/rollback grammar and performs no download/install.

### Compatibility

- Bare `factory` and `factory serve` retain managed startup behavior and address defaults.
- Existing service command `./factory serve` and lifecycle contract remain unchanged.
- Existing internal agent command grammar and exit codes remain unchanged.
- `${PORT:-8092}` continues to work. An explicit CLI `--port` overrides it only for the applicable management invocation.
- Existing production health response fields and status semantics remain unchanged.

### Rollout

- Before merge, all lifecycle mutations are tested with injected runners or an isolated temporary HOME and port. Production `stop` is never exercised in automated verification.
- After human merge, deploy only from updated clean primary `main` at the exact merge-containing commit.
- Confirm the managed service still runs through explicit `serve`, while the new read-only CLI probes work against it.

### Rollback and recovery

- Before merge, remove/revise the branch normally.
- After merge, use a corrective or revert commit merged to `main` and the same exact-commit deployment, or the provider's retained-release rollback.
- If CLI behavior is wrong but the service is healthy, use existing provider commands and direct health/receipt inspection; never edit persistent event/run/routing data.
- If deployment fails, preserve failed receipts and report `deployment_failed`; do not clean up evidence or deploy from the issue worktree.

## Verification matrix

| Acceptance criterion / risk | Verification |
| --- | --- |
| Help aliases, public surface, hidden internals | `go test . -run 'TestManagementHelp|TestManagementDispatch'`; built `./factory --help` and `./factory -h`; assert internal verbs absent |
| Version aliases and compiled identity | `go test . -run TestManagementVersion`; build with explicit ldflags; compare `--version` and `-v` output |
| Existing bare/serve/internal compatibility | Existing `go test . -run 'TestRunAgentCommand|TestRunAgent'`; new serve-argument tests; full Go suites |
| Address validation and precedence | `go test . -run 'TestManagementAddress|TestRuntimeAddress'` covering IPv4/IPv6/hostname/wildcard, invalid URL syntax, ports 0/65536/non-numeric, explicit > runtime > env/default |
| Local loopback start without provider/OAuth | Built binary, temporary HOME, placeholder required app secrets, built frontend, free temporary port; no plist/release/receipt/`nags`; start, poll `/api/healthz`, probe protected local API |
| Local trust rejects DNS rebinding | `go test ./internal/viewerauth` with exact canonical authority plus attacker hostname resolving to loopback, alternate `localhost`, missing/wrong port, hostile Host, mismatched/cross-site Origin, and canonical same-origin cases |
| Off-loopback cannot inherit local trust | Unit/integration cases requiring Google fields plus HTTPS `FACTORY_GOOGLE_REDIRECT_URL`; missing/HTTP redirect fails before listen |
| Runtime record safety | `go test . -run 'TestLocalRuntimeRecord|TestLocalStop'` for mode `0600`, atomicity, schema, stale/unknown/mismatch, PID reuse guard, exact-owner cleanup |
| Local graceful stop | Second built CLI process uses matching temporary-HOME record and health, sends SIGTERM, waits for exit, verifies record removal and data preservation |
| Managed start/stop exact scope and identity | `go test . -run 'TestManagedStart|TestManagedStop|TestManagedDetection'`; fixtures at exact plist/wrapper/release/receipt paths; fake runner asserts fixed print/bootstrap/kickstart/bootout args, stopped-then-started behavior, receipt/health match and mismatch, and no shell |
| Status healthy/degraded/malformed/unreachable/timeout/JSON | `go test . -run TestManagementStatus` with `httptest.Server`; assert exit codes and JSON passthrough |
| Doctor both modes and secret hygiene | `go test . -run TestManagementDoctor` using temporary HOME fixtures; seed sentinel secret values and assert none appear |
| Server/viewer-auth regression | `go test ./internal/server ./internal/viewerauth`; focused local policy tests; production OAuth tests unchanged |
| Documentation and provider translator | Compare `./factory --help` to README examples; fake `nags` records exact argv for deploy and rollback; unsupported-command and isolated-HOME missing-provider failure probes |
| Required publication suites | `go test ./...`; `go test -race ./...`; `go vet ./...`; `export MISE_BUN_VERSION=1.3.11; bun install --cwd frontend --frozen-lockfile && bun run --cwd frontend build` |
| Final diff hygiene | `git diff --check origin/main...HEAD`; inspect complete diff/status for secrets, debug artifacts, unrelated churn, and running processes |
| PR safeguards | Fresh `gh pr view`, `gh pr checks` all/required, top-level/inline/review queries, unresolved-thread GraphQL query, and fresh Linear conversation |
| Exact verified head | `VERIFIED_HEAD_OID=$(git rev-parse HEAD)` and equality with PR `headRefOid`; then successful Factory checkpoint command |

## Exact post-merge deployment, verification, and recovery

From the updated, clean primary checkout `/Users/tom/repos/tomnagengast/factory` on tracked `main` after proving the human merge contains the exact checkpointed head:

```bash
bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
```

Post-deploy probes:

```bash
curl -fsS http://127.0.0.1:8092/api/healthz | jq -e '.status == "ok" and .app == "factory"'
curl -fsS https://factory.nags.cloud/api/healthz | jq -e '.status == "ok" and .app == "factory"'
launchctl print "gui/$(id -u)/com.nags.factory"
tmux -L "$FACTORY_TMUX_SOCKET" has-session -t "$FACTORY_TMUX_SESSION"
jq -e '.status == "success" and .app == "factory"' "$HOME/.local/share/factory/deployments/current.json"
"$HOME/.local/share/factory/current/factory" --version
"$HOME/.local/share/factory/current/factory" status --host 127.0.0.1 --port 8092 --json
"$HOME/.local/share/factory/current/factory" doctor --host 127.0.0.1 --port 8092 --json
```

Require local health, public health, receipt, active release, and CLI identity to agree on commit, tree, build ID, deployment ID, and lifecycle contract. Do not run production `stop` during automated verification.

Recovery:

```bash
bin/network-app rollback factory --to <deployment-id>
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

Use rollback only to a retained previously successful release, or merge a corrective/revert commit to `main` and deploy that exact commit. Preserve failed receipts and persistent state.

## Unresolved questions

None.
