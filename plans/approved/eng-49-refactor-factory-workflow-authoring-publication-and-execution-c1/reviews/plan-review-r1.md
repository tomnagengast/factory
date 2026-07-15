# ENG-49 recovery plan review, round 1

> reviewed: 2026-07-15T00:02:54-07:00

Both providers received the same rendered read-only adversarial review prompt and completed successfully.

## Claude

Claude validated the ancestry-only merge, launcher insertion point, GitHub field and flag surface, Linear provenance parser, exact-head gate, workflow-default gap, current deployment interface, repository allowlist, and PR state.

Non-blocking observations:

- P2: explicitly mirror the existing empty repository/GitHub-path guard when implementing reconciliation.
- P3: `internal/workflow/defaults_test.go` will be a new file.
- P3: reconciliation is lazy for idle managed repositories outside the explicit live rollout.
- P3: rollback to the prior merge settings is intentionally incident-prone and therefore cannot accompany deployment.

`VERDICT: READY`

Durable child output: `/Users/tom/.local/share/factory/runs/run-0c33a6685b5a2c2f/children/plan-review-r1-claude-cf6c5529`

## Codex

### P1-1: compiled workflow carries a nonexistent deployment command

Plan lines covering `internal/workflow/defaults/full-sdlc.md` addressed only Linear signatures and merge instructions, while the compiled workflow still directed Factory self-deployment through `bin/network-app deploy factory`. That path does not exist in the repository or on `PATH`; the current repository runbook and installed CLI use `~/.local/bin/nags deploy --expected-commit`.

Concrete failure: a new post-merge Run could follow the pinned compiled Markdown, invoke a nonexistent command, and fail deployment and completion.

Smallest correction: add the current deployment-command replacement to Phase 3 and assert the compiled default contains `nags deploy` and excludes `bin/network-app`.

Non-blocking observation:

- P2: capture authoritative per-repository merge-policy state before the live rollout if rollback becomes necessary.

`VERDICT: REVISE`

Blocking finding: `P1-1`

Durable child output: `/Users/tom/.local/share/factory/runs/run-0c33a6685b5a2c2f/children/plan-review-r1-codex-99e32f8c`

## Principal disposition

Round verdict: `REVISE`.

The plan was changed only for supported P1-1: the compiled workflow change now replaces the stale deployment path with the current `nags deploy` interface and tests both presence of the current command and absence of the nonexistent path. P2/P3 observations remain recorded without scope expansion.
