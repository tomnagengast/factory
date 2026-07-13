# ENG-33 Research: Show All Events on the Wire

Linear: https://linear.app/nags-cloud/issue/ENG-33/show-all-events-on-the-wire

## Revision history

- Revision 1 proposed canonical page names while retaining redirects, compatibility APIs, and the one-segment agent-run route.
- Tom rejected that compatibility direction at the research gate. The required direction is a confident hard cut where only the canonical paths exist.
- Revision 2 removes every legacy HTTP and frontend compatibility surface while preserving internal replay and payload-storage invariants that are not URL fallbacks.

## Research questions

1. What current behavior prevents the UI from showing every retained event on the unified wire?
2. Which server, frontend, authentication, persistence, documentation, and test surfaces participate in the requested route changes?
3. What generic wire representation can display existing and future sources, event types, actions, subjects, channels, and attributes without source-specific frontend branches?
4. Which old paths and presentation methods must be removed for a true hard cut?
5. Which internal activity-store responsibilities remain runtime invariants rather than compatibility behavior?
6. How can each acceptance criterion and material risk be observed before publication and after deployment?
7. What exact merged-main deployment, health/content verification, and recovery procedure applies?

## Evidence-backed answers

### 1. Current behavior and root cause

Observed:

- The public overview is served by the SPA at `/activity` and reads `/api/activity`. That API is backed by `activity.Store.Snapshot`, which contains only a lifetime delivery count, a bounded Linear/GitHub delivery projection, and opaque agent-run totals (`internal/server/server.go:186-192`, `internal/server/server.go:303-315`).
- The authenticated `/activity/linear` page calls `/api/activity/linear` and `/api/activity/linear/{id}`. Those handlers call `activity.Store.LinearPage` and `activity.Store.LinearEvent` (`internal/server/server.go:267-270`, `internal/server/server.go:317-352`, `frontend/src/index.tsx:232-269`).
- `activity.Store` retains at most 250 entries in production (`main.go:29-34`, `main.go:123-126`). Its Linear page filters records by a `github:` delivery-ID prefix (`internal/activity/store.go:192-253`). Factory service, lifecycle, and agent-record events never enter this projection.
- The actual unified wire is `eventwire.Journal`, opened at `~/.local/share/factory/data/system-events.jsonl` with a 10,000-record acknowledged retention target (`main.go:33-34`, `main.go:139-149`). Each record has a monotonic sequence and a generic event containing `id`, `source`, `type`, `action`, optional `subject`, attributes, channels, and receive time (`internal/eventwire/journal.go:18-27`, `internal/eventwire/event.go:26-35`).
- `Journal.Snapshot` already returns a cloned in-memory snapshot without rereading the journal file, but it is not paged and `Wire` does not expose it (`internal/eventwire/journal.go:250-254`, `internal/eventwire/wire.go:16-20`). `eventwire.Read` reparses the file and is intended for CLI/wait adapters, so it is not the correct polling-page primitive (`internal/eventwire/journal.go:434-469`).
- A read-only live probe on 2026-07-13 found the running Factory service on port 8092 at merged commit `606360a947c5d830712fcef32abe47b77b52ae4a`. The wire contained Linear, GitHub, and Factory sources plus dynamic event types including `service`, `agent-record`, `agent-run`, `pull_request`, `Issue`, `Comment`, and `Attachment`. Factory heartbeats were the largest class, proving that the UI needs generic source/type grouping and filters rather than a renamed Linear-only list.

Decision:

- `/wire` must read `eventwire.Journal`, not a generalized `activity.Store`. Only the journal can satisfy “all events on the wire,” including Factory-originated events.

### 2. Hard-cut route, API, link, and authentication surface

Observed:

