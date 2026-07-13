# ENG-33 Implementation Plan: Show All Events on the Wire
> updated: 2026-07-13T22:07:38Z

Linear: https://linear.app/nags-cloud/issue/ENG-33/show-all-events-on-the-wire

## Current baseline

- Before implementation, the issue branch was synchronized with `origin/main` at `0e48f6b5f81f59a18f51f6f379b907b8cbc87c20` and the plan was revalidated against the merged source.
- The synchronized baseline adds project onboarding through `activity.Store.StagedPayload`, health/status reporting through `Wire.Status`, and durable permanent-event rejection. ENG-33 must reuse and preserve all three behaviors.

## Objective

Replace the legacy activity presentation with one canonical, generic wire workspace backed by the retained `eventwire.Journal`. Make `/home`, `/wire`, `/agents`, and `/agents/<issue>/<started>/run` the only application routes for these surfaces. Remove every legacy page and API path without redirects, aliases, or cached-client compatibility behavior, while preserving the internal payload projection and replay transaction.

## Scope boundaries

- Keep `/` as the existing public landing page and `/settings` plus `/api/settings` behavior unchanged.
- Keep `activity.Store` only for privacy-safe home totals, staged payload persistence and lookup, project-setup dispatch, compact delivery projection, and replay-before-ack safety.
- Do not change event ingestion, project-setup dispatch, dispatch ordering, acknowledgment, permanent-event rejection, `Wire.Status`, journal retention, sidecar formats, trigger policy, repository routing, merge authority, or deployment authority.
- Do not migrate or rewrite existing journal, activity, or payload files.
- Accept that cached old frontend bundles fail until refreshed. Do not add compatibility routes.

## Implementation

### 1. Add generic in-memory wire queries

Files: `internal/eventwire/journal.go`, `internal/eventwire/wire.go`, focused eventwire tests.

- Expose a read-only query surface over one cloned `Journal.Snapshot`, rather than reparsing the JSONL journal for every page poll.
- Return retained records newest first with offset/limit paging, optional exact source and type filters, retained and matching totals, the existing `Wire.Status` dispatch/rejection state, and dynamically derived source, type, and hourly counts.
- Treat source, type, action, subject, attributes, and channels as opaque data. Do not enumerate current source/type values in the query or response construction.
- Add exact retained-sequence lookup for detail. Return a cloned normalized record and a not-found result when the requested sequence has aged out or never existed.
- Preserve `Journal.Reject`, rejection persistence/compaction, and `eventwire.Permanent` dispatch isolation unchanged.
- Test ordering, page bounds, filters, dynamic unknown types, counts, snapshot isolation, sequence lookup, and unchanged status/rejection behavior.

### 2. Narrow the activity store to active runtime responsibilities

Files: `internal/activity/store.go` and its focused tests.

- Retain `StagePayload`, `StagedPayload`, `AddStaged`, `Add`, privacy-safe `Snapshot`, sidecar ownership/permissions, pruning, and atomic persistence.
- Remove `LinearPage` and presentation-only paging/count helpers.
- Replace hash-oriented `LinearEvent` presentation lookup with an internal payload lookup keyed by delivery ID for canonical wire detail, reusing `StagedPayload` where its current semantics apply rather than introducing a competing sidecar read path.
- Keep existing state and payload file formats unchanged.
- Preserve and extend tests for private payload lookup, missing/aged-out payload behavior, pruning, and staged projection durability.

### 3. Replace legacy HTTP routes with explicit canonical routes

Files: `internal/server/server.go`, `internal/server/server_test.go`, `internal/viewerauth/auth.go`, `internal/viewerauth/auth_test.go`.

- Register `/api/home` as the public sanitized overview endpoint backed by `activity.Store.Snapshot`.
- Register authenticated `/api/wire` and `/api/wire/<sequence>` handlers. Parse bounded paging and exact source/type filters; return generic wire records and counts. For Linear records only, resolve the normalized `deliveryId` attribute through the private activity payload lookup and attach an optional raw payload plus `payloadAvailable`; missing payloads remain successful normalized detail responses.
- Register authenticated `/api/agents` and `/api/agents/<issue>/<started>/run` only. Remove `/api/activity*` and `/api/agents/<run-id>` registrations and handlers.
- Register explicit SPA page handlers for `/home`, `/wire`, `/agents`, `/agents/<issue>/<started>/run`, and `/settings`. Keep `/` landing behavior. Serve only real frontend assets from the final static handler and return `404` for removed, unknown, malformed, and trailing-slash application paths.
- Protect only exact canonical authenticated page shapes. Change invalid OAuth `next` and logout fallbacks to `/home`; do not recognize legacy destinations.
- Keep Linear/GitHub dispatch projection before acknowledgment, project-setup enqueue from the staged Linear payload, permanent-failure isolation, and replay/catch-up behavior unchanged.
- Test public/private boundaries, successful list/detail responses, optional raw Linear payloads, unknown sequences, invalid paging/filter inputs, exact canonical page matching, old-path `404`s, malformed/trailing-slash `404`s, safe-next fallback, the existing project-setup dispatch path, wire rejection/status behavior, and the replay safeguard regression.

