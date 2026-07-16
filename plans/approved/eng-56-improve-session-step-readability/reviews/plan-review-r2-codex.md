P0/P1: None.

P2: None.

P3-01: Browser verification lacks a named runnable harness. Plan lines 109 and 163-165 require mocked responses, viewport checks, failure injection, and refresh-state validation, while `frontend/package.json:6-17` provides only dev, build, and typecheck scripts. This is non-blocking because the checks can be performed with an ephemeral external browser fixture.

VERDICT: READY
