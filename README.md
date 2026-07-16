# Factory agent runner

Factory turns normalized system events into durable, workflow-pinned Codex SDLC runs inside trusted repository workspaces. Linear labels and later human feedback remain the compiled defaults, while authenticated operators can add exact event rules and cron-produced events without weakening the protected lifecycle.

## Triggers

Apply the workspace label:

```text
Factory
```

The compiled registry initially launches for a signed `Issue` `update` webhook where the configured Linear actor newly added that label. It compares the current label IDs with `updatedFrom.labelIds`, so unrelated issue updates, label removal, and updates where `Factory` was already present do not start agents. After an issue has retained Factory run history, the protected human `Comment/create` continuation behaves as described below. The compiled generic comment rule is also enabled to preserve the legacy configured comment choice, so one eligible comment may both resume protected lifecycle work and enqueue one independent generic invocation; same-issue serialization prevents the two Runs from executing together.

Authenticated operators manage additional exact rules and cron schedules at `/triggers`. Configured matches are additive: they can create distinct serialized invocations, but they cannot replace or disable contextual feedback, GitHub remediation, post-merge completion, human-only merge authority, exact verified-head deployment, or cleanup gates.

## Project onboarding

Factory also handles signed `Project` `create` and `update` webhooks from the configured Linear actor. A new project description uses this metadata:

```text
GitHub Repo: tomnagengast/example
Local Path: /Users/tom/repos/tomnagengast/example
Cloud URL: https://example.nags.cloud
```

`GitHub Repo` may instead be a canonical `https://github.com/tomnagengast/example` URL. `Cloud URL` is optional. Project creation may arrive before the description is complete, so Factory records it as `awaiting_metadata` and waits for a later project update. Once both required fields are present, Factory persists the routing identity in `~/.local/share/factory/data/project-setups.json` and updates the in-process repository catalog before acknowledging the ordered event. A Factory label applied to the next issue can therefore resolve the new project without a restart or a Factory code change.

The onboarding worker then reconciles the external state idempotently. It creates or verifies a private `tomnagengast` GitHub repository, creates the canonical local primary below `~/repos/tomnagengast`, initializes and publishes `main` when the remote is empty, verifies origin and default-branch identity, and runs `~/.local/bin/nags github-hook owner/repository`. Repository preparation also requires merge commits, disables squash and rebase merges, and enables automatic head-branch deletion. It re-reads authoritative GitHub state after any edit and fails closed unless the complete policy converges. Existing paths must already be clean matching Git checkouts. Files, non-Git directories, public repositories, unexpected branches, mismatched origins, symlink escapes, other GitHub owners, and noncanonical paths fail closed.

Repository and local-path identity become immutable after a Linear project is admitted because provisioning may already have created durable external state and active runs may retain that route. The project name and optional Cloud URL may change. The Cloud URL is validated as one HTTPS `<app>.nags.cloud` hostname, persisted in the setup registry, and passed to issue agents as `FACTORY_CLOUD_URL`.

For any admitted project with a Cloud URL, onboarding also creates one marker-backed issue per desired Cloud URL in the Network Linear project, applies `Factory` and `Yolo`, and directly claims a separately routed `tomnagengast/network` run. Retries reuse the same issue and run delivery, while a later hostname change creates fresh provider work, so webhook timing or a service restart cannot duplicate or suppress the handoff. The provider issue carries the tenant repository, canonical path, hostname, private-by-default access policy, and sequencing contract. It owns the reviewed registry and generated-route PR while the tenant implementation owns the root `nags.toml`; neither agent receives cross-repository write authority. A Cloud URL still cannot safely invent the tenant health or process contract, and the provider PR remains subject to the normal exact-head checkpoint and human merge. Existing Cloud setups without the new coordination marker are requeued once after upgrade to backfill this handoff; the store schema remains rollback-compatible.

## Run lifecycle

1. The webhook is signature-checked and replay-window checked.
2. The delivery is persisted, the Linear project's GitHub Repo and Local Path are resolved against the current repository catalog, and a pending run is claimed before the handler returns `200`.
3. The background manager prepares that repository's isolated internal clone. Existing checkouts must match the configured managed root, GitHub origin, base branch, GitHub default branch, merge-commit-only policy, and automatic branch deletion. A catalog entry may explicitly allow greenfield bootstrap: Factory creates the private GitHub repository when absent, clones through a staging directory, initializes and publishes the configured base branch when the remote is empty, and verifies the final identity before starting an agent. GitHub policy drift is reconciled once and verified by a second authoritative read; non-convergence fails preparation. Symlink escapes, files or non-Git directories at the target, mismatched origins, and unexpected branches fail closed. Registered linked worktrees are excluded from the clone's dirty check; all other local changes and divergence fail preparation instead of being overwritten. Before starting a pending run with no other agent active, Factory removes clean, unlocked worktrees whose branches are already integrated into that repository's fetched upstream as a backstop for interrupted cleanup.
4. One isolated tmux session named `factory-<issue-lower>` starts on the `factory-agents` tmux socket.
5. Factory starts the principal with the complete Markdown body and identity of the workflow revision pinned on the Run. The compiled `full-sdlc-provider-neutral` workflow drives gated research, dual-provider plan review, implementation, and the complete ready-for-human-merge predicate through Factory-owned task helpers. Retained older Linear pins continue through their original `full-sdlc` workflow. Comment continuations fresh-read the provider conversation and treat only unaddressed human feedback as new scope.
6. A failed Codex process is resumed, when a thread ID is available, up to three total attempts.
7. After the PR is ready, the principal records the exact locally verified head through `agent checkpoint ready-for-merge` and exits with `FACTORY_RESULT: READY_FOR_HUMAN_MERGE`. Factory validates the contract-v1 checkpoint, parks the run as `awaiting_human_merge`, and closes the tmux segment. This is nonterminal and does not consume an LLM while waiting.
8. GitHub webhooks wake the parked run immediately. A supervisor sweep also refreshes authoritative GitHub state at least once per minute with a persisted cursor, bounded backoff, and restart-safe schedule. An `OPEN` PR stays parked, a closed-unmerged PR becomes a typed blocker, and only a fresh `MERGED` snapshot with a merge commit starts a new `post-merge` continuation.
9. The fresh continuation reconstructs the repository, PR, base, head branch, and verified head from durable evidence. It revalidates that the human merged the exact locally verified head and that final checks and feedback still pass, then fast-forwards the clean primary checkout, deploys every applicable changed surface from merged main, verifies health and GitHub's automatic remote-branch deletion, and removes the clean integrated local worktree/branch with Worktrunk.
10. A terminal intent is accepted only after Factory mechanically validates the authoritative PR, source checkout, Linear completion, child results, and branch/worktree cleanup. Repositories with a deployment contract also require a successful deployment receipt and exact health identity; repository-only projects do not fabricate those requirements. Missing or transient evidence reparks the run; contradictory evidence records the typed rejection instead of falsely declaring success.

