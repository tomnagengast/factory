# ENG-46 plan review round 1: Codex

## P0

None.

## P1 blockers

### P1-1: Legacy Linear identity migration is not deterministic

Plan lines 27-28 and 60 make provider ID the canonical ownership key, while the migration permits retained provider evidence or an unspecified compatibility representation. Existing Run and Invocation records contain only `IssueIdentifier`, not the Linear UUID (`internal/agentrun/store.go:79-123`, `internal/triggerrouter/model.go:46-66`), while new Linear webhook events contain both UUID and identifier (`internal/linearhook/event.go:11-18`). Different migration and ingress identities could admit duplicate active Runs.

Smallest correction: define one canonical Linear provider-ID algorithm for migration and all future ingress, including unavailable or ambiguous UUID evidence.

### P1-2: Provider-neutral ownership still collides at tmux

The plan retained display identifiers for tmux sessions while requiring same-text TaskRefs to remain isolated. The manager derives the session solely from the issue identifier (`internal/agentrun/manager.go:476-483`, `internal/agentrun/launcher.go:578-595,702-703`). Two provider refs with equal display text can pass ownership checks but collide at launch.

Smallest correction: namespace internal session and workspace identities by canonical source/provider identity or Run ID while keeping display identifiers only for human-facing labels and compatibility URLs.

### P1-3: Removing `LINEAR_API_KEY` breaks retained pinned Runs

Existing pinned Full SDLC revisions call `factory agent linear-graphql` (`internal/workflow/defaults/full-sdlc.md:13,68`), and that helper requires the environment key (`agent_commands.go:193-197`) currently injected by the launcher (`internal/agentrun/launcher.go:641-650`). Retained live/parked Runs could resume with their old workflow and lose Linear access.

Smallest correction: retain key injection for nonterminal Runs pinned to legacy workflows until termination, or provide a compatible scoped implementation. Cover running, parked, and post-merge pins.

### P1-4: The provider-neutral workflow has no safe live publication path

Embedded defaults only apply when `settings.json` is absent (`internal/settings/store.go:48-52`); an existing checkpoint remains authoritative (`internal/settings/store.go:83-95`). Live publication requires expected policy/workflow revisions (`internal/triggerrouter/coordinator.go:148-184`). A deployed installation could retain only the Linear-specific workflow.

Smallest correction: specify an idempotent publication/operator rollout that preserves customized workflows, records the exact new revision/digest, and makes native admission require it.

### P1-5: The rollback marker is later than the actual incompatibility boundary

Run and Invocation version bumps can be persisted by ordinary Linear activity before native activation. Current stores strictly reject unknown versions and rewrite/checkpoint during normal transitions (`internal/agentrun/store.go:19,233-235,795-818`; `internal/triggerrouter/store.go:242-279`). The prior binary can therefore become unreadable while the proposed native marker is absent.

Smallest correction: keep pre-activation records readable by the prior binary or write the monotonic marker before the first incompatible Run/router record, then align preflight, recovery, and tests.

## P2 observations

- The API list has no explicit link mutation or message-page interface even though the plan requires link management and bounded message pagination.
- Phase 2 names start/continuation dispatcher actions before Phase 3 admission/wake behavior exists.
- The impacted-file map should include `internal/triggerregistry`, where persisted target policy and schema live.

## P3

None.

VERDICT: REVISE

BLOCKERS: P1-1, P1-2, P1-3, P1-4, P1-5