- Server page protection is currently registered for `/activity/linear`, `/activity/agents`, `/agents`, and `/settings`; `/activity` reaches the public SPA fallback (`internal/server/server.go:264-289`).
- `frontend` serves `index.html` for every unknown non-API path, and the frontend returns `HomePage` for every unmatched pathname (`internal/server/server.go:810-839`, `frontend/src/index.tsx:1741-1769`). Removing matchers alone would therefore leave old and misspelled paths returning a misleading `200` home page.
- Frontend routing recognizes `/activity`, `/activity/linear`, `/activity/agents`, `/settings`, `/activity/agents/<issue>/<started>/run`, and `/agents/<run-id>`. Navigation, destination cards, settings links, agent backlinks, API clients, and `agentRunHref` contain the old paths (`frontend/src/index.tsx:231-270`, `frontend/src/index.tsx:354-401`, `frontend/src/index.tsx:475-487`, `frontend/src/index.tsx:693-816`, `frontend/src/index.tsx:1188-1191`, `frontend/src/index.tsx:1568-1587`).
- OAuth return destinations are fail-closed, but `safeNext` and logout fall back to `/activity`; `protectedPagePath` broadly allows `/agents/` and the old authenticated activity routes (`internal/viewerauth/auth.go:244-248`, `internal/viewerauth/auth.go:404-427`).
- `/settings` has independent authenticated page/API registration. Only its navigation links need canonical destinations; its behavior and APIs do not change (`internal/server/server.go:272-273`, `internal/server/server.go:286-287`).

Decision:

| Remove entirely | Only canonical replacement |
|---|---|
| `/activity` | `/home` |
| `/activity/linear` | `/wire` |
| `/activity/agents` | `/agents` |
| `/activity/agents/<issue>/<started>/run` | `/agents/<issue>/<started>/run` |
| `/agents/<run-id>` | none |
| `/api/activity` | `/api/home` |
| `/api/activity/linear` | `/api/wire` |
| `/api/activity/linear/<hash>` | `/api/wire/<sequence>` |
| `/api/activity/agents` | `/api/agents` |
| `/api/activity/agents/<issue>/<started>/run` | `/api/agents/<issue>/<started>/run` |
| `/api/agents/<run-id>` | none |

- Do not add redirects, aliases, cached-bundle compatibility handlers, or legacy frontend matchers. Removed paths and their trailing-slash variants return `404`.
- Serve the SPA only for explicit canonical application routes. The final static handler serves real files or returns `404`, so unknown paths do not silently become Home.
- Keep `/` as the existing public landing page. Add `/home` as the public operational overview. Protect `/wire`, `/agents`, `/agents/<issue>/<started>/run`, and `/settings`.
- Make logout and all invalid `safeNext` values fall back to `/home`. Allow only exact protected canonical pages and the structured issue/start observer path. A legacy `next` value receives no special treatment and falls back to `/home`.
- Pending runs without `startedAt` render as non-link rows. The removed run-ID URL has no replacement because the canonical issue/start identity does not yet exist.

### 3. Generic wire API and UI behavior

Observed:

- The backend wire representation is source-agnostic above ingestion: source is a string-backed type and type/action/subject/attributes/channels are data, not sealed event-specific structs (`internal/eventwire/event.go:11-35`). Ingestion currently validates three source constants, while type and action values are open strings (`internal/eventwire/event.go:37-77`).
- The wire stores normalized routing and audit metadata, not raw message bodies. Linear raw bodies remain in private sidecars, GitHub bodies are never retained, and agent bodies remain in run JSONL files (`README.md:51-55`).
- Every normalized Linear wire event contains a delivery ID attribute (`internal/server/server.go:567-602`), so a generic wire detail can resolve its optional private sidecar without retaining a Linear-named HTTP route.

Decision:

- Add an in-memory paged wire snapshot that returns lifetime cursor/dispatch state, retained and matching counts, newest-first records, and dynamically calculated source/type/hour counts. Accept optional exact source and type filters. Do not hardcode source or type names in the response or frontend controls.
- Protect `/api/wire` and `/api/wire/<sequence>` with the existing viewer authenticator because subjects and attributes can contain issue IDs, repository names, run IDs, URLs, and relative audit paths.
- Generalize the current ledger into a `/wire` workspace. Render source, type, action, subject, sequence, time, channels, and sorted attributes for every record. Populate filters from API-provided counts so an unfamiliar future type appears without a frontend release.
- Resolve detail by retained wire sequence. Return normalized detail for every source. For a Linear record, use its `deliveryId` attribute to read the existing private sidecar and return an optional raw payload. Missing or aged-out payloads are expected, not errors.
- Do not modify event ingestion, dispatch order, acknowledgment, journal retention, or payload file format.

