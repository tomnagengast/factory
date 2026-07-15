# ENG-49 Adversarial Plan Review Round 3

Both providers received the same rendered read-only review prompt. Claude completed successfully with `VERDICT: READY`. Codex completed successfully with `VERDICT: REVISE` on P1-1 and P1-2. Under the Factory dual-provider rule, the logical round verdict is `REVISE`.

## Claude review

Claude validated the plan's settings, admission, coordination, execution, continuation, launcher, trigger API, frontend, migration, and safety claims against current source. It reported no P0/P1 blockers and confirmed the plan now targets the actual `ClaimContinuation` and launcher snapshot gaps.

Non-blocking findings:

- P2: the external provider `$do` source was unavailable to that child, so compiled default parity is effort-heavy and should be grounded in the repository prompt, current runbook, and ENG-42 behavior.
- P2: the existing trigger-registry `LegacyRollbackIncompatible` marker is related but not identical to the new workflow compatibility boundary; a separate marker is feasible but introduces a settings write in admission.
- P3: keep schema migration dependency direction from settings to workflow to avoid an import cycle.
- P3: existing policy mutation returns pending rather than literally waiting; rejection still satisfies the gate.
- P3: verify the protected route allowlist and actual health port while implementing.

VERDICT: READY

## Codex review

### P1-1 - Fresh feedback continuation has no authoritative workflow-selection path

The plan requires `ClaimContinuation` to pin the “selected current published definition” but does not define what selects that workflow or how the selection participates in disable/delete validation. Current protected feedback uses `settings.Triggers.LinearComment` and directly calls `ClaimContinuation` without workflow identity. The generic `/triggers` rule is separately configurable, while protected contextual feedback is intentionally system-owned and cannot be disabled by configured rules.

Concrete failure: an operator can repoint the published `linear-comment` rule to workflow B, then delete workflow A after rule-reference validation passes. The protected continuation path may still select A through a legacy field or have no defined selection, causing failure or policy divergence.

Smallest correction: define the authoritative published-policy source for protected feedback continuation pins, remove new-continuation dependence on legacy trigger fields, include the source in disable/delete validation, and test repoint/delete plus active-pin preservation.

### P1-2 - Pre-admission rollback can still produce an unbootable prior release

A workflow and enabled rule referencing it can be published before any event admission, leaving the compatibility marker false. Restoring the schema-1 backup then removes the workflow but leaves `triggers.json` referencing it. Startup validates the retained registry against restored settings and fails.

Smallest correction: require rollback eligibility to validate the retained trigger registry against the schema-1 backup before restoration, with schema-2-aware recovery on incompatibility, and test a no-admission newly published workflow referenced by an enabled rule.

VERDICT: REVISE
P1-1, P1-2

## Parent independent validation

- P1-2 is supported by `main.go` startup ordering and `triggerregistry.Open` validation. The plan must add the backup-registry compatibility preflight.
- P1-1 is also supported and cannot be dismissed as stale. Choosing between a dedicated protected workflow binding, the configurable `linear-comment` rule, the prior terminal Run's workflow identity, or another policy source changes operator semantics and disable/delete authority. Repository and issue evidence do not resolve that choice unambiguously.
- This was the third and final allowed adversarial review round. Implementation cannot begin with a supported P1 and no reviewed authoritative selection contract.

Logical verdict: REVISE. Owner decision required before another reviewed plan can be authorized.
