# ENG-41: Align auth on Google Auth

> updated: 2026-07-14T22:41:03Z

## Outcome

Factory will use Google OAuth sessions as its only viewer authentication mechanism. HTTP Basic authentication and the `FACTORY_VIEWER_PASSWORD` deployment secret will be removed without changing the existing public-route boundary, OAuth allowlist, session security properties, or human-controlled Factory lifecycle.

## Issue and acceptance criteria

Linear ENG-41 asks Factory to replace its old username/password viewer access with Google Auth. The implementation is complete when:

1. An HTTP Basic `Authorization` header cannot authenticate any protected Factory route.
2. Protected browser pages continue to redirect unauthenticated users into the Google OAuth login flow.
3. A verified, allowlisted Google identity receives a signed session cookie that authenticates protected pages and APIs.
4. Factory no longer reads, requires, documents, or tests `FACTORY_VIEWER_PASSWORD` or its fixed `factory` username.
5. Unauthenticated API requests still return a non-redirecting `401 Unauthorized`, but no longer advertise a Basic challenge.
6. Public routes, OAuth callback behavior, email allowlisting, cookie protections, deployment gates, and human-only merge authority remain unchanged.

## Research questions and evidence

### Where does legacy authentication enter the system?

- `internal/viewerauth/auth.go` stores Basic credentials, requires them in `New`, checks them as a fallback in `Authenticated`, and emits `WWW-Authenticate: Basic` from API middleware.
- `main.go` requires `FACTORY_VIEWER_PASSWORD`, supplies the fixed username `factory`, passes both fields to `viewerauth.New`, and adds the password to observer redactions.
- `nags.toml` declares the password as a required deployment secret.
- `README.md` documents password-file generation and Basic break-glass access.
- `internal/viewerauth/auth_test.go` and `internal/server/server_test.go` authenticate protected requests with Basic credentials.

### Is Google authentication already production-capable?

Yes. The existing flow redirects through Google, validates OAuth state, exchanges the authorization code, fetches user info, requires a subject and verified email, checks the configured allowlist, and issues a signed, secure, host-only session cookie with a 24-hour lifetime. Session validation rechecks the signature, expiry, and allowed email.

### Does the frontend depend on Basic authentication?

No. Browser API calls are same-origin requests and already send session cookies. No frontend source change is required.

### Should unauthenticated APIs redirect to Google?

No. The current split is intentional: page middleware redirects to login, while API middleware returns `401`. Redirecting `fetch` calls to an HTML OAuth flow would violate the JSON API contract. The API response will remain `401` while removing only the Basic challenge header.

### Are there repository consumers or migrations to preserve?

No non-test in-repository caller depends on Basic authentication, and there is no stored auth schema or data migration. Existing valid Google session cookies remain valid. The external provider still knows how to create the retired password secret, but changing that separate repository is outside ENG-41; Factory will simply stop consuming and requiring it.

### What does history clarify?

Basic and Google authentication were introduced together in commit `145b2fb`; “legacy” is the product classification in ENG-41 rather than a strict chronological claim. This does not alter the requested target state.

## Root cause

Factory's authenticator models two successful viewer identities: a Google-backed signed session and an HTTP Basic credential. Because the Basic credential is wired through startup configuration, deployment metadata, middleware challenges, documentation, and tests, removing only its runtime comparison would leave a misleading required secret and an incorrectly advertised API contract. The full legacy surface must be removed as one coherent change.

## Decisions

- Make a valid Google session the sole success condition in `Authenticator.Authenticated`.
- Preserve separate page and API unauthenticated behavior: pages redirect; APIs return plain `401`.
- Remove the Basic challenge instead of replacing it with another authentication header.
- Exercise protected server routes through the real OAuth handlers in tests, using an injected fake OAuth transport for deterministic token and user-info responses.
- Keep the existing Google OAuth configuration, allowlist rules, cookie format, cookie attributes, and route table unchanged.
- Remove the password from Factory configuration and documentation, but do not edit the external `nags` provider repository.

## Alternatives considered

