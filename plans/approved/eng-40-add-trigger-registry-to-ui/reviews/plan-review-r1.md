# Adversarial Review: ENG-40 Trigger Registry Plan

Reviewer: Claude Opus 4.8
Reviewed plan commit: `8e91ab3`
Result: completed successfully

The plan's factual anchors against the repository all verify correctly: the `Source` enum and closed allowlists (`event.go:41`, `server.go:384`, `agent_commands.go:214`), `catchUpLocked`/`PublishBatch` semantics (`wire.go:75-143`), legacy label/comment admission in `dispatchLinear` (`server.go:735-747`, `725`), `Claim` same-issue coalescing (`store.go:236-255`), `WorkflowForTrigger` (`settings.go:146`), `RepositoryResolver` (`repository.go:171`), `agent-exec` loading workflow via `WorkflowForTrigger` into `ExecuteConfig.Workflow` (`agent_commands.go:57,72`, `execute.go:33`), and every numeric baseline the plan's bounds derive from (`agentRunLimit=100`, `systemEventLimit=10_000`, `maxConcurrentRuns=10`, `maxWorkflows=8`, `maxWorkflowSteps=20`, `maxAttributeCount=32`, `maxFieldLength=256`, identifier 48-byte bound, workflow name 80-byte bound). New packages (`triggerregistry`, `triggerrouter`, `triggerscheduler`) correctly do not yet exist.

## P0

None.

## P1

None.

No concrete evidence of catastrophic or irreversible harm, plan-defeating impossibility, or a claim that is likely incorrect against current callers. The known hazards (admission-before-dispatch ordering, replay idempotency for rate/outstanding, reflection-before-next-promotion, lock ordering and no coordinator re-entry while holding store locks, no outward calls under lock) are each explicitly guarded in sections 2, 5, and 6. The batch/per-record retry path is consistent with the existing `catchUpLocked` acknowledgment semantics (`wire.go:103-143`): a transient per-record failure returns before `Acknowledge`, so the whole still-pending prefix, including batch admission, reprocesses, and the plan makes re-decision idempotent (lines 104, 71).

## P2: non-blocking

- First third-party Go dependency (section 8, lines 139-140). The module is 100% standard library today: `go.mod` has no `require` block, and there is no `go.sum` or `vendor/`. Adding `robfig/cron/v3` introduces the codebase's first external dependency and a committed `go.sum` for a five-field parser that is straightforward in the standard library. Concrete risk: the required checks (`go test ./...`, `go test -race ./...`) and the deploy build (`bin/network-app deploy factory`) must be able to fetch and verify the module; if that environment lacks module-proxy access or a warmed cache, they fail. Smallest correction: either state and confirm module fetch is available in the CI/deploy environment and that committing `go.sum` is acceptable, or implement the constrained five-field parser in the standard library to preserve the zero-dependency invariant. Verify before Step 8, not after.
- Coordinated-wire exposed surface vs. server self-registration (section 2, lines 67-70). The exposed method list omits `Handle`, but `server.New` currently self-registers protected routes via `app.events.Handle(...)` (`server.go:299-304`) and the server field is concrete `Events *eventwire.Wire` (`server.go:106,125`) also used for `Publish` (`server.go:570,631`), `Query` (`393`), `Record` (`407`), and `Status` (`332`). This is implementable, but the plan leaves implicit the registration-order mechanism by which protected handlers attach to the raw wire before it is hidden and the field-type/interface change so those server methods keep compiling. This is only a plumbing concern, not a correctness gap: batch admission is a `BatchHandler` on the raw wire and runs inside `catchUpLocked` for any publisher, so admission coverage does not depend on who holds the pointer.

## P3: observations

- Scope breadth. The plan is materially larger than the literal issue title, "Add trigger registry to UI": it builds causation/ancestry, a batch-admission coordinator, an invocation/Run saga, pinned-workflow execution, and a cron subsystem. The plan frames all of this as an already-approved product contract; a registry does require backing storage and evaluation, so this is flagged for confirmation, not as a defect.
- Publish latency. Each publish now performs two serialized fsyncs under `dispatchMu` (`journal.go:348-373` plus `ApplyDecisionBatch`), roughly doubling publish latency. The plan bounds this only with a 1-second focused-test budget (line 47); real steady-state webhook publish latency should be expected to increase.

VERDICT: READY