Run state and output live under `~/.local/share/factory/runs/<run-id>/`. Standard output, diagnostics, final messages, prompts, and process results are separate files with private permissions.

## Deduplication

Redundant work is prevented at three layers:

- Linear delivery IDs make webhook retries idempotent.
- The persistent run store allows only one nonterminal record for an issue, including parked merge waits and post-merge continuations.
- The deterministic tmux session name is a final process-level lock.

Additional `Factory` label applications and eligible human comments are coalesced while the issue has an active run. After a run becomes terminal, either remove and reapply the label or add a human comment to start another run. Active work retains its original workflow pin; after an earlier PR is integrated, a comment continuation pins the currently protected feedback workflow and starts a deterministic focused follow-up branch instead of rewriting completed work.

## Operator views

Factory separates public health from authenticated operational detail:

- `/` is the public Factory landing page.
- `/home` is the public, privacy-safe summary of verified deliveries and agent-run totals.
- `/wire` is the authenticated system-event workspace with source and type filters, retained-window charts, 25-event pages, normalized journal records, and available Linear raw-payload inspection.
- `/agents` is the authenticated run dashboard with issue context, lifecycle phase, ready-checkpoint PR and verified head, authoritative refresh timing, resume counts, deployment receipt identity, and terminal rejection evidence.
- `/agents/<issue-id>/<started-unix-ms>/run` is the authenticated, read-only loop observer for one started run.
- `/tasks` is the authenticated source-neutral task workspace. Factory tasks are native and mutable; retained managed Linear tasks are read-only and load their private detail live from Linear.
- `/workflows` is the authenticated Markdown workflow authoring and publication workspace.
- `/triggers` is the authenticated generic event-rule, cron schedule, protected-route binding, and recent-routing workspace.
- `/settings` is the authenticated agent-provider and manager-capacity editor.

Validated Linear request bodies are retained prospectively as private `0600` sidecar files beside the bounded activity index. Sidecars age out with their projection records. Historical wire records from before payload retention remain listable without a body, and GitHub request bodies are never retained. Pending runs without a start timestamp appear as non-link rows until the canonical observer reference exists. Deprecated activity URLs, direct run-ID URLs, unknown paths, malformed paths, and trailing-slash variants return `404` without redirects or compatibility aliases.

## Native tasks and provider authority

Factory and Linear remain separate task authorities. Factory owns `FAC-N` tasks, their descriptions, immutable discussion, links, gates, project selection, and lifecycle state. Linear owns `TEAM-N` issues. The `/tasks` index includes only Linear issues already retained by a Factory Run or admitted Invocation, never the whole Linear workspace. Opening one of those records fetches its current description, state, project, and comments from Linear without copying them into native task storage. A Linear outage therefore leaves native tasks readable but makes live Linear detail and Linear-backed workflow operations temporarily unavailable.

Native task creation and start are dark by default. A project appears as an eligible choice only after Linear project onboarding has reached `succeeded` and its repository metadata resolves through the allowlisted catalog. Activation is per project and stored separately from the task journal. The authenticated control API returns both choices and the optimistic control revision:

```bash
curl -fsS -b "$FACTORY_COOKIE_JAR" \
  https://factory.nags.cloud/api/task-projects | jq .

curl -fsS -b "$FACTORY_COOKIE_JAR" \
  -H 'Origin: https://factory.nags.cloud' \
  -H 'Content-Type: application/json' \
  -X PUT \
  --data '{"expectedRevision":0,"enabled":true}' \
  https://factory.nags.cloud/api/task-projects/project-id
```

Use the `control.revision` returned by the read endpoint, substitute the exact admitted project ID, and preserve the same authenticated browser session. A stale revision returns `409` with the authoritative snapshot. Disable the project with the same request and `enabled:false`. Disabling prevents new create/start operations; it does not rewrite native records or terminate an admitted Run.

Startup reconciles the exact compiled `full-sdlc-provider-neutral` workflow. A conflicting customized definition stops startup instead of being overwritten. Native start rechecks the enabled project, succeeded project setup, repository catalog, and exact workflow digest before it snapshots the route and admits work. The committed default enables no project. After ENG-46 deployment, rollout remains limited to the admitted Factory project and one separately scoped native canary. The canary must independently cross its research and plan gates and complete its own checkpointed pull request, human merge, deployment, and cleanup. Network-provider workflow work and broader project enablement remain separate follow-ups.

