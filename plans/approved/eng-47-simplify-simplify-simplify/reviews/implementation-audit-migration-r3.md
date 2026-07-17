The Phase 1 migration boundary is incomplete. The current harness can pass inputs that the canonical policy or repository converter rejects, so the smallest acceptable addition must integrate both converters together.

## Findings

- P0: None. `DryRun` remains non-activating, and neither pushed code nor concurrent repository onboarding changes invoke canonical writers or select a generation.

- P1: The approved migration premise is currently violated. The plan requires each owner phase to add its converter and validator to the harness ([plan.md](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/plans/approved/eng-47-simplify-simplify-simplify/plan.md:220)). Yet `internal/migration` imports neither canonical package, its options contain no compiled repositories ([decode.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/internal/migration/decode.go:36)), and `auditSources` only performs its older reserved-workflow check ([audit.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/internal/migration/audit.go:111)).

  A concrete false positive exists: an explicit reserved rule differing from the authoritative default only by actor can pass the legacy registry validation and current dry run, while `policy.ConvertSources` rejects it as ambiguous ([migration.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/internal/policy/migration.go:73)). Repository conversion is not attempted at all.

- P2: The report currently has no prospective canonical policy/repository counts or digests ([model.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/internal/migration/model.go:54)). Consequently, `VerifyDryRun` cannot bind evidence to changes in compiled repository configuration outside the data root.

- P3: None. The concurrent `store.go`, `onboarding.go`, and `onboarding_test.go` changes do not close or worsen this migration boundary.

## Smallest exact addition

1. Add `CompiledRepositories []repositories.CompiledSource` to `migration.Options`.

2. Supply it from the exact pre-overlay `staticRepositoryConfigs` constructed in [main.go](/Users/tom/repos/tomnagengast/factory/.worktrees/eng-47-simplify-simplify-simplify/main.go:276), using a mechanical field-for-field adapter. Do not use `repositoryConfigsWithSetups` at line 343: that result already includes admitted managed repositories and would incorrectly classify them as compiled provenance.

3. Preserve whether `triggers.json` was absent. Pass `nil` to `policy.Sources.Registry` when absent so the harness exercises the converter’s real implicit-registry path.

4. After legacy decoding and before the cross-artifact audit:

   - Call `policy.ConvertSources`, then `Snapshot.Validate`.
   - Explicitly translate every decoded `projectsetup.Entry` into `repositories.SetupSource`, rejecting unknown state mappings.
   - Call `repositories.ConvertSources` with compiled sources plus setup sources.
   - Pass the result through `repositories.NewCatalog` and digest its canonical snapshot.

   These are pure in-memory operations. Do not call `policy.Create`, `repositories.Create`, either `Open`, or any writer. The candidates may carry prospective internal generation `1`, as required by their validators, but no generation directory, selector, manifest, or canonical artifact is created.

5. Remove the duplicate `auditReservedWorkflows` policy classification. The canonical policy converter should be the single authority.

## Required evidence

Extend the body-free audit with:

- Exact compiled-repository input digest, because these inputs are not covered by `SourceRootDigest`.
- Canonical policy schema, generation, digest, workflow/rule/schedule/enabled-project counts.
- Canonical repository schema, generation, digest, and compiled/admitted/awaiting/routable counts.
- Target schema declarations for `policy` and `repositories`.

The existing aggregate `AuditDigest` must bind all these fields, which also makes `VerifyDryRun` reject changed compiled options. No canonical bodies or filesystem paths beyond existing source evidence need to enter the report.

## Golden and option changes

- Keep the current golden directory legacy-only. Do not add `policy.json`, `repositories.json`, or a generation directory.
- Extend `testOptions()` with one sanitized compiled Factory source matching the existing `/srv/factory/repos/factory` setup.
- Assert exact prospective policy/repository counts and stable digests.
- Retain before/after source-tree equality and `Activates == false`; also assert no canonical artifact or selector exists.
- Add rejection tests for:
  - actor-only reserved-rule ambiguity through `DryRun`;
  - missing/conflicting compiled repository input;
  - compiled/setup path, origin, or branch conflict;
  - duplicate app or overlapping compiled paths;
  - `VerifyDryRun` with altered compiled options.
- Add failure-injection points around policy conversion, repository conversion, and canonical evidence calculation.
- Ensure the absent-registry golden test reaches `policy.ConvertSources` with a nil registry.

A policy-only check would be misleading. It would appear to satisfy the Phase 1 harness requirement while leaving compiled repositories, deployment metadata, provenance, path overlap, immutable overlays, Cloud coordination, and admitted setup lifecycle completely outside migration proof.

No tests were run because this was a strictly read-only audit.

REVISE
