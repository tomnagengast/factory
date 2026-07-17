# Factory agent runner

Factory turns authenticated system events into durable, workflow-pinned engineering Runs inside trusted repository workspaces. The managed service uses one selected state generation and five domain authorities: policy, repositories, Runs, tasks, and the system event wire.

## Architecture

The managed runtime has one owner for each durable concept:

| Concept | Owner | Selected-generation artifact |
| --- | --- | --- |
| Published workflows, protected bindings, trigger rules, schedules, runtime settings, and native-project activation | policy | `policy.json` |
| Compiled and Linear-admitted repository routes, onboarding state, and deployment contracts | repositories | `repositories.json` |
| Admission decisions, causation, lifecycle, retries, merge parking, remediation, and completion | Runs | `runs.jsonl` |
| Native task commands, private bodies, results, Linear identity bindings, messages, links, and gates | tasks | `tasks.jsonl` |
| Ordered Linear, GitHub, Factory, task, and Run events | event wire | `system-events.jsonl` |

The event owner also keeps the bounded Home projection and private Linear payload corpus under `activity/`. Workflow drafts, schedule cursors, and agent-event offsets remain narrow runtime sidecars under `runtime/`. They do not decide admissions or Run lifecycle.

`internal/app` is the managed composition boundary. It adapts the canonical stores to the stable HTTP, launcher, observer, and provider interfaces. Compatibility packages remain compiled for the first production migration, retained old workflow pins, and unmanaged localhost operation. The selected managed runtime never opens the legacy data-root stores as writers.

## State generations and activation

Managed startup uses `~/.local/share/factory` as the state root:

```text
~/.local/share/factory/
├── data/
│   ├── state-generation.json
│   ├── provider-finalization.json
│   ├── state-transition.lock
│   └── <legacy source artifacts retained for migration and recovery>
├── generations/<migration-id>/
│   ├── policy.json
│   ├── repositories.json
│   ├── runs.jsonl
│   ├── tasks.jsonl
│   ├── system-events.jsonl
│   ├── activity/
│   ├── runtime/
│   ├── generation.json
│   ├── migration.json
│   ├── audit.json
│   ├── backup-receipt.json
│   ├── backup/
│   └── canonicalWritesStarted
└── restorations/
```

When no generation is selected, startup strictly decodes the immediately previous production state, builds a complete sibling generation and full backup, verifies conversion digests and permissions, and opens only bootstrap health. It does not mutate legacy source state or start advancing managers.

After the deployment provider writes an exact successful receipt, Factory acquires the state-transition lease before the provider deployment lock. It validates the receipt, current release, binary, wrapper, launchd artifact, source commit and tree, build identity, and retained generation. It then fsyncs the provider graph, writes `provider-finalization.json`, publishes `state-generation.json`, and creates the monotonic `canonicalWritesStarted` boundary. Canonical stores open for mutation only after that boundary exists.

On restart, Factory revalidates the selected generation and provider graph, reacquires the state-transition lease, strictly replays every canonical journal, and never rewrites activation evidence.

## Runtime supervision

The outer supervisor owns the HTTP server and generation runtime. The HTTP listener exposes exact bootstrap health while first activation is pending. After selection, the handler switches atomically to the complete application.

The selected runtime first drains the wire, reconciles the task outbox, repository onboarding, and Runs, and only then marks advancing work ready. A joined supervisor then owns four advancing components:

- repository onboarding;
- one Run manager for routing, launch, feedback, GitHub reconciliation, and completion;
- the cron scheduler;
- service heartbeat publication.

Cancellation propagates to every in-process component and their errors stop the runtime. Run-owned `factory-agents` tmux sessions are deliberately outside the service process tree. They survive a service restart and are reconciled from canonical Run state.

## Triggers and admission

Apply the Linear workspace label:

```text
Factory
```

The compiled label rule accepts only a signed `Issue/update` from the configured Linear actor where that label was newly added. Unrelated updates, removal, or an already-present label do not start work.

Human `Comment/create` events on issues with retained Factory history use the protected feedback path. They coalesce into an active Run or create one fresh continuation after a terminal Run. The old compiled generic comment rule is removed. An operator-created visible generic comment rule remains supported and is intentionally additive.

