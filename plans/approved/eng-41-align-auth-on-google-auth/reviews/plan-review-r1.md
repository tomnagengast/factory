I read the full plan and opened every source file, symbol, command, and route it depends on. Findings below.

## Verification performed
- Confirmed the legacy Basic surface exists exactly where the plan claims: `internal/viewerauth/auth.go:29` (`basicRealm`), `:42-43` (`Config.BasicUsername/BasicPassword`), `:56-57` (stored fields), `:86-88` (constructor requirement), `:149` (challenge header), `:158-166` (`Authenticated` Basic fallback).
- Confirmed `main.go:48` (`viewerUsername`), `:95-98` (required `FACTORY_VIEWER_PASSWORD`), `:109-110` (Config wiring), `:366` (observer redaction).
- Confirmed `nags.toml:8` secret entry and README lines 177-182, 195 (break-glass docs).
- Confirmed test surface: `internal/viewerauth/auth_test.go:39,122-134,249-250`; `internal/server/server_test.go:38,143-144,239,248,337,1540-1556,1598-1622`.
- Confirmed routed OAuth handlers exist so Phase 2's "drive login/callback through the handler" is feasible: `internal/server/server.go:308-326`.
- Confirmed no non-test in-repo consumer of `viewerauth.Config` beyond `main.go` and the two test files (repo-wide grep).
- Confirmed the "no frontend change" claim: `frontend/src` has no Basic/Authorization/password usage.
- Confirmed frontend verification commands are real (`frontend/bun.lock`, `frontend/package.json` `build`).

## P0 / P1
None. The plan is internally consistent, scoped to ENG-41, and every path/symbol/command it references is accurate against the current tree. Removing the Basic config fields does not break any non-test caller, the session-cookie mechanism it relies on already exists and is unchanged, and existing Google sessions stay valid because `sessionKey`/format are untouched.

## P2 / P3 (non-blocking)
- **P2** – Phase 2 step 1 (plan.md:112) adds a deterministic fake OAuth transport but does not state it must be injected into the *handler's* authenticator (`testViewerAuth` `Config.HTTPClient`, server_test.go:1540-1556). Because the session helper drives the routed `/auth/google/callback` (server.go:318) on that same authenticator, the fake transport has to live there or the callback will attempt a real Google network call (`New` defaults `HTTPClient` to a live 10s client, auth.go:109-111). Intent is implied, but making it explicit removes the only real implementation ambiguity.
- **P3** – README rewrite (plan.md:128) should also update line 182, which describes `nags refresh-env` creating "the 48-character break-glass password" and a `0600` password file; deleting only lines 177-180/195 would leave a dangling password reference in the same section.
- **P3** – Verification matrix (plan.md:164) and research already enumerate `testViewerPassword`, `break-glass`, `basicRealm`, `SetBasicAuth`; grep coverage is complete. No action, noted only to confirm the negative-search is sufficient.

VERDICT: READY
