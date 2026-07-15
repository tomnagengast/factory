# ENG-49 Adversarial Plan Review Round 2

Both providers received the same rendered read-only review prompt. Claude completed successfully with `VERDICT: READY`. Codex completed successfully with `VERDICT: REVISE` on P1-1 and P1-2. Under the Factory dual-provider rule, the logical round verdict is `REVISE`.

## Claude review

Claude read the entire revised plan and the named settings, routing, registry, Run, launcher, server, auth, frontend, and documentation surfaces. It confirmed the pinning seam, coordinator boundary, partial reference protection, auth return-path requirement, `$do` prompt surface, strict legacy-decode risk, terminal compaction change, pre-deploy state filter, health response shape, and current trigger workflow-body leak.

It reported no P0/P1 blockers. Its non-blocking findings were:

- P2: admission-time and snapshot-read digest validation must use the identical canonical form after newline normalization.
- P3: dual-shape strict decoding likely belongs in a custom workflow JSON unmarshaller.
- P3: the native-tasks plan edit is external planning hygiene, not a PR diff.
- P3: verify no remaining writer relies on editing legacy trigger fields through the slim settings DTO.
- P3: verify the operator health port against actual serve configuration.

VERDICT: READY

## Codex review

### P1-1 - New feedback Runs still bypass workflow pinning

The current protected feedback path creates fresh continuation Runs outside generic routed admission: `server.go:811-823` calls `ClaimContinuation`, `agentrun/store.go:374-403` creates a Run without `InvocationID` or `PinnedWorkflow`, `launcher.go:508-520` writes `workflow.json` only when `InvocationID` is present, and `agent_commands.go:53-63` then resolves live settings at process start. Publication or deletion between claim and launch can change or break that new Run.

Smallest correction: pin the selected published definition and digest when creating every fresh feedback continuation Run, preserve an active Run's pin when feedback resumes it, restrict live-settings fallback to identifiable retained schema-1 Runs, and verify a queued continuation remains unchanged across publication/deletion.

### P1-2 - The rollback boundary lacks non-prunable evidence

The revised plan treated retained routing/Run identity as monotonic evidence, but terminal invocations and Runs are intentionally prunable (`triggerrouter/store.go:127-180`, `agentrun/store.go:813-823`). After both prune, a retained-state scan can incorrectly report the boundary was never crossed.

Smallest correction: persist a dedicated monotonic compatibility marker outside prunable records, set it no later than first schema-2 admission, preserve it through compaction, and test admission, terminal compaction, pruning, and reopen.

VERDICT: REVISE
P1-1, P1-2

## Parent disposition

- P1-1 accepted. The plan now pins fresh continuation Runs at claim, preserves active pins, writes snapshots for all new-shape pins, restricts live-settings fallback to identifiable retained schema-1 Runs, and adds the publication/deletion race verification.
- P1-2 accepted. The plan now defines a dedicated non-prunable monotonic `workflowRollbackIncompatible` settings marker, written conservatively before the first new-shape admission or continuation and preserved through all later updates, compaction, pruning, and reopen.
- P2/P3 findings remain visible and did not expand the plan scope.