Provider-neutral Runs receive a private capability bound to the exact Run, TaskRef, and repository. They do not receive `LINEAR_API_KEY`. Retained Runs pinned to older workflows keep the legacy Linear access needed to finish safely. Inside an exact provider-neutral Run, use the scoped helper rather than selecting a task ID:

```bash
"$FACTORY_AGENT_HELPER" agent task show
"$FACTORY_AGENT_HELPER" agent task messages --after 0
"$FACTORY_AGENT_HELPER" agent task activity --after 0 --revision 0 --wait 60s
"$FACTORY_AGENT_HELPER" agent task comment --body 'Implementation evidence is ready'
"$FACTORY_AGENT_HELPER" agent task reply --parent message-id --body 'Addressed'
"$FACTORY_AGENT_HELPER" agent task link --label 'Pull request' --url 'https://github.com/owner/repository/pull/1'
"$FACTORY_AGENT_HELPER" agent task gate open --kind plan --mode gated --artifact-url 'https://example.invalid/plan'
```

Mutating helper operations derive a stable idempotency key when one is not supplied. The service rejects unknown, terminal, cross-repository, or capability-mismatched Runs. Native human messages and gate decisions can continue an in-progress Run; agent and system messages cannot. Native completion is not an editable state transition. Factory records `completed` only after the existing pull-request, review, verified-head, deployment, cleanup, child-window, task-gate, and unanswered-human-feedback evidence all passes. The only direct terminal task action is cancellation.

### Native task storage and recovery

All task state is private under `~/.local/share/factory/data`:

- `native-tasks.jsonl` is the append journal and compacted checkpoint. It contains the FAC sequence, tasks, messages, links, gates, idempotency outcomes, pinned routing, and completion evidence.
- `task-operations/` contains private staged commands until their body-free `system-events.jsonl` records are applied and acknowledged.
- `native-task-control.json` contains the per-project dark-launch control.
- `task-source-neutral.json` is the monotonic compatibility marker written and fsynced before the first source-neutral Run, Invocation, staged task, or task-journal write.

Directories are `0700`; files are `0600` and use fsync plus append or atomic replacement. Do not edit, truncate, combine, or selectively restore these files. The task journal may recover only an incomplete final JSONL record. A malformed complete record, invalid sequence, revision conflict, duplicate identity, or incompatible checkpoint fails startup closed and must be preserved for diagnosis.

For a consistent backup, quiesce Factory and copy the complete data directory, not only `native-tasks.jsonl`. This keeps the task journal, pending wire records, staged command bodies, Run/Invocation ownership, sequence state, control, and compatibility marker at one point in time:

```bash
factory stop
backup="$HOME/.local/share/factory/backups/tasks-$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 700 "$backup"
cp -a "$HOME/.local/share/factory/data" "$backup/data"
test "$(stat -f '%Lp' "$backup/data")" = 700
find "$backup/data" -type f ! -perm 600 -print
```

The final `find` must print nothing. Keep Factory stopped until the copy and permission checks finish. For restore, preserve the failed live directory, prepare a complete sibling directory from one backup, verify permissions, then replace the data directory using directory renames before starting Factory:

```bash
factory stop
state="$HOME/.local/share/factory"
restore="$state/.data-restore.$(date -u +%Y%m%dT%H%M%SZ)"
cp -a "/absolute/path/to/backup/data" "$restore"
test "$(stat -f '%Lp' "$restore")" = 700
test -z "$(find "$restore" -type f ! -perm 600 -print -quit)"
mv "$state/data" "$state/data.pre-restore.$(date -u +%Y%m%dT%H%M%SZ)"
mv "$restore" "$state/data"
factory start
factory doctor --json
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
```

Each rename is atomic. If recovery is interrupted between them, keep the service stopped and select one complete directory rather than merging files. After restart, require a drained wire, healthy task store, expected FAC sequence, and correct authenticated `/tasks` detail before admitting new work.

The compatibility marker is not a feature flag. Once `task-source-neutral.json` exists, a task-unaware release is not a valid rollback target even if native projects are disabled or no FAC task was created. Never delete or edit the marker to force rollback. Restore a complete backup with the marker and deploy a TaskRef-aware release, or forward-fix from clean merged `main`.

## System event wire

Every accepted Linear delivery, GitHub delivery, Factory service lifecycle change, agent-run state transition, and complete principal or child JSONL record enters `~/.local/share/factory/data/system-events.jsonl`. This private `0600` append journal is the single ingress and dispatch sequence. It retains the newest 10,000 acknowledged records plus any records still awaiting dispatch, preserves a lifetime monotonic cursor, and replays unacknowledged effects after a restart or before the next publication.

Normalized records contain only routing and audit metadata. Linear bodies remain in protected activity sidecars, GitHub bodies are not retained, and agent message bodies remain in private run JSONL files. Agent audit events contain only the run ID, run-relative file, byte offset and length, SHA-256 digest, and an allowlisted record type. `agent-event-offsets.json` checkpoints completed lines so partial tails wait for completion and restarts do not duplicate audit records.

Use the source-agnostic helper inside a Factory run to read or wait on the global cursor:

```bash
"$FACTORY_AGENT_HELPER" agent events \
  --source factory \
  --type agent-run \
  --subject TEAM-123 \
  --match runId="$FACTORY_RUN_ID" \
  --after 0 \
  --wait 60s
```

