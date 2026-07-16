# Plan review round 2: Claude

## Validation performed against source

- Round 1 P1 (Codex P1-1) correction is complete and feasible. The revised plan specifies a candidate build served through the production handler on `127.0.0.1:18092`, a temporary `HOME` and disposable fixture root, fail-closed gating on `FACTORY_BROWSER_FIXTURE=1`, an exact lifecycle, native success plus a real stale-revision `409`, and read-only treatment of port 8092 and public Factory.
- The fixture is technically realizable. `server.New` accepts `Config.Web fs.FS`, production injects `os.DirFS("frontend/dist")`, and existing tests already compose `New(Config{...})` with disposable stores and a stub Linear controller.
- Local viewer authentication is a browser-reachable production path. `viewerauth.Local` authorizes loopback `Host` and `Origin` without OAuth, so the fixture exercises the changed client under the production auth path.
- The provider-typing simplification is behavior-preserving. The route regex validates exactly `factory|linear`, the server returns provider-specific payloads, and the obsolete union and runtime discrimination are isolated.
- Every proposed shared symbol has consumers in both index-resident code and Tasks, so the shared-module boundary is justified.
- The trust boundary holds because the revision control and fixture live in `_test.go`, never compile into the production binary, bind loopback only, and use disposable stores.

No P0 or P1 findings.

## P2/P3 (non-blocking)

- P2: Require the environment-gated fixture test to `t.Skip` when the gate is absent so normal test suites cannot fail or hang.
- P3: `runStateLabel` could semantically live with the agent model rather than the activity shell; this is cosmetic.
- P3: Splitting detail ownership must carefully preserve the shared activity shell, error/loading guards, footer refetch control, and distinct document-title effects. The plan already requires preserving all JSX.

VERDICT: READY