### 4. Rebuild the frontend around canonical navigation and generic wire data

Files: `frontend/src/index.tsx`, `frontend/src/styles.css`, and frontend configuration/tests if the existing project supplies them.

- Replace all activity URLs and clients with `/home`, `/api/home`, `/wire`, `/api/wire`, `/agents`, and the issue/start observer route.
- Render the public `/home` operational overview using only the sanitized home response.
- Generalize the ledger into an authenticated `/wire` workspace. Populate source and type filters from API counts, show every record newest first, and render source, type, action, subject, sequence, time, channels, and sorted attributes without source-specific display branches.
- Add generic selected-record detail and show an optional raw payload section only when the detail response supplies one. Keep historical and non-Linear detail useful when `payloadAvailable` is false.
- Make agent rows link only when both issue identifier and start time form the canonical observer path. Render pending runs as non-link rows; remove all run-ID URL generation and matching.
- Update global navigation, destination cards, settings links, breadcrumbs, loading/empty/error states, paging, focus behavior, and responsive layouts to use only canonical paths.
- Route only exact canonical frontend locations. The server remains authoritative for rejecting malformed and unknown locations.

### 5. Update durable documentation

Files: `README.md` and any directly affected checked-in route documentation found during implementation.

- Replace the legacy route/API map with the canonical page and API contract.
- Document that `/wire` is authenticated, generic, journal-backed, dynamically filterable, and may expose an optional private Linear raw payload in detail.
- Document the intentional hard cut and `404` behavior without describing a compatibility period.
- Preserve the existing human-only merge, exact verified-head, clean-main deployment, health, receipt, and rollback instructions.

## Verification before publication

### Focused automated checks

- Format changed Go files with `gofmt`.
- Run focused packages while iterating: `go test ./internal/eventwire ./internal/activity ./internal/server ./internal/viewerauth`.
- Exercise focused tests that prove replay projection happens before acknowledgment and that raw payload lookup remains private and optional.
- Run frontend type checking and a frozen production build while iterating.

### Required repository checks

From the issue worktree, with the repository-pinned Bun version:

```bash
go test ./...
go test -race ./...
go vet ./...
export MISE_BUN_VERSION=1.3.11
bun install --cwd frontend --frozen-lockfile
bun run --cwd frontend typecheck
bun run --cwd frontend build
```

Require a clean `git diff --check` and record the exact verified head after all review/CI remediation.

### Browser verification

- First check for an existing development server. If a changed-code server is needed, run it on an isolated temporary port and stop it before the turn ends.
- At desktop and mobile widths, verify `/home`, authenticated `/wire`, `/agents`, one canonical observer, and `/settings`.
- On `/wire`, verify dynamic source/type filtering, paging, generic current and unfamiliar event types, normalized detail, optional Linear raw detail, unavailable-payload detail, keyboard focus, loading/empty/error states, and no console errors or unexpected failed requests.
- Verify pending agents have no link.
- Verify `/activity`, every removed page/API, `/agents/<run-id>`, trailing-slash variants, malformed observer paths, and a random unknown route return `404` without redirecting or rendering Home.
- Verify unauthenticated `/api/home` remains sanitized while `/api/wire`, detail, and agent APIs return `401`.

## Publication and green loop

- Commit implementation in reviewable logical units using the repository's recent commit-message style and push the issue branch.
- Update draft PR #3 with the approved scope, test evidence, and hard-cut route matrix, then mark it ready for review.
- Publish the implementation summary to ENG-33 and move it to In Review.
- Use durable GitHub event waits as wake signals. After each event or timeout, refresh authoritative PR review, check, and head state with `gh`.
- Address only actionable review findings within ENG-33 scope. Re-run affected focused checks and all required repository checks after the final remediation.
- Prove the PR head equals the locally verified clean commit, then write the `ready-for-merge` Factory checkpoint for that exact OID. Stop for human merge authority.

## Post-merge deployment and cleanup

- Resume only after authoritative GitHub state proves PR #3 was human-merged and its merge result contains the exact verified head. Treat closure without merge or verified-head mismatch according to lifecycle contract v1.
- In the primary checkout `/Users/tom/repos/tomnagengast/factory`, require clean `main`, fetch, and fast-forward to `origin/main`. Never deploy from the issue worktree or T9 mirror.
- Capture the current successful deployment ID, then run `~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"` from clean updated `main`.
- Verify exact local and public health identity, the current deployment receipt, active release ancestry, canonical route/API behavior, authenticated wire content, and removed-path `404`s.
- On deployment or identity/content failure, preserve evidence and roll back to the captured successful deployment, then verify local and public health.
- After successful deployment, use Worktrunk for issue-worktree cleanup, remove only issue-scoped artifacts that the lifecycle requires, and confirm primary `main` remains clean and healthy.

## Unresolved questions

None.