Authenticated operators manage exact event rules, schedules, workflows, protected bindings, runtime settings, and native-project activation at `/triggers`, `/workflows`, and `/settings`. Editable policy cannot change repository allowlisting, capability boundaries, human-only merge authority, exact-head ancestry, deployment source, completion validation, or cleanup checks.

Every event-derived admission is recorded in the canonical Run journal with its policy generation, workflow digest, task identity, causal chain, routing outcome, and rate accounting. Same-task ownership, hop and cycle limits, outstanding caps, and rolling-hour limits remain mechanical.

## Project onboarding

Factory accepts signed Linear `Project/create` and `Project/update` metadata in this form:

```text
GitHub Repo: tomnagengast/example
Local Path: /Users/tom/repos/tomnagengast/example
Cloud URL: https://example.nags.cloud
```

`Cloud URL` is optional. The repository and local path must match a compiled catalog entry or a valid private `tomnagengast` repository below the managed root. Onboarding is persisted in `repositories.json`, not `project-setups.json`.

The onboarding component verifies or creates the private GitHub repository, prepares a canonical checkout, enforces merge-commit-only policy and automatic head deletion, installs the GitHub hook, and records the exact route. Symlink escapes, mismatched origins, dirty unexpected checkouts, public repositories, unknown owners, and ambiguous paths fail closed.

Cloud-enabled projects create an idempotent Network provider issue. Coordinated work may touch any admitted repository, but each repository still requires its own prefixed branch, pull request, human merge, exact verified-head proof, repository-native deployment, and cleanup.

## Run lifecycle

1. A webhook is signature-checked, replay-window checked, normalized, and appended to the system wire.
2. Protected and configured admission handlers make one coordinated policy decision and append the resulting Run batch.
3. The Run manager resolves the exact allowlisted repository route, preserves oldest-per-task ownership, and prepares the isolated workspace.
4. A deterministic `factory-<source>-<run>` tmux session starts on the `factory-agents` socket.
5. The principal receives the complete immutable workflow pin. New Runs use `full-sdlc-provider-neutral`; retained older Runs keep their exact prior pin and compatibility capability.
6. A ready principal writes the exact checkpoint through `agent checkpoint ready-for-merge` and exits. The Run parks without consuming an LLM.
7. GitHub events wake the parked Run, while bounded authoritative refresh closes missed-webhook gaps.
8. Only a human merge commit that contains the exact checkpointed head starts post-merge work.
9. The continuation fast-forwards a clean primary checkout, deploys applicable surfaces from merged `main`, verifies identity and health, and cleans the remote branch plus Worktrunk checkout.
10. Factory accepts terminal success only after pull-request, review, checks, Linear feedback, deployment, child, branch, and worktree evidence all pass.

The principal never merges, enables auto-merge, bypasses branch protection, deploys from an issue worktree, or invents a deployment target.

## Native tasks

Factory owns `FAC-N` tasks. Linear owns its provider tasks. Both project into the same Run lifecycle, but private bodies stay in their owning task authority and never enter the global event wire.

The canonical task journal contains commands and their outbox state. A command records its deterministic event identity before publication, applies once when the wire dispatches it, and is acknowledged only after the global dispatch cursor advances. Startup republishes an unpublished command or reuses its durable result; it does not need `task-operations/`.

Provider-neutral Runs receive a private capability bound to the exact Run, TaskRef, and repository. They do not receive `LINEAR_API_KEY`. Use the scoped helper inside those Runs:

```bash
"$FACTORY_AGENT_HELPER" agent task show
"$FACTORY_AGENT_HELPER" agent task messages --after 0
"$FACTORY_AGENT_HELPER" agent task activity --after 0 --revision 0 --wait 60s
"$FACTORY_AGENT_HELPER" agent task comment --body 'Implementation evidence is ready'
"$FACTORY_AGENT_HELPER" agent task reply --parent message-id --body 'Addressed'
"$FACTORY_AGENT_HELPER" agent task link --label 'Pull request' --url 'https://github.com/owner/repository/pull/1'
"$FACTORY_AGENT_HELPER" agent task gate open --kind plan --mode gated --artifact-url 'https://example.invalid/plan'
```

## Event wire and helper cursors

