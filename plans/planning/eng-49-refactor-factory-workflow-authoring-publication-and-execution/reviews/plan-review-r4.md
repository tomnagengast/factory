# ENG-49 Adversarial Plan Review Round 4

Both providers received the same rendered read-only review prompt after the human-authorized corrections for the protected feedback binding and rollback preflight were committed at `6bb1dca`. Claude and Codex both completed successfully with `VERDICT: READY`. Under the Factory dual-provider rule, the logical round verdict is `READY`.

Claude's normal `final.txt` handoff was absent, but the child exited successfully and its durable `events.jsonl` result contained the complete usable verdict reproduced below. Codex produced its normal `final.txt` result.

## Claude review

The reviewer validated the plan's pinning seam, strict retained-record compatibility risk, trigger Markdown leak, stale settings overwrite risk, launcher gap, fresh-continuation fallback, viewer-auth allowlist, and verification commands against the current source.

It reported no P0 or P1 findings and concluded that the plan can achieve ENG-49 while preserving the mechanical trust boundaries, keeping autosave outside the policy lock, and remaining within scope.

### P2 (non-blocking)

- Event handlers dispatch synchronously while `CoordinatedWire` holds its non-reentrant policy mutex. The workflow rollback marker must therefore use a direct monotonic store method, like the existing registry rollback marker, rather than re-entering a coordinator mutation from admission or continuation dispatch.
- The implementation should distinguish the new workflow compatibility marker from the existing trigger-registry `LegacyRollbackIncompatible` marker.
- The planned logical phases may not each leave the whole repository compiling until the later caller migrations land, even though focused checks and the final complete suite remain required.

### P3 (observations)

- Server-generated draft IDs must satisfy the existing lowercase slug contract and be unique across the published-and-draft union.
- `/triggers` is not currently in `protectedPagePath`; this is pre-existing and should be confirmed while adding `/workflows`.

VERDICT: READY

## Codex review

P0/P1: None.

### P2-1 - The pre-deploy invocation check can miss outstanding work for deleted rules

The plan requires zero nonterminal invocations and currently describes summing `ruleStatus[].outstanding`. `internal/server/triggers.go` reports outstanding counts only for rules still present in the registry, while `internal/triggerrouter/coordinator.go` permits registry updates without checking outstanding invocations. A queued invocation belonging to a subsequently deleted rule is therefore omitted. The reviewer suggested exposing a routing-wide outstanding count or including orphaned rule IDs in `ruleStatus` as a non-blocking correction.

P3: None.

VERDICT: READY

## Parent disposition

- Both providers returned usable `READY` verdicts, so the human-authorized fresh review cycle is complete.
- All findings are P2/P3. Per the issue's review rules and the human instruction to fix only the two supported P1 blockers, no further plan change is made.
