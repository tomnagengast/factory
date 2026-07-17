# ENG-58 Plan Review Round 1 - Codex

## Findings

P0: None.

P1-1: The required-value claim API cannot satisfy the planned dedupe-before-selection guarantee. The plan required callers to construct a full pin and select the legacy/provider workflow before calling `Store.Claim`, while also requiring retries to succeed when the current binding is missing. `WorkflowForTrigger` returns an error for a missing or disabled workflow, so callers could not construct the claim and would never reach the store's dedupe/coalescing loop. Smallest correction: define lazy or error-carrying pin resolution that is evaluated only after the store atomically rules out duplicate delivery and active-task coalescing, then add caller-level tests with the binding removed between attempts.

P1-2: "Valid policy revision" was undefined and could not be enforced as planned. The store has no policy context and currently treats the revision as opaque. Revision zero is valid in default and rollback-boundary states. Smallest correction: explicitly define policy revision as an opaque admission snapshot value, allow zero, and test exact persistence, or define concrete same-snapshot validation at the caller boundary.

P2-1: The post-deploy requirement to confirm a newly admitted pin without creating a probe run is not deterministically runnable. A quiet deployment may have no post-deploy admission, and existing public/activity snapshots omit pin fields. Make this conditional on the first organic admission or rely on deterministic pre-publication admission tests.

P3: None.

## Verdict

```text
VERDICT: REVISE
BLOCKERS: P1-1, P1-2
```

Round 1 correction: the plan now uses an error-carrying candidate that always reaches store dedupe/coalescing, defines policy revision as opaque with zero allowed, and adds caller-level removed/disabled-binding retry coverage. P2-1 remains recorded and does not expand the approved issue scope.
