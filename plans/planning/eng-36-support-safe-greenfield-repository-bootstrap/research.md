# ENG-36 research: safe greenfield repository bootstrap

## Research questions

1. What current behavior and failure mode are proven by source and live state?
2. Which files and interfaces own repository routing, workspace preparation, event dispatch, health, and completion evidence?
3. What trust and lifecycle invariants must remain unchanged?
4. How can a greenfield repository be created idempotently without treating webhook metadata as authority?
5. How should permanent dispatch failures remain auditable without blocking later events?
6. How should non-deployable repositories complete without fabricated deployment evidence?
7. What exact deployment and recovery procedure applies?

## Evidence-backed answers

### 1. Current behavior and failure

- `main.go` builds a static `RepositoryCatalog` containing network, notebook, and factory. Artifacts is absent, so the Artifacts project cannot resolve even though its Linear project metadata declares `tomnagengast/artifacts`.
- `internal/agentrun/repository.go` treats the catalog as the repository trust boundary. It requires exact GitHub Repo and Local Path lines, but all catalog entries also require deployment receipt, pending receipt, and health locations.
- `internal/agentrun/launcher.go` creates the checkout parent and clones a missing remote. It does not create a remote, initialize an empty remote, validate the configured owner or managed root, reject a symlink escape, validate `origin`, or verify the configured default branch before tmux launch.
- Live `system-events.jsonl` has total sequence 6651 and dispatched sequence 6650. Sequence 6651 is the ENG-34 label event. It resolves to `tomnagengast/artifacts` and remains pending.
- `internal/eventwire/wire.go` calls catch-up before every new publication. Any handler error returns immediately without an acknowledgment. Consequently every heartbeat, agent record, and webhook retried sequence 6651 before it could append its own event.
- Live logs alternated between `tomnagengast/artifacts is not allowlisted` and `Linear HTTP 400`. The Linear API reported the 2500 request per hour limit exhausted. This proves the pending event caused both head-of-line blocking and a retry storm.
- The ENG-36 re-label delivery did not enter the system journal because `Wire.Publish` failed in its pre-publication catch-up. It must be retriggered after recovery if Linear does not retry the failed webhook.
- `/api/healthz` returned HTTP 200 with `status: ok` while sequence 6651 was pending. Health had no event-wire dependency.
- At 2026-07-13 13:19 PDT, `com.nags.factory` was booted out to stop consuming newly replenished Linear requests. The separate `factory-eng-33` tmux session and all journals remain present.

### 2. Participating files and interfaces

- `main.go`: repository catalog, resolver, launchers, completion readers, event journal, server, and deployment identity wiring.
- `internal/agentrun/repository.go`: trusted project metadata parsing and allowlisted repository configuration.
- `internal/agentrun/store.go`: durable trigger and run routing fields.
- `internal/agentrun/launcher.go`: managed checkout preparation and tmux environment.
- `internal/agentrun/completion.go` and `completion_system.go`: exact verified-head, deployment, repository, Linear, child, and cleanup evidence.
- `internal/eventwire/wire.go` and `journal.go`: ordered dispatch, acknowledgment, persistence, replay, and compaction.
- `internal/server/server.go`: normalized Linear dispatch and public health response.
- Tests beside each source file already provide fake HTTP, Git, tmux, journal, and server fixtures suitable for the required cases.
- `README.md` documents the catalog, wire, deployment, and recovery contracts and must change with those contracts.

### 3. Invariants to preserve

- Linear metadata is routing evidence, not authority. Only an exact compiled catalog entry may authorize repository creation.
- Managed repository paths must remain absolute and contained by a configured root after symlink resolution.
- A run cannot start until its managed checkout is a clean Git top-level whose `origin`, branch, upstream, and remote default branch match the configured repository.
- Human-only merge authority, exact verified-head validation, clean updated-main deployment, receipt validation for deployable apps, and Worktrunk cleanup remain mandatory.
- Existing network, notebook, and factory repository behavior and completion evidence remain deployable and unchanged.
- The journal remains append-only for recovery. Sequence 6651 must not be deleted or rewritten.

