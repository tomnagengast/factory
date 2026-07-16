# Plan review round 8: Codex

The reviewer received the identical round-8 read-only prompt, read the complete artifact and review set, and traced deployment, direct rollback, active selection, runtime artifacts, and receipt durability through the live `tomnagengast/network` provider.

The receipt-gated staged-generation correction was judged coherent and sufficient for the two automatic fallback branches. No P0 findings.

## P1 findings

1. Direct provider entry points remain able to bypass Factory's state-transition lease. `nags rollback factory` activates a retained release without the Factory preflight, and a later `nags deploy` can launch a state-incompatible Factory binary before health. Require a provider-enforced Factory activation guard shared by deploy and rollback before release activation. Once a canonical manifest exists, it must reject a candidate/target that does not declare support for the selected generation, and rollback must require the valid state-transition lease. Add direct-provider incompatible deploy/rollback refusal tests proving no selection, process, receipt, or source-store mutation.
2. The provider's successful receipt and active-release selection are not fsync-durable. Power loss after Factory durably selects the canonical generation can lose or revert the provider receipt or boot selection. Before manifest publication, require a durable provider-finalization acknowledgement that fsyncs the active release selection, wrapper/plist artifacts, successful receipt, and their parent directories. Add crash/power-loss injection between provider finalization, receipt observation, manifest fsync, and boundary fsync.

## P2 findings

- `internal/linearidentity` still lacks an explicit final owner and migration disposition.
- Pre-receipt health versus post-activation readiness remains loosely specified.
- Rollback command ownership remains split across phases.

## P3 findings

- Holding the rollback lease through the provider requires changing the wrapper's current `exec` behavior or transferring lock ownership.
- Package and line budgets should remain reported outcomes rather than gates.

Both P1 corrections require the separately routed `tomnagengast/network` provider repository; ENG-47's Factory project metadata does not grant that write authority.

VERDICT: REVISE
