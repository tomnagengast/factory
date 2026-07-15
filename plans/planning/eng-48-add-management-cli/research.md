# ENG-48 Management CLI Research

> updated: 2026-07-15T06:00:13Z

## Research questions

1. What management behavior exists today, and what fails for the requested invocations?
2. Which entry points, runtime contracts, generated artifacts, APIs, and external tools participate?
3. What does the new requirement to work without the private network-app provider change?
4. What safe semantics can the repository support for help, version, start, status, stop, and doctor?
5. What production compatibility, local security, process, data, and recovery constraints must remain unchanged?
6. How will every requested behavior and identified risk be verified?
7. What exact post-merge deployment, verification, and recovery procedures apply?
8. Which decisions remain unresolved after the human feedback?

## Issue, continuation feedback, and repository scope

- Linear issue: [ENG-48](https://linear.app/nags-cloud/issue/ENG-48/add-management-cli), **Add management cli**.
- Requested surface: `factory --help`/`-h`, `factory --version`/`-v`, `factory start`, `factory status`, `factory stop`, `factory doctor`, and evidence-backed recommendations about setup/install or other commands.
- The 2026-07-15 continuation feedback adds a controlling acceptance criterion: the CLI must work for users without the private network-app setup, and the first standalone behavior should run Factory on localhost. Adjusting private network integration is allowed only as needed to preserve that fallback.
- Linear project routing is complete and allowlisted: project `Factory`, GitHub repository `https://github.com/tomnagengast/factory`, and local path `/Users/tom/repos/tomnagengast/factory` all normalize to the Factory-managed repository `tomnagengast/factory`.
- The issue has no parent, sub-issues, or linked designs/incidents. It has the `Factory` label but not `Yolo` as of the continuation read.
- The existing draft PR and clean worktree remain active on `eng-48-add-management-cli` at research commit `314e78d`; no plan or product implementation exists yet.

## Evidence-backed answers

### 1. Current behavior and failure mode

Observed facts:

- `main()` offers the argument slice to `runAgentCommand`; an unhandled invocation falls through to the long-running `serve` path (`main.go:54-64`).
- `runAgentCommand` recognizes explicit `serve` plus internal `agent-exec`, `child-exec`, and `agent` surfaces. Every requested management flag or verb is currently unknown and exits 2 (`agent_commands.go:23-40`).
- The deployed process explicitly runs `./factory serve`, so the production entry point is stable and can remain distinct from a new standalone `start` path (`nags.toml:17-18`).
- Existing subcommands use standard-library `flag.FlagSet` parsers and integer exit codes. No CLI framework is present or needed (`agent_commands.go:42-55`; `go.mod`).

Conclusion: ENG-48 belongs in the existing binary dispatcher. Public management commands must not expose or change the internal agent helper protocol.

### 2. Current runtime and provider coupling

Observed facts:

- The HTTP listener already binds only `127.0.0.1:${PORT:-8092}` (`main.go:438-450`). Caddy/DNS exposure is provider behavior outside this process.
- Production requires webhook secrets, Linear access, Google OAuth credentials, a session key, a built frontend, and command-line dependencies (`main.go:74-112`, `main.go:193-197`). Protected pages and APIs use Google OAuth with a fixed `https://factory.nags.cloud/auth/google/callback` redirect (`main.go:51,98-109`; `internal/viewerauth/auth.go:55-74`).
- The fixed cloud redirect and required Google configuration mean the current binary cannot provide a usable protected localhost UI to a user without the private deployment environment, even though the listener is loopback-only.
- Provider integration is also hard-coded into project onboarding. The process constructs the `Network` provider coordinator and invokes `~/.local/bin/nags github-hook` for new repositories (`main.go:355-381`; `project_setup.go:124-153`). `requiredCommand` does not verify the executable at startup, but onboarding later fails if the private command is absent (`agent_commands.go:404-410`).
- Production is a `com.nags.factory` user launch agent with immutable releases and receipts. `RunAtLoad`/`KeepAlive`, deployment, restart, receipt generation, rollback, and secrets remain provider-owned. The separate `factory-agents` tmux server must survive service lifecycle operations (`README.md:137-139`, `README.md:176-187`, `README.md:245-298`).

Conclusion: a launchd-only CLI does not satisfy the continuation feedback. Standalone operation needs an explicit local mode inside this repository, while explicit `serve` must retain the existing managed production contract.

### 3. Standalone localhost boundary

Evidence-backed design direction:

- `factory start` selects the managed path only when the existing provider launch-agent artifacts identify a managed installation. Otherwise it starts Factory attached to the current terminal in an explicit local mode and prints the loopback URL. Like `docker compose up`, local `start` remains attached; Ctrl-C is a normal stop path. This avoids unsafe cross-platform daemonization, PID reuse, generated service files, or a hidden install side effect.
- Local mode still binds only `127.0.0.1`; caller-provided bind addresses are out of scope. It uses the same port and persistent data root as `serve` unless the existing environment overrides them.
- Local mode may replace Google OAuth only with a loopback-only local viewer policy. This is not a fallback inside managed `serve`: production remains fail-closed on missing OAuth configuration. The server's concrete `*viewerauth.Authenticator` dependency therefore needs a small interface so managed OAuth and local-loopback authorization are visibly separate implementations.
- Local mode omits provider-only project cloud coordination when `nags` is unavailable. Repository-only Factory work remains available; requesting a Cloud URL without provider capability fails clearly instead of calling a missing private tool.
- `factory stop` detects the managed launchd installation and uses the fixed user service operation there. For a standalone foreground process, it uses an authenticated loopback control request whose per-process token is stored under the private Factory state root and removed on shutdown. The handler accepts loopback requests only, compares the token in constant time, and triggers the existing graceful context shutdown. This avoids signaling an unrelated reused PID.
- Local control metadata is private, contains no provider or application credentials, is written atomically with restrictive permissions, and is reconciled on startup so a stale record cannot authorize a new process.
- Setup/install, PATH alias installation, background daemonization, arbitrary bind addresses, and generated OS service definitions are non-goals. Open-source packaging can add those later without changing the first localhost lifecycle.

This is the smallest direction that makes all requested verbs useful to an unmanaged user without weakening the managed deployment boundary.

### 4. Command semantics

- `factory --help` and `factory -h`: print concise public usage and exit 0. Show `start`, `status`, `stop`, `doctor`, and `serve`; keep internal agent commands hidden.
- `factory --version` and `factory -v`: print the compiled commit, tree, build ID, deployment ID, and lifecycle contract. The repository has no semantic-release version; these are its canonical identity fields (`build.go`, `nags.toml:14-15`).
- `factory start`: managed install means idempotently start/bootstrap the fixed `com.nags.factory` job and wait for bounded health. Otherwise run the server attached in local mode on loopback, with explicit local authorization and provider capability detection.
- `factory status [--json]`: make a bounded `http://127.0.0.1:${PORT:-8092}/api/healthz` request. Human output distinguishes healthy, degraded, and unreachable. JSON emits the health contract unchanged. Healthy exits 0; degraded, malformed, timeout, or unreachable exits 1; invalid arguments exit 2.
- `factory stop`: managed install means fixed-domain `launchctl bootout`, idempotently succeeding when unloaded. Standalone mode authenticates to the loopback control endpoint and waits for bounded shutdown. It never deletes data, releases, receipts, configuration, or tmux sessions.
- `factory doctor [--json]`: remain read-only and mode-aware. Common checks cover platform, frontend, required environment variable names without values, command dependencies, writable private state, port availability/health, and build identity. Managed checks additionally reconcile plist, wrapper, active release, receipt, health identity, and launchd. Local checks treat absent provider artifacts as expected, not unhealthy.
- Keep bare `factory` and explicit `factory serve` compatible with managed server startup. Reject trailing `serve` arguments with exit 2.
- Do not add restart/log commands. Managed users already have provider operations; standalone users can stop then start and read the attached process output.

### 5. Compatibility, security, and failure constraints

- Preserve the exact deployed `./factory serve` contract and all internal agent commands.
- Never silently enter local authorization from `serve`; only `start` without a verified managed installation selects it.
- Both modes remain loopback-bound. Local control requires a private per-process token and a loopback peer; no unauthenticated shutdown route is exposed.
- Never print webhook, API, OAuth, session, control, or provider credential values. Doctor reports names and state only.
- Use bounded HTTP and lifecycle waits so management commands cannot hang.
- Do not parse unstable `launchctl print` text for fields. Use command success only as loaded/unloaded evidence and `/api/healthz` for runtime health.
- Managed start/stop target only `gui/<uid>/com.nags.factory`; they never install, deploy, refresh secrets, change receipts, or touch `factory-agents`.
- Local startup must fail clearly before accepting work when required app configuration or frontend assets are absent. Provider absence alone is not a startup error unless the user requests provider-only Cloud onboarding.
- Persistent stores and existing recovery rules remain unchanged. A management command never edits or deletes event, settings, routing, cursor, setup, run, deployment, or receipt data.

### 6. Verification mapping

| Acceptance criterion or risk | Exact evidence to add/run |
| --- | --- |
| Help/version aliases and hidden internals | Dispatcher table tests plus built-binary exit/output probes with explicit ldflags |
| Existing serve/internal behavior unchanged | Existing command tests, explicit no-arg/`serve` dispatch tests, and full Go suites |
| Standalone start works without network-app | Isolated temporary HOME, no `nags` or launchd artifacts, test credentials/fakes, built frontend, temporary port; start attached, poll loopback health, then stop through authenticated control |
| Local mode cannot weaken production auth | Tests proving explicit `serve` still rejects missing Google/OAuth configuration and only unmanaged `start` selects local viewer policy |
| Local control safety | Wrong/missing token, non-loopback request, stale record, successful graceful shutdown, restrictive file mode, and cleanup tests |
| Managed start/stop scope and idempotency | Injected command-runner tests for healthy, loaded unhealthy, unloaded, missing artifacts, command failure, readiness timeout, and exact fixed launchctl arguments |
| Status contract | `httptest.Server` cases for healthy, degraded, malformed, unreachable, JSON, and timeout |
| Doctor mode awareness and secret hygiene | Temporary HOME fixtures for standalone and managed layouts; identity mismatch and missing-artifact cases; assert secrets never appear |
| Publication gates | `gofmt`; `go test ./...`; `go test -race ./...`; `go vet ./...`; frozen Bun install and build |
| Post-deploy behavior | Version/help/status/doctor probes plus local/public health, receipt, release, process, and exact identity checks; do not stop production during automated verification |

### 7. Deployment, post-deploy verification, and recovery

Factory is deployable, so deployment applies. The only authorized source is the clean, human-merged, updated primary `main` checkout at `/Users/tom/repos/tomnagengast/factory`.

Repository instructions and the Factory lifecycle contract require:

```bash
bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
```

The repository does not currently track that compatibility entry point, while the installed provider supplies `~/.local/bin/nags` and `~/.local/share/nags/provider/bin/network-app`. The implementation plan must add a minimal repository-local `bin/network-app` delegator to the installed provider CLI so the mandatory command exists at the exact merged head. It is deployment plumbing for this managed installation, not a runtime dependency of standalone localhost mode. The delegator must fail clearly when the provider is absent and must not install or download it.

Required post-deploy evidence:

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

Local health, public health, receipt, active release, and CLI identity must agree on commit/tree/build/deployment/contract. Recovery after merge is a corrective or revert commit merged to `main` and the same commit-pinned deployment, or an explicit provider rollback to a retained successful deployment. Persistent data is never edited or deleted as recovery.

## Alternatives rejected

- **Launchd-only CLI:** excludes the users named in the continuation feedback.
- **Silently disable OAuth in `serve` when credentials are missing:** could turn a production configuration error into an authorization bypass.
- **Background daemon/PID-file management in this issue:** adds platform-specific detach behavior and PID-reuse hazards. Attached local start plus authenticated graceful stop is smaller and observable.
- **Generate launchd/systemd service files:** converts `start` into an implicit installer and expands the platform surface beyond the requested localhost first step.
- **Make provider artifacts mandatory for doctor:** would incorrectly mark the intended standalone mode unhealthy.
- **Install or download the private provider automatically:** violates standalone expectations and the provider's clean-main deployment authority.

## Assumptions authorized by the feedback

- “Works for any user” means the management CLI and localhost runtime do not require the private network-app provider; users must still supply Factory's application credentials and executable dependencies.
- “Start by just running this on localhost” authorizes an attached loopback-only local mode, not packaging, daemon installation, arbitrary interfaces, or public exposure.
- Existing managed installs retain their launchd/OAuth/receipt behavior. Provider integration may be made optional for standalone startup, not removed from managed production.
- `--json` on status and doctor is included because both surfaces consume structured contracts and need stable automation output.

## Unresolved questions

None. The continuation feedback resolves the prior product boundary in favor of a repository-native localhost fallback. The mandatory deployment command is resolved by adding its non-installing repository-local compatibility delegator and remains subject to adversarial plan review before implementation.
