# Factory agent runner

Factory turns an explicit Linear label application or later human feedback on a previously managed issue into a durable Codex SDLC run.

## Trigger

Apply the workspace label:

```text
Factory
```

Factory initially launches only for a signed `Issue` `update` webhook where the configured Linear actor newly added that label. It compares the current label IDs with `updatedFrom.labelIds`, so unrelated issue updates, label removal, and updates where `Factory` was already present do not start agents. After an issue has retained Factory run history, an eligible human `Comment/create` can start a focused continuation as described below.

## Run lifecycle

1. The webhook is signature-checked and replay-window checked.
2. The delivery and a pending run are persisted before the handler returns `200`.
3. The background manager fetches and fast-forwards the internal clone at `~/.local/share/factory/workspace/network` to its configured upstream. Registered linked worktrees are excluded from the main checkout's dirty check; all other local changes and divergence still fail preparation instead of being overwritten. Before starting a new pending run with no other agent active, Factory removes clean, unlocked worktrees whose branches are already integrated into the fetched upstream as a backstop for interrupted cleanup.
4. One isolated tmux session named `factory-<issue-lower>` starts on the `factory-agents` tmux socket.
5. The principal runs `$do TEAM-123` with Codex `gpt-5.6-sol` and high reasoning through gated research, implementation, GitHub approval, merge, deployment from updated main, and cleanup. Comment continuations fresh-read the Linear thread and treat only unaddressed human feedback as new scope.
6. A failed Codex process is resumed, when a thread ID is available, up to three total attempts.
7. After the PR is green, the principal remains active until a distinct authorized GitHub reviewer approves the current head. Review webhooks wake the loop, but only a fresh authoritative GitHub snapshot can authorize merge.
8. The principal merges the exact validated head, fast-forwards the clean primary checkout, deploys every applicable changed surface from merged main, verifies health and GitHub's automatic remote-branch deletion, then removes the clean integrated local worktree/branch with Worktrunk.
9. The session stays active while any child windows remain. Factory records the terminal result only after the tmux session exits, so post-merge deployment or cleanup failures remain visible as incomplete runs.

Run state and output live under `~/.local/share/factory/runs/<run-id>/`. Standard output, diagnostics, final messages, prompts, and process results are separate files with private permissions.

## Deduplication

Redundant work is prevented at three layers:

- Linear delivery IDs make webhook retries idempotent.
- The persistent run store allows only one pending, starting, or running record for an issue.
- The deterministic tmux session name is a final process-level lock.

Additional `Factory` label applications and eligible human comments are coalesced while the issue has an active run. After a run becomes terminal, either remove and reapply the label or add a human comment to start another run. The `$do` skill resumes active work when it exists; after an earlier PR is integrated, a comment continuation starts a deterministic focused follow-up branch instead of rewriting completed work.

## Activity views

Factory separates public health from authenticated operational detail:

- `/activity` is a public, privacy-safe summary of verified deliveries and agent-run totals.
- `/activity/linear` is an authenticated Linear delivery workspace with retained-window charts, 25-event pages, a scrollable ledger, and raw-payload inspection.
- `/activity/agents` is an authenticated run dashboard with issue context and state totals.
- `/activity/agents/<issue-id>/<started-unix-ms>/run` is the authenticated, read-only loop observer for one started run.

Validated Linear request bodies are retained prospectively as private `0600` sidecar files beside the bounded activity index. Sidecars age out with their metadata records. Historical records from before payload retention remain listable without a body, and GitHub request bodies are never retained. `/agents/<run-id>` remains available for existing links and for pending runs that do not have a start timestamp yet.

## GitHub event sink

Factory accepts signed repository webhooks at `https://factory.nags.cloud/api/webhooks/github`. It verifies `X-Hub-Signature-256`, deduplicates `X-GitHub-Delivery`, and stores a bounded journal of compact PR, review, check, status, and workflow metadata. Raw GitHub webhook payloads and comment bodies are not retained.

During the PR green and approval loop, a Factory agent waits on the local journal instead of polling GitHub:

```bash
"$FACTORY_AGENT_HELPER" agent github-events \
  --repo owner/repository \
  --pr 123 \
  --branch issue-123-branch \
  --after 0 \
  --wait 60s
```