### 4. Internal activity store boundary

Observed:

- Linear webhook handling stages a raw payload in `activity.Store` before publishing to the wire (`internal/server/server.go:493-499`).
- `dispatchLinear` calls `AddStaged` before downstream comment/run effects, while `dispatchGitHub` also records its compact projection before reconciliation scheduling (`internal/server/server.go:604-700`).
- A projection failure prevents wire acknowledgment. Startup and later publications retry pending records through wire catch-up (`main.go:318-329`). `TestWireReplayRecoversProjectionWithoutDuplicatingRun` proves this recovery contract (`internal/server/server_test.go:544-624`).
- The store owns private sidecar permissions, atomic state replacement, pruning, and payload deletion (`internal/activity/store.go:119-182`, `internal/activity/store.go:318-376`).

Decision:

- Do not delete `activity.Store` in ENG-33. It remains an active internal delivery/payload projection inside the replay transaction, not a compatibility route.
- Retain `StagePayload`, `AddStaged`, `Add`, the privacy-safe `Snapshot` used by `/api/home`, sidecar persistence, and replay behavior.
- Remove `LinearPage` and its presentation-only pagination/count helpers. Replace `LinearEvent(hash)` with an internal payload lookup by delivery ID or equivalent canonical wire-detail adapter.
- Preserve the existing store file and sidecar formats. No data migration is required.

### 5. Risks and mitigations

- **Replay safeguard regression:** acknowledging a Linear record before its staged payload projection is durable would weaken recovery. Keep projection-before-ack behavior and the replay regression test.
- **Silent old-path fallback:** a generic SPA catch-all would make removed URLs appear valid. Explicit canonical SPA routing plus a real static/404 fallback must prove old, malformed, and unknown paths return `404`.
- **Authentication drift:** broad `/agents/` acceptance can authorize malformed destinations. Test only the exact canonical dashboard, settings, wire, and issue/start observer shapes.
- **Public data leak:** `/api/home` remains privacy-safe; `/api/wire` and detail remain private. Leak tests must cover raw payloads, issue identifiers, repository context, and audit attributes.
- **Unknown type rendering:** source/type/action are opaque strings and controls derive from returned counts. Publish all current sources plus an unfamiliar event type in tests.
- **Heartbeat dominance:** exact source/type filters and global counts make the ledger usable without hiding any retained class.
- **Pagination under concurrent writes:** each response uses one cloned journal snapshot. Later pages can shift between polls as new events arrive, which is acceptable for a live operational view.
- **Cached old bundle:** tabs with the old JavaScript may call removed APIs and fail until refreshed. That is an accepted consequence of the requested hard cut; no compatibility endpoint is added.
- **Pending observation:** pending runs have no canonical issue/start identity and therefore no observer link until they start.
- **Settings and lifecycle authority:** `/settings`, trigger policy, repository routing, human merge authority, verified-head gates, deployment source, and completion validation remain unchanged.

### 6. Observable acceptance criteria and verification

1. `/home` renders the public operational overview. `/activity` and all other removed page paths return `404` without redirecting or rendering Home.
2. `/wire` requires viewer authentication and lists paged retained records from Linear, GitHub, and Factory with dynamic source/type filters and generic detail. `/api/activity*` paths return `404`.
3. `/agents` renders the authenticated run dashboard, and `/agents/<issue>/<started>/run` renders the referenced observer. `/agents/<run-id>` and `/activity/agents*` return `404`. Pending runs are visible but not linked.
4. `/settings` and `/api/settings` retain their current behavior.
5. `/api/home` remains public and privacy-safe. `/api/wire`, `/api/wire/<sequence>`, and `/api/agents*` return `401` without auth.
6. Recent Linear wire detail can include its private raw body; historical or non-Linear detail remains useful with `payloadAvailable: false`.
7. Existing event dispatch, journal replay, activity projection, run observation, and settings suites remain green.
8. Desktop and mobile browser checks cover navigation, filtering, selection, normalized/raw detail, pagination, pending-run rendering, keyboard focus, loading/empty/error states, unknown-route handling, console errors, and failed requests.

Baseline evidence before implementation:

