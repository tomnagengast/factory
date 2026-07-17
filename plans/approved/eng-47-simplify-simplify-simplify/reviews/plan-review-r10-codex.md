# Plan Review Round 10 - Codex

Provider: Codex (`gpt-5.6-sol`, high reasoning)

Prompt SHA-256: `64e629cb33dd516c1c2dd87c4077fe1cef863b79e1eb8e88be4aea0846cb567a`

No P0 findings.

## P1 - Network rollback guard is point-in-time, not continuously lease-bound

The plan validates that the wrapper's lock is held and its PID is an ancestor, then allows `nags` to continue independently. Current rollback performs artifact backup and activation after the proposed guard point. If the wrapper dies after validation, its advisory lock disappears while the child provider can continue and activate. The same interval permits retained-manifest replacement after its initial hash check.

Smallest correction: specify a two-phase guard with an inherited, inode-verified locked descriptor retained by `nags` through activation, health, and receipt completion, plus a second manifest/generation/hash validation immediately before `activate_release`. Tests must kill the wrapper and mutate the retained manifest at pauses after preflight and before activation, proving zero release, receipt, artifact, process, or `current` mutation.

## P1 - The migration inventory omits the durable Linear identity bijection

Factory opens `linear-task-identities.json` as an independent authority and durably rejects identifier-to-UUID conflicts, including at webhook ingress. The live file contains four bindings, but neither the Phase 0 fixtures/audit nor the canonical owners name it. Starting an empty replacement silently forgets prior conflicts and can accept identifier reuse with a different provider UUID.

Smallest correction: assign the bijection to `internal/tasks`, migrate every binding atomically, include its count/digest and cross-task references in the audit, and test duplicate identifier, duplicate UUID, and changed mapping rejection across migration and restart.

## P1 - Activity history and private Linear payloads have no implementable post-cut owner

The plan creates only canonical policy, repository, Run, and task artifacts, says private payloads are not copied, and describes activity as derivable. In reality, `linear-activity.json` owns the Home lifetime total and retained history, while its private payload files are required for authenticated wire detail and project-setup dispatch. That total cannot be reconstructed from the wire's global checkpoint or GitHub/comment channel counters.

Smallest correction: make `internal/events` explicitly own a migrated activity projection and one private payload corpus. Define whether the corpus is migrated or retained in place, then verify totals, delivery-ID mappings, hashes, modes, pruning, historical detail, project-setup replay, and body-free wire/log behavior.

## P2 - Projection-journal baseline is stale

The inspected live files now contain nonzero GitHub and Linear projection records. Their totals currently equal the unified wire's channel totals, so deletion of runtime projection writers can still be safe, but emptiness cannot justify it. This P2 remains visible and does not expand the P0/P1 correction scope.

VERDICT: REVISE
