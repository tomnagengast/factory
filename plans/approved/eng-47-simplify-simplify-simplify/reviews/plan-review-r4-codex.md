# Plan review round 4: Codex

The reviewer used the identical read-only prompt with SHA-256 `f9d281a127e20c0604b1f5365b30e75114d316638cdc974c188f209549967abc`, traced the plan through the event, routing, Run, task, policy, server, deployment, and frontend contracts, and ran the focused existing suites.

No P0 findings.

## P1 findings

1. The generation manifest records canonical artifact hashes as though they remain current, but Run and task journals legitimately change. The recovery split between before and after canonical writes also lacks a durable boundary. Make generation identity and initial hashes immutable audit evidence, validate mutable stores by replay/schema/poison rules, and persist a monotonic `canonicalWritesStarted` boundary before the first mutation.
2. Phase 1 deletes `workflow-rollback-preflight` even though checked-in `bin/network-app` requires it for every rollback. Replace it in the same phase with a generation-aware preflight that validates the manifest, write boundary, backup identity, quiescence, and selected release compatibility.
3. Requiring every public expected revision to equal one global policy revision changes observable `409` behavior across currently independent settings, registry, workflow, and task-control domains. Keep one durable policy owner and internal generation, but preserve current independent public revision domains.
4. Deleting Invocation leaves derived wire causation and terminal-event ordering undefined. Retain one immutable admission/causation ID in a Run or version new wire causation, and add a Run-transition outbox that publishes terminal events only after terminal Run/admission state is durable.
5. The task outbox does not distinguish operations never published from published-but-undispatched operations. Define durable unpublished, published, applied/result, and acknowledged states keyed by idempotency scope/hash, plus a recovery owner that republishes or cancels only with proof.
6. Joining every child process contradicts self-deployment, which requires durable `factory-agents` tmux sessions to survive service restart. Limit supervision to in-process loops and ephemeral subprocesses; reconcile durable Run-owned tmux sessions after restart and test the active old helper/pin path.
7. Phase 0 cannot activate canonical artifacts whose readers and writers are only implemented in Phases 1 through 3. Keep Phase 0 to characterization, decoding, audit, fixtures, and non-activating dry runs; integrate activation only after all canonical owners exist.

## P2/P3 findings

- P2: clarify minimal health/diagnostics before migration with all advancing work readiness-gated.
- P3: keep package and line targets as reported outcomes rather than phase exit gates.

VERDICT: REVISE