The JSON response contains a monotonic cursor and matching events ordered by Factory receipt sequence. The journal retains the latest 1,000 deliveries globally and deduplicates retained GitHub delivery IDs. Events are wake signals only; the agent always refreshes authoritative PR, review, and check state with `gh` before acting. An empty `reviewDecision` keeps waiting, and Yolo never bypasses GitHub approval. Register or refresh a repository webhook with `bin/network-app github-hook owner/repository` after `refresh-env` and deployment.

## Approved merge and deployment

Immediately before merge, `$do` repeats the complete checks, mergeability, review, comment, thread, and Linear feedback snapshot and binds the merge to the observed `headRefOid`. It never uses admin bypass or auto-merge. Retries detect an already merged PR and resume at the first incomplete post-merge boundary.

After merge, the principal resolves the primary checkout with Worktrunk, refuses to overwrite unrelated changes, fetches/prunes origin, and fast-forwards the default branch. Deployment commands and post-deploy probes come from the issue's approved plan and run from that updated primary checkout, never from the feature worktree. A failed deployment is reported as a post-merge recovery blocker; cleanup does not hide it. The manager's next-run cleanup can still remove a clean integrated worktree as a backstop, so retained worktrees are not guaranteed to persist indefinitely.

Factory self-deployment restarts `com.nags.factory`, but the issue tmux session runs under the separate `factory-agents` tmux server and continues. The loop verifies service health after restart and performs a timed authoritative `gh` refresh even if the brief restart missed a webhook delivery.

Only after deployment verification does the principal prove GitHub auto-deleted the remote head ref, fetch/prune the remote-tracking ref, wait for every child window, and use foreground Worktrunk removal without force flags. Success means merged, deployed, healthy, updated main, and no local or remote issue branch/worktree.

## Linear comment wake

Factory also records eligible signed `Comment/create` deliveries in a body-free wake journal. A running issue agent waits for top-level comments and replies with:

```bash
"$FACTORY_AGENT_HELPER" agent linear-comments \
  --issue TEAM-123 \
  --after 0 \
  --wait 60s
```

The journal retains the latest 500 eligible deliveries, deduplicates Linear delivery IDs, and returns matching events in Factory receipt order with a monotonic cursor. Events contain comment and issue identifiers, reply parent ID, private Linear URL, and receipt time, but never the comment body. The agent refreshes the authoritative conversation through Linear GraphQL after every wake or timeout.

Only comments created by the configured trigger actor enter this journal. Factory-authored comments are excluded by the reserved final-line `🐘` signature or `🐘` plus `codex-do` marker, because Factory and the human can share the same Linear identity.

For an issue with retained Factory run history, a human comment coalesces into a pending, starting, or running agent and wakes its journal listener. If the latest run is terminal, the same delivery creates exactly one fresh `linear-comment` continuation run. The new principal reads the authoritative Linear thread, addresses unhandled feedback, and may open a focused follow-up PR when prior work is already merged. Comments on issues without retained Factory history never start runs; applying the `Factory` label remains the initial opt-in. Run history is bounded to the latest 100 records globally, so pruned issues lose comment-continuation eligibility. Factory-authored comments never start or wake runs.

## Child agents

Every principal and child receives these environment variables:

- `FACTORY_TMUX_SOCKET`
- `FACTORY_TMUX_SESSION`
- `FACTORY_RUN_ID`
- `FACTORY_RUN_DIR`
- `FACTORY_TRIGGER_KIND`
- `FACTORY_REPO_PATH`
- `FACTORY_AGENT_HELPER`

Launch a bounded Codex or Claude child as another window in the same issue session:

```bash
"$FACTORY_AGENT_HELPER" agent spawn --provider claude --name plan-critic <<'PROMPT'
Review the plan for blocking correctness or security problems. Do not modify files.
PROMPT
```

The helper returns JSON containing the tmux window ID and durable output paths. Codex children use `gpt-5.6-sol` with high reasoning. Claude children use `fable` with high effort and a reduced headless tool configuration. Children inherit the helper, so they can launch their own bounded windows.

## Inspecting runs

Factory uses a dedicated tmux socket so personal sessions are not mixed with agent processes:

