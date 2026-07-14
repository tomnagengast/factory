All checks pass. The remaining references are confined to plan/docs (this issue's own plan, plus an unrelated `eng-32` historical plan) and to tests that now assert the *absence* of the Basic challenge. No production code retains any Basic/password surface.

---

## Review: ENG-41 commit b49a05e

**Scope & correctness** – The diff exactly matches the approved plan's impacted-file list (`auth.go`, `auth_test.go`, `server_test.go`, `main.go`, `nags.toml`, `README.md`) with no scope creep into OAuth/session/route logic.

**Acceptance criteria verified:**

1. *Basic cannot authenticate* – `Authenticator.Authenticated` now returns `a.validSession(r)` only (`internal/viewerauth/auth.go:147-148`); the constant-time Basic comparison and the `basicUsername`/`basicPassword` fields, config fields, and constructor requirement are all removed. Negative coverage is real: `TestBasicAuthorizationDoesNotAuthenticate` (auth_test.go:122) sends a raw `Authorization: Basic …` header, fails the handler if it runs, and asserts a 302 to `/auth/google/login`. `server_test.go:139-146` sends a Basic header to every protected canonical API route and asserts `401` + empty challenge.
2. *Google OAuth still authenticates* – `viewerSessionCookie` (server_test.go:1649) drives the real `/auth/google/login` → state-cookie → `/auth/google/callback` flow through an injected fake OAuth transport (validating the `Bearer access-token` userinfo call) and applies the resulting `__Host-factory_session` cookie to protected page and settings requests. `go test ./internal/viewerauth/ ./internal/server/` passes.
3. *Plain 401, no Basic challenge* – API middleware no longer sets `WWW-Authenticate` (auth.go:136-139 region); both auth_test.go:39 and server_test.go:145 assert the header is empty on 401.
4. *Startup/config/docs no longer require the password* – `FACTORY_VIEWER_PASSWORD` env requirement, `viewerUsername` const, authenticator fields, and the redaction-list entry are gone from `main.go`; secret removed from `nags.toml`; README rewritten to Google-only.

**Test fidelity** – Tests exercise the production OAuth/session path rather than a bypass, per plan. `responseCookie` guards against grabbing a deletion cookie via the `MaxAge >= 0` filter. No duplicate `roundTripFunc`/`testOAuthResponse` declarations; `go vet` clean. The `crypto/subtle` import remains legitimately used at auth.go:248 (state comparison), so no dead import was left behind.

**Regressions** – None found. Public routes, OAuth callback, allowlist, and cookie attributes are untouched.

### Non-blocking

- **P3** – `plans/approved/eng-32-add-settings-page/plan.md:181` still references "break-glass authentication." It's a historical plan doc outside ENG-41's scope; no action required, just noting the stale phrasing.
- **P3** – `viewerSessionCookie` hardcodes the `__Host-factory_oauth_state` / `__Host-factory_session` cookie names as string literals rather than reusing exported constants (none are exported today). Acceptable given the package boundary; purely optional.

No P0/P1 findings.

VERDICT: READY
