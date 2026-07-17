# Plan review round 5: Codex

The reviewer received the identical round-5 read-only prompt, read the complete revised research, plan, and round-4 evidence, and traced the seven corrected boundaries through task persistence, Run reconciliation, deployment receipts, `bin/network-app`, and the `nags` provider.

No P0 findings.

## P1 findings

1. Rollback preflight has a time-of-check/time-of-use race. The application can advance `canonicalWritesStarted` and mutate after the wrapper's read-only preflight but before the provider acquires its deployment lock and stops the service. Require one exclusion held continuously across quiescence, preflight, optional restore, manifest deactivation, provider activation, health, and receipt finalization. The application boundary transition must honor it, and a paused-preflight race test must prove exclusion.
2. Whole-backup restore is not bounded against loss of post-cut work or orphaned tmux sessions. Permit source-backup restore only when replay proves no post-cut admission, task/policy/repository mutation, external side effect, or canonical-only Run/session exists. Otherwise refuse rollback and require a forward correction. Add refusal fixtures for post-cut task creation and a canonical-only live tmux session.

## P2 findings

- `internal/linearidentity` still lacks an explicit final owner and migration disposition.
- Pre-receipt migration health versus fully active health remains underspecified.

## P3 finding

- Package, line, and entry-size targets should remain reported outcomes rather than implementation gates.

Corrections 3 through 7 from round 4 were otherwise judged sufficient. P2/P3 findings remain visible and do not expand this correction pass.

VERDICT: REVISE
