# OIDC Flow — How TenantFlow authenticates (from memory, checked against code)

> Written to be reproducible from memory: a one-sitting walk of every hop a
> token takes from "user clicks Sign in" to "Go handler runs". If you can
> redraw this without looking at the code, you can answer interview question
> #3 ("How does OIDC/OAuth2 actually work?") with this project's specifics.

## 0. The cast

| Thing | What it is here |
| --- | --- |
| IdP | Keycloak 26 (Docker), realm `tenantflow` |
| Issuer | `http://localhost:8081/realms/tenantflow` |
| Client | **one confidential client** `tenantflow-api` (secret `api-secret-123`), redirect URI `http://localhost:3000` — shared by the dashboard *and* used as the API's audience |
| Dashboard | Next.js 16 App Router + Auth.js (next-auth v5 beta, Keycloak provider) |
| API | Go, `net/http`, OIDC middleware in `internal/auth` |
| Roles | `platform-admin`, `platform-operator` — carried in the JWT, not looked up anywhere |

The one diagram that explains the whole project:

```
┌──────────┐ ① sign-in   ┌─────────────┐ ② authorize   ┌──────────┐
│ dashboard│ ──────────► │   Auth.js   │ ─────────────► │ Keycloak │
│ (Next.js)│             │ (middleware)│                │ (IdP)    │
└──────────┘             └─────────────┘                └──────────┘
      ▲                       │ ▲                            │
      │                       │ │ ③ code (after login+consent│
      │                       │ └────────────────────────────┘
      │                       ▼ ④ code → token exchange (client_secret)
      │                ┌──────────────┐
      │                │ id_token +   │
      │                │ access_token │
      │                └──────────────┘
      │
      │ ⑤ fetch /api/tenants with
      │    Authorization: Bearer <access_token>        ┌──────────┐
      ▼                                                │ Go API   │
┌──────────┐  ⑥ proxy route handler forwards the SAME │ (9090)   │
│ Next.js  │ ── token via apiFetch() ───────────────► │          │
│ route    │                                          │ JWKS     │
│ handler  │                                          │ verify   │
└──────────┘                                          │ + roles  │
                                                      └──────────┘
```

The mental model has **two independent verifications**:

- Auth.js verifies the **ID token** (it trusts the browser session).
- The Go API verifies the **access token** (it trusts nobody; checks the signature itself).

They are connected by *one string*: the same access token minted by Keycloak
travels from the browser session to the API in the `Authorization` header.

## 1. The browser leg (Auth.js ↔ Keycloak)

1. **User clicks Sign in.** Auth.js builds the authorization URL for the
   Keycloak provider:
   `{issuer}/protocol/openid-connect/auth?response_type=code&client_id=tenantflow-api&redirect_uri=http://localhost:3000&scope=openid profile email&state=...&nonce=...`
   — Authorization Code flow, state + nonce handled by Auth.js, and we a 302
   to it.

2. **Keycloak authenticates.** User types credentials; Keycloak sets a session
   cookie and redirects back to the dashboard callback with
   `?code=...&state=...`.

3. **Auth.js exchanges the code** at
   `{issuer}/protocol/openid-connect/token` using the client secret
   (confidential client → `client_secret_basic`). It gets back:
   - `id_token` (JWT, identity claims: sub, name, email) — Auth.js verifies
     signature + iss + exp + nonce and decodes it to build the profile.
   - `access_token` (JWT, the one the API will trust) — Auth.js does *not*
     verify this one.
   - `refresh_token` (for later rotation).

4. **Session is a JWT**, not a server-side thing (`strategy: "jwt"`). The
   `jwt` callback in `web/lib/auth.ts` grabs the access token and decodes its
   payload by hand:

   ```ts
   const payload = JSON.parse(Buffer.from(accessToken.split(".")[1], "base64url").toString());
   realmRoles = payload.realm_access?.roles ?? [];
   ```

   **Gotcha worth remembering:** `realm_access.roles` lives in the *access*
   token, **not** in the ID token / Auth.js profile. That's why the callback
   reaches into the JWT payload instead of using `profile.roles`.

## 2. The API leg (dashboard → Go API)

5. **Route handler forwards the token.** Next.js route handlers
   (`web/app/api/tenants/route.ts` etc.) read `session.user.accessToken` and
   call `apiFetch(path, token)` (`web/lib/api.ts`), which stamps
   `Authorization: Bearer <accessToken>` onto the upstream call to
   `http://localhost:9090`. No re-auth, no new token — the Keycloak token
   is reused end-to-end.

