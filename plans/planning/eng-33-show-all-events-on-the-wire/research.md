# ENG-33 Research: Show All Events on the Wire

Linear: https://linear.app/nags-cloud/issue/ENG-33/show-all-events-on-the-wire

## Research questions

1. What current behavior prevents the UI from showing every retained event on the unified wire?
2. Which server, frontend, authentication, persistence, documentation, and test surfaces participate in the requested route changes?
3. What generic wire representation can display existing and future sources, event types, actions, subjects, channels, and attributes without source-specific frontend branches?
4. What must remain unchanged, especially public/private boundaries, retained Linear payload access, settings behavior, event dispatch, and lifecycle authority?
5. How should old URLs behave after the rename, and which compatibility constraints matter during deployment?
6. How can each acceptance criterion and material risk be observed before publication and after deployment?
7. What exact merged-main deployment, health/content verification, and recovery procedure applies?

## Evidence-backed answers

### 1. Current behavior and root cause

Observed:

- The public overview is served by the SPA at `/activity` and reads `/api/activity`. That API is backed by `activity.Store.Snapshot`, which contains only a lifetime delivery count, the bounded legacy activity projection, and opaque agent-run totals (`internal/server/server.go:186-192`, `internal/server/server.go:303-315`).
- The authenticated `/activity/linear` page calls `/api/activity/linear` and `/api/activity/linear/{id}`. Those handlers call `activity.Store.LinearPage` and `activity.Store.LinearEvent` (`internal/server/server.go:267-270`, `internal/server/server.go:317-352`, `frontend/src/index.tsx:232-269`).
- `activity.Store` retains at most 250 entries in production (`main.go:29-34`, `main.go:123-126`). Its Linear page explicitly filters records whose delivery ID begins with `github:`, and its detail method repeats the same source-by-prefix assumption (`internal/activity/store.go:192-253`). Factory service, lifecycle, and agent-record events never enter this legacy projection.
- The actual unified wire is `eventwire.Journal`, opened at `~/.local/share/factory/data/system-events.jsonl` with a 10,000-record acknowledged retention target (`main.go:33-34`, `main.go:139-149`). Each record has a monotonic sequence and a generic event containing `id`, `source`, `type`, `action`, optional `subject`, attributes, channels, and receive time (`internal/eventwire/journal.go:18-27`, `internal/eventwire/event.go:26-35`).
- `Journal.Snapshot` already returns a cloned in-memory snapshot without rereading the journal file, but it is not paged and `Wire` does not expose it (`internal/eventwire/journal.go:250-254`, `internal/eventwire/wire.go:16-20`). `eventwire.Read` reparses the file and is intended for CLI/wait adapters, so it is not the correct polling-page primitive (`internal/eventwire/journal.go:434-469`).
- A read-only live probe on 2026-07-13 found the running Factory service on port 8092 at merged commit `606360a947c5d830712fcef32abe47b77b52ae4a`. The wire contained Linear, GitHub, and Factory sources plus dynamic event types including `service`, `agent-record`, `agent-run`, `pull_request`, `Issue`, `Comment`, and `Attachment`. Factory heartbeats were the largest class, proving that the UI needs generic source/type grouping and filters rather than a renamed Linear-only list.

Inference supported by the issue title and storage model:

- `/wire` must be backed by `eventwire.Journal`, not by a generalized `activity.Store`. Only the journal can satisfy “all events on the wire,” including Factory-originated events.

### 2. Route, link, and authentication surface

Observed:

- Server page protection is currently registered for `/activity/linear`, `/activity/agents`, `/agents`, and `/settings`; `/activity` itself reaches the public SPA fallback (`internal/server/server.go:264-289`, `internal/server/server.go:810-839`). Unknown non-API paths also receive the SPA, so merely deleting old frontend branches would make legacy links silently render the landing page.
- Frontend routing is plain pathname matching. It recognizes `/activity`, `/activity/linear`, `/activity/agents`, `/settings`, `/activity/agents/<issue>/<started>/run`, and legacy `/agents/<run-id>` (`frontend/src/index.tsx:1741-1769`). Navigation, destination cards, settings links, agent backlinks, and `agentRunHref` contain the old paths (`frontend/src/index.tsx:354-401`, `frontend/src/index.tsx:475-487`, `frontend/src/index.tsx:693-816`, `frontend/src/index.tsx:1188-1191`, `frontend/src/index.tsx:1568-1587`).
- OAuth return destinations are fail-closed. `safeNext` falls back to `/activity`; `protectedPagePath` allows `/agents`, `/settings`, and the old authenticated activity routes; logout also returns to `/activity` (`internal/viewerauth/auth.go:244-248`, `internal/viewerauth/auth.go:404-427`). `/wire` must be added or an authenticated deep link will be downgraded to the fallback after Google sign-in.
- `/agents/<run-id>` is an existing compatibility path for pending runs and old bookmarks. A new `/agents/<issue>/<started>/run` route does not collide with its one-segment matcher (`frontend/src/index.tsx:1582-1587`, `frontend/src/index.tsx:1741-1743`).
- `/settings` has its own authenticated page/API registration and is not coupled to the activity route names (`internal/server/server.go:272-273`, `internal/server/server.go:286-287`). Only its navigation links need to point to the renamed pages; settings behavior and APIs must not change.