```text
go test ./internal/eventwire ./internal/server ./internal/viewerauth
ok github.com/tomnagengast/factory/internal/eventwire
ok github.com/tomnagengast/factory/internal/server
ok github.com/tomnagengast/factory/internal/viewerauth
```

The repository requires final `go test ./...`, `go test -race ./...`, `go vet ./...`, and the frozen Bun frontend build before publication (`AGENTS.md`; `nags.toml:15-16`).

### 7. Deployment, health/content verification, and recovery

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

Then use break-glass authentication without printing the password to verify `/api/home`, `/api/wire`, `/api/wire/<sequence>`, `/wire`, `/agents`, `/settings`, old-path `404` responses, source/type counts, and a record whose type was not special-cased. Recheck local/public health and receipt ancestry after cleanup.

If deployment or identity/content verification fails, preserve the failed receipt and restore the known prior successful release:

```bash
~/.local/bin/nags rollback factory --to "$PREVIOUS_DEPLOYMENT_ID"
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

Do not deploy from this issue worktree and do not manually rewrite the journal, activity store, or payload sidecars.

## Alternatives considered

- **Keep redirects or compatibility APIs:** rejected by the owner. The canonical route must be the only route, and cached old clients may fail until refreshed.
- **Rename the Linear page but keep its legacy activity data source:** rejected because Factory-originated wire events are absent.
- **Delete `activity.Store` wholesale:** rejected because payload staging and delivery projection are currently part of wire acknowledgment and replay safety. Presentation methods can be removed without weakening that invariant.
- **Leave unknown paths on the SPA catch-all:** rejected because removed and misspelled URLs would falsely render Home with HTTP `200`.
- **Expose the wire publicly:** rejected because normalized attributes are intentionally private operational metadata.
- **Hardcode cards or colors for current sources and event types:** rejected because it fails the explicit future-stream/type requirement.

## Child research and repository discovery

- A read-only Claude child independently traced the initial route, data, authentication, compatibility, and verification surfaces and completed successfully. Durable output: `/Users/tom/.local/share/factory/runs/run-e617a803fcacb916/children/ui-route-research-38ca6788/events.jsonl`; process result: `result.json` in the same directory.
- After the revision request, a bounded Claude hard-cut child completed its evidence sweep but stopped producing output before a terminal report. Only its tmux window was stopped. Its partial durable output remains at `/Users/tom/.local/share/factory/runs/run-e617a803fcacb916/children/hard-cut-research-798ddf45/events.jsonl`.
- A Codex child reran the exact hard-cut research prompt, completed successfully, and independently confirmed the route/API removal matrix, `404` behavior, internal store boundary, raw-payload path, and focused verification. Durable output: `/Users/tom/.local/share/factory/runs/run-e617a803fcacb916/children/hard-cut-research-codex-f8f7510c/events.jsonl`; process result: `result.json` in the same directory.
- The required `nr` relatedness query and index refresh produced no usable output in the initial checkout. Exact `rg` searches, full relevant-file reads, Git history/blame, tests, the live health/activity APIs, and the read-only wire journal probe supplied the cited evidence instead.

## Contradictions

- The legacy UI calls its detailed feed “Linear,” while the current architecture documents the event journal as the single ingress sequence for Linear, GitHub, Factory service, lifecycle, and agent-record events. ENG-33 resolves this stale presentation boundary.
- The initial research treated deployment skew as a reason to retain URL compatibility. The owner explicitly chose a hard cut instead, accepting that cached old bundles may fail until refreshed.
- The provider skill text contains an older `bin/network-app` Factory deployment example, but this repository has no such path. The current repository README, installed CLI, successful receipt history, and durable memory identify `~/.local/bin/nags deploy --expected-commit` as the live entrypoint.

## Assumptions

- “All events” means all retained normalized journal records, not unbounded history beyond the wire retention contract.
- “Hard cut” means no redirects, aliases, old route matchers, or compatibility APIs. Removed and unknown paths return `404`.
- Pending runs remain visible but do not link to an observer until `startedAt` supplies the canonical identity.
- Missing non-Linear or aged-out Linear raw payloads are expected and do not block generic normalized inspection.

## Unresolved questions

None. The owner feedback and repository evidence resolve the implementation-shaping choices.
