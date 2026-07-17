## Conclusion

The canonical policy foundation is sound but still dormant. The smallest safe next Phase 1 slice is to make the migration dry-run harness consume `policy.ConvertSources` and prove canonical/legacy equivalence in memory. It is not safe to convert a live runtime caller yet.

The approved plan’s Phase 1 rollback-deletion sequencing is P1-inconsistent with its Phase 4 activation design. Live cutover or latch removal now would require early canonical activation, dual ownership, or loss of old-release rollback refusal.

## Interfaces

Change now only the internal migration conversion/reporting boundary:

- Preserve whether `triggers.json` was actually present rather than silently replacing absence with synthesized defaults.
- Have `migration.DryRun` call `policy.ConvertSources`.
- Add canonical policy schema, immutable digest, counts, preserved revision domains, and compatibility-equivalence results to the nonactivating report.
- Do not call `policy.Create`, `policy.Open`, or construct the canonical coordinator from `main`.
- Do not write `policy.json`, a generation manifest, or `canonicalWritesStarted`.

The eventual live admission boundary should be narrow:

```go
type AdmissionPolicy interface {
    Admit(policy.AdmissionCallback) error
}
```

The callback receives exactly one `policy.Snapshot`. It must perform all policy-derived workflow selection, digest/revision capture, admission decisions, and the durable decision append before returning. `ApplyDecisionBatch` should therefore accept that canonical snapshot, not separately fetched settings and registry projections.

The existing coordinator already provides the required guarantee in [policy/coordinator.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/internal/policy/coordinator.go:57). Callers must not invoke `Snapshot()` again inside the callback.

At the eventual activation boundary:

- `triggerrouter` should replace its separate `RegistryStore` and `SettingsStore` admission dependencies with `AdmissionPolicy`.
- `taskservice` should replace its separate `Policy` and `Admitter` dependencies with the same callback boundary.
- Server reads/mutations should use one canonical policy reader/coordinator, projecting compatibility views from one snapshot.
- Scheduler ticks should project schedules from one canonical snapshot per tick.

Those production interfaces cannot be wired independently before the Phase 4 generation selector exists.

## Legacy runtime that must remain

Until Phase 4 activation and rollback machinery are installed, retain:

- Legacy settings, trigger-registry, and task-control stores and their startup composition in [main.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/main.go:155).
- `ReconcileCompiledDefaults` startup behavior.
- `WorkflowRollbackIncompatible` and `LegacyRollbackIncompatible`, including every marker call.
- The task compatibility marker and all writers that can make an old release unsafe.
- `workflow-rollback-preflight` and the current wrapper invocation in [bin/network-app](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/bin/network-app:26).
- Schema-1 settings backup/migration validation.
- Legacy repository resolution/fallback.
- Legacy pin execution. Per the plan, this remains even after activation for retained old pins.

The compatibility projections intentionally omit the rollback latch fields in [policy/compatibility.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/internal/policy/compatibility.go:38). Consequently, they are not drop-in replacements for the live legacy stores during the rollback window.

## Equivalence evidence required

The migration slice should prove:

- Explicit and absent registry source cases.
- Exact compiled-default consolidation and protected-binding repointing.
- Preservation of custom workflows, rules, schedules, task activation, agent/runtime settings, and independent revisions.
- Rejection of ambiguous reserved/custom defaults and actor identity conflicts.
- Compatibility projections validate and do not alias the canonical snapshot.
- Source files and hashes remain unchanged.
- `Activates == false`; no canonical artifact, generation manifest, or write boundary appears.

Before live admission cutover, characterization tests should run legacy and canonical admission against cloned routing state and compare:

- Decisions, suppression reasons, invocation identities, workflow pins and digests.
- Settings and registry revision fields.
- Rate limits, batch atomicity, replay/idempotency, and pending-admission behavior.
- Native start/continuation behavior under concurrent policy mutation.
- Rollback marker creation and old-preflight refusal behavior.

Focused and race tests passed for policy, repositories, triggerrouter, scheduler, migration, taskservice, and server.

## Findings

### P0

None. No canonical policy or repository store is selected by production startup, and no canonical generation is active.

### P1

- The Phase 1 instruction to replace preflight and then delete rollback latches conflicts with Phase 4, where the selector, state-transition lease, provider acknowledgement, manifest publication, `canonicalWritesStarted`, `state-rollback-preflight`, and bounded restore are actually introduced. See [plan.md](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/plans/approved/eng-47-simplify-simplify-simplify/plan.md:238) versus [Phase 4](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/plans/approved/eng-47-simplify-simplify-simplify/plan.md:273). Deletion must remain a Phase 4, activation-bound operation.
- The premise that individual live callers can adopt canonical policy during Phase 1 is false. Production currently has only legacy authoritative stores. Early wiring would create partial activation, require dual synchronization, or bypass rollback refusal state.

### P2

- Current event admission reads registry and settings independently in [triggerrouter/coordinator.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/internal/triggerrouter/coordinator.go:294).
- Native task admission likewise obtains policy state at separate points in [taskservice/service.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/internal/taskservice/service.go:254).
- The migration harness does not yet exercise the canonical policy converter. This is the correct next nonactivating slice.

### P3

Concurrent `internal/repositories` work adds package-private persistence locking and dormant onboarding operations. It has no runtime references, does not activate canonical ownership, and passes package and race tests. The worktree remained otherwise untouched by this audit.

REVISE
