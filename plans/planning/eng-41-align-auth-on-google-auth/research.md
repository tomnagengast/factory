# ENG-41 research: align auth on Google Auth

## Research questions

1. What current user/password and Google authentication behavior is proven by the code and running service?
2. Which files, symbols, configuration, tests, routes, and callers participate?
3. What does "replace with Google Auth" require, and what behavior must remain unchanged?
4. Which acceptance criteria are observable, and how can each be verified?
5. What security, compatibility, migration, rollout, and rollback risks exist?
6. What exact command deploys the changed Factory surface from merged `main`, and what proves deployment health and identity?
7. Which apparent ambiguities can repository and issue evidence resolve, and which require an owner decision?

## Context and acceptance criteria

Linear issue [ENG-41](https://linear.app/nags-cloud/issue/ENG-41/align-auth-on-google-auth) says Factory currently has legacy user/password authentication and newer Google OAuth, and asks to replace the former with the latter. The issue supplies no separate checklist. The observable acceptance criteria derived from that direction are:

1. A protected request cannot authenticate with the legacy username/password mechanism.
2. Protected browser pages continue to enter the existing Google OAuth flow.
3. A verified, allowlisted Google identity continues to receive a secure session that authorizes protected pages and same-origin APIs.
4. Factory no longer requires, consumes, or declares `FACTORY_VIEWER_PASSWORD` in this repository.
5. Documentation and tests describe and exercise one authentication mechanism, Google OAuth sessions.
6. Public health/home behavior, protected route boundaries, session security, lifecycle gates, and deployment safeguards remain unchanged.

## Evidence-backed answers

### 1. Current behavior and failure mode

Observed in `internal/viewerauth/auth.go`:

- Google OAuth uses the Google authorization, token, and OpenID userinfo endpoints, signed `__Host-factory_oauth_state` and `__Host-factory_session` cookies, a 10-minute state lifetime, and a 24-hour session lifetime (`internal/viewerauth/auth.go:23-33`).
- `Login` signs a state value and safe return path before redirecting to Google for `openid email` (`internal/viewerauth/auth.go:168-198`). `Callback` exchanges the code, requires a non-empty subject, a verified email, and membership in the configured allowlist, then issues the signed session cookie (`internal/viewerauth/auth.go:200-246`). `validSession` re-verifies the signature, expiry, and current allowlist on every protected request (`internal/viewerauth/auth.go:344-354`).
- The same `Authenticator` also carries `BasicUsername` and `BasicPassword`, requires both during construction, and stores them for constant-time comparison (`internal/viewerauth/auth.go:36-60,75-130`). `Authenticated` authorizes either a valid Google session or matching HTTP Basic credentials (`internal/viewerauth/auth.go:158-166`). Basic therefore bypasses the Google subject, verified-email, and allowlist checks rather than participating in them.
- Protected pages redirect unauthenticated requests to `/auth/google/login`, while protected APIs return `401` and advertise `WWW-Authenticate: Basic realm="Factory agents"` (`internal/viewerauth/auth.go:133-156`). The API challenge actively presents the legacy mechanism as the remedy even though browser navigation uses Google.

Observed in the running deployment on 2026-07-14 PDT:

- `GET https://factory.nags.cloud/wire` returned `302` to `/auth/google/login?next=%2Fwire`.
- An unauthenticated `GET https://factory.nags.cloud/api/wire` returned `401` with the Basic challenge.
- The same API returned `200` when supplied the documented local Basic credential without printing it.

Git history adds one useful contradiction: commit `145b2fb` introduced the Google authenticator and its Basic fallback together. "Legacy" is therefore the issue owner's current product classification, not a chronological claim about when the code landed. The requested outcome is still unambiguous: retire the Basic branch and keep the Google branch.

### 2. Participating files, interfaces, and callers

- `internal/viewerauth/auth.go` owns both mechanisms. The legacy-only surface is `basicRealm`, `Config.BasicUsername`, `Config.BasicPassword`, `Authenticator.basicUsername`, `Authenticator.basicPassword`, the constructor requirement, the Basic fallback in `Authenticated`, and the API challenge header (`internal/viewerauth/auth.go:29,36-60,75-130,145-166`).
- `internal/server/server.go` correctly centralizes protection through `viewerAuth.API` for `/api/wire`, `/api/agents`, and `/api/settings`, and `viewerAuth.Page` for `/wire`, `/agents`, and `/settings`; it also mounts the Google login, callback, and logout routes (`internal/server/server.go:308-326`). The route table does not require a structural change.
- `main.go` hard-requires `FACTORY_VIEWER_PASSWORD`, passes the fixed username `factory` and password into the authenticator, and includes the password in observer redactions (`main.go:47-48,95-115,353-372`). The other redacted values remain sensitive and must stay covered.
- `nags.toml` declares `FACTORY_VIEWER_PASSWORD` as one of Factory's required deployment secrets (`nags.toml:1-13`). Removing it makes the deployed manifest stop injecting the obsolete runtime input.
- `internal/viewerauth/auth_test.go` asserts the Basic challenge, proves Basic succeeds, and configures every test authenticator with Basic credentials (`internal/viewerauth/auth_test.go:16-42,122-134,242-255`). These assertions must invert or disappear while the OAuth callback/session coverage remains.
- `internal/server/server_test.go` uses Basic authentication as its general authenticated-request fixture. It has direct Basic uses for settings and an agent page plus shared `authenticatedRequest` and `authenticatedSettingsRequest` helpers (`internal/server/server_test.go:226-253,331-345,1540-1556,1598-1621`). Those tests need sessions produced through the real Google login/callback handlers so they continue testing production authentication semantics rather than a test-only backdoor.
- The frontend sends same-origin credentials on all protected API reads and settings writes (`frontend/src/index.tsx:248-322`). Browser session cookies therefore already satisfy the frontend contract; no frontend change is required.
- `README.md` documents OAuth plus Basic break-glass access, the password file, password creation during `refresh-env`, and the password variable (`README.md:173-197`). Those claims become stale when Basic is removed.
- Exact-string repository search found no non-test internal client that sends Factory Basic credentials. Linear and provider `Authorization` headers elsewhere are unrelated external-service authentication and must not change.

The graph-oriented `nr` discovery required by repository instructions was attempted first. Its worktree index reported 90 files but no embeddings or graph edges and returned no usable ranking, so the map above was verified with exact searches, full relevant-file reads, blame/history, and the independent tmux child research report at `/Users/tom/.local/share/factory/runs/run-6fbf076c94f95119/children/auth-map-2e0ad997/`.

### 3. Required change and preserved behavior

The minimal complete replacement is:

1. Make `Authenticator.Authenticated` accept only a valid Google session. Remove all Basic-specific config, fields, validation, constant-time comparisons, realm, and challenge behavior. Protected APIs should retain their current non-redirecting `401` contract but omit the misleading Basic challenge.
2. Stop reading and requiring `FACTORY_VIEWER_PASSWORD` in `main.go`; stop passing it to auth and observer redactions; remove it from `nags.toml`.
3. Replace Basic-based tests with OAuth callback-created session cookies and add an explicit regression assertion that a Basic Authorization header cannot authenticate.
4. Remove Basic/password documentation and describe Google OAuth as the sole protection for private pages and same-origin APIs.

The following remain unchanged:

- Google state generation, code exchange, verified-email allowlist, signed session claims, cookie flags and lifetimes, logout, safe return-path validation, and security headers.
- The public `/`, `/home`, `/api/home`, and `/api/healthz` surfaces and the private wire, agent, and settings surfaces.
- Same-origin enforcement for settings writes.
- Observer redaction for current secrets such as the Linear key, webhook secret, Google client secret, session key, and GitHub token.
- Factory lifecycle contract v1, human-only merge authority, exact verified-head deployment gating, repository routing, receipts, and health identity.

### 4. Verification mapping

| Acceptance criterion or risk | Evidence and exact verification |
| --- | --- |
| Basic can no longer authenticate | Focused `go test ./internal/viewerauth -run 'TestPageRedirectsToGoogleLoginAndAPIReturnsUnauthorized|TestBasicAuthenticationIsRejected'`; assert no `WWW-Authenticate` Basic challenge and no protected handler execution. Server tests exercise Basic rejection at the routed surface. |
| Google session still authorizes pages and APIs | Existing callback test plus server integration helpers that traverse `/auth/google/login` and `/auth/google/callback` using a deterministic fake Google token/userinfo transport. Run `go test ./internal/viewerauth ./internal/server`. |
| Unlisted/unverified identities and tampered state remain rejected | Existing `internal/viewerauth` callback, tampered-state, signed-value, expiry, and safe-return tests via `go test ./internal/viewerauth`. |
| No Factory password runtime/config surface remains | `rg -n 'FACTORY_VIEWER_PASSWORD|BasicUsername|BasicPassword|SetBasicAuth|basicRealm|testViewerPassword|break-glass|factory-viewer-password' -- main.go nags.toml internal README.md` must return no legacy Factory auth references. Unrelated external-service Authorization headers remain. |
| Public/private routing and settings security remain stable | `go test ./internal/server`, then full required Go suites. |
| Repository quality gate | `go test ./...`; `go test -race ./...`; `go vet ./...`; `export MISE_BUN_VERSION=1.3.11; bun install --cwd frontend --frozen-lockfile`; `export MISE_BUN_VERSION=1.3.11; bun run --cwd frontend build`. |

### 5. Security, compatibility, migration, rollout, and rollback

Security improves by eliminating a long-lived shared credential that bypasses Google identity verification and the email allowlist. Existing session protections remain the sole authority.

The intentional availability tradeoff is that a Google outage or allowlist misconfiguration can prevent access to private operator surfaces. ENG-41 explicitly asks to replace the legacy mechanism, so this is an accepted consequence rather than an unresolved design choice. Public health and aggregate home probes remain available for diagnosis, and deployment/rollback do not require the private web API.

Any undocumented curl/script consumer using Basic will stop working. No such consumer exists in this repository. Private API callers must use a browser-issued Google session cookie; unauthenticated APIs continue returning `401` rather than redirecting and no longer prompt for an unusable password.

There is no data or schema migration. Existing Google session cookies remain valid because claim format, key, allowlist checks, and cookie name do not change. The old password environment value and `~/.config/network-app/factory-viewer-password.txt` may remain temporarily as unused provider-managed material after deployment. The provider's `refresh-env` implementation lives outside the allowlisted `tomnagengast/factory` repository, so modifying it from this run would violate repository isolation. Removing that orphaned provider-side material is follow-up provider ownership, not a condition for eliminating Factory's runtime mechanism.

Rollout is one clean merged-main Factory deployment. The installed compatibility command is `/Users/tom/.local/share/nags/provider/bin/network-app`, which delegates to `bin/nags` and accepts `deploy factory --expected-commit <oid>`. Deployment atomically activates an immutable release and automatically restores the prior verified release when verification fails. After an irreversible merge, recovery is a reviewed corrective or revert commit on `main`, followed by the same commit-pinned deployment. An explicit retained-release rollback is available with `~/.local/bin/nags rollback factory --to <deployment-id>`.

### 6. Deployment, health, content, and recovery evidence

The running service and successful receipt currently agree on commit `8a6bf5082dc5b622a0b8e0dc5e77248ad1a7bab9`, tree `0db3ba9f1cb6787dde1c757ae764ee3bc8bfd27f`, deployment `20260714T015159Z-8a6bf5082dc5-44980`, and lifecycle contract `1`. `nags doctor factory --json` reports healthy receipt and process checks.

After a human merges the exact locally verified PR head, update only `/Users/tom/repos/tomnagengast/factory` to fetched `origin/main` and require it to be clean and equal to upstream. From that primary checkout, run:

```bash
/Users/tom/.local/share/nags/provider/bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
curl -fsS http://127.0.0.1:8092/api/healthz | jq .
curl -fsS https://factory.nags.cloud/api/healthz | jq .
jq . ~/.local/share/factory/deployments/current.json
/Users/tom/.local/bin/nags doctor factory --json
```

Both health responses and `current.json` must agree on commit, tree, build ID, deployment ID, and contract version, and the receipt source commit must contain the human merge commit. A post-deploy content probe must also confirm `/wire` redirects to `/auth/google/login`, unauthenticated `/api/wire` returns `401` without a Basic challenge, and a request carrying the retired Basic credential does not return `200`; the credential must not be printed.

If deployment or identity verification fails, preserve pending/failed receipts and inspect `~/Library/Logs/factory.err`, local/public health, `~/.local/share/factory/deployments/`, and the active release. Confirm automatic recovery of the previous release. Use a reviewed corrective or revert commit and redeploy from clean updated `main`; use the explicit retained-release rollback only when the deployment runbook calls for it.

## Contradictions and assumptions

- Contradiction resolved: Basic and OAuth landed together, despite the issue calling Basic legacy. The owner-directed target state, not chronology, controls scope.
- Assumption resolved from existing middleware behavior: APIs should remain non-redirecting and return plain `401`; only the Basic challenge is obsolete. Redirecting fetches to HTML would break the frontend's JSON contract.
- Assumption resolved from repository search: no supported in-repository non-browser client depends on Basic.
- Assumption resolved from Factory routing: the provider's out-of-repository password minting is not writable scope for ENG-41. Factory stops consuming and declaring the secret here.

## Unresolved questions

None. The issue supplies the product decision to make Google OAuth the only Factory viewer authentication mechanism, and current code, tests, routing, and deployment evidence determine the smallest complete implementation.
