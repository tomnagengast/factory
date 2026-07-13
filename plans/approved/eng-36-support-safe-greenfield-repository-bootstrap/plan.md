# ENG-36 implementation plan: safe greenfield repository bootstrap

> updated: 2026-07-13T20:31:40Z

## Issue context

Factory must safely start work for an explicitly authorized project whose GitHub repository and managed checkout do not yet exist. It must also recover from the ENG-34 sequence 6651 head-of-line block, isolate future permanent routing failures, report dispatcher degradation truthfully, and preserve every existing merge and deployment safeguard.

The complete evidence is in `plans/planning/eng-36-support-safe-greenfield-repository-bootstrap/research.md`.

## Acceptance criteria

- Existing checkout, existing remote with missing checkout, authorized greenfield, and invalid or mismatched target states are classified and tested.
- Greenfield creation is explicit, private, deterministic, retry-safe, and produces a clean managed Git checkout with `origin/main` and a GitHub default branch.
- Owner, path, origin, symlink, dirty tree, unexpected branch, and partial bootstrap cases fail closed.
- Non-deployable repositories run and complete without fabricated deployment receipts or health URLs.
- Permanent dispatch failures are durably rejected and acknowledged without blocking a later valid event.
- Transient dispatch failures remain pending and make health return a degraded response with wire backlog evidence.
- Version 1 journals reopen and compact safely after rejection support is added, and the current release can reopen a journal written by the new release for rollback.
- Sequence 6651 is recovered append-only, ENG-34 is claimed, and ENG-36 can be retriggered if its failed webhook is not retried.
- Existing network, notebook, and factory routing, completion, and deployment remain compatible.
- The Go suite, race suite, vet, and frozen frontend build pass.

## Decisions and alternatives

### Authorization

The compiled `RepositoryCatalog` remains the authority. Add exact configuration for Artifacts and persist bootstrap policy into the run. Linear project metadata remains evidence that must exactly match the catalog.

Rejected alternatives:

- Trusting arbitrary Linear repository or path values would turn a signed webhook into filesystem and GitHub creation authority.
- An owner-only allowlist would still allow unintended repositories. Exact catalog membership is narrower and already established.
- Creating an empty directory does not satisfy Git, Worktrunk, pull request, or completion requirements.

### Bootstrap

The launcher will validate the managed root before any creation, query GitHub authoritatively, create only a missing allowlisted bootstrap remote, clone it, initialize an empty remote with one empty commit on the configured base, set the default branch, and then run the same clean checkout validation used for existing targets.

Existing non-Git directories are rejected, including empty ones. This prevents adoption or overwrite of an unexpected path. Partial retries are supported only when the path is already a matching Git checkout or the remote was created but remains empty.

### Event rejection

Permanent handler failures become atomic journal rejection-plus-ack records. Transient failures retain the current retry behavior. The error contract is typed so HTTP, rate limit, authentication, disk, and process failures cannot be accidentally dead-lettered.

The journal records the event identity, original sequence, reason, and rejection time. A bounded recent rejection list and lifetime total survive compaction. The original event remains in the retained journal according to normal retention. Rejection data is encoded as optional fields on the existing version 1 `ack` record and checkpoint shapes. Go's JSON decoder in the current release ignores those unknown fields while retaining the known acknowledgment, so rollback remains readable. No new checkpoint version or line kind is introduced.

### Health

`/api/healthz` will include total, dispatched, pending, rejected total, and the last rejection. Any pending record returns HTTP 503 and `status: degraded`; no pending records return HTTP 200 and `status: ok`, even when historical rejections exist. The HTTP listener starts before restart catch-up so this state is observable. The manager, service-started publication, and heartbeat loop remain gated until catch-up succeeds. A bounded recovery loop retries transient catch-up failures without starting agent work; webhook publications continue to fail retryably while the backlog remains.

### Deployment evidence

Completion evidence gains an explicit `DeploymentRequired` result. Existing system readers set it and retain every receipt and health check. A repository-only reader verifies updated base, merge containment, origin, branch deletion, Worktrunk cleanup, Linear completion, and child completion without deployment fields.

## Impacted files and interfaces

- `internal/eventwire/journal.go`: version-compatible rejection records, status snapshots, atomic reject acknowledgment, compaction.
- `internal/eventwire/wire.go`: permanent error wrapper and dispatch continuation.
- `internal/eventwire/journal_test.go`: version 1 compatibility, rejection persistence, compaction, and later-event dispatch.
- `internal/agentrun/repository.go`: repository configuration policy, path containment, normalized GitHub remote identity, typed permanent routing failures.
- `internal/agentrun/repository_test.go`: catalog, owner/path, deployment contract, and resolver classification.
- `internal/agentrun/store.go`: persist managed root and bootstrap authorization from trigger to run.
- `internal/agentrun/launcher.go`: target classification, GitHub creation, empty remote initialization, identity validation, and existing checkout synchronization.
- `internal/agentrun/launcher_test.go`: fake GitHub CLI and Git remotes covering all bootstrap and failure states.
- `internal/agentrun/completion.go` and `completion_system.go`: conditional deployment checks and repository-only evidence.
- Completion tests: deployable compatibility and repository-only success/failure evidence.
- `internal/server/server.go`: wire status in health and permanent routing propagation.
- `internal/server/server_test.go`: degraded health and permanent rejection followed by a valid trigger.
- `main.go`: GitHub CLI injection, explicit managed roots, Artifacts bootstrap entry, and reader selection.
- `README.md`: catalog, bootstrap, rejection, health, and recovery operations.

## Implementation phases

### Phase 1: Durable failure isolation and health

