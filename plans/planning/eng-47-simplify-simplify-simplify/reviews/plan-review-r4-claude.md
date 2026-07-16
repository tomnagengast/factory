# Plan review round 4: Claude

The reviewer read the revised research and plan, the prior review history, and the relevant implementation evidence. The identical rendered prompt used for both providers has SHA-256 `f9d281a127e20c0604b1f5365b30e75114d316638cdc974c188f209549967abc`.

## Validation

- Parallel ownership is real: `triggerrouter` owns `Decision`, `Invocation`, and a claim manager, while `agentrun.Run` is a separate lifecycle and terminal state has a reflection receipt.
- Retained helper compatibility is correctly preserved because the GitHub and Linear helper adapters already read `system-events.jsonl`, not the provider projection journals.
- The task stage directory and duplicate outcome read are present as described.
- The narrow durable atomic-replacement primitive is supported by repeated identical code.
- The single deployment cutover can keep intermediate implementation commits buildable without claiming that each one is independently deployable.
- G1-G8 safeguards, private-body opacity, recovery-before-readiness, and the active ENG-47 pin/executor are explicitly preserved.

No P0 or P1 findings.

## P2 findings

- `linearidentity` needs an explicit final owner and migration audit disposition.
- The cross-package non-reentrant policy/admission lock should have one named owner through every implementation phase.
- Rate-bucket/window state should be included in the migration audit or explicitly reset with rationale.

## P3 findings

- The ownership table should make clear whether `internal/auth` is a rename of `internal/viewerauth`.
- The archival `taskcompat` marker is omitted from the ownership table.
- The schema-2-envelope rationale is mostly redundant once the generation cutover and whole-backup recovery model is used.

VERDICT: READY
