# Plan review round 1: Codex

## P1

- **P1-1: Candidate browser verification is neither safely specified nor guaranteed to exercise the changed frontend.** Phase 3 permits using the existing authenticated service and makes a disposable fixture conditional. That service is explicitly the unchanged `main` deployment, while Factory serves `frontend/dist` from the running process's working directory. The focused Go tests exercise HTTP handlers, not the changed client refetch and form-state logic. Starting an ad hoc candidate server is unsafe unless isolated because it opens state under the user's real Factory data directory. Consequently, the matrix can pass against the old bundle or risk production state while missing regressions in the central success/conflict behavior.

  Smallest correction: require browser verification against the candidate build on a distinct loopback port with a temporary home or seeded disposable state and exact start/stop instructions; require native success and stale-revision conflict there, reserving port 8092 and public Factory for read-only baseline and post-deploy checks.

## P2/P3

None.

VERDICT: REVISE

P1-1