Filters may use any validated lowercase source token with `--source`, plus `--type`, `--action`, `--subject`, and repeated `--match key=value`. Factory emits `service/started`, `service/heartbeat` every 30 seconds, `service/stopping` during graceful shutdown, each agent-run state, and one compact `agent-record` event per complete JSONL record. Normalized attributes include bounded producer and provenance values. Linear records may also carry actor, canonical issue, and newly added label identity; GitHub records carry bounded repository and pull-request context. A permanent routing or policy failure is durably rejected and acknowledged so one bad delivery cannot block later records. Transient provider, transport, and projection failures remain pending for ordered retry.

`/api/healthz` is the synchronous current-state probe. Its `wire` object reports total, dispatched, pending, rejected-total, and the last rejected event identity without exposing the private rejection reason. Its `projectSetups` object reports awaiting-metadata, pending, running, succeeded, and failed counts without exposing project names, repository paths, or provider errors. A pending wire record or failed project setup returns `503` with `status: "degraded"`; a drained wire returns `200` when project setup has no failures, even when historical rejected records or incomplete project drafts exist. Manager launch, service-start publication, and heartbeats wait for startup catch-up to drain, while the HTTP listener remains available to expose degraded recovery state.

## Generic trigger registry

Every event admitted to the normalized wire is evaluated against every enabled configured rule before protected per-record dispatch. Rule filters are exact over optional source, type, action, nullable subject, and attribute membership. An omitted field is a wildcard; a JSON `null` or omitted subject is a wildcard, an empty subject matches subject absence, and a nonempty subject matches exactly. Attribute entries use AND semantics, while each value is membership-tested against the event's bounded value list. All matching rules fire in stable rule-ID order.

A rule selects one enabled workflow and one Linear issue target policy: the event subject, exactly one named event attribute, or a preflighted fixed issue. Each match pins the complete validated workflow, resolved issue, rule revision, settings digest, and causal identity before promotion. Later workflow or rule edits do not change admitted work. Invocation-owned agent events inherit root event, parent invocation, parent Run, hop, and ancestor rule IDs; only a stable rule already present in that ancestor path is cycle-suppressed. Independent siblings may match the same rule.

Registry limits are 32 rules and 32 schedules. A rule defaults to a four-hop ceiling, 10 outstanding invocations, and 120 admissions per rolling hour; configured maxima are 8 hops, 100 outstanding, and 10,000 hourly admissions. Global nonterminal invocations are capped at 100. Limit, cycle, unavailable-workflow, and invalid-target decisions are durable visible outcomes rather than hidden drops. Each issue promotes its oldest invocation first, while different issues may use normal global concurrency.

Schedules use standard five-field cron plus a separate IANA timezone. They contain optional event subject and bounded context only, never a workflow selector. A due schedule emits a deterministic `factory / cron / due` event, and ordinary rules decide what workflow and target it reaches. On restart Factory publishes only the oldest missed occurrence, records how many later occurrences were skipped, and advances the cursor only after successful wire publication. A new, re-enabled, or materially edited schedule starts strictly after its edit time.

The authenticated `GET /api/triggers` returns the editable registry, enabled published workflow summaries, observed source suggestions, schedule and rule status, recent admitted/rejected/suppressed outcomes, compatibility evidence, and protected routes. `PUT /api/triggers` requires same-origin JSON, strict bounded decoding, the current registry and policy revisions, whole-snapshot validation, and fixed-target repository preflight. The dedicated `PUT /api/triggers/protected/linear-feedback` repoints the always-enabled feedback route to another enabled published workflow without changing the generic registry. Conflicts return `409` with the authoritative snapshot, and failed writes do not partially mutate policy.

Private state lives in `~/.local/share/factory/data/triggers.json`, `trigger-routing.jsonl`, and `trigger-cursors.json`, all written with `0600` permissions, fsync, and atomic checkpoint or append semantics. An invocation Run receives a contained `workflow.json` snapshot with `0600` permissions. Routing retains one decision per retained wire event, nonterminal invocations even after wire eviction, terminal audit summaries until safe pruning, and rolling UTC rate buckets independently of wire retention. Recovery truncates only an incomplete final routing-log line; complete corruption fails startup closed.

Startup opens and validates every store, registers generic admission before protected handlers, reconciles invocation and Run pairs, drains ordered wire catch-up, reconciles again, and only then enables mutating APIs, cron, promotion, and Run launch. Until that readiness boundary, health remains available but work does not advance.

The legacy label/comment fields remain round-tripped in `settings.json` for compatibility but are no longer edited on `/settings`. Schema-2 migration writes one private `settings.schema1.backup.json`, preserves every legacy workflow and trigger reference, and seeds the protected feedback binding from the former comment workflow. Before the first new-shape workflow admission or fresh feedback continuation, rollback also requires quiescence, zero pending wire records, no nonterminal legacy-shape work, a false `workflowRollbackIncompatible` marker, and a successful read-only `factory workflow-rollback-preflight` against the exact schema-1 backup and retained trigger registry. A schema-2-only registry reference fails preflight even before admission. Once the marker is set, prior-binary activation is unavailable; recovery requires a schema-2-aware release or a forward corrective commit deployed from clean merged `main`.

## Workflow authoring and execution

Published workflows are versioned Markdown documents inside the private schema-2 policy checkpoint. `/workflows` separates authoring from execution: edits autosave to `~/.local/share/factory/data/workflow-drafts.json`, but drafts never enter trigger choices, take the coordinated policy lock, or change an admitted Run. Create starts a disabled server-ID draft. Discard removes only authoring state, and Publish promotes the exact acknowledged draft revision after checking the draft, base workflow, and policy revisions. Published disable or delete is rejected while a live generic rule or protected binding requires the workflow.

