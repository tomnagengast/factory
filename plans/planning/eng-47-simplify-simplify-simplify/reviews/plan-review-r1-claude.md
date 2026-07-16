# Plan review round 1: Claude

## Validation performed

- `frontend/src/index.tsx` is 3,273 lines; it is the sole frontend module, so all symbol moves are internal with no external importers to break.
- `getTaskDetail` returns `NativeTaskDetail | TaskSummary`, is called only once, and is discriminated by `"task" in value` in the combined detail page.
- The task route regex hard-validates the provider to exactly `factory|linear`, so explicit dispatch to provider-typed pages is behavior-preserving.
- Native mutation endpoints hardcode `/api/tasks/factory/...` and Linear has no mutation controls, so the read/write split is clean.
- Shared symbols exist with multiple consumers, and the proposed dependency edges can remain acyclic.
- The focused server tests and frontend typecheck command are runnable and pass on the baseline.

No P0 or P1 findings. The plan's claims match the code, the sequencing is sound, scope is disciplined to the evidenced slice, and no server, CSS, dependency, or persistence change is implied.

## P2/P3 (non-blocking)

- P3: `runStateLabel` is semantically closer to the run/agent model than the activity shell. This is cosmetic module placement only.
- P3: The shared-export rule is slightly self-inconsistent if `agentRunHref` ends up consumed only by Tasks after extraction, though its placement in an agent lifecycle module remains reasonable.
- P3: The post-merge deployment and rollback section is broad relative to a pure frontend extraction but matches Factory's standard publication flow.
- P2: Splitting `TaskDetailPage` must reproduce the currently shared ActivityHeader label branch and the shared error, loading, and footer wrappers per provider page. The plan already requires preserving all existing JSX; this is the highest-attention implementation detail.

VERDICT: READY
