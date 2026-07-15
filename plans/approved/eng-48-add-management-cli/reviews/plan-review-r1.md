# ENG-48 Plan Review Round 1

> reviewed: 2026-07-15T06:27:54Z

## Claude review

Claude inspected the complete plan and relevant dispatcher, server, auth, lifecycle, build, manifest, policy, README, and module evidence.

No P0 or P1 findings.

P2 observations:

- A standalone instance with placeholder Linear credentials may remain degraded while recovery retries, so real integration checks should distinguish listening from healthy and use `httptest` for a synthetic OK status.
- Local stop must persist the exact `serviceStartedAt` exposed by health rather than taking a second timestamp.
- The provider deploy spelling should be verified because the current README uses `nags deploy` without an app positional.
- A repository-local deployment compatibility entry point is adjacent operational scope.

P3 observations:

- The local runtime record shares the Factory state root but managed-marker detection prevents local startup on the managed host.
- An ambient `PORT` affects no-flag probes by documented precedence.

`VERDICT: READY`

## Codex review

### P1-1: Deployment argument translation

The proposed wrapper forwarded `network-app deploy factory ...` unchanged to `nags`, while the current README documents `nags deploy ...` without the app positional and retains `factory` only for rollback. Smallest correction: translate the supported compatibility commands and test exact argv.

### P1-2: Managed artifact contract

The plan did not name the plist, wrapper, release, receipt paths, or exact launchctl argv required by a stopped-then-started flow. Smallest correction: name the evidenced paths/arguments and add sequence fixtures.

### P1-3: DNS rebinding

Loopback binding alone does not make unconditional local authorization safe because protected mutation routes accept same-origin requests using the caller-controlled Host. Smallest correction: enforce the configured canonical loopback Host/Origin in local auth and test hostile authorities.

### P1-4: Managed receipt identity

Managed start could accept any healthy Factory process without proving it matches the active successful receipt. Smallest correction: use the existing completion identity rules for healthy short-circuit and post-launch readiness.

No P2/P3 findings.

`VERDICT: REVISE`

Blocking identifiers: `P1-1`, `P1-2`, `P1-3`, `P1-4`.

## Combined round verdict

`VERDICT: REVISE`