The authenticated API mirrors that model:

- `GET /api/workflows` returns published definitions, saved or synthesized editor drafts, draft status, and safe live references.
- `POST /api/workflow-drafts` creates and persists a disabled starter draft.
- `PUT` or `DELETE /api/workflow-drafts/{id}` autosaves or discards one exact draft revision.
- `POST /api/workflow-drafts/{id}/publish` publishes the exact saved draft against its base workflow and policy revisions.
- `DELETE /api/workflows/{id}` deletes an unreferenced published workflow against exact workflow and policy revisions.

Every fresh admission snapshots the selected published ID, revision, complete Markdown body, digest, rule revision where applicable, and policy revision before promotion. The launcher validates and writes that pin as private `workflow.json`, then places the Markdown verbatim in the principal prompt. Retries, active-run feedback, GitHub remediation, and post-merge segments keep the same pin. Repointing the protected feedback binding affects only later fresh post-terminal continuations. The generic `linear-comment` rule remains independent and may create an additional serialized invocation for the same human event.

The compiled `full-sdlc-provider-neutral` document is self-contained and calls `factory agent task` for the exact capability-scoped TaskRef. The retained `full-sdlc` document continues to call `factory agent linear-graphql` for older Linear pins. New Factory Runs do not depend on a provider-installed skill or repository-relative workflow assets. Narrow legacy decoding and the old prompt path remain only for already admitted records during the compatibility window.

## GitHub event sink

Factory accepts signed repository webhooks at `https://factory.nags.cloud/api/webhooks/github`. It verifies `X-Hub-Signature-256`, deduplicates `X-GitHub-Delivery`, and appends compact PR, review, check, status, and workflow metadata to the unified system wire. Raw GitHub webhook payloads and comment bodies are not retained.

During the PR green and human-merge loop, a Factory agent waits through the deprecated compatibility adapter instead of polling GitHub:

```bash
"$FACTORY_AGENT_HELPER" agent github-events \
  --repo owner/repository \
  --pr 123 \
  --branch issue-123-branch \
  --after 0 \
  --wait 60s
```

The JSON response and GitHub-specific cursor domain remain unchanged, but the adapter now reads the unified wire. Factory still maintains the old `github-events.json` only as an exact-sequence post-wire rollback projection, never as ingress or dispatch authority. Events are wake signals only; the agent always refreshes authoritative PR, review, check, and merge state with `gh` before acting. While the PR is open, the agent remediates or keeps waiting; Yolo never authorizes a merge. Register or refresh a repository webhook with `~/.local/bin/nags github-hook owner/repository` after `refresh-env` and deployment.

## Human merge and deployment

At the ready checkpoint, the pinned lifecycle workflow repeats the complete checks, mergeability, review, comment, thread, and Linear feedback snapshot and durably records the locally verified `headRefOid`. The human performs the merge with **Create a merge commit**; the principal never calls a merge mutation or enables auto-merge. Squash and rebase merges rewrite the verified head out of ancestry and therefore block deployment. Factory repository preparation keeps those incompatible merge methods disabled. The LLM exits after writing the checkpoint while Factory owns the durable parked wait. A close/merge webhook only wakes the supervisor, and the periodic authoritative sweep closes the missed-webhook gap. Before deployment, a fresh continuation must prove GitHub reports `MERGED` with a merge commit, the merged head equals the recorded verified head, and the final checks and feedback snapshot still passes. A closed-unmerged, changed, or regressed head becomes a precise blocker. Retries resume at the first incomplete post-merge boundary only after reconstructing and corroborating the verified-head record.

After merge, the principal resolves the primary checkout with Worktrunk, refuses to overwrite unrelated changes, fetches/prunes origin, and fast-forwards the default branch. Deployment commands and post-deploy probes come from the issue's approved plan and run from that updated primary checkout, never from the feature worktree. A failed deployment is reported as a post-merge recovery blocker; cleanup does not hide it. The manager's next-run cleanup can still remove a clean integrated worktree as a backstop, so retained worktrees are not guaranteed to persist indefinitely.

Factory self-deployment builds an immutable release under `~/.local/share/factory/releases/<deployment-id>`, writes a pending receipt, switches the `current` symlink atomically, restarts `com.nags.factory`, and requires local and public health to report the exact expected commit, tree, build, deployment, and lifecycle-contract identity. Only then does it finalize the success receipt. A failed verification restores the prior release and records the failed attempt. The issue tmux session runs under the separate `factory-agents` tmux server and survives the service restart.

Only after deployment verification does the principal prove GitHub auto-deleted the remote head ref, fetch/prune the remote-tracking ref, wait for every child window, and use foreground Worktrunk removal without force flags. Success means merged, deployed, healthy, updated main, and no local or remote issue branch/worktree.

## Linear comment wake

Factory also records eligible signed `Comment/create` deliveries as body-free events on the unified wire. A running issue agent waits for top-level comments and replies through the deprecated compatibility adapter:

```bash
"$FACTORY_AGENT_HELPER" agent linear-comments \
  --issue TEAM-123 \
  --after 0 \
  --wait 60s
```

The adapter's response and Linear-comment cursor domain remain unchanged. Factory maintains `linear-comments.json` only as an exact-sequence post-wire rollback projection. Events contain comment and issue identifiers, reply parent ID, private Linear URL, and receipt time, but never the comment body. The agent refreshes the authoritative conversation through Linear GraphQL after every wake or timeout.

