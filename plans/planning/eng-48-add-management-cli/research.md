# ENG-48 Management CLI Research

> updated: 2026-07-15T05:39:51Z

## Research questions

1. What management behavior exists today, and what fails for the requested invocations?
2. Which entry points, runtime contracts, generated artifacts, APIs, and external tools participate?
3. What semantics can the repository evidence support for help, version, start, status, stop, and doctor?
4. Should setup, install, restart, logs, or JSON output be part of this issue?
5. What compatibility, privilege, path, service, security, and recovery constraints must remain unchanged?
6. How will every requested behavior and identified risk be verified?
7. What exact post-merge deployment, verification, and recovery procedures apply?
8. Which decisions are evidence-backed implementation assumptions, and which require human approval?

## Issue and repository scope

- Linear issue: [ENG-48](https://linear.app/nags-cloud/issue/ENG-48/add-management-cli), **Add management cli**.
- Requested surface: `factory --help`/`-h`, `factory --version`/`-v`, `factory start`, `factory status`, `factory stop`, `factory doctor`, and evidence-backed recommendations about setup/install or other commands.
- Linear project routing is complete and allowlisted: project `Factory`, GitHub repository `https://github.com/tomnagengast/factory`, and local path `/Users/tom/repos/tomnagengast/factory` all normalize to the Factory-managed repository `tomnagengast/factory`.
- The issue has no parent, sub-issues, attachments, prior comments, or linked design/incidents. It has the `Factory` label but not `Yolo` as of intake.
- Work is isolated on `eng-48-add-management-cli`, based exactly on `origin/main` commit `f383b179d890abab1378cb670ce4964c64013539`.

## Evidence-backed answers

### 1. Current behavior and failure mode

Observed facts:

- `main()` offers the argument slice to `runAgentCommand`; an unhandled invocation falls through to the long-running `serve` path (`main.go:54-64`).
- `runAgentCommand` currently recognizes only explicit `serve` plus the internal `agent-exec`, `child-exec`, and `agent` surfaces. No arguments and `serve` both fall through to the server. Every unknown first token prints `unknown Factory command` and exits 2 (`agent_commands.go:23-40`).
- Direct probes of the deployed `f383b179` binary proved that `--help` and `--version` both print the unknown-command error and exit 2. `serve --help` falls through to server startup rather than rejecting the unsupported argument.
- The deployed process explicitly runs `./factory serve`, so adding operator commands does not require changing the service entry point (`nags.toml:17-18`).
- Existing subcommands use small standard-library `flag.FlagSet` parsers and integer exit codes. No CLI framework is present or needed (`agent_commands.go:42-55`; `go.mod`).

Conclusion: ENG-48 is an extension of the existing binary dispatcher, not a second executable. The implementation must preserve `serve` and every internal agent helper exactly while making the requested operator surface first-class.

### 2. Runtime and management ownership

Observed facts:

- Factory is a user launch agent named `com.nags.factory`. The live plist is `~/Library/LaunchAgents/com.nags.factory.plist`; the generated wrapper is `~/.local/bin/factory-run`; and the immutable active release is reached through `~/.local/share/factory/current`.
- The live plist has `RunAtLoad=true` and `KeepAlive=true`. It invokes `factory-run`, which sources the private provider environment, changes into the immutable release, and executes `./factory serve`.
- Apple `launchctl(1)` documents `bootstrap`/`bootout` as the supported operations for adding/removing a service definition, `kickstart` for requesting an already-loaded service to run, and `print` for human inspection. It explicitly warns that `print` output is not a stable machine API.
- The provider currently owns installation, deployment, restart, receipt generation, and rollback through `~/.local/bin/nags`. The repository deployment manifest supplies only build, run, health, and secret declarations; it has no command-alias installation field (`nags.toml:1-22` and the provider's schema-1 manifest validator).
- There is currently no `~/.local/bin/factory` alias. The deployed operator binary is available at `~/.local/share/factory/current/factory` and in each immutable release.
- The separate `factory-agents` tmux server is intentionally independent of the launch agent and must survive service lifecycle operations (`README.md:137-139`, `README.md:176-187`).

Conclusion: start and stop may manage only the existing launchd job. They must not deploy, refresh secrets, create provider artifacts, touch issue tmux sessions, or become an alternative to the clean-main/verified-commit deployment path. Installing a stable `factory` alias is not expressible in this repository's provider manifest today and would require provider-repository scope or a self-install side effect that this issue does not authorize.

### 3. Canonical identity and status data

Observed facts:

- Build identity is already injected into `buildCommit`, `buildTree`, `buildID`, `buildDeploymentID`, and `buildContractVersion` (`build.go:3-9`, `nags.toml:14-15`). The server refuses a lifecycle-contract mismatch (`main.go:67-71`).
- The local health endpoint is `http://127.0.0.1:${PORT:-8092}/api/healthz` (`main.go:33-34`, `main.go:72`, `main.go:231`, `main.go:438-445`).
- `/api/healthz` returns privacy-safe status, wire counts, project-setup counts, commit, tree, build ID, deployment ID, contract version, and start time. It returns 503 with `status: degraded` when startup is incomplete, ordered wire work remains pending, or a project setup has failed (`internal/server/server.go:166-173`, `internal/server/server.go:192-205`, `internal/server/server.go:375-395`).
- A live read returned HTTP 200 with `status: ok`, zero pending wire events, zero failed project setups, and identity matching the current deployment receipt at `f383b179`.
- The authoritative deployment receipt is private `0600` JSON at `~/.local/share/factory/deployments/current.json`; the active release symlink, local/public health identities, and receipt are required to agree (`README.md:259-268`).

Conclusion: `--version` should use the binary's compiled identity. `status` should use the local health contract rather than parsing unstable `launchctl print` output. `doctor` should reconcile static artifacts, the current receipt, and local health identity without exposing secret values.

### 4. Proposed operator semantics

These are the smallest evidence-backed semantics to take to the research gate:

- `factory --help` and `factory -h`: print concise operator usage and exit 0. Show `start`, `status`, `stop`, `doctor`, and `serve`; keep `agent-exec`, `child-exec`, and `agent *` hidden because they are lifecycle plumbing.
- `factory --version` and `factory -v`: print the compiled Factory identity and exit 0. The contract should include commit, tree, build ID, deployment ID, and lifecycle contract, because the repository has no semantic-release version and those are the existing authoritative fields.
- `factory status [--json]`: make a bounded local `/api/healthz` request. Human output summarizes running/healthy or degraded plus compiled deployment identity; JSON emits the health body unchanged for automation. Healthy exits 0; degraded, malformed, or unreachable exits 1; invalid arguments exit 2.
- `factory doctor [--json]`: remain read-only. Check supported platform, launchd plist, generated wrapper, active-release symlink and binary, current success receipt, local health, and exact receipt-to-health identity. Report every check without secret values. Any failed check exits 1; invalid arguments exit 2.
- `factory start`: operate only on the existing user launch agent. If its plist or active release is absent, fail with an actionable instruction to deploy through the provider. If already healthy, succeed without restarting. If loaded but unhealthy/not running, use launchd's explicit service operation, then require bounded healthy local status before succeeding. If unloaded, bootstrap the existing plist and require health. It does not install, deploy, or refresh secrets.
- `factory stop`: gracefully remove only `gui/<uid>/com.nags.factory` with `launchctl bootout`. An already-unloaded service is idempotent success. It must not touch the plist, wrapper, active release, data, receipts, or `factory-agents` tmux server.
- Keep bare `factory` as the current server-start compatibility path. Keep explicit `factory serve` as the deployed path and reject trailing `serve` arguments with exit 2.

Inference: a separate `restart` command would be conventional but is not necessary to satisfy the issue because the provider already offers commit-aware `nags restart factory`, and an explicit restart expands mutation surface. Logs are likewise already available through `nags logs factory`. No alias command is proposed without evidence that the repository can install it durably.

### 5. Compatibility, security, and failure constraints

- Preserve the exact deployed `./factory serve` contract and all existing internal agent commands.
- Never source or print the private service environment from interactive management commands. Doctor reports only artifact/health/identity state, never credential values.
- Use the same `${PORT:-8092}` resolution as the service for local probes. Use bounded HTTP and lifecycle waits so a management command cannot hang indefinitely.
- Do not parse `launchctl print` text for stable fields. Treat command success/failure only as loaded/unloaded evidence and use the health API for runtime state.
- Start/stop must target only the current user's GUI domain and fixed label `com.nags.factory`; no root privilege or caller-provided label/path is needed.
- Start must refuse to manufacture or modify provider-owned plist, wrapper, release, receipt, secrets, or deployment locks.
- Stop must remove the launchd service so `KeepAlive` does not immediately respawn it. Graceful SIGTERM remains handled by `signal.NotifyContext`, which publishes `service/stopping` and allows a ten-second HTTP shutdown (`main.go:54-56`, `main.go:469-481`).
- A stopped service leaves immutable releases and persistent state intact, so start can bootstrap the existing verified artifact. Recovery from a bad release remains provider rollback or a corrective merged-main deploy, never management-CLI file mutation.

### 6. Verification mapping

| Acceptance criterion or risk | Exact evidence to add/run |
| --- | --- |
| Help aliases and documented surface | Unit tests calling the dispatcher with `--help` and `-h`; built-binary probes asserting exit 0, usage text, and hidden internal commands |
| Version aliases and compiled identity | Unit tests with overridden build variables; a test binary built with explicit ldflags; `--version` and `-v` output/exit assertions |
| Status healthy, degraded, malformed, unreachable, JSON, timeout | `httptest.Server` table tests around an injected health client/URL plus built-binary local healthy probe after deployment |
| Doctor complete reporting and identity reconciliation | Temporary HOME fixtures for plist/wrapper/current/receipt, fake health servers, mismatched identity and absent-artifact cases; assert no secret values are emitted |
| Start is idempotent and scoped | Fake command runner and health server tests for already healthy, loaded unhealthy, unloaded, missing plist/release, launchctl failure, and readiness timeout; assert exact fixed launchctl arguments |
| Stop is idempotent and scoped | Fake command runner tests for loaded and unloaded cases; assert only the fixed `bootout gui/<uid>/com.nags.factory` operation and no file deletion |
| Existing runtime/internal behavior unchanged | Existing `agent_commands_test.go` suite, explicit `serve` dispatch test, and all repository tests |
| Required repository publication gates | `gofmt -w` on changed Go files; `go test ./...`; `go test -race ./...`; `go vet ./...`; `export MISE_BUN_VERSION=1.3.11; bun install --cwd frontend --frozen-lockfile && bun run --cwd frontend build` |
| Live operator behavior after deployment | From updated merged main/deployed release: version/help probes, `status --json`, `doctor --json`, local/public health identity and process probes. Do not exercise `stop` against the production run during automated deployment verification because that would intentionally interrupt Factory; launchd mutations are covered by injected tests. |

### 7. Deployment, post-deploy verification, and recovery

Factory is deployable, so deployment applies.

Mandatory source: the clean, human-merged, updated primary `main` checkout at `/Users/tom/repos/tomnagengast/factory`. Never deploy from this issue worktree or the T9 mirror.

The Factory lifecycle contract now specifies this exact self-deployment command:

```bash
bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
```

The current installed provider exposes the equivalent command as `~/.local/bin/nags deploy factory --expected-commit "$(git rev-parse HEAD)"`; the post-merge segment must use the contract-mandated `bin/network-app` command if it is present in updated main/provider context and must not silently invent a different source. This naming discrepancy is recorded as an operational contradiction to resolve before the plan is approved.

Required post-deploy checks:

```bash
curl -fsS http://127.0.0.1:8092/api/healthz | jq -e '.status == "ok" and .app == "factory"'
curl -fsS https://factory.nags.cloud/api/healthz | jq -e '.status == "ok" and .app == "factory"'
launchctl print "gui/$(id -u)/com.nags.factory"
tmux -L "$FACTORY_TMUX_SOCKET" has-session -t "$FACTORY_TMUX_SESSION"
jq -e '.status == "success" and .app == "factory"' "$HOME/.local/share/factory/deployments/current.json"
"$HOME/.local/share/factory/current/factory" --version
"$HOME/.local/share/factory/current/factory" status --json
"$HOME/.local/share/factory/current/factory" doctor --json
```

The local health, public health, receipt, active release, and CLI version identities must match the deployed merged-main commit/tree/build/deployment/contract. Recovery after an irreversible merge is a corrective or revert commit merged to `main` followed by the same commit-pinned deployment, or an explicit provider rollback to a retained successful deployment. Persistent data files must never be edited or deleted as recovery.

## Contradictions

1. Project instructions and the current `$do` lifecycle require `bin/network-app deploy factory ...`, but this standalone repository has no tracked `bin/network-app`. The installed provider currently exposes `~/.local/bin/nags` and its compatibility `network-app` script under the provider release. The plan must resolve the executable path from authoritative lifecycle/provider evidence before implementation approval; deployment cannot be invented after merge.
2. The issue names the command `factory`, but the provider currently installs only `factory-run`; it does not install `~/.local/bin/factory`. The binary itself can implement the CLI, but durable PATH installation is provider-owned and outside the current repository manifest schema.

## Assumptions proposed for approval

- Keep no-argument `factory` server behavior for compatibility; operators use explicit management verbs or flags.
- Implement start/stop only for the already-installed user launch agent. Setup/install and PATH alias publication are non-goals unless the human explicitly expands scope and repository authority.
- Include `--json` on status and doctor because both consume structured contracts and need deterministic automation output.
- Do not add restart/logs aliases in this issue; the provider already owns those operations.
- Hide internal agent commands from public help while preserving them unchanged.

## Unresolved questions

1. Does approval of this research direction confirm that `setup`/`install` and durable `~/.local/bin/factory` alias installation are out of scope for ENG-48, with start limited to an existing provider-installed launch agent?
2. Which contract-authorized deployment executable will exist at the post-merge boundary: repository-local `bin/network-app`, provider `~/.local/share/nags/provider/bin/network-app`, or installed `~/.local/bin/nags`? This must be made exact in the implementation plan before approval.

