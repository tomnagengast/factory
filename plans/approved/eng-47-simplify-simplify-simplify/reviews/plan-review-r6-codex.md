# Plan review round 6: Codex

The reviewer received the identical round-6 read-only prompt, read the complete artifact and review set, and traced the corrections through the live active Run, tmux launcher, manager reconciliation, workflow instructions, rollback wrapper, and deployment provider.

No P0 findings. The state-transition lease was judged coherent and sufficient to close the round-5 time-of-check/time-of-use race.

## P1 finding

An activation-spanning tmux session escapes the proposed restore proof. A retained session such as active ENG-47 can mutate GitHub or Linear directly after cutover while canonical journals still equal the activation snapshot, the Run remains `running`, no Factory-owned external-effect receipt exists, and webhook delivery has not advanced the wire. Refusing only canonical-only sessions is insufficient. Make a pre-cut backup ineligible for post-boundary restoration whenever the activation snapshot contains any nonterminal Run or live effect-capable tmux session, unless mandatory receipts and authoritative external-state revalidation cover every session. Under the current architecture, an activation-spanning session must force forward correction. Add a delayed-or-absent-webhook refusal fixture for a retained legacy session.

## P2 findings

- `internal/linearidentity` still lacks an explicit final owner and migration disposition.
- Pre-receipt identity health versus active readiness remains underspecified.

## P3 finding

- Package, line, and entry-size targets should remain reported outcomes rather than implementation gates.

VERDICT: REVISE