Decision:

- Register new canonical pages at `/home`, `/wire`, `/agents`, and `/agents/<issue>/<started>/run`.
- Add server-side permanent redirects from `/activity`, `/activity/linear`, `/activity/agents`, and nested old agent-run URLs. Redirects must happen before authentication so the canonical protected path becomes the OAuth `next` value. Preserve query strings.
- Keep the existing `/api/activity`, `/api/activity/linear*`, `/api/activity/agents*`, and `/api/agents/<run-id>` endpoints as compatibility APIs. Add an authenticated `/api/wire` rather than renaming working APIs during the page deployment.

### 3. Generic wire API and UI behavior

Observed:

- The backend wire representation is already source-agnostic above ingestion: source is a string-backed type and type/action/subject/attributes/channels are data, not sealed event-specific structs (`internal/eventwire/event.go:11-35`). Ingestion currently validates three source constants, but type and action values are open strings (`internal/eventwire/event.go:37-77`).
- The wire deliberately stores normalized routing/audit metadata, not raw message bodies. Linear raw bodies remain in private activity sidecars, GitHub bodies are never retained, and agent bodies remain in run JSONL files (`README.md:51-55`).
- Current Linear detail supports raw payload inspection through the retained sidecar API (`internal/activity/store.go:235-253`). Every normalized Linear wire event contains a delivery ID attribute (`internal/server/server.go:567-602`), so the frontend can preserve recent Linear raw-body inspection without making the generic wire API source-specific.

Decision:

- Add an in-memory paged wire snapshot that returns lifetime cursor/dispatch state, retained and matching counts, newest-first records, and dynamically calculated source/type/hour counts. Accept optional exact source and type filters. Do not hardcode source or type names in the response or frontend controls.
- Protect `/api/wire` with the existing viewer authenticator because event subjects and attributes can contain issue IDs, repository names, run IDs, URLs, and relative audit paths.
- Generalize the current ledger into a `/wire` workspace. Render source, type, action, subject, sequence, time, channels, and sorted attributes for every record. Populate filter controls from API-provided counts so a future source/type appears without a frontend release.
- Keep normalized detail available for every record. For retained Linear records, conditionally use the existing private payload API to preserve raw-body inspection; for every other source or an aged-out sidecar, show normalized metadata without treating the missing raw body as an error.
- Do not modify event ingestion, dispatch order, acknowledgment, retention, or payload persistence.

### 4. Compatibility, security, data, and rollout risks

- **Silent legacy-route fallback:** without explicit redirects, historical links would show the landing page. Server route tests must assert canonical `Location` values, nested suffixes, and query preservation.
- **Authentication drift:** `/wire` and `/agents/...` expose private operational detail. Page/API auth tests and `safeNext` tests must prove unauthenticated access redirects/challenges and authenticated deep links survive OAuth.
- **Public data leak:** `/api/activity` is intentionally privacy-safe. The new wire API must be private, and existing public leak tests must continue to prove raw payloads and issue identifiers are absent.
- **Unknown source/type rendering:** the frontend must treat source/type/action as opaque strings and derive controls/counts from data. Tests should publish all current sources plus an unfamiliar event type and verify it appears without a source-specific handler.
- **Heartbeat dominance:** 30-second service heartbeats can dominate the 10,000-record window. Exact source/type filters and global counts make the ledger usable without hiding any event class.
- **Pagination under concurrent writes:** each response is computed from one cloned journal snapshot. New writes can shift later pages between polls, which is acceptable for an operational live view; no event order or journal cursor is mutated by reads.
- **Raw-payload asymmetry:** only recent Linear deliveries have sidecars. The UI must present payload absence as expected while retaining normalized event detail.
- **Deployment skew:** keeping old APIs and adding page redirects avoids cached-bundle/API mismatch during the atomic release switch.
- **Settings and lifecycle authority:** `/settings`, trigger policy, repository routing, human merge authority, verified-head gates, deployment source, and completion validation remain unchanged.

No schema migration is required. The wire journal and legacy activity files remain byte-compatible. Rollback is a release rollback, not a data rollback.

### 5. Observable acceptance criteria and verification