1. Add typed permanent dispatch errors.
2. Add append-only rejection metadata to backward-compatible version 1 acknowledgment records and expose journal status.
3. Continue after permanent errors while retaining transient failures.
4. Start the HTTP listener before catch-up, wire health to journal status, return degraded HTTP 503 for backlog, and start manager work only after catch-up succeeds.

Success: a permanent record is rejected once, a later event dispatches, restart does not replay the rejected record, a transient record remains pending behind an available degraded health response, and no manager run starts before catch-up succeeds.

### Phase 2: Trusted repository policy and run persistence

1. Extend catalog configuration with managed root, bootstrap permission, and optional deployment evidence.
2. Validate exact GitHub identity and secure target containment.
3. Mark immutable routing failures permanent.
4. Persist bootstrap policy through `Trigger` and `Run`.

Success: arbitrary metadata, path escapes, symlinks, remote mismatches, and incomplete deployment contracts fail closed before launch.

### Phase 3: Idempotent GitHub and checkout bootstrap

1. Inject `gh` into the launcher.
2. Classify local and remote state before mutation.
3. Create only an authorized missing private remote.
4. Clone or resume the checkout, initialize an empty base branch, and set GitHub's default branch.
5. Validate Git top-level, origin, base, upstream, remote base, synchronization, and cleanliness before tmux.

Success: initial bootstrap and retry both reach the same valid checkout; every unexpected state produces actionable failure without overwrite.

### Phase 4: Repository-only completion

1. Add repository-only completion construction.
2. Share repository and cleanup checks while making deployment checks conditional.
3. Select completion readers per catalog entry.

Success: Artifacts can satisfy exact verified-head completion without deployment evidence, while the three existing deployable repositories still require receipts and matching health.

### Phase 5: Production wiring, documentation, and recovery

1. Add the exact Artifacts authorization and document the operational contract.
2. Run all focused and full verification.
3. Publish the reviewed PR and record the verified head.
4. After human merge, fast-forward clean primary main and deploy the exact merge commit.
5. Verify sequence 6651 acknowledgment, zero pending backlog, ENG-34 claim and repository bootstrap, health identity, and ENG-33 session preservation.
6. Register the Artifacts GitHub webhook and retrigger ENG-36 only if its failed delivery was not retried.

Success: Factory is online with truthful healthy status, no wire backlog, Artifacts initialized, and later events dispatch normally.

## Security, compatibility, rollout, and rollback

- Repository creation is limited to exact catalog entries and private visibility.
- Path checks use both lexical containment and symlink-resolved existing ancestors before mutation.
- Existing v1 journal files remain readable; rejection metadata uses optional fields on existing v1 checkpoint and acknowledgment records, and an old-reader/new-journal test proves rollback compatibility. No offline migration or journal deletion occurs.
- Existing deployable configs retain current completion behavior. Optional deployment fields are all-or-none.
- Rollout is one commit-pinned Factory deployment after human merge. The listener starts immediately for degraded health visibility; the manager, service-started event, and heartbeats start only after catch-up succeeds.
- Rollback uses `bin/network-app rollback factory --to <previous-deployment-id>` from the network repository if identity or recovery checks fail. Append-only recovery evidence and any safely created private repository remain available for diagnosis.

## Post-merge deployment and recovery commands

From `/Users/tom/repos/tomnagengast/factory`:

```bash
git fetch --prune origin
git merge --ff-only origin/main
test -z "$(git status --porcelain --untracked-files=normal -- . ':(exclude,literal).worktrees')"
```

From `/Volumes/T9/Repos/tomnagengast/network`:

```bash
bin/network-app deploy factory --expected-commit "$(git -C /Users/tom/repos/tomnagengast/factory rev-parse HEAD)"
```

Verification:

```bash
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq '.runs[] | select(.issueIdentifier == "ENG-34")' ~/.local/share/factory/data/agent-runs.json
gh repo view tomnagengast/artifacts --json nameWithOwner,defaultBranchRef,isPrivate
git -C ~/repos/tomnagengast/artifacts status --short --branch
tmux -L factory-agents has-session -t factory-eng-33
```

After the Artifacts remote exists:

```bash
/Volumes/T9/Repos/tomnagengast/network/bin/nags github-hook tomnagengast/artifacts
```

## Verification matrix

| Criterion or risk | Verification |
| --- | --- |
| Permanent failure does not block later events | Focused eventwire and server tests dispatch permanent-invalid then valid records |
| Transient failure remains retryable | Eventwire test asserts pending cursor and later catch-up |
| Journal compatibility and audit | Open a v1 fixture, reject, reopen, compact, inspect status, and parse the new journal with a frozen copy of the current v1 reader |
| Truthful health | Startup and server tests assert HTTP 503 with pending count while manager work is gated, then HTTP 200 and manager start after catch-up |
| Existing checkout | Launcher integration fixture validates reuse and fast-forward |
| Existing remote, missing checkout | Launcher integration fixture clones and validates |
| Greenfield and retry | Fake `gh` plus real local bare Git remote validates create, initial commit, default branch, and second prepare |
| Path and symlink security | Catalog and launcher table tests cover traversal, outside root, target symlink, and ancestor symlink |
| Origin and branch mismatch | Launcher tests cover unexpected remote and default branch |
| Partial bootstrap | Tests cover existing empty remote and matching unborn local clone |
| Optional deployment | Completion tests prove repository-only success and deployable receipt enforcement |
| Existing behavior | `go test ./...`, `go test -race ./...`, `go vet ./...` |
| Frontend remains buildable | `bun install --cwd frontend --frozen-lockfile && bun run --cwd frontend build` |
| Production recovery | Health identity, zero pending, sequence at least 6651, ENG-34 run, Artifacts remote/checkout, ENG-33 tmux |

## Unresolved questions

None.
