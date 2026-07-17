# Plan review round 9: Codex

The Codex reviewer received the identical read-only round-9 prompt with SHA-256 `0971b5b4eab25e4f8eabd63445c60469c8bd6ad98cb8c0bbc524d7077be61748`. It read the complete revised research and plan, round-8 evidence, Factory deployment wrapper and manifest, the relevant Factory implementation, `origin/main` at `02994e84295d6b7b9a2e725f3b51868e266f8709`, and the authoritative Network provider at `bb6f5d2c0676925611561ed2e4c90b61ff95e04f`.

## P0

None.

## P1 - blocking

1. The remaining activation guard is genuinely Network-owned and still blocks ENG-47. Direct Network deploy and rollback acquire only the provider `.deployment-lock` before `activate_release` changes `current`, rewrites runtime artifacts, and starts the selected release. Neither path checks Factory's canonical generation, and rollback requires no Factory state-transition lease. Factory's `bin/network-app` cannot protect direct `nags` callers. The Network provider must reject generation-incompatible deploy and rollback targets before selection, with rollback additionally validating the live Factory state-transition lease.

## Round-9 durability disposition

The former provider-durability P1 is closed by the Factory-local correction:

- Network holds `.deployment-lock` across selection, wrapper/plist generation, health verification, success-receipt replacement, and post-success cleanup, releasing it only through the exit trap.
- Factory consistently orders its state-transition lease before the provider lock. Candidate health remains available while advancement is gated, avoiding a health-check deadlock.
- Exact revalidation handles a provider action that won the lock first.
- Recursive release sync, `current` parent-directory sync, exact wrapper/plist-set sync, receipt sync, parent-directory sync, identity-bound acknowledgement, manifest publication, and the `canonicalWritesStarted` boundary close the power-loss window.
- The crash matrix and recovery rules keep advancement gated before acknowledgement and require idempotent revalidation. Symlinks are inspected without following them and their directory entries become durable through parent fsync.
- launchd adds no durable artifact beyond the generated wrappers and plists. Reboot recovery comes from those durable files and their exact release identity.

No Factory-local P0/P1 remains.

## P2

- Finalizer liveness remains implicit. `current.json` becomes visible while Network still owns `.deployment-lock`. Implementation must retry lock acquisition without blocking health and treat a crash-stale lock as fail-closed until owner death is proven.
- The existing `internal/linearidentity` ownership ambiguity remains non-blocking.

## P3

`origin/main` adds source-neutral observer lookup/query semantics and conflicts with the revision-1 frontend slice. Implementation must eventually rebase while preserving `FindObserverRun` and `source=factory|linear`; this does not invalidate the unified Run or frontend ownership plan, so deferring the rebase while the Network prerequisite blocks planning is appropriate.

VERDICT: REVISE
