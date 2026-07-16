# Plan review round 3: Claude

## Validation performed

- Provider typing remains behavior-preserving: the route validates `factory|linear`, the server returns provider-specific payloads, native mutations stay factory-specific, and the Linear branch remains read-only.
- Shared-module extraction remains justified by consumers across multiple workspaces and can stay acyclic.
- The test-only fixture is realizable through `Config.Web fs.FS`, the production `os.DirFS("frontend/dist")` pattern, disposable test stores, and loopback local authentication.
- Round 2 P1-1 is substantively corrected. Foreground `wait` preserves the shell, variables, and trap; readiness is bounded to approximately 30 seconds; child death and timeout emit fixture logs; Ctrl-C stops the retained PTY session; and the required port assertion detects lingering listeners.
- A cold isolated Go cache compiled the server test package in approximately five seconds, within the readiness bound.

No P0 or P1 findings.

## P2/P3 (non-blocking)

- P2: Go's read-only module cache can make `rm -R` leave the temporary root. Preserve cache paths, prebuild, or make the temporary tree writable before removal during implementation.
- P2: Full authenticated-route parity requires benign non-nil fixture collaborators beyond seeded task data.
- P3: Prefer interrupt semantics for abnormal fixture shutdown because wrapper-only termination may not forward every signal to the test binary.
- P3: The earlier conditional browser language in research is superseded by the mandatory candidate fixture in the reviewed plan.

VERDICT: READY