Only comments created by the configured trigger actor enter this journal. Factory-authored comments are excluded by the reserved final non-empty line: either `🐘` alone or `🐘` followed by one inline-code `codex-do:<ISSUE>:<slug>` marker. No prose may follow the footer, and emoji or marker prose elsewhere does not establish provenance. This exact convention is required because Factory and the human can share the same Linear identity.

For an issue with retained Factory run history, a human comment coalesces into a pending, starting, or running agent and wakes its journal listener. If the latest run is terminal, the same delivery creates exactly one fresh `linear-comment` continuation run. The new principal reads the authoritative Linear thread, addresses unhandled feedback, and may open a focused follow-up PR when prior work is already merged. Comments on issues without retained Factory history never start runs; applying the `Factory` label remains the initial opt-in. Run history is bounded to the latest 100 records globally, so pruned issues lose comment-continuation eligibility. Factory-authored comments never start or wake runs.

## Child agents

Every principal and child receives these environment variables:

- `FACTORY_TMUX_SOCKET`
- `FACTORY_TMUX_SESSION`
- `FACTORY_RUN_ID`
- `FACTORY_RUN_DIR`
- `FACTORY_TRIGGER_KIND`
- `FACTORY_REPO_PATH`
- `FACTORY_CLOUD_URL`, empty when the Linear project has no Cloud URL.
- `FACTORY_AGENT_HELPER`

Launch a bounded Codex or Claude child as another window in the same issue session:

```bash
"$FACTORY_AGENT_HELPER" agent spawn --provider claude --name plan-critic <<'PROMPT'
Review the plan for blocking correctness or security problems. Do not modify files.
PROMPT
```

The helper returns JSON containing the tmux window ID and durable output paths. Codex children use `gpt-5.6-sol` with high reasoning. Claude children use `fable` with high effort and a reduced headless tool configuration. Children inherit the helper, so they can launch their own bounded windows. For every adversarial review round, the principal starts one Claude child and one Codex child with the same prompt before waiting for either. Both usable reviews form one conservative round: either provider's P0/P1 finding requires revision, while one terminal provider failure is retained as evidence and tolerated when the other review is usable. If neither provider returns a usable review after safe retries, the principal stops before implementation with `authority_unavailable`. Factory terminal completion requires every launched child to write a finished result, but a finished nonzero child exit does not by itself make the run incomplete.

## Inspecting runs

Factory uses a dedicated tmux socket so personal sessions are not mixed with agent processes:

```bash
tmux -L factory-agents list-sessions
tmux -L factory-agents list-windows -t factory-team-123
tmux -L factory-agents attach -t factory-team-123
tmux -L factory-agents capture-pane -pt factory-team-123:principal
```

Detach with the configured tmux prefix followed by `d`. Kill only a specific session or window when intervention is necessary. Never use `tmux kill-server`, because it terminates every Factory issue run.

The home and active agent views poll their APIs every two seconds. Each started run links to `https://factory.nags.cloud/agents/<issue-id>/<started-unix-ms>/run`; pending runs remain non-link rows. Every observer response includes its observation time, current retry attempt, tmux windows, commands, and recent pane output. Agent events appear as collapsed steps; expanding one reveals its redacted raw JSON payload. When tmux exits, the observer reconstructs the complete principal-attempt and child-agent histories from their retained JSONL event files without the live pane limit. Terminal views stop polling after loading this immutable history. Plain terminal output remains available when a pane does not contain structured events. A live session that cannot be observed is reported as an observer error instead of an empty session. The view never accepts terminal input. Use the attach command shown on the page when interactive local control is required.

Managed browser navigation and any non-loopback standalone bind use Google OAuth over HTTPS. Factory accepts only verified Google identities in `FACTORY_GOOGLE_ALLOWED_EMAILS`, keeps the OAuth tokens server-side for the duration of the callback, and issues a signed, secure, host-only session cookie for 24 hours. Visit `/auth/logout` to clear the Factory session.

An unmanaged loopback `factory start` uses a separate local-machine authorization policy. It accepts protected requests only when the request Host is the exact configured loopback authority and any Origin matches that same HTTP origin. Alternate loopback names, DNS-rebinding authorities, cross-site fetches, and mismatched ports fail closed. This policy is never selected by `factory serve` or a non-loopback bind. `~/.local/bin/nags refresh-env` supplies the managed OAuth configuration. Agent pane output is redacted against credentials available to the agent before it is returned by the API.

## Management CLI and standalone localhost

Build the frontend and binary from the repository root:

```bash
export MISE_BUN_VERSION=1.3.11
bun install --cwd frontend --frozen-lockfile
bun run --cwd frontend build
go build -o factory .
```

An unmanaged checkout needs the four application variables `LINEAR_WEBHOOK_SECRET`, `GITHUB_WEBHOOK_SECRET`, `LINEAR_API_KEY`, and `LINEAR_TRIGGER_ACTOR_ID`. It does not need the private `nags` provider, launchd artifacts, deployment receipts, Caddy, DNS, or Google OAuth for the default loopback bind. Start stays attached to the terminal and streams normal service output:

```bash
./factory --help
./factory --version
./factory start
```

From another terminal, inspect or stop the exact recorded process:

```bash
./factory status
./factory status --json
./factory doctor
./factory doctor --json
./factory stop
```

The standalone default is `127.0.0.1:${PORT:-8092}`. `start`, `status`, `stop`, and `doctor` accept `--host` and `--port`. Start flags override `PORT` and the default; later commands use explicit flags, then the private `0600` runtime record, then `PORT` and the default. The foreground server writes `~/.local/share/factory/local-runtime.json` only after acquiring its listener. `stop` signals the recorded PID only after the process, address, start time, and complete health/build identity agree, and the owning process removes only its exact record during shutdown.

