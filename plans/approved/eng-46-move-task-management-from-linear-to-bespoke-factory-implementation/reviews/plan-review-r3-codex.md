# ENG-46 plan review round 3: Codex

## P0

None.

## P1

### P1-1: The required canary cannot complete without a second Factory pull request

The plan confines Factory repository work to one PR but requires a post-deployment native canary to reach terminal completion and implies repository safeguards apply only if the canary requires a change. The current completion validator rejects a successful Run without a manager-validated ready checkpoint; with a checkpoint, success requires a merged PR and repository/deployment evidence (`internal/agentrun/completion.go:212-277,343-366`). A create/edit/comment/gate/start/continuation-only canary cannot become terminal.

Smallest correction: make rollout wait for a separately scoped and approved real Factory task whose own gates authorize its required PR and deployment, distinguish it from ENG-46's one implementation PR, and remove the implication that a no-change canary can complete.

## P2/P3

None.

VERDICT: REVISE

BLOCKERS: P1-1
