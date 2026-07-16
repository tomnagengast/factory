# Plan review round 3: Codex

Round 2 P1-1 is completely corrected. No P0 or P1 findings block implementation.

The plan now retains one PTY, foreground-waits on the fixture, bounds readiness to 30 seconds, detects early child death with logs, stops through the same PTY, waits and removes disposable state, and requires port 18092 to be free afterward. A PTY probe confirmed Ctrl-C reached the shell, wrapper, and descendant process group and left no process behind.

## P2/P3 (non-blocking)

- P2: Abnormal cleanup directly signals only the `go test` wrapper PID. Normal PTY interruption is sound and the final port assertion prevents silent success, but directly running a prebuilt test binary or signaling the process group would be more robust.
- P2: The exact `POST` fixture revision-control endpoint should remain behind local viewer authorization.
- P2: Full route parity requires fixture dependencies beyond task data, including trigger and observer collaborators.
- P2: The fixture test should `t.Skip` when `FACTORY_BROWSER_FIXTURE=1` is absent.
- P2: The mandatory candidate-fixture plan supersedes earlier conditional browser language in research.
- P3: Preserve Go cache locations or prebuild to avoid coupling readiness to cold cache behavior.

Scope, provider typing, capability preservation, trust boundaries, publication checks, exact-head deployment, and verified rollback are otherwise adequately specified.

VERDICT: READY
