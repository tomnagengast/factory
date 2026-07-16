# Full SDLC

Own one Linear engineering issue from intake through a human merge, verified deployment from updated main, and cleanup. Treat the work as a persistent lifecycle, not a request to propose changes.

## Terminal conditions

Finish only after the implementation satisfies the issue and reviewed plan, all relevant verification passes, a human merges the exact verified head of every pull request the plan created, merged checks and review safeguards remain clear, deployment succeeds from updated clean main or the plan proves none applies, remote and local issue branches are cleaned up, and Linear contains the final evidence with no unanswered feedback.

The principal never merges, enables auto-merge, bypasses protection, deploys from an issue worktree, or invents a deployment target. Human merge authority and Factory's mechanical validators are authoritative.

## Intake and resume

1. Fresh-read the complete Linear issue and conversation with `factory agent linear-graphql`, including project routing, labels, parent or child issues, attachments, and linked evidence.
2. Resolve the primary repository through the Linear project's routing metadata. In a managed Factory run, use `FACTORY_REPO_PATH` as the primary mutable repository and deployment source after confirming its origin matches the project metadata. `FACTORY_REPOSITORIES` lists every admitted repository on this machine as JSON.
3. Search durable memory and repository history for prior decisions. Detect an existing branch, Worktrunk checkout, pull request, checkpoint, or merged result before creating anything.
4. Resume the first incomplete lifecycle boundary. Never duplicate a branch, pull request, gate comment, plan, or completed implementation.
5. Use Worktrunk for every worktree operation. Preserve unrelated human changes and keep repository-specific state isolated.

## Cross-repository authority

The run owns its issue end to end, wherever the required changes live. Its write authority spans every repository admitted on this machine, not only the routed primary. When research or review shows the correct change spans additional repositories, include those changes in the same plan and implement them in this run. Never stop to request a coordination issue, a separately routed run, or new authority for another repository; that blocker class does not exist.

For each additional repository: use Worktrunk in that repository's listed path, keep one branch and one draft pull request per repository using the same required branch prefix, and cover every repository's diff in the same adversarial review rounds. The plan must state merge and deployment sequencing across repositories. Each pull request still requires its own human merge with exact verified-head proof, and each repository deploys only per its own contract from its own updated clean main.

## Research

Write a bounded set of questions whose answers can change design, scope, risk, sequencing, verification, deployment, or recovery. Answer every question with repository or runtime evidence before planning. Use relatedness discovery before exact searches in unfamiliar code, then read the complete relevant source, tests, configuration, documentation, and history.

Research must establish current behavior, root cause, participating interfaces and callers, invariants that must remain unchanged, compatibility and security constraints, observable acceptance criteria, exact deployment and post-deploy checks, and any decision that cannot be derived safely.

Save research at `plans/planning/<branch>/research.md`, commit it, push it, and maintain one draft pull request. Refresh Linear labels immediately before the research gate. A current `Yolo` label permits immediate progression after the normal artifact and gate comment are published, and it covers the complete plan scope, including changes in additional repositories. Otherwise require explicit human approval through an affirmative reaction or contextual reply. Address requested revisions in the exact thread and do not plan while the gate is waiting.

## Plan and adversarial review

Create `plans/planning/<branch>/plan.md` with an ISO-8601 update timestamp immediately below the title. Include issue context, acceptance criteria, evidence-backed research, root cause, decisions and alternatives, non-goals, impacted files and interfaces, vertical implementation phases, migration and rollback handling, exact post-merge deployment and recovery commands, a verification matrix, and unresolved questions. When the plan spans repositories, name every repository it touches and state per-repository branches, pull requests, and merge and deployment sequencing.

For each adversarial review round, render one identical read-only prompt and spawn one Claude child and one Codex child through the Factory helper. Spawn both before waiting. Each reviewer reads the complete plan and relevant repository evidence, reports P0-P3 findings, and ends in READY or REVISE. Both usable reviews form one logical round. P0/P1 findings require the smallest matching plan correction and another complete dual-provider round. P2/P3 findings remain visible and do not expand scope.

If exactly one provider fails operationally or has no usable verdict, preserve the failure and use the other result without a fallback round. If neither yields authority after safe retries, stop before implementation with the allowed authority blocker.

Commit and push the reviewed plan and review evidence. Refresh Linear immediately before the plan gate. A current `Yolo` label permits immediate progression after the reviewed plan and gate comment are published. Otherwise require explicit human approval. After approval, move the plan and reviews to `plans/approved/<branch>/` and commit that move before editing implementation files.

## Implementation

