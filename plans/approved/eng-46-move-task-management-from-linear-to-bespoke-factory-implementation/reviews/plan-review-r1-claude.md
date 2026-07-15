# ENG-46 plan review round 1: Claude

## Verification of premises

- `internal/agentrun/store.go`: `stateVersion = 1`, a single version-rejecting JSON snapshot, `IssueIdentifier` as the owning key, and one-active-Run comparison by string confirm the plan's migration premise.
- `internal/agentrun/completion.go`: `LinearComplete` is mandatory terminal evidence and completion derives the task from `run.IssueIdentifier`.
- `internal/agentrun/repository.go`: `LinearRepositoryResolver.Resolve` delegates to catalog project metadata and has no project-ID resolver.
- `internal/viewerauth/auth.go`: the protected-page list excludes `/tasks`; API middleware exposes no actor; Google identity retains verified email/subject.
- `internal/viewerauth/local.go`: loopback authorization has no person identity.
- `internal/server/server.go`: method routing, readiness, same-origin, strict JSON, and `409` conventions exist; `PATCH` is new.
- The frozen Bun install, typecheck, and build commands are present and runnable.

Phase verticality is sound, each phase ends committable, native writes stay dark until the final phase, and the source-neutral seam precedes native records. Scope is bounded by the approved v1 contract and one-PR authorization.

## P0/P1

None.

## P2

- The rollback boundary understates the persistence-version bump. Current stores reject unknown versions, and an ordinary Run mutation can persist a new snapshot before native activation. The plan should state that source-neutral Run/Invocation persistence is an earlier rollback boundary that deployment preflight must detect.

## P3

- Migration wording implies a retained Linear provider UUID, but current records retain only the display issue identifier. Tighten the deterministic compatibility identity.
- A one-second development-machine threshold can be machine-dependent; treat it as a focused local benchmark if it proves flaky in CI.

VERDICT: READY