- **Keep Basic as break-glass access:** rejected because it directly contradicts the Google-only acceptance criterion and continues a shared-secret bypass.
- **Redirect API clients to `/auth/google/login`:** rejected because API callers expect a `401` JSON-compatible boundary, not an HTML redirect chain.
- **Add a test-only authentication bypass:** rejected because it would hide regressions in the production OAuth/session path. Tests can use the existing injected HTTP client instead.
- **Change the session or OAuth implementation while removing Basic:** rejected as unrelated risk. ENG-41 needs authentication consolidation, not a session redesign.
- **Clean up password generation in the provider repository:** deferred because repository routing confines this issue to `tomnagengast/factory`; Factory can safely stop declaring the secret independently.

## Assumptions

- At least one allowed Google identity remains available to operators after deployment.
- The deployed Google OAuth client ID, client secret, base URL, and allowed-email configuration are valid and unchanged.
- Consumers treat an unauthenticated protected API response as `401` and do not require the retired `WWW-Authenticate: Basic` header.

## Non-goals

- Changing the Google OAuth provider, scopes, allowlist, cookie lifetime, or cookie signing format.
- Adding roles, multiple authorization tiers, service tokens, or a new break-glass mechanism.
- Changing public versus protected route classification.
- Editing or deploying the separate `nags` provider repository.
- Merging the pull request without a human or deploying an unmerged branch.

## Impacted files and interfaces

- `internal/viewerauth/auth.go`: remove Basic configuration/state, constructor validation, fallback comparison, and API challenge header.
- `internal/viewerauth/auth_test.go`: replace Basic-success coverage with explicit Basic-rejection and no-challenge coverage while retaining OAuth/session security tests.
- `internal/server/server_test.go`: authenticate protected requests through deterministic Google callback/session helpers and update unauthenticated API assertions.
- `main.go`: remove fixed viewer username, password environment requirement, authenticator fields, and password redaction.
- `nags.toml`: remove `FACTORY_VIEWER_PASSWORD` from required secrets.
- `README.md`: document Google-only viewer access and remove password-file setup.

No production route, frontend API, persistent data, or external Go package interface is added. `viewerauth.Config` intentionally loses the internal-repository-only `BasicUsername` and `BasicPassword` fields.

## Implementation phases

### Phase 1: Collapse the authenticator to Google sessions

1. Remove Basic constants, configuration fields, stored credential fields, and constructor requirements from `internal/viewerauth/auth.go`.
2. Make `Authenticated` delegate solely to existing signed-session validation.
3. Keep API middleware's `401` response and remove its `WWW-Authenticate` header.

Success criteria:

- A request containing formerly valid Basic credentials remains unauthenticated.
- A valid allowlisted Google session remains authenticated.
- Page and API middleware retain their distinct unauthenticated status behavior.

### Phase 2: Rewire integration tests through OAuth

1. Add a deterministic fake OAuth HTTP transport in `internal/server/server_test.go` that returns a token and a verified, allowlisted Google identity.
2. Add a helper that invokes `/auth/google/login`, carries the state cookie into `/auth/google/callback`, and applies the resulting session cookie to protected test requests.
3. Replace direct `SetBasicAuth` calls and shared Basic helpers with that session helper.
4. Update the unauthenticated API assertion to require no Basic challenge.
5. Update focused authenticator tests to prove Basic headers do not bypass the handler and APIs do not advertise Basic.

Success criteria:

- Server tests validate the same OAuth callback and signed-cookie mechanism used in production.
- Protected route tests pass without any Basic credential configuration.
- Security-negative tests explicitly cover the retired bypass.

### Phase 3: Remove startup, deployment, and documentation surfaces

1. Remove password loading, fixed username wiring, and password redaction from `main.go`.
2. Remove the password secret from `nags.toml`.
3. Rewrite the README viewer-authentication section around Google OAuth and delete password generation/setup instructions.

Success criteria:

- Factory starts without `FACTORY_VIEWER_PASSWORD` when the remaining required OAuth settings are present.
- Repository configuration and operator documentation describe only Google viewer authentication.
- A repository search finds no active legacy Basic viewer-auth surface.