### 4. Idempotent bootstrap design

- Add explicit bootstrap and managed-root fields to the allowlisted repository configuration and persist them into each claimed run.
- Add Artifacts as an exact catalog entry for `tomnagengast/artifacts` at `/Users/tom/repos/tomnagengast/artifacts`, rooted at `/Users/tom/repos/tomnagengast`, with bootstrap enabled and no deployment contract.
- Before touching the target, reject non-absolute paths, lexical escapes, existing symlinks, and existing ancestors that resolve outside the managed root.
- For a missing target, use authenticated `gh repo view` to distinguish an existing remote from a confirmed missing remote. Create only a configured bootstrap repository, as private, and verify its canonical name afterward.
- Clone the configured remote. An empty authorized remote is initialized with the configured base branch, an empty bootstrap commit, upstream tracking, and an explicit GitHub default branch.
- Retries resume partial states: a created empty remote can be cloned and initialized, and a matching empty local checkout can be initialized. Existing non-Git directories, mismatched origins, unexpected branches, dirty trees, and unexpected non-empty remote states fail closed.

### 5. Permanent dispatch failure design

- Introduce a typed permanent error contract. Catalog and immutable routing failures use it; transport, authentication, rate limit, disk, and process errors remain transient.
- When a handler returns a permanent error, append a rejection record containing sequence, event identity, reason, and timestamp while advancing global and channel acknowledgments in the same fsynced journal line. Continue dispatching later records.
- Retain lifetime rejection totals and a bounded recent rejection list through journal compaction. This is an auditable dead-letter outcome, not a silent drop.
- Existing version 1 journals must open without migration. New optional rejection fields can be written in a backward-compatible version 2 checkpoint.

### 6. Non-deployable completion

- Repository execution and deployment evidence are separate concerns. Existing deployable configurations continue using receipts and health.
- A repository-only completion reader verifies clean updated base, matching origin, merged commit containment, remote branch deletion, Worktrunk removal, completed Linear state, and child completion without reading a deployment receipt or health URL.
- Completion validation checks deployment receipt and health only when the selected evidence reader declares deployment required.

### 7. Deployment, recovery, and verification

- Full pre-publication verification is `go test ./...`, `go test -race ./...`, `go vet ./...`, and `bun install --cwd frontend --frozen-lockfile && bun run --cwd frontend build`.
- After a human merges the exact verified PR head, update the primary checkout and deploy only from clean merged main with `bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"` from the network repository, as required by `AGENTS.md` and the existing Factory runbook.
- Verify local and public health identity, wire pending count zero, dispatched sequence at least 6651, and a durable rejection or claimed ENG-34 run.
- ENG-34 should resolve through the newly allowlisted bootstrap entry, causing sequence 6651 to be acknowledged and the Artifacts remote and checkout to initialize. Register its GitHub webhook after the remote exists.
- Reapply the ENG-36 Factory label only if Linear does not retry the failed delivery. Run-store idempotency prevents duplicate nonterminal runs.
- Rollback remains the existing commit-pinned Factory rollback command. The pre-deploy journal and current release are never deleted.

## Contradictions and resolved assumptions

- The original title implied only directory creation. Source and live evidence prove that an empty directory would make the failure worse because Git and Worktrunk require an initialized repository.
- A permanent unallowlisted event should normally be rejected. Sequence 6651 is different after this change because Artifacts becomes explicitly allowlisted and should be processed, not rejected.
- Health should not remain degraded merely because historical rejections exist. It is degraded while the wire has pending dispatch work and exposes rejection totals for audit after recovery.

## Unresolved questions

None. The catalog entry is the explicit operator authorization, Artifacts is non-deployable, repositories are private by default, and all merge and deployment gates remain unchanged.
