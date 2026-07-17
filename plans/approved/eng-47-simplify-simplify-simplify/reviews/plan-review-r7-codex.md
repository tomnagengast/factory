# Plan review round 7: Codex

The reviewer received the identical round-7 read-only prompt, read the complete artifact and review set, and traced the final correction through the active Run, tmux launcher and manager, Factory deploy wrapper, and the deployment provider's fallback branches.

No P0 findings. The activation-spanning session exclusion and state-transition lease were judged coherent and sufficient for their identified failure modes.

## P1 finding

The deployment provider's automatic fallback bypasses the explicit rollback protocol. `nags deploy` restores the previous release directly after candidate health failure or success-receipt-finalization failure, but the plan activates the canonical manifest before health. The old release can therefore restart before Factory deactivates the manifest, then mutate stale source state. Require a fail-closed provider hook under the same lease before fallback, or keep the canonical generation staged and unselected until the exact successful deployment receipt exists. Add injected coverage for both provider fallback branches after generation staging.

## P2 findings

- `internal/linearidentity` still lacks an explicit final owner and migration disposition.
- Pre-receipt identity health versus active readiness remains underspecified.
- Phase ownership for the rollback command replacement remains ambiguous.

## P3 findings

- Holding the lease through the provider requires changing the wrapper's current `exec` behavior or transferring lock ownership.
- Package and line budgets should remain reported outcomes rather than gates.

VERDICT: REVISE