`system-events.jsonl` is the only event journal opened by the selected managed runtime. It retains the newest 10,000 acknowledged records plus pending records and a monotonic lifetime cursor. Linear bodies live only in private `0600` activity sidecars; GitHub bodies are not retained; principal and child bodies remain in private Run JSONL files.

Use the source-neutral helper for new workflows:

```bash
"$FACTORY_AGENT_HELPER" agent events \
  --source factory \
  --type agent-run \
  --subject ENG-47 \
  --match runId="$FACTORY_RUN_ID" \
  --after 0 \
  --wait 60s
```

Retained workflow pins may continue using:

```bash
"$FACTORY_AGENT_HELPER" agent github-events --repo owner/repository --pr 123 --branch eng-47-work --after 0 --wait 60s
"$FACTORY_AGENT_HELPER" agent linear-comments --issue ENG-47 --after 0 --wait 60s
```

Those are cursor-compatible views over the unified wire. The managed runtime does not write `github-events.json` or `linear-comments.json`. Every wake is advisory; the principal fresh-reads authoritative GitHub or Linear state before acting.

Factory-authored Linear comments end with exactly one reserved final non-empty line:

```text
🐘
```

or an exact inline-code coordination marker. No prose follows the footer.

## Web interface

- `/` is public health identity.
- `/home` is a public privacy-safe activity summary.
- `/wire`, `/agents`, `/tasks`, `/workflows`, `/triggers`, and `/settings` are authenticated operator surfaces.
- `/agents/<task>/<started-ms>/run?source=factory|linear` is the read-only live or retained Run observer.

`frontend/src/index.tsx` is exact-route composition only. Feature owners live in `home.tsx`, `wire.tsx`, `agent-activity.tsx`, `agent-detail.tsx`, `tasks.tsx`, `workflows.tsx`, `triggers.tsx`, and `settings.tsx`. All raw HTTP transport is in `http.ts`; all interval lifecycle is in `poll.ts`; optimistic form state is shared through `forms.tsx`. Tasks retain their idempotency and conflict behavior, while workflow autosave remains distinct from ordinary optimistic saves.

Managed browser access uses Google OAuth and `FACTORY_GOOGLE_ALLOWED_EMAILS`. Protected API automation may use the private `0600` token at `~/.local/share/factory/data/api-token` as `Authorization: Bearer <token>`. Browser sign-in and logout never accept that token.

## Configuration and local operation

The managed launchd wrapper supplies:

- `LINEAR_WEBHOOK_SECRET`
- `GITHUB_WEBHOOK_SECRET`
- `LINEAR_API_KEY`
- `LINEAR_TRIGGER_ACTOR_ID`
- `FACTORY_GOOGLE_CLIENT_ID`
- `FACTORY_GOOGLE_CLIENT_SECRET`
- `FACTORY_GOOGLE_ALLOWED_EMAILS`
- `FACTORY_SESSION_KEY`

Build from the repository root:

```bash
export MISE_BUN_VERSION=1.3.11
bun install --cwd frontend --frozen-lockfile
bun run --cwd frontend typecheck
bun run --cwd frontend build
go build -o factory .
```

Management commands are:

```bash
./factory --help
./factory --version
./factory start
./factory status --json
./factory doctor --json
./factory stop
```

`factory serve` is the managed generation-selected entry point. An unmanaged loopback `factory start` remains a foreground compatibility environment for local development and uses the pre-cutover stores. It is not a deployment source or evidence that managed canonical activation is healthy. A non-loopback local start requires explicit HTTPS termination and the managed Google variables.

## Deploy and verify

Factory deployment is allowed only from a clean primary `main` checkout that exactly matches fetched `origin/main` and the expected commit:

```bash
~/.local/bin/nags refresh-env
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
```

Local health, public health, the successful receipt, the `current` release, and the selected generation must agree on commit, tree, build ID, deployment ID, and contract version. A listening process with mismatched identity is not healthy deployment evidence.

## Recovery

Inspect before changing anything:

```bash
curl -sS http://127.0.0.1:8092/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
jq . ~/.local/share/factory/data/state-generation.json
jq . ~/.local/share/factory/data/provider-finalization.json
tmux -L factory-agents list-sessions
launchctl print "gui/$(id -u)/com.nags.factory"
```

Normal rollback always crosses the Factory wrapper:

```bash
bin/network-app rollback factory --to <successful-deployment-id>
```