### Phase 4: Verify and publish the exact head

1. Format changed Go files and run focused authenticator/server tests.
2. Search production, tests, manifest, and README for retired identifiers.
3. Run all mandated backend and frontend verification commands.
4. Commit and push the verified code, update the draft PR with evidence, and remediate blocking review or CI findings without widening scope.
5. Re-run affected verification after every code change and record the exact verified head before presenting the PR for human merge.

Success criteria:

- All required local checks and required GitHub checks pass on one exact commit.
- No unresolved P0/P1 review finding remains.
- The ready-for-merge checkpoint names the same verified head as the PR.

## Security, compatibility, and migration

The change closes a second credential path and reduces the secret surface. It preserves OAuth state validation, verified-email enforcement, email allowlisting, HMAC-signed sessions, expiry checks, and secure host-only cookies. API clients still receive `401`; the only wire-level compatibility change is removal of the Basic challenge and rejection of Basic credentials. Browser clients continue to use cookies with no frontend migration. Existing Google session cookies remain readable because the session format and signing secret do not change.

Operators lose the Basic break-glass path at deployment. This is intentional and accepted by ENG-41; rollout depends on working Google OAuth configuration and an allowed operator identity. No database or file migration is needed.

## Verification matrix

| Requirement | Evidence |
| --- | --- |
| Basic cannot authenticate | Authenticator negative test and protected server request behavior |
| Browser pages use Google | Existing page redirect tests plus OAuth login/callback session helper |
| Allowed Google session authenticates | Focused OAuth/session tests and protected server route tests |
| APIs preserve non-redirecting boundary | API middleware test asserts `401` and an empty `WWW-Authenticate` header |
| Password surface is removed | Repository search for `FACTORY_VIEWER_PASSWORD`, Basic config fields, `SetBasicAuth`, fixed test password, and break-glass documentation |
| Backend is healthy | `go test ./...`, `go test -race ./...`, and `go vet ./...` |
| Frontend remains buildable | `MISE_BUN_VERSION=1.3.11 bun install --cwd frontend --frozen-lockfile` and `MISE_BUN_VERSION=1.3.11 bun run --cwd frontend build` |
| Exact head is reviewable | GitHub checks and reviews refreshed against the recorded commit OID |

## Rollout, deployment, and recovery

Merge authority remains human-only. Before the merge boundary, record the validated ready-for-merge checkpoint for PR #10 and stop the run. Do not deploy the branch or the T9 working mirror.

After a human merges the PR:

1. Update the primary checkout at `/Users/tom/repos/tomnagengast/factory` to clean `main` and verify the merged PR commit is contained in `HEAD`.
2. Re-run the required backend and frozen frontend checks on that exact clean `main` commit.
3. Deploy from the primary checkout only:

   ```sh
   /Users/tom/.local/share/nags/provider/bin/network-app deploy factory --expected-commit "$(git rev-parse HEAD)"
   ```

4. Verify deployment identity and health:

   ```sh
   curl -fsS http://127.0.0.1:8092/api/healthz | jq .
   curl -fsS https://factory.nags.cloud/api/healthz | jq .
   jq . ~/.local/share/factory/deployments/current.json
   /Users/tom/.local/bin/nags doctor factory --json
   ```

5. Probe behavior without exposing secrets: `/wire` redirects to Google, unauthenticated `/api/wire` returns `401` without a Basic challenge, and a Basic header does not return `200`.
6. Publish deployment evidence to Linear and clean only issue-scoped branch/worktree artifacts after all post-deploy checks succeed.

If deployment or safeguards regress, inspect the release receipt and service logs first. The deployer can restore the previous release automatically; if explicit rollback is required, use `/Users/tom/.local/bin/nags rollback factory --to <deployment-id>`, verify both health endpoints, and record the failed and restored deployment identities. Any corrective code must be reviewed, human-merged, and deployed from updated clean `main` through the same exact-commit gate.

## Unresolved questions

None.
