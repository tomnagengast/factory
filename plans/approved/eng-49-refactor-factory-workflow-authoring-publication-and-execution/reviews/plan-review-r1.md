# ENG-49 Adversarial Plan Review Round 1

Both providers received the same rendered read-only review prompt. Claude completed successfully with `VERDICT: READY`. Codex completed successfully with `VERDICT: REVISE` on P1-1 and P1-2. Under the Factory dual-provider rule, the logical round verdict is `REVISE`.

## Claude review

### Summary

The plan is well-grounded in the current code. Every major seam it names is real and used as claimed. Phase verticality is sound (domain+migration → API → execution → UI → docs), scope tracks the Linear issue without unrelated expansion, and the safety/rollback narrative is concrete and matches actual state strings and file paths.

### Verified against source

- Pinning: `internal/triggerrouter/admission.go:174-192` serializes and digests the selected workflow onto the durable `Invocation` (`model.go:51-52`), copied to `Run.PinnedWorkflow` (`store.go:119, 270-280`) and written to `workflow.json` at `launcher.go:508-519`. The plan's “replace the pinned value, not the runner” premise holds.
- Coordination: `CoordinatedWire.UpdateSettings/UpdateRegistry` (`coordinator.go:105-136`) already enforce `pendingDecisionsComplete()` plus cross-validation. Extending publish/delete here is feasible as stated.
- Prompt: `execute.go:290-336` contains `Use $do`, `The /do skill owns`, and `.agents/skills/do/scripts/linear_graphql.py`; retry text says “Resume the Factory /do run” (`execute.go:70`). All match the plan's removal targets.
- Reference protection: `triggerregistry.Snapshot.Validate` (`model.go:150-153`) only rejects enabled rules pointing at unavailable workflows; the plan correctly identifies that delete while any rule references it needs new logic.
- Gates: run-state strings in the pre-deploy jq exactly match `store.go:37-41`; `.status`, `.wire.pending`, and `.commit` exist in the health DTO.
- `factory agent linear-graphql` does not yet exist; adding it is net-new as planned.

No P0 or P1 findings. The plan can achieve the issue and no step is incorrect, unsafe, or impossible without a plan change.

### P2 / P3 (non-blocking)

- P2: `runPrincipal` falls back to live `configuration.WorkflowForTrigger` when `--workflow-file` is empty. The plan covers this conceptually as the old-Run fallback but does not name the branch in Phase 3.
- P3: keep schema-1 snapshot migration in `internal/settings` and workflow helpers primitive to avoid an `internal/workflow` ↔ `internal/settings` import cycle.
- P3: `protectedPagePath` currently omits `/triggers` even though the page is registered; adding `/workflows` should make the intended handling explicit.
- P3: terminal compaction currently retains only workflow ID; retaining revision and digest is a deliberate planned change.

VERDICT: READY

## Codex review

P0: None.

### P1-1 - Draft conflict controls are incomplete and contradictory

`plan.md:152` requires the current published revision when materializing a synthesized draft, but `plan.md:279` says autosave is conditional only on draft revision. `plan.md:153` also makes draft discard unconditional. A stale tab can recreate a just-deleted workflow as a never-published draft, or discard a newer cross-tab draft after operations serialize. A keyed lock does not prevent stale sequential mutations. This contradicts the conflict contract and the repository's existing revision-checked mutation pattern.

Smallest correction: require expected published revision for revision-zero materialization, require expected draft revision for discard, return 409 on mismatch, and test stale autosave after live deletion plus stale cross-tab discard.

### P1-2 - The rollback boundary is unsafe

The plan permits prior-binary rollback until an operator publishes Markdown even though every new admission immediately writes the new workflow shape and terminal compaction retains its revision. The current prior binary knows only `runner` and `steps`, strictly rejects unknown routing fields, and rejects schema-2 settings. One schema-2 admission can therefore make the old binary unable to replay `trigger-routing.jsonl` regardless of whether an operator published an edit, and a plain code revert after publication cannot read the resulting state.

Smallest correction: make the compatibility boundary the first durable new-shape admission, gate or mark it monotonically, and require schema-2-aware forward recovery or corrective workflow publication after that boundary. Do not promise ordinary prior-binary or plain-revert recovery.

### P2 (non-blocking)

The suggested phase commits may require additive compatibility adapters because Phase 1 changes the settings model before all old-shape Go callers, and Phase 2 changes API shapes before the frontend cutover. The final implementation can still succeed.

P3: None.

VERDICT: REVISE
P1-1, P1-2

## Parent disposition

- P1-1 accepted. The plan now makes revision-zero materialization conditional on the exact live revision, makes discard conditional on draft/base revisions, forbids recreation when neither live nor saved state exists, and adds the two stale-operation tests.
- P1-2 accepted. The plan now defines the first durable schema-2 admission as the monotonic compatibility boundary and restricts later recovery to schema-2-aware releases or forward corrective commits.
- P2/P3 findings remain visible and did not expand the plan scope.