```bash
tmux -L factory-agents list-sessions
tmux -L factory-agents list-windows -t factory-team-123
tmux -L factory-agents attach -t factory-team-123
tmux -L factory-agents capture-pane -pt factory-team-123:principal
```

Detach with the configured tmux prefix followed by `d`. Kill only a specific session or window when intervention is necessary. Never use `tmux kill-server`, because it terminates every Factory issue run.

The activity and active agent views poll their APIs every two seconds. Each started activity run links to `https://factory.nags.cloud/activity/agents/<issue-id>/<started-unix-ms>/run`; pending runs and existing bookmarks continue to use `/agents/<run-id>`. Every observer response includes its observation time, current retry attempt, tmux windows, commands, and recent pane output. Agent events appear as collapsed steps; expanding one reveals its redacted raw JSON payload. When tmux exits, the observer reconstructs the complete principal-attempt and child-agent histories from their retained JSONL event files without the live pane limit. Terminal views stop polling after loading this immutable history. Plain terminal output remains available when a pane does not contain structured events. A live session that cannot be observed is reported as an observer error instead of an empty session. The view never accepts terminal input. Use the attach command shown on the page when interactive local control is required.

Browser navigation uses Google OAuth over HTTPS. Factory accepts only verified Google identities in `FACTORY_GOOGLE_ALLOWED_EMAILS`, keeps the OAuth tokens server-side for the duration of the callback, and issues a signed, secure, host-only session cookie for 24 hours. Visit `/auth/logout` to clear the Factory session.

HTTP Basic authentication remains available as break-glass access for the protected page and API:

- Username: `factory`
- Password: `~/.config/network-app/factory-viewer-password.txt`

`bin/network-app refresh-env` reads the Factory-specific OAuth client from `op://Code/GCP the-nags/factory oauth credentials`, preserves or creates the session signing key and 48-character break-glass password, and writes them to the private service environment. It maintains the password in a separate `0600` file for operator use. Agent pane output is redacted against credentials available to the agent before it is returned by the API.

## Configuration

The launchd wrapper sources `~/.config/network-app/env`. Factory requires:

- `LINEAR_WEBHOOK_SECRET` for webhook authentication.
- `LINEAR_API_KEY` for the principal and child agents' Linear access.
- `LINEAR_TRIGGER_ACTOR_ID` for the only Linear user allowed to start runs.
- `GITHUB_WEBHOOK_SECRET` for GitHub repository webhook authentication.
- `FACTORY_GOOGLE_CLIENT_ID` and `FACTORY_GOOGLE_CLIENT_SECRET` for Google sign-in.
- `FACTORY_GOOGLE_ALLOWED_EMAILS`, a comma-separated allowlist of verified Google emails.
- `FACTORY_SESSION_KEY` for signed browser sessions.
- `FACTORY_VIEWER_PASSWORD` for break-glass agent inspection.

`bin/network-app refresh-env` reads the API key from `op://Code/Linear API key/credential`, validates it against Linear, derives the trigger actor ID from the authenticated viewer, and writes both values to the private launchd environment. Codex uses `.agents/skills/do/scripts/linear_graphql.py` to call Linear's GraphQL API directly, so the headless workflow does not depend on MCP tool discovery.

Optional variables:

- `FACTORY_MAX_AGENTS`, default `3`.
- `FACTORY_REPO_URL`, default `git@github.com:tomnagengast/network.git`.
- `FACTORY_REPO_PATH`, default `~/.local/share/factory/workspace/network`.
- `FACTORY_TMUX_SOCKET`, default `factory-agents`.

The public activity API exposes only delivery metadata and opaque run state. Linear issue identifiers, raw request bodies, prompts, logs, errors, repository paths, and session names remain private unless the operator authenticates to the dedicated Linear or agent activity routes.

Factory also starts its tmux server with a restricted environment. Agent processes receive normal shell/GitHub runtime variables and the dedicated Linear API key, but not the webhook signing secret, Cloudflare token, UniFi key, tunnel token, or 1Password service-account token sourced by the parent service.

## Deploy and verify

```bash
bin/network-app refresh-env
bin/network-app deploy factory
bin/network-app github-hook tomnagengast/network
curl -fsS https://factory.nags.cloud/api/healthz
curl -fsS https://factory.nags.cloud/api/activity | jq .agentRuns
```