A non-loopback `start` never inherits local authorization. It requires the four managed Google variables, `FACTORY_SESSION_KEY`, and an explicit HTTPS `FACTORY_GOOGLE_REDIRECT_URL`. Factory still serves HTTP itself, so the chosen host also needs operator-provided HTTPS termination for browser OAuth; the CLI does not configure public routing or TLS:

```bash
FACTORY_GOOGLE_REDIRECT_URL=https://factory.example/auth/google/callback \
  ./factory start --host 0.0.0.0 --port 8092
```

If any managed marker exists at the fixed plist, wrapper, active release, or receipt path, lifecycle commands classify the installation as managed and never fall back to local trust. Managed `start` validates those artifacts and the successful receipt, then bootstraps or kickstarts only `gui/<uid>/com.nags.factory` and waits for receipt-matched health. Managed `stop` boots out only that job. Address overrides are rejected for managed start/stop, and neither command deploys, refreshes secrets, deletes state, or touches the separate `factory-agents` tmux server.

## Configuration

The launchd wrapper sources `~/.config/network-app/env`. Factory requires:

- `LINEAR_WEBHOOK_SECRET` for webhook authentication.
- `LINEAR_API_KEY` for the Factory service's Linear adapter and retained legacy workflow pins. Exact provider-neutral Runs receive only their scoped task capability.
- `LINEAR_TRIGGER_ACTOR_ID` for the only Linear user allowed to start runs.
- `GITHUB_WEBHOOK_SECRET` for GitHub repository webhook authentication.
- `FACTORY_GOOGLE_CLIENT_ID` and `FACTORY_GOOGLE_CLIENT_SECRET` for Google sign-in.
- `FACTORY_GOOGLE_ALLOWED_EMAILS`, a comma-separated allowlist of verified Google emails.
- `FACTORY_SESSION_KEY` for signed browser sessions.

`~/.local/bin/nags refresh-env` reads the API key from `op://Code/Linear API key/credential`, validates it against Linear, derives the trigger actor ID from the authenticated viewer, and writes both values to the private launchd environment. Factory owns its compiled default Markdown workflow, helper commands, prompt envelope, and mechanical terminal validators, so tenant repositories do not carry lifecycle policy.

Optional variables:

- `FACTORY_MAX_AGENTS`, default `3`; seeds the runtime default only until settings are first persisted.
- `FACTORY_REPO_URL`, default `git@github.com:tomnagengast/network.git`.
- `FACTORY_REPO_PATH`, default `~/.local/share/factory/workspace/network`.
- `FACTORY_TMUX_SOCKET`, default `factory-agents`.

### Runtime settings

Authenticated operators edit provider launch policy and manager capacity at `https://factory.nags.cloud/settings`, and author workflows separately at `https://factory.nags.cloud/workflows`. The private `GET /api/settings` and `PUT /api/settings` endpoints expose only the narrow agent/runtime document for automation. Writes require the current policy revision, use strict validation and same-origin browser checks, and return `409 Conflict` with the latest snapshot when another writer has already advanced the revision.

Factory stores the first successful update at `~/.local/share/factory/data/settings.json` as a versioned `0600` file using fsync and atomic replacement. Until that file exists, compiled defaults preserve the current `Factory` label, protected comment continuations, self-contained `full-sdlc` Markdown workflow, provider models, high effort, three principal attempts, and three concurrent runs. A present invalid or unknown settings schema stops startup instead of silently resetting policy.

The settings surface controls principal, Codex child, and Claude child models and effort, plus principal retry count and manager concurrency. The workflow surface controls up to eight published or never-published workflow IDs, each with a bounded Markdown body and independent draft and published revisions.

Settings, workflow publication, protected-binding changes, and registry writes share one policy coordinator. Draft autosave does not. A settings update cannot overwrite the workflow collection, and publication cannot disable or remove a workflow required by an enabled rule or protected binding. Policy takes effect at safe boundaries; each admitted invocation and each running provider segment keeps its pinned snapshot. Existing provider processes are never restarted by a settings or workflow save. Repository routing, paths, secrets, actor identity, executable commands, arbitrary provider flags, human merge authority, exact verified-head checks, deployment source, and completion validation remain locked in code.

If a saved value is valid but undesirable, publish a corrected workflow or restore the launch setting through its owning page or API with the current revision. If a manually edited file prevents startup, preserve the invalid file for diagnosis. Do not replace a corrupt draft store with an empty file or restore the schema-1 backup without satisfying the rollback compatibility preflight. Use a schema-2-aware known-good release or forward repair, then restart and verify both health endpoints before allowing new runs.

The compiled catalog routes `tomnagengast/network`, `tomnagengast/notebook`, `tomnagengast/factory`, and `tomnagengast/artifacts`, all on `main`. Network, Notebook, and Factory require deployment receipts and health identity at completion. Artifacts is an explicit private greenfield-bootstrap, repository-only entry rooted at `~/repos/tomnagengast`; it requires merged-main and cleanup evidence without a deployment receipt. Validated project onboarding records extend this catalog at runtime and survive restart. Linear project GitHub Repo and Local Path metadata must exactly match either a compiled entry or an admitted onboarding record. Unknown, mismatched, or unregistered repositories are durably rejected before a run is claimed. Every run persists its repository, path, managed root, base branch, bootstrap policy, and optional Cloud URL through launch and completion.

The public health API exposes only service and aggregate wire state. `/api/home` returns privacy-safe delivery metadata and opaque run state, while `/api/wire`, `/api/agents`, canonical run observers, and settings remain authenticated. Linear issue identifiers, raw request bodies, prompts, logs, errors, repository paths, and session names remain private.