1. `/home` renders the public operational overview, and `/activity` redirects to it.
2. `/wire` requires viewer authentication and lists paged records from Linear, GitHub, and Factory with dynamic source/type filters and generic detail. `/activity/linear` redirects to it.
3. `/agents` renders the authenticated run dashboard; `/agents/<issue>/<started>/run` renders the referenced observer; legacy `/agents/<run-id>` remains supported; old `/activity/agents...` paths redirect canonically.
4. `/settings` and `/api/settings` retain their current behavior.
5. `/api/activity` remains public and privacy-safe; `/api/wire` returns `401` without auth.
6. Existing event dispatch, journal replay, activity projection, run observation, and settings suites remain green.
7. Desktop and mobile browser checks cover navigation, source/type filtering, selection, normalized detail, recent Linear raw detail, pagination, keyboard focus, loading/empty/error behavior, console errors, and request failures.

Baseline evidence before implementation:

```text
go test ./internal/eventwire ./internal/server ./internal/viewerauth
ok github.com/tomnagengast/factory/internal/eventwire
ok github.com/tomnagengast/factory/internal/server
ok github.com/tomnagengast/factory/internal/viewerauth
```

The repository requires final `go test ./...`, `go test -race ./...`, `go vet ./...`, and the frozen Bun frontend build before publication (`AGENTS.md`; `nags.toml:15-16`).

### 6. Deployment, health/content verification, and recovery

Observed:

- The documented deployment entrypoint is `~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"`. It refuses a dirty, non-main, divergent, or unexpected source checkout (`README.md:205-217`).
- Factory builds the frozen frontend and Go binary from `nags.toml`, switches an immutable release, restarts `com.nags.factory`, and verifies exact health identity before finalizing the receipt (`nags.toml:15-23`; `README.md:121-130`).
- Local and public `/api/healthz`, `~/.local/share/factory/deployments/current.json`, and the active release must agree on commit, tree, build, deployment, and lifecycle contract (`README.md:219-230`).

Post-merge deployment must run only after the clean primary checkout is fast-forwarded to `origin/main` and the merged exact verified head is proven:

```bash
PREVIOUS_DEPLOYMENT_ID=$(jq -er .deploymentId ~/.local/share/factory/deployments/current.json)
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
```

Then use break-glass authentication without printing the password to verify `/api/wire`, `/wire`, `/agents`, `/settings`, legacy redirects, source/type counts, and a record whose source/type was not special-cased. Recheck local/public health and receipt ancestry after cleanup.

If deployment or identity/content verification fails, preserve the failed receipt and restore the known prior successful release:

```bash
~/.local/bin/nags rollback factory --to "$PREVIOUS_DEPLOYMENT_ID"
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

Do not deploy from this issue worktree and do not manually rewrite the journal or activity stores.

## Alternatives considered

- **Rename the Linear page but keep the legacy activity store:** rejected because Factory-originated wire events are absent and the acceptance criterion cannot be met.
- **Rename every API together with the pages:** rejected for this change because it increases deploy skew and removes useful compatibility without improving the requested UI.
- **Drop old page routes:** rejected because the SPA catch-all would silently render the landing page for historical links.
- **Expose the wire publicly:** rejected because normalized attributes are intentionally private operational metadata.
- **Hardcode cards/colors for current sources and event types:** rejected because it fails the explicit future-stream/type requirement.

## Child research and repository discovery

- A read-only Claude child independently traced the route, data, authentication, compatibility, and verification surfaces and completed successfully. Durable output: `/Users/tom/.local/share/factory/runs/run-e617a803fcacb916/children/ui-route-research-38ca6788/events.jsonl`; process result: `result.json` in the same directory.
- The required `nr` relatedness query and index refresh produced no usable output in this checkout. Exact `rg` searches, full relevant-file reads, Git history/blame, tests, the live health/activity APIs, and the read-only wire journal probe supplied the cited evidence instead.

## Contradictions

- The legacy UI calls its detailed feed “Linear,” while the current architecture documents the event journal as the single ingress sequence for Linear, GitHub, Factory service, lifecycle, and agent-record events. ENG-33 resolves this stale presentation boundary.
- The provider skill text contains an older `bin/network-app` Factory deployment example, but this repository has no such path. The current repository README, installed CLI, successful receipt history, and durable memory all identify `~/.local/bin/nags deploy --expected-commit` as the live entrypoint. The plan must use the current repository-backed command.

## Assumptions

- “All events” means all retained normalized journal records, not unbounded historical events beyond the wire retention contract.
- Existing page URLs should remain usable through redirects, while existing private APIs remain compatible.
- Missing non-Linear raw payloads are expected by design and must not block generic normalized inspection.

## Unresolved questions

None. Repository, runtime, issue, and deployment evidence resolve the implementation-shaping choices without an owner decision.