The wrapper serializes recovery, asks the live service to quiesce with `SIGUSR1`, continuously holds the exact state-transition lease, runs Factory preflight, passes the inherited locked descriptor and token to the provider, and remains the live parent through provider completion. The provider rejects a missing, stale, wrong-token, wrong-inode, or unrelated lease.

If canonical writes never started, rollback may remove an incomplete selector safely. Once `canonicalWritesStarted` exists, rollback refuses until `state-restore` proves the complete source backup is still eligible. Restoration is intentionally rare. It requires:

- the exact generation `backup-receipt.json`;
- unchanged initial canonical artifact bytes;
- unchanged retained source state and backup hashes;
- no nonterminal Run or retained effect-capable session in the activation manifest;
- no current `factory-agents` tmux session;
- the matching live state-transition lease.

Run the explicit restoration only after preserving the failed state and reviewing the evidence:

```bash
factory state-restore \
  --data-root "$HOME/.local/share/factory/data" \
  --migration-receipt "$HOME/.local/share/factory/generations/<migration-id>/backup-receipt.json"
```

A successful restore archives the selected generation, removes selection and provider acknowledgement, and writes `state-restoration.json`. Then retry the same wrapper rollback. Any changed canonical state, activation-spanning Run/session, later tmux session, ambiguous archive, or incomplete restoration refuses whole-backup recovery and requires a forward correction.

Never repair state by editing journals, manifests, receipts, selectors, lease records, or compatibility markers. Never stash, reset, or deploy around a dirty or divergent primary checkout.

## Troubleshooting

For degraded health, inspect the selected generation, not the legacy data-root journals:

```bash
generation=$(jq -r '.migrationId' ~/.local/share/factory/data/provider-finalization.json)
root="$HOME/.local/share/factory/generations/$generation"
jq . "$root/generation.json"
jq . "$root/audit.json"
tail -n 20 "$root/system-events.jsonl"
tail -n 20 "$root/runs.jsonl"
jq . "$root/policy.json"
jq . "$root/repositories.json"
test "$(stat -f '%Lp' "$root")" = 700
find "$root" -type f ! -perm 600 -print
```

The final `find` must print nothing. Preserve malformed artifacts for diagnosis. Pending wire work indicates a retryable dependency or handler failure; a rejection is a durable isolated policy or routing outcome. Repository onboarding failures keep their bounded retry evidence in `repositories.json`. Run retry and authoritative GitHub refresh state lives in `runs.jsonl`.

Use `factory doctor --json` for bounded configuration and identity checks. Use the authenticated `/wire` and `/agents` views for normalized event and Run evidence. Do not use process existence as a substitute for health identity.

## Implementation footprint and removal conditions

The ENG-47 baseline at `8d4b3d6` contained 20 Go packages, 22,977 production Go lines, 650 exported top-level declarations or methods by the repository regex audit, 16 named data-root authorities, five unjoined post-readiness loops, a 2,584-line frontend entry, four raw-fetch implementations, and four interval implementations.

The current implementation has:

- 26 Go packages, 43,017 production Go lines, and 1,111 matching exported declarations or methods;
- nine named selected-generation runtime artifacts, excluding manifests, receipts, backups, and payload sidecars;
- four joined advancing components after one recovery boundary;
- a 47-line frontend route entry;
- raw fetch in one transport module and interval creation in one polling module.

The package, Go-line, export, and three-loop targets are not met. The increase is the honest cost of preserving three distinct pre-cutover invariants in the same release: strict one-shot conversion from the immediately previous production formats, exact execution of retained nonterminal old workflow pins, and the existing unmanaged localhost compatibility runtime. Deleting those implementations before the first canonical production cutover would remove the only rollback source decoder or break retained work. The managed runtime nevertheless reduces active durable authorities from 16 to nine and removes all legacy runtime writers from production composition.

Removal becomes eligible only after a successful canonical production deployment, retained-state expiry, and proof that no nonterminal Run uses an old pin. At that boundary, remove the legacy source decoders, unmanaged legacy composition or replace it with a generation-native disposable fixture, compatibility packages and projections, then repeat the full migration, recovery, API, race, and browser matrices. Numeric budgets never justify weakening migration, recovery, capability, human-merge, exact-head, deployment-source, or completion safeguards.
