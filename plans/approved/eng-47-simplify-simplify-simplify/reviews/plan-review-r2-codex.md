# Plan review round 2: Codex

The round 1 isolation design is directionally correct, but its exact lifecycle remains unusable. No P0 findings.

## P1

- **P1-1: The fixture launch block cannot reliably remain alive for browser verification.** The plan installs an `EXIT` cleanup trap, backgrounds `go test`, completes the health loop, and then reaches the end of the shell block without a foreground `wait` or persistent-session handoff. In normal noninteractive execution, the shell exits immediately after readiness and the trap kills the fixture before Computer Use can connect. On startup failure, `kill -0` is not checked by an `if`, no `errexit` is established, and there is no readiness deadline, so the loop can continue after the test process dies. The later cleanup instruction cannot access variables from an exited shell.

  Required correction: use an explicitly persistent execution session or foreground `wait "$fixture_pid"`, a bounded readiness deadline with explicit child-death failure and log output, and a stop procedure that signals, waits, cleans, and verifies the port.

## P2/P3 (non-blocking)

- P2: `research.md` retains the earlier conditional browser path. The revised plan is unambiguous, so this does not independently block implementation, but that research language is superseded.
- P2: The fixture-only revision control should use an exact method, loopback authorization, and remain unavailable outside the gated test binary.
- P2: Candidate parity for every authenticated route requires fixture dependencies beyond task data, including non-nil trigger dependencies.
- P3: Overriding `HOME` also isolates Go caches. Preserve cache locations or build the test binary before entering the isolated runtime environment.

VERDICT: REVISE

P1-1
