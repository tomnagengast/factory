I've completed the review — I read the full plan and research doc, and validated the design against the eventwire journal/wire, launcher, repository catalog, store, completion readers, server health endpoint, main.go wiring, and the README runbook. Here is the review.

---

# Adversarial review: ENG-36 plan (round 2)

Scope reviewed: `plans/planning/eng-36-support-safe-greenfield-repository-bootstrap/plan.md` (186 lines) against the repository at the worktree head, including the research doc it cites as its evidence base.

## Confirmed-sound foundations

The plan's core claims check out against source:

- Health today is unconditionally `200 ok` with no wire dependency (`internal/server/server.go:292-297`), so the degraded-health phase is real work, not duplication.
- The wire aborts all dispatch on the first handler error without acknowledging (`internal/eventwire/wire.go:98-99`), confirming the head-of-line failure mode the plan targets.
- The catalog is compiled-in with exactly network/notebook/factory and mandatory receipt/health fields (`main.go:168-198`, `internal/agentrun/repository.go:48-50`), so both the Artifacts entry and the optional-deployment-contract change are required, as planned.
- The launcher clones but never creates remotes, never validates origin/branch/root/symlinks before tmux (`internal/agentrun/launcher.go:62-91`), matching the plan's bootstrap gap analysis.
- Per-run repository routing already flows through `Trigger`/`Run` (`internal/agentrun/store.go:53-76`), `runRepository` fallback (`internal/agentrun/manager.go:372-377`), per-run launcher config (`launcher.go:250-258`), and per-repository completion readers (`internal/agentrun/completion.go:94-99`) — so Phases 2-4 extend existing seams rather than inventing new ones.
- Rejection channel-ack semantics are specified (research.md:60 advances global and channel acknowledgments atomically), which is consistent with `ReadChannel` ack gating (`journal.go:459-463`) and projection ordering in `dispatchLinear` (`server.go:609-621`).
- The verification commands are runnable as written: `agent-runs.json` is `{"runs": [...]}` with `issueIdentifier` keys (`store.go:70-72,155-158`), the tmux session name/socket are `factory-eng-33`/`factory-agents` (`launcher.go:331-333`, `main.go:39`), and the clean-tree check mirrors `completion_system.go:143`.

## P0/P1 findings (blockers)

**P1-1 — Rollback is foreclosed by the proposed journal encoding; the plan's rollback claim is unsound as written.**
Plan lines 44-46 and 58 add rejection records and a rejection-surviving checkpoint ("version-compatible rejection records"), line 126 claims "Existing v1 journal files remain readable," and line 129 relies on `network-app rollback factory` as the failure path. The research the plan binds itself to (research.md:62) prescribes writing a "version 2 checkpoint." But compatibility only runs one direction: the currently deployed binary hard-fails on any checkpoint with `Version != 1` (`internal/eventwire/journal.go:396`) and on any unknown journal line kind (`journal.go:424-425`), and journal `Open` failure is fatal at startup (`main.go:139-145`). Concrete failure mode: after the new release writes its first rejection line, or rewrites the checkpoint during compaction or a seed-change at `Open` (`journal.go:102-109`, `197-201`), rolling back restores a binary that cannot open `system-events.jsonl` — Factory will not start, and the append-only journal cannot be "fixed" without violating the plan's own no-rewrite invariant (research.md:46). This forecloses the plan's only recovery mechanism, permanently, from the first real rejection onward.
Smallest correction: pin the on-disk encoding in the plan — keep checkpoint `version: 1` and carry rejection totals/recent-list as additive optional fields (the old decoder ignores unknown JSON fields on `diskLine`), and emit the atomic rejection-plus-ack as the existing `"ack"` kind with additive rejection metadata fields rather than a new kind or version. Alternatively, explicitly amend the rollback section to state that rollback is unavailable once new-format lines exist and define the manual procedure. Either is a one-paragraph plan change; the current text promises both compatibility and rollback while its cited design breaks one with the other.

No P0. Creation is gated to exact catalog entries and private visibility, nothing is deleted or rewritten, deployment stays behind human merge, and the recovery steps are append-only — no catastrophic or irreversible path found.

## P2 (non-blocking)

- **P2-1 — Degraded-health coupling to completion evidence and deploy verification is unstated.** Plan line 50 makes any pending record return 503, but `SystemCompletionEvidence.readHealth` errors on non-200 (`completion_system.go:230-232`) and `HealthMatches` requires `status == "ok"` (`completion_system.go:81`); evidence errors repark the run (`completion.go:218-221`). Factory-repository run completions — including the ENG-36 run itself post-merge — will repark whenever any transient backlog exists at validation time, and the README's deploy verification `curl -fsS .../api/healthz` (README:214, 227) fails while degraded. Behavior converges once backlog clears and is arguably the intended "truthful" semantics, but plan line 21 claims existing completion "remain[s] compatible"; state the coupling explicitly so it isn't misread as a regression during Phase 5 verification.
- **P2-2 — Public information exposure of the last rejection.** `/api/healthz` is unauthenticated and public via factory.nags.cloud, and README:200 promises issue identifiers and error details stay behind authentication. Plan line 50 puts "the last rejection" (event identity, reason) into that response. Bound the public payload to counts, or explicitly accept the exposure.

## P3 (observations)

- **P3-1** — Plan lines 129/144 use `bin/network-app deploy factory` / `bin/network-app rollback factory`; the repo runbook documents `~/.local/bin/nags deploy --expected-commit ...` and `~/.local/bin/nags rollback factory --to ...` (README:206-236). Likely the same tool, but confirm the exact CLI form in the network repo before executing Phase 5.
- **P3-2** — The channel-acknowledgment rule for rejected records lives only in research.md:60; lift it into plan.md's "Event rejection" section, since `ReadChannel` gating (`journal.go:459-463`) and `AddAt` contiguity (`internal/linearhook/journal.go:105-106`) both silently misbehave if the implementer omits it.
- **P3-3** — The prior review file `reviews/plan-review-r1.md` is empty (0 bytes); if round-1 findings existed they are not recorded there.

## Verdict

The plan is well-grounded in source, phases are vertical, scope matches the issue, and the security posture of the bootstrap design fails closed. The single blocker is the journal-format/rollback contradiction, which needs a small plan-level encoding decision before implementation.

VERDICT: REVISE
P1-1