Re-read the approved plan and current status. Implement one vertical phase at a time, run focused checks after each coherent change, and commit logical increments in the repository's established style. Preserve public behavior and trust boundaries unless the approved plan explicitly changes them.

If implementation disproves a plan premise, return to research and reviewed planning. Do not patch around a false premise. Review each diff for accidental churn, secrets, debug artifacts, stale comments, generated output, and unrelated files.

Use current language and framework conventions. Keep mechanical safeguards in code. Editable workflow instructions cannot waive routing, one-run ownership, human merge, checkpoint, exact-head, deployment-source, completion, or cleanup validation.

## Verification

Execute the approved verification matrix and add focused checks for risks discovered during implementation. Select verification by changed surface: tests, race detector, build, lint, typecheck, static analysis, realistic API or CLI flows, browser behavior, data compatibility, and security probes as applicable. Verify every repository the plan touches.

Check for an existing development server before starting one. Record and stop every temporary process. For an interactive web change, inspect the authenticated app at desktop and mobile sizes, exercise keyboard and focus behavior plus loading, empty, error, conflict, offline, and success states, and inspect console and network failures.

For Factory itself, final publication requires:

```text
go test ./...
go test -race ./...
go vet ./...
MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend typecheck
MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build
```

## Pull request green loop

Push the implementation, update each draft pull-request body with problem, decisions, risks, non-goals, exact verification evidence, approved-plan path, and exact locally verified head, then mark it ready for review. Ensure Linear links every pull request, move the issue to the unambiguous review state, and publish one implementation summary with the verified heads.

Use `factory agent github-events` as a durable wake signal and refresh authoritative GitHub state after every event or timeout. Inspect all reported and required checks, merge state, review decision, issue comments, inline comments, reviews, and unresolved threads. Diagnose failures from logs, make only in-scope fixes, re-run affected verification, commit, and push. Address every actionable request with evidence.

Use `factory agent linear-comments` concurrently for Linear feedback wakes. After every wake, fresh-read the complete conversation with `factory agent linear-graphql`. Address every later contextual human comment, verify accepted changes, and reply in the same thread. Wake events are never authority by themselves.

Every Factory-authored Linear comment must end with one reserved signature on its final non-empty line. Use either:

```text
🐘
```

or an exact inline-code coordination marker:

```text
🐘 `codex-do:TEAM-123:phase:r1`
```

Emoji or marker prose elsewhere is not a signature, and no prose may follow the footer.

The ready boundary requires, for every pull request the plan created, an open non-draft mergeable pull request, a non-regressed merge state, no requested changes, all reported and required checks passing or legitimately skipped, no actionable unresolved thread or comment, no unanswered Linear feedback, and an exact local head matching GitHub.

Write the Factory ready checkpoint for the routed primary repository's exact verified head and return the required ready terminal marker. Tell the human to use **Create a merge commit** on every pull request, in the plan's stated order. A squash or rebase merge does not preserve the checkpointed head ancestry and blocks deployment. Do not keep a principal process alive waiting for the ordinary human merge.

## Post-merge deployment and cleanup

On a post-merge segment, reconstruct the repository, pull request, base, head, and verified head from the retained checkpoint and durable pull-request or Linear evidence. Fresh-read GitHub and Linear. Continue only when GitHub authoritatively reports a merge commit and the reported merge contains the exact checkpointed head as an ancestor. A squash or rebase replay without that ancestry is not the verified head. Apply the same verified-head proof to every additional repository's pull request before deploying that repository.

Repeat the final check, review, comment, thread, and Linear safeguards. Resolve exactly one main Worktrunk checkout per repository and require it to be the managed repository. Refuse to stash, reset, overwrite, or deploy around a dirty or divergent main checkout.

Fetch and prune origin, fast-forward the tracked default branch, and require local main to match fetched upstream before deployment. Run the approved deployment command only from that updated clean primary checkout, followed by every approved health, identity, content, receipt, and recovery probe. Deployment commands come from the approved plan and each repository's own deployment contract; a repository without one must be proven to need no deployment.

For Factory self-deployment use:

```text
~/.local/bin/nags deploy --expected-commit "$(git rev-parse HEAD)"
```

After successful deployment, verify GitHub auto-deleted each remote issue branch, fetch and prune, ensure all child windows are complete and consumed, and remove each clean integrated issue checkout with Worktrunk without force. Re-run deployment health and refresh GitHub and Linear one final time. Move Linear to its unambiguous completed state and publish merge, deployment, and cleanup evidence.

If a genuine blocker is reached, preserve coherent verified work, report the first incomplete boundary and exact needed action in Linear, and return only a blocker type allowed by the Factory runtime protocol.
