# Factory agent runner

Factory turns an explicit Linear label application into a durable Codex SDLC run.

## Trigger

Apply the workspace label:

```text
Factory
```

Factory launches only for a signed `Issue` `update` webhook where the configured Linear actor newly added that label. It compares the current label IDs with `updatedFrom.labelIds`, so unrelated issue updates, label removal, and updates where `Factory` was already present do not start agents.

## Run lifecycle

1. The webhook is signature-checked and replay-window checked.
2. The delivery and a pending run are persisted before the handler returns `200`.
3. The background manager refreshes the internal clone at `~/.local/share/factory/workspace/network`.
4. One isolated tmux session named `factory-<issue-lower>` starts on the `factory-agents` tmux socket.
5. The principal runs `$do TEAM-123` with Codex `gpt-5.6-sol` and high reasoning.
6. A failed Codex process is resumed, when a thread ID is available, up to three total attempts.
7. The session stays active while any child windows remain. Factory records the terminal result only after the tmux session exits.

Run state and output live under `~/.local/share/factory/runs/<run-id>/`. Standard output, diagnostics, final messages, prompts, and process results are separate files with private permissions.

## Deduplication

Redundant work is prevented at three layers:

- Linear delivery IDs make webhook retries idempotent.
- The persistent run store allows only one pending, starting, or running record for an issue.
- The deterministic tmux session name is a final process-level lock.

Additional `Factory` label applications are coalesced while the issue has an active run. After a run becomes terminal, remove and reapply the label to start another run. The `$do` skill then resumes any existing branch, worktree, or pull request instead of duplicating them.

## GitHub event sink

Factory accepts signed repository webhooks at `https://factory.nags.cloud/api/webhooks/github`. It verifies `X-Hub-Signature-256`, deduplicates `X-GitHub-Delivery`, and stores a bounded journal of compact PR, review, check, status, and workflow metadata. Raw webhook payloads and comment bodies are not retained.

During the PR green loop, a Factory agent waits on the local journal instead of polling GitHub:

```bash
"$FACTORY_AGENT_HELPER" agent github-events \
  --repo owner/repository \
  --pr 123 \
  --branch issue-123-branch \
  --after 0 \
  --wait 60s
```

The JSON response contains a monotonic cursor and matching events ordered by Factory receipt sequence. The journal retains the latest 1,000 deliveries globally and deduplicates retained GitHub delivery IDs. Events are wake signals only; the agent always refreshes authoritative PR, review, and check state with `gh` before acting. Register or refresh a repository webhook with `bin/network-app github-hook owner/repository` after `refresh-env` and deployment.

## Child agents

Every principal and child receives these environment variables:

- `FACTORY_TMUX_SOCKET`
- `FACTORY_TMUX_SESSION`
- `FACTORY_RUN_ID`
- `FACTORY_RUN_DIR`
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

The activity and authenticated agent views poll their APIs every two seconds. Each activity run links to `https://factory.nags.cloud/agents/<run-id>`, where every response includes its observation time, current retry attempt, tmux windows, commands, and recent pane output. A live session that cannot be observed is reported as an observer error instead of an empty session. The view never accepts terminal input. Use the attach command shown on the page when interactive local control is required.

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

The public activity API exposes only delivery metadata and opaque run state. Linear issue identifiers, prompts, logs, errors, repository paths, and session names remain private unless the operator authenticates to an `/agents/<run-id>` route.

Factory also starts its tmux server with a restricted environment. Agent processes receive normal shell/GitHub runtime variables and the dedicated Linear API key, but not the webhook signing secret, Cloudflare token, UniFi key, tunnel token, or 1Password service-account token sourced by the parent service.

## Deploy and verify

```bash
bin/network-app refresh-env
bin/network-app deploy factory
bin/network-app github-hook tomnagengast/network
curl -fsS https://factory.nags.cloud/api/healthz
curl -fsS https://factory.nags.cloud/api/activity | jq .agentRuns
```