Factory also starts its tmux server with a restricted environment. Agent processes receive normal shell/GitHub runtime variables. Exact provider-neutral Runs receive the scoped task capability instead of the Linear API key; retained older pins receive the key only for compatibility. No agent receives the webhook signing secret, Cloudflare token, UniFi key, tunnel token, or 1Password service-account token sourced by the parent service.

## Deploy and verify

```bash
~/.local/bin/nags refresh-env
bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
~/.local/bin/nags github-hook tomnagengast/network
~/.local/bin/nags github-hook tomnagengast/notebook
~/.local/bin/nags github-hook tomnagengast/factory
~/.local/bin/nags github-hook tomnagengast/artifacts
curl -fsS https://factory.nags.cloud/api/healthz
curl -fsS https://factory.nags.cloud/api/home | jq .agentRuns
```

Normal deployment is intentionally refused unless the checkout is clean, on `main`, tracking the official `origin`, and exactly equal to `origin/main` and `--expected-commit`. A detached release checkout may be used only with `--allow-detached` and only when its expected commit is contained in `origin/main`. Deploy and rollback share a fail-closed per-app lock so concurrent post-merge continuations cannot interleave release or receipt state.

Repository merge-policy recovery is also fail closed. Verify authoritative state with:

```bash
gh repo view owner/repository --json mergeCommitAllowed,squashMergeAllowed,rebaseMergeAllowed,deleteBranchOnMerge
```

The required values are `true`, `false`, `false`, and `true`, respectively. If reconciliation cannot prove them, fix GitHub authentication or repository administration before retrying preparation. Do not enable squash or rebase to route around the exact-head gate.

## Recovery runbook

Inspect the exact running identity and receipts first:

```bash
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
find ~/.local/share/factory/deployments -maxdepth 2 -type f -name '*.json' -print
```

The local health response, public health response, `current.json`, and `current` release symlink must agree on commit, tree, build ID, deployment ID, and contract version. A mismatch means the deployment is not verified even when the process is listening.

If health returns `503`, inspect the wire before restarting or deploying again:

```bash
curl -sS http://127.0.0.1:8092/api/healthz | jq '.status, .wire, .projectSetups'
tail -n 20 ~/.local/share/factory/data/system-events.jsonl
jq . ~/.local/share/factory/data/project-setups.json
jq . ~/.local/share/factory/data/triggers.json
tail -n 20 ~/.local/share/factory/data/trigger-routing.jsonl
jq . ~/.local/share/factory/data/trigger-cursors.json
test "$(stat -f '%Lp' ~/.local/share/factory/data/settings.json)" = 600
test "$(stat -f '%Lp' ~/.local/share/factory/data/settings.schema1.backup.json)" = 600
test ! -e ~/.local/share/factory/data/workflow-drafts.json || \
  test "$(stat -f '%Lp' ~/.local/share/factory/data/workflow-drafts.json)" = 600
launchctl print "gui/$(id -u)/com.nags.factory"
tmux -L factory-agents list-sessions
```

Pending wire records indicate a retryable dependency or projection failure and keep manager work gated until ordered catch-up succeeds. A failed project setup retains its bounded error and next-attempt time in the private setup store and retries with capped backoff. A growing rejection count indicates a permanent routing or policy failure that was isolated so later records could continue. Do not delete, truncate, or rewrite `system-events.jsonl`, `trigger-routing.jsonl`, `trigger-cursors.json`, or `project-setups.json`; correct the policy, project metadata, credentials, local-path conflict, or dependency and allow normal reconciliation to resume. Factory issue tmux sessions use a separate server and should remain intact across service recovery.

If schema-2 startup fails before any new-shape admission, preserve the failed checkpoint and stop Factory before considering a prior release. Prove the service is quiescent, the wire has no pending record, no nonterminal Run or invocation uses a legacy workflow, and `workflowRollbackIncompatible` is false. Then run the read-only compatibility check:

```bash
factory workflow-rollback-preflight \
  --settings-backup "$HOME/.local/share/factory/data/settings.schema1.backup.json" \
  --trigger-registry "$HOME/.local/share/factory/data/triggers.json"
```

Only a successful preflight permits restoring that exact schema-1 backup and activating a prior verified release. A failure, a true compatibility marker, or any durable new-shape admission requires a schema-2-aware release or forward repair. Never rewrite the retained registry, routing journal, Run records, or backup to make preflight pass. A corrupt `workflow-drafts.json` disables authoring but does not invalidate the last published policy; preserve it and repair draft authoring separately.

For a failed release, inspect the failed receipt under `~/.local/share/factory/deployments/failed/` and confirm the previous release recovered. To select a known successful release explicitly:

```bash
bin/network-app rollback factory --to <deployment-id>
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
```

If a run is parked at `awaiting_human_merge`, confirm its ready checkpoint under `~/.local/share/factory/runs/<run-id>/ready-for-merge.json`, then restart the Factory service if necessary. The manager reloads persisted schedules and its next authoritative sweep resumes the parked run without replaying the implementation segment.

Never repair deployment drift by stashing, resetting, or deploying a dirty or diverged checkout. Make the primary checkout clean and fast-forwardable, or use a clean detached release checkout whose expected commit is already contained in `origin/main`.

If deployment reports an existing `~/.local/share/factory/.deployment-lock`, read its `owner` PID and confirm whether that process is still running. Never remove a live lock. After proving the owner is gone, remove only that stale lock directory and retry the same commit-pinned command.
