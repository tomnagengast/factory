# ENG-46 plan review round 2: Codex

## P0

None.

## P1

### P1-1: The rollout completes ENG-46 while native task creation remains disabled

The plan keeps native create/start dark through publication, documents a future canary, and then completes ENG-46 without actually enabling the central production capability. The Linear contract requires rollout to enable native creation for Factory and prove native create, gate, start, continuation, and completion flows.

Smallest correction: before completing ENG-46, reconcile the protected workflow and Factory project route, obtain operator authorization, enable the Factory-scoped rollout, execute the native canary through both gates and completion, and record the evidence. The committed default can remain dark and broader enablement can remain deferred.

## Validated prior corrections

The round-1 identity, tmux isolation, retained-run secret, live workflow publication, and early rollback-boundary blockers are resolved against current ownership, launcher, workflow, and strict persistence interfaces.

## P2/P3

None.

VERDICT: REVISE

BLOCKERS: P1-1