6. **The Go API verifies offline.** `internal/auth/provider.go`:

   ```go
   oidcProvider, _ := oidc.NewProvider(ctx, issuerURL) // discovery: fetches .well-known/openid-configuration
   verifier := oidcProvider.Verifier(&oidc.Config{ClientID: clientID, SkipClientIDCheck: true})
   idToken, err := verifier.Verify(ctx, token)
   ```

   `oidc.NewProvider` is **discovery**: one HTTP call to
   `{issuer}/.well-known/openid-configuration` at boot to learn the
   `jwks_uri`, token endpoint, etc. go-oidc then uses that metadata to fetch
   the **JWKS** (public keys) and caches them.

   `Verify` checks, with **no further network calls**:
   - signature (the JWT is signed by Keycloak's private key; we hold the public key from JWKS)
   - `iss` == our issuer URL
   - `exp` (not expired)
   - `aud`/client — **skipped** here, see the trade-off below

7. **Claims become context.** `RequireAuth` (`internal/auth/middleware.go`)
   runs before every API route:
   - `ExtractToken` pulls the Bearer header.
   - `VerifyToken` succeeds → `user_id = claims.sub` and
     `user_roles = claims.realm_access.roles` are stored in the request
     context.
   - `RequireRole(...)` gates read it from the context — **no database
     lookup on the request path.** Roles come from the signed JWT claim and
     are trusted because the signature was verified.

8. **Route-level authorization.** `internal/router/router.go`:
   - `GET /status` — public (no token).
   - Every other read (list, detail, events, backups, cost) — any
     authenticated user.
   - Mutations (create, delete, migrate, upgrade, restore, backup, retry,
     user admin) — `platform-admin` only.

## 3. The access token, dissected

A JWT is three base64url parts separated by dots: `header.payload.signature`.

Payload claims we actually rely on:

| Claim | Value here | Used for |
| --- | --- | --- |
| `iss` | `http://localhost:8081/realms/tenantflow` | checked by both Auth.js and the API |
| `sub` | immutable user id | `user_id` in API context; `user.id` in dashboard |
| `exp` | ~5 min (Keycloak default) | API rejects after expiry with 401 |
| `realm_access.roles` | `["platform-admin"]` etc. | RBAC gates + role-aware UI |
| `preferred_username`, `email`, `name` | demo users | dashboard profile display |

Key point: because `realm_access.roles` is *inside the signed payload*, the
API can make authorization decisions with zero per-request calls to
Keycloak. That's the entire reason for JWKS: **verify the signature locally,
then trust the claims it says.**

## 4. Trade-offs and honest gaps

- **`SkipClientIDCheck: true`.** The API does not verify the `aud` claim, so
  any validly-signed token for *any* client of this realm would pass. With a
  single shared client this is harmless (every token the dashboard holds was
  minted for `tenantflow-api` anyway). If a second client were ever added,
  audience checks should be enabled — this is a deliberate demo simplification.

- **No token-refresh wiring.** The Auth.js `jwt` callback only runs on
  sign-in. When Keycloak's access token expires (~5 min), the API returns
  401 and there is no automatic re-exchange: the user signs in again. This
  matches the (still open) learning-goal item "Token refresh flow in the
  dashboard; 401 handling in the frontend."

- **Single client, single secret.** The confidential client's secret ships
  in `web/.env.local` and `deploy/keycloak/setup.sh`. Fine for a localhost
  demo; in production it would move to a secret manager and each
  environment would get its own client.

- **Offline ≠ no trust.** The API trusts the token because it verified the
  signature against Keycloak's published keys, not because Keycloak
  "approved" it per-request. Revoked sessions are only rejected after `exp`
  — there is no session revocation.

## 5. Bug checklist (what breaks when a piece is missing)

| If you remove… | What fails |
| --- | --- |
| `oidc.NewProvider` | API can't discover `jwks_uri`; every 401 at boot |
| `verifier.Verify` | API trusts any signature — fake token passes |
| `exp` check | expired tokens live forever |
| `ExtractToken` | missing Bearer header → 401 (correctly) |
| `RequireRole` gate | any logged-in user can delete tenants |
| the `jwt` callback role decode | dashboard shows no admin controls despite admin login |
| `apiFetch` Authorization header | API 401s every dashboard request |

## 6. Reproduce from memory in 90 seconds

1. Sign-in → Auth.js redirects to `{issuer}/protocol/openid-connect/auth` (code flow, state + nonce).
2. Login → Keycloak redirects back with `code`.
3. Auth.js exchanges code + client secret → `id_token` + `access_token`.
4. `jwt` callback decodes access token payload → `realm_access.roles` into session.
5. Dashboard calls Next route handler; handler forwards `Bearer <access_token>` to Go API.
6. Go API verifies signature via JWKS (discovered at boot, cached), checks `iss`/`exp`, extracts `sub` + `realm_access.roles` into context.
7. `RequireRole` gates mutations on `platform-admin`. No DB lookup, no per-request IdP call.

The punchline to remember: **discovery buys offline verification, and offline
verification buys claims-based authorization** — one public-key fetch at boot
replaces a Keycloak round-trip on every request.