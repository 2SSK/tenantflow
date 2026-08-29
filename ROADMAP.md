# TenantFlow — Multi-Tenant SaaS Control Plane

> **One-liner:** **TenantFlow — a Kubernetes-inspired control plane** that automates the _complete lifecycle_ of SaaS tenants (provision → upgrade → **migrate** → backup → restore → delete) using **Temporal workflows, Saga compensations, PostgreSQL, Docker, and Keycloak IAM**, wrapped in a full-stack dashboard.
>
> **Career goal this serves:** grow from DevOps/SRE → Platform Engineering, while building real fullstack skills. This is the "mini Kubernetes-for-tenants" project: instead of managing VMs, you manage _tenants_.

**Created:** 2026-08-02
**Updated:** 2026-08-17 (SAGA Compensations milestone complete — chaos hook, MarkTenantFailed activity, LIFO workflow, workflow tests)
**Boilerplate:** https://github.com/2SSK/go-echo-boilerplate
**Status:** Phase 0–4 complete + Phase 5 partial (dashboard scaffold, auth, tenants list, users page, audit timeline)

---

## Table of Contents

1. [The Idea — why this project](#1-the-idea)
2. [Core Concept — tenants, providers, sagas](#2-core-concept)
3. [High-Level Architecture](#3-high-level-architecture)
4. [Technology Stack](#4-technology-stack)
5. [The Boilerplate We Start From](#5-the-boilerplate-we-start-from)
6. [Authentication & IAM with Keycloak](#6-authentication--iam-with-keycloak)
7. [Data Model (PostgreSQL)](#7-data-model-postgresql)
8. [Temporal Workflow Design](#8-temporal-workflow-design)
9. [API Design](#9-api-design)
10. [Frontend Dashboard](#10-frontend-dashboard)
11. [Roadmap — Milestones](#11-roadmap--milestones)
12. [Implementation Progress Tracker](#12-implementation-progress-tracker)
13. [Learning Goals Checklist](#13-learning-goals-checklist)
14. [Showable Artifacts](#14-showable-artifacts)
15. [Interview Talking Points](#15-interview-talking-points)
15.5 [Design Notes & Trade-offs (Phase 6)](#155-design-notes--trade-offs-phase-6)
16. [Open Decisions](#16-open-decisions)

---

## 1. The Idea

### Why this project (not another e-commerce saga)

- **Day job is already infra.** We run PostgreSQL HA + Temporal + monitoring at Amoga. This project turns that ops experience into a _product_ we designed ourselves.
- **Amoga domain fit.** Amoga is a multi-tenant, low-code enterprise platform (one platform, many customer orgs, strong isolation + governance). Tenant lifecycle automation is literally the platform-engineering layer of that kind of business.
- **The stacks we master in one project:**
  1. **Multitenancy** — database-per-tenant AND shared-schema isolation modes, implemented, not just discussed.
  2. **Temporal** — long-running workflows, saga compensations, signals, queries, workflow versioning, long timers.
  3. **PostgreSQL** — control-plane database, per-tenant databases, migrations, audit log as a queryable table.
  4. **Keycloak** — the enterprise identity layer. OIDC/OAuth2 done properly: realms, clients, roles, token verification, admin API automation.
- **SAGA is the spine.** Provisioning a tenant = many steps that can fail halfway. Saga = forward steps + compensation steps that undo partial work so no orphan resources remain.
- **Fullstack growth.** The dashboard is a real Next.js app with live workflow progress — not a bolted-on UI.

### Problem statement

> When a customer signs up for a SaaS today, provisioning is a script that "kinda does it all" — create tenant row, create DB, deploy app, send email. If step 4 fails, step 2 and 3 are orphaned. TenantFlow replaces that with durable, replayable workflows where every step is an Activity, every failed run **compensates** back to a clean state, and every action is authenticated and audited.

---

## 2. Core Concept

### What is a "tenant" here?

A tenant = one customer organization. It owns real resources:

```
A tenant owns:
├── a control-plane record (row in platform_db.tenants)
├── a dedicated PostgreSQL database (or rows in a shared schema)
├── a Redis instance
├── an application container
├── an identity in Keycloak (a realm client + users/roles)
└── a lifecycle state: pending → provisioning → active → (failed/deleting/deleted)
```

### The key design decision: Docker as the "cloud"

Real clouds (Azure/AWS) cost money and can't be demoed on an interviewer's laptop. So the cloud is **simulated with Docker on localhost**:

```
Real platform team                          Our project
──────────────────                         ──────────────────
Azure Resource Manager                     Docker Engine
  ├─ Create PostgreSQL                     ├─ docker run postgres → create tenant DB
  ├─ Create Storage Account                ├─ docker run redis
  ├─ Create AKS namespace                  ├─ docker run app container
  └─ ...                                   └─ docker exec → health checks
```

This is **not cheating** — the important thing is the _abstraction_. We define a `CloudProvider` interface. `DockerProvider` implements it. A future `AzureProvider` implements the same interface. That interface + its semantics (async, failure modes, idempotency) is the platform-engineering part.

### What Temporal adds

- **Durability** — a workflow that's half-way through survives a server restart; it resumes where it left off (event-sourced execution history).
- **Retries** — Activities retry automatically with backoff. A transient network blip doesn't fail the whole run.
- **Saga compensation** — Temporal lets us model the _forward_ path cleanly; on failure we run the reverse path (compensation activities).
- **Long waits** — a "wait 30 days before deleting data" step costs nothing (Temporal timers).
- **Visibility** — every step is an event in the workflow history → we can stream it to the UI and store it in PostgreSQL as an audit trail.

### What SAGA means here (the mental model)

```
Forward path (happy):   A → B → C → D → E
Failure at D:           A ✓ B ✓ C ✓ D ✗
Compensation path:      E-skip → D-skip → C⁻ (undo C) → B⁻ (undo B) → A⁻ (undo A)
Result:                 system back to a clean "no tenant" state
```

Key rule we will implement: **every non-idempotent forward step gets a matching compensation step.** Steps that are idempotent or cheap to retry are handled by Temporal retries instead of compensation.

---

## 3. High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  BROWSER (Next.js dashboard)                                │
│  tenant list · provision progress (live via SSE) · audit log│
│  login handled by Keycloak (Authorization Code + PKCE)      │
└───────────────┬──────────────────────────┬──────────────────┘
                │ redirects / login        │ REST + SSE
                ▼                          │ Authorization: Bearer <access token>
┌───────────────────────────────┐  ┌───────▼──────────────────────────────┐
│  KEYCLOAK (realm: tenantflow) │  │  API (Go + Echo)                     │
│  ├─ users / groups            │  │  AuthMiddleware (OIDC verifier)      │
│  ├─ clients:                  │  │  ├─ discovers OIDC config at startup │
│  │   tenantflow-web (public)  │  │  ├─ caches JWKS (signature keys)     │
│  │   tenantflow-admin (svc acct)││  ├─ verifies sig / exp / iss / aud   │
│  │   tenant-<id> (per tenant) │  │  ├─ extracts roles from claims       │
│  ├─ roles: platform-admin,    │  │  └─ sets user_id + user_role context │
│  │   platform-operator        │  └───────────────────┬──────────────────┘
│  └─ admin REST API            │                      │ starts workflows / events
└───────────────────────────────┘  ┌───────────────────▼──────────────────┐
                                   │  TEMPORAL (server + workers)          │
                                   │  TenantProvisionWorkflow (a Saga)     │
                                   │    ├─ Activity: createTenantRecord ✓  │
                                   │    ├─ Activity: createPostgresDatabase✓│
                                   │    ├─ Activity: runMigrations      ✓  │
                                   │    ├─ Activity: createRedis        ✓  │
                                   │    ├─ Activity: createTenantIdentity✓  │
                                   │    ├─ Activity: createAppContainer ✗  │
                                   │    └─ COMPENSATION (reverse order):   │
                                   │         dropApp → deleteIdentity →    │
                                   │         dropRedis → dropDatabase →    │
                                   │         deleteTenantRecord            │
                                   └───────────────────┬──────────────────┘
                                                       │ control-plane data
┌──────────────────────────────────────────────────────▼────────────────────┐
│  POSTGRESQL                                                               │
│  platform_db (control plane)                                              │
│    ├─ tenants                                                             │
│    ├─ audit_events        ← every workflow step writes here               │
│    ├─ workflow_instances  ← workflow metadata + status                    │
│  tenant_<id>_db (one real database per dedicated tenant)                  │
│  + redis containers + app containers (all via Docker)                     │
└───────────────────────────────────────────────────────────────────────────┘
```

### Request flow — `POST /tenants` (authenticated)

```
1. User logs in on dashboard → Keycloak issues access token (JWT)
2. Dashboard ──POST /api/v1/tenants + Bearer token──▶ Echo handler
3. AuthMiddleware verifies the token (signature/exp/iss/aud) ← no Keycloak call per request
4. Handler binds + validates payload (boilerplate validation layer)
5. Service calls TenantService.CreateTenant
6. Repository inserts `tenants` row (status = pending)
7. Service starts Temporal Workflow (TenantProvisionWorkflow)
8. Handler responds: HTTP 202 { tenantID, workflowID }   ← instant, async
9. Worker executes Activities against Docker (+ Keycloak admin API for identity)
10. Each Activity writes an `audit_events` row
11. Workflow completes → tenant status = active
12. Dashboard sees status change via SSE + polling the API
```

### Failure flow — the saga in action

```
Activity E (createAppContainer) fails permanently
        ↓
Temporal stops the forward path
        ↓
Workflow runs compensations in REVERSE order:
  dropApp (E⁻) → deleteTenantIdentity (D⁻) → dropRedis (C⁻)
  → dropDatabase (B⁻) → deleteTenantRecord (A⁻)
        ↓
Tenant status = failed, audit_events show COMPENSATION_COMPLETED
        ↓
No orphaned DBs/containers/identities — the system is clean
```

---

## 4. Technology Stack

| Component          | Tech                                                         | Why                                                                 |
| ------------------ | ------------------------------------------------------------ | ------------------------------------------------------------------- |
| API framework      | Go + Echo v4                                                 | Already in boilerplate, familiar                                    |
| Language           | Go 1.25+                                                     | Strict types, single binary                                         |
| DB driver          | pgx/v5 + pgxpool                                             | Already in boilerplate, excellent Postgres support                  |
| Migrations         | tern (embedded SQL files)                                    | Already in boilerplate                                              |
| Config             | koanf from env (`TENANTFLOW_` prefix)                        | Already in boilerplate                                              |
| Logging            | zerolog (JSON in prod)                                       | Already in boilerplate                                              |
| Validation         | go-playground/validator                                      | Already in boilerplate                                              |
| Errors             | custom `errs.HTTPError` + `sqlerr`                           | Already in boilerplate — converts pg errors to friendly HTTP errors |
| Identity / Auth    | **Keycloak 26.x** (OIDC/OAuth2)                              | **New** — realm, clients, roles, token verification                 |
| OIDC verification  | **coreos/go-oidc v3**                                        | **New** — discovery + JWKS + verifier                               |
| Keycloak admin API | **Nerzal/gocloak v14**                                       | **New** — automate identity creation in the saga                    |
| JWT parsing        | **go-jose/go-jose v3**                                       | **New** (already an indirect dep of the template)                   |
| Workflow engine    | Temporal Go SDK                                              | **New** — core of the project                                       |
| Container runtime  | Docker SDK / docker CLI                                      | **New** — the "cloud provider"                                      |
| Frontend           | Next.js + React + TypeScript                                 | **New** — fullstack dashboard                                       |
| Frontend auth      | Auth.js (NextAuth) Keycloak provider                         | **New** — Authorization Code + PKCE                                 |
| Realtime UI        | SSE (Server-Sent Events)                                     | **New** — live workflow progress                                    |
| Local infra        | docker-compose (postgres, redis, temporal, keycloak, worker) | **New**                                                             |
| Observability      | Prometheus + Grafana (later phase)                           | Extends day-job skills                                              |

---

## 5. The Boilerplate We Start From

`go-echo-boilerplate` gives us a production-shaped skeleton. The most important thing is understanding **why each layer exists** before we write new code.

### Layered architecture

```
cmd/boilerplate/main.go        ← composition root: wires everything
        │
        ▼
internal/server                ← owns Config, Logger, DB pool, http.Server lifecycle
        │
        ├──── internal/router   ← HTTP routes + global middleware order
        │          └── internal/router/v1   ← /api/v1 group
        │
        ├──── internal/handler  ← HTTP layer: bind, validate, call service, respond
        │          └── base.go  ← generic Handle() wrapper (removes boilerplate)
        │
        ├──── internal/service  ← business logic (where workflows get started)
        │
        ├──── internal/repository ← SQL access (pgx queries) — one file per entity
        │
        └──── internal/model    ← structs shared across layers
```

### Supporting packages (read once, then trust them)

| Package               | What it does                                                                  | Why it matters for TenantFlow                                              |
| --------------------- | ----------------------------------------------------------------------------- | -------------------------------------------------------------------------- |
| `internal/config`     | Loads + validates env config with `TENANTFLOW_` prefix via koanf              | We add Keycloak + Temporal + Docker settings here                          |
| `internal/database`   | pgxpool + ping + tern migrations (embedded FS, `schema_version` table)        | We add migration files for `tenants`, `audit_events`, `workflow_instances` |
| `internal/logger`     | zerolog, JSON in prod / console in dev                                        | Activity logs will use this                                                |
| `internal/middleware` | CORS, rate limit, request ID, context logger, recover, global error handler   | **Auth middleware gets swapped from Clerk → Keycloak here**                |
| `internal/errs`       | Typed `HTTPError` (code/message/status/fieldErrors)                           | Tenant 404s, validation errors, 401s use this                              |
| `internal/sqlerr`     | Maps `pgconn.PgError` → friendly errors (unique violation → "already exists") | Free: duplicate tenant slug becomes a clean 400                            |
| `internal/validation` | `Validatable` interface + field errors                                        | Tenant create payloads use this                                            |

### The Clerk → Keycloak swap (the crucial refactor)

The boilerplate already has auth scaffolding — it just uses **Clerk**. Two files matter:

| File                          | Current (Clerk)                                                          | New (Keycloak)                                                                                 |
| ----------------------------- | ------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------- |
| `internal/middleware/auth.go` | `clerkhttp.WithHeaderAuthorization` validates Bearer token via Clerk SDK | Our own `OIDCMiddleware` using `go-oidc` verifier (discovery + cached JWKS)                    |
| `internal/service/auth.go`    | `clerk.SetKey(...)`                                                      | `authService` holding OIDC provider + gocloak admin client                                     |
| `internal/config`             | `Auth.SecretKey` (Clerk secret)                                          | `Auth.KeycloakURL`, `Auth.Realm`, `Auth.WebClientID`, `Auth.AdminClientID`, `Auth.AdminSecret` |

**Why this swap is small (and what it teaches):** the rest of the app reads identity from _context_, not from Clerk directly. `internal/middleware/context.go` already extracts `user_id` and `user_role` from `c.Get(...)` — set by the auth middleware. So we only replace _who_ sets those values (Clerk → Keycloak claims), and every handler, logger, and audit event keeps working. That's the payoff of the middleware abstraction — swap the identity provider without touching business logic.

### What stays / changes / is added

- **Stays:** config, database, middleware (except auth), errs, sqlerr, validation, logger, server, handler pattern.
- **Changes:** `BOILERPLATE_` → `TENANTFLOW_` prefix; module path → `github.com/2SSK/tenantflow`; `cmd/boilerplate` → `cmd/api`; Clerk deps removed, Keycloak deps added.
- **Added:**
  - `internal/auth` — OIDC verifier + role middleware + gocloak admin client
  - `internal/temporal` — client + worker bootstrap
  - `internal/workflow` — workflow definitions (provision, upgrade, …)
  - `internal/activity` — activity implementations
  - `internal/cloud` — `CloudProvider` interface + `DockerProvider`
  - `internal/audit` — audit event writer (used by activities)
  - `internal/handler/tenant.go`, `internal/service/tenant.go`, `internal/repository/tenant.go`
  - `deploy/keycloak/` — realm import JSON + scripts
  - `web/` — Next.js dashboard
  - root `docker-compose.yml` — local stack

---

## 6. Authentication & IAM with Keycloak

### Why Keycloak

Keycloak is the standard open-source enterprise identity provider (IAM). It implements **OIDC** (OpenID Connect) on top of **OAuth2**. Amoga uses it. Mastering it = mastering the vocabulary every enterprise uses: realms, clients, users, roles, tokens, JWKS, PKCE.

### The mental model — Keycloak concepts

```
Keycloak server
└── Realm "tenantflow"         ← an isolated tenant space (like a separate "world")
    ├── Users                  ← humans (email, password, MFA, groups)
    ├── Groups                 ← organizational buckets (optional)
    ├── Roles                  ← permissions:
    │    ├── realm roles       ← apply across the realm: platform-admin, platform-operator
    │    └── client roles      ← scoped to one client (rarely needed for us)
    ├── Clients                ← applications allowed to ask for tokens:
    │    ├── tenantflow-web    ← PUBLIC client (browser): login page, Authorization Code + PKCE
    │    ├── tenantflow-admin  ← CONFIDENTIAL client: service account for automation (gocloak)
    │    └── tenant-<id>       ← created per tenant by the provisioning saga
    ├── OIDC discovery          ← {keycloak}/realms/tenantflow/.well-known/openid-configuration
    ├── JWKS                    ← {keycloak}/realms/tenantflow/protocol/openid-connect/certs
    └── Admin REST API          ← {keycloak}/admin/realms/tenantflow/...
```

**Analogy to lock in:** a **realm** is like a separate Keycloak "world" — in _our_ project, our _tenant_ concept maps nicely onto creating per-tenant **clients + roles** inside one realm. (In some products, each customer gets their own realm — that's the "database-per-tenant" equivalent of IAM. We'll do realm-per-platform + client-per-tenant, which matches how a control plane like ours is usually built.)

### Token types (the part everyone gets wrong until they learn it)

| Token         | Who sees it      | What it is                           | Used for                                   |
| ------------- | ---------------- | ------------------------------------ | ------------------------------------------ |
| Access token  | Browser + API    | JWT signed by realm keys, ~5 min TTL | Sent as `Authorization: Bearer` to our API |
| ID token      | Browser only     | JWT about the user                   | The app knows who the user is              |
| Refresh token | Browser (secure) | Opaque-ish, long TTL                 | Get new access tokens without re-login     |

Our API **only ever validates access tokens**. It does this **without calling Keycloak per request**: it caches the realm's public signing keys from the **JWKS** endpoint and verifies the JWT signature locally.

### Token verification flow in the Go API (the core pattern)

```
API starts
  ↓
oidc.NewProvider(ctx, "http://keycloak:8080/realms/tenantflow")
  → discovers OIDC config + fetches JWKS (cached, refreshed on rotation)
  ↓
per request:
  Authorization: Bearer <token>
  ↓
verifier.Verify(ctx, token)
  → checks signature (JWKS), exp (not expired), iss (issuer matches realm), aud (audience contains our client)
  ↓
parse claims → sub (user id), preferred_username, realm_access.roles
  ↓
c.Set("user_id", sub); c.Set("user_role", highestRole)   ← existing context enhancer picks these up
```

Code shape (we'll write the real thing in Phase 1):

```go
// internal/auth/oidc.go — concept
provider, _ := oidc.NewProvider(ctx, cfg.Auth.Issuer) // e.g. http://localhost:8081/realms/tenantflow
verifier := provider.Verifier(&oidc.Config{ClientID: cfg.Auth.WebClientID})

idToken, err := verifier.Verify(ctx, rawToken)   // signature, exp, iss, aud
if err != nil { return errs.NewUnauthorizedError("Invalid token", false) }

var claims struct {
    PreferredUsername string `json:"preferred_username"`
    RealmAccess       struct {
        Roles []string `json:"roles"`
    } `json:"realm_access"`
}
_ = idToken.Claims(&claims)                        // sub is in idToken.Subject
```

### RBAC design

| Role                          | Permissions                                                          |
| ----------------------------- | -------------------------------------------------------------------- |
| `platform-admin`              | Everything: create/upgrade/backup/restore/migrate/**delete** tenants |
| `platform-operator`           | Create + view tenants, view audit trails (no delete)                 |
| _(per-tenant)_ `tenant-owner` | View their own tenant status only (Phase 6+ stretch)                 |

Middleware: `auth.RequireRole("platform-admin")` — chainable on Echo routes. Destructive endpoints (`DELETE /tenants/{id}`) require admin.

### Keycloak inside the Saga (this is what makes it "mastery")

The provisioning workflow creates the tenant's **identity** too — via the **admin REST API** using gocloak:

```
Activity: CreateTenantIdentity
  ├─ gocloak.LoginClient (tenantflow-admin service account, client_credentials grant)
  ├─ CreateClient: tenant-<id> (confidential)
  ├─ CreateRole / assign to the client
  └─ audit event: TENANT_IDENTITY_CREATED

Compensation: DeleteTenantIdentity
  ├─ gocloak.DeleteClient
  └─ audit event: TENANT_IDENTITY_DELETED
```

Why this matters: it shows **IAM as part of infrastructure-as-code**. The same saga that creates a database also creates the identity — and rolls it back on failure. That's exactly what an enterprise low-code platform's control plane does.

### Local setup (Phase 0)

- Keycloak 26.7.0 container (`quay.io/keycloak/keycloak:26.7.0`)
- Dev mode: `start-dev --import-realm` with realm JSON mounted at `/opt/keycloak/data/import/`
- Bootstrap admin via `KC_BOOTSTRAP_ADMIN_USERNAME` / `KC_BOOTSTRAP_ADMIN_PASSWORD`
- Realm `tenantflow` imported from `deploy/keycloak/realm-export.json` (hand-written first — best way to learn the structure — then we can also export from the UI to compare)
- Admin console at `http://localhost:8081/admin` (master realm), app login at the tenantflow realm URL

---

## 7. Data Model (PostgreSQL)

### Control plane tables (in `platform_db`)

```sql
-- Tenants
CREATE TABLE tenants (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text        NOT NULL,
    slug              text        NOT NULL UNIQUE,           -- unique → sqlerr maps to friendly 400
    plan              text        NOT NULL DEFAULT 'starter',-- starter | pro | enterprise
    isolation_mode    text        NOT NULL DEFAULT 'dedicated', -- dedicated | shared
    status            text        NOT NULL DEFAULT 'pending',
    -- pending → provisioning → active | failed | deleting | deleted
    workflow_id       text,                                  -- Temporal workflow ID
    keycloak_client_id text,                                 -- tenant-<id> created by the saga
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);

-- Audit trail (event sourcing for the tenant lifecycle)
CREATE TABLE audit_events (
    id           bigserial PRIMARY KEY,
    tenant_id    uuid        NOT NULL REFERENCES tenants(id),
    workflow_id  text,
    event_type   text        NOT NULL,  -- TENANT_CREATED, DB_CREATED, IDENTITY_CREATED, COMPENSATION_STARTED, ...
    actor        text,                  -- who/what triggered it (user_id from Keycloak, or "workflow")
    payload      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_events_tenant ON audit_events (tenant_id, created_at DESC);

-- Workflow instances (metadata about Temporal runs)
CREATE TABLE workflow_instances (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid        REFERENCES tenants(id),
    workflow_type text        NOT NULL,  -- provision | upgrade | backup | restore | migrate | delete
    workflow_id   text        NOT NULL,  -- Temporal workflow ID
    status        text        NOT NULL,  -- running | completed | failed | compensated
    error         jsonb,
    started_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);
```

> The `updated_at` trigger function and `camel()` JSON helper already exist in migration `0001_setup.sql` — we build on top of them.

### Per-tenant resources

```
Dedicated isolation mode:
  platform_db (control plane) + tenant_<id>_db (their own database)
  + tenant_<id> redis + app container + Keycloak client tenant-<id>

Shared isolation mode (Phase 6):
  platform_db only; tenant data lives in shared tables with tenant_id column
```

Implementing **both modes** is the multitenancy depth — we can then defend either choice in interviews.

---

## 8. Temporal Workflow Design

### Mental model: Workflows vs Activities

- **Workflow** = orchestration logic. Plain Go code, must be deterministic (no direct DB/network calls, no time.Now(), no randomness). It decides _what to do and in what order_. Temporal replays it from history.
- **Activity** = the actual work (create DB, run migration, call Docker, call Keycloak admin API). Can do anything, retried by Temporal on failure, must be **idempotent**.
- Rule we follow: _orchestration in workflows, side effects in activities, progress in PostgreSQL._

### Provision workflow (the primary Saga)

```
TenantProvisionWorkflow (workflowId = "provision-" + tenantID)
  step 1  CreateTenantRecord         activity  (control plane DB)
  step 2  CreatePostgresDatabase     activity  (docker)
  step 3  RunDatabaseMigrations      activity  (docker exec psql)
  step 4  CreateRedis                activity  (docker)
  step 5  CreateTenantIdentity       activity  (Keycloak admin API via gocloak)
  step 6  CreateAppContainer         activity  (docker)
  step 7  HealthCheck                activity  (docker exec curl)
  step 8  MarkTenantActive           activity  (control plane DB update)
```

### Compensation (the Saga pattern in Temporal)

```go
// Official Temporal Go saga pattern (verified: temporalio/samples-go/saga/workflow.go)
// workflow.Defer does NOT exist in Go SDK v1.47.0 — use plain Go defer with named return
func ProvisionTenantWorkflow(ctx workflow.Context, input ProvisionInput) (err error) {
    // Step 1: forward
    err = workflow.ExecuteActivity(ctx, activities.CreateTenantRecord, input).Get(ctx, nil)
    if err != nil { return err }

    // Register compensation right after forward step succeeds (NOT after all steps)
    // LIFO order = defer registers in forward order, executes in reverse
    defer func() {
        if err != nil {
            // compensation logic: call MarkTenantFailed, join errors
        }
    }()

    // Step 2: forward (compensation already registered above)
    err = workflow.ExecuteActivity(ctx, activities.ProvisionTenant, input).Get(ctx, nil)
    if err != nil { return err }

    // ...more steps...
    return nil
}
```

If any step returns an error (after retries are exhausted), `defer` runs every registered compensation **in reverse order (LIFO)**. That is the Saga.

### Compensation design rules (interview-grade rules we'll implement)

1. **Retries first, compensation second.** Transient failures → Temporal retries. Only _permanent_ failures trigger compensation.
2. **Every non-idempotent forward step gets a compensation.** If the forward step is idempotent (safe to run twice), retry is enough — no compensation needed.
3. **Compensations must be idempotent too.** `dropDatabase` on a DB that no longer exists must not error out the saga.
4. **Compensations should be best-effort but must be attempted.** A failed compensation is recorded in `audit_events` and surfaced — never silently swallowed.
5. **Each step writes an audit event** so the whole run is reconstructable from PostgreSQL even if Temporal UI is gone.

### Failure escalation ladder: retries → compensation → DLQ

This is the exact answer to "what happens when things break?" in an interview:

```
Activity fails (transient)  ──► Temporal retries with backoff
        │ retries exhausted
Permanent failure            ──► workflow compensates (Saga) → status = failed
        │
DLQ: the failed run is recorded in workflow_instances
        (status = failed, error jsonb) + audit_events
        │
Operator sees it in UI/API → "Retry workflow" endpoint restarts the run
```

The DLQ here is **not a message queue** — it's the durable `failed` state + audit trail that makes permanent failures visible and human-recoverable. We build a "failed runs" view + retry endpoint (Phase 8).

### Workflow versioning (the upgrade path)

Temporal workflows are code — when we change `TenantProvisionWorkflow` (e.g. add a step), **in-flight** runs must keep executing the old code. We use `workflow.GetVersion()`:

```
Workflow v1 (already running) ──► keeps the v1 path (Temporal replays v1 history)
Workflow v2 (new runs)        ──► takes the v2 path via workflow.GetVersion("step", DefaultVersion, V2)
```

Demonstrated in Phase 8 by adding a brand-new step (e.g. `createTlsCert`) while an old provision run is still going — both must complete correctly.

### Workflow inputs / signals / queries (Temporal concepts we'll use)

- **Input:** `TenantProvisionInput{ TenantID, Name, Slug, Plan, IsolationMode }`
- **Query:** `GetProvisionStatus` — read current step from workflow state (used by the dashboard).
- **Signal (Phase 6+):** e.g. `CancelProvisionSignal` — ask a running workflow to stop and compensate.

---

## 9. API Design

All under `/api/v1`, **protected by Keycloak bearer tokens**. `DELETE` + `migrate` require `platform-admin`.

| Method | Path                             | Auth      | Action                            | Workflow    |
| ------ | -------------------------------- | --------- | --------------------------------- | ----------- |
| POST   | `/tenants`                       | operator+ | Create tenant (async)             | `provision` |
| GET    | `/tenants`                       | operator+ | List tenants (paginated)          | —           |
| GET    | `/tenants/{id}`                  | operator+ | Tenant detail + status            | —           |
| POST   | `/tenants/{id}/upgrade`          | operator+ | Change plan                       | `upgrade`   |
| POST   | `/tenants/{id}/backup`           | operator+ | Snapshot + verify                 | `backup`    |
| POST   | `/tenants/{id}/restore`          | operator+ | Restore from backup               | `restore`   |
| POST   | `/tenants/{id}/migrate`          | **admin** | Move to another host              | `migrate`   |
| DELETE | `/tenants/{id}`                  | **admin** | Soft-delete → 30-day wait → purge | `delete`    |
| GET    | `/tenants/{id}/events`           | operator+ | Audit trail                       | —           |
| GET    | `/workflows/{workflowId}/events` | operator+ | Live progress (SSE)               | —           |
| GET    | `/status`                        | public    | Health (exists already)           | —           |

**Every mutating endpoint returns immediately with the workflow ID** (202 Accepted). The UI tracks progress via SSE + polling — exactly how real control planes (Azure, AWS) behave.

---

## 10. Frontend Dashboard

Next.js app in `web/`:

- **Login** — Auth.js (NextAuth) with the Keycloak provider (Authorization Code + PKCE). Access token lives in the session; API calls send it as a Bearer token.
- **Tenants page** — table of tenants, status badges, isolation mode, plan.
- **Create tenant** — form (name, slug, plan, isolation mode) → POST → shows live progress.
- **Tenant detail** — workflow progress like GitHub Actions: `✓ Create DB → ✓ Migrations → ⏳ Deploy App`, powered by SSE stream of workflow events.
- **Audit timeline** — reads `/tenants/{id}/events` and renders the event-sourced history.
- **Role-aware UI** — hide `DELETE` button unless the user has `platform-admin` (from token claims).
- **Chaos toggle** (Phase 8) — set failure rate, watch the saga compensate.

---

## 11. Roadmap — Milestones

> Every phase is small enough to finish. **Phases 1–4 are the MVP.** Phase 5+ only after the MVP works end-to-end.

### Phase 0 — Foundation: template → runnable stack

- [ ] Create project from `go-echo-boilerplate` (copy, rename module to `github.com/2SSK/tenantflow`, rename `cmd/boilerplate` → `cmd/api`)
- [ ] Rename env prefix `BOILERPLATE_` → `TENANTFLOW_`
- [ ] `go mod tidy`, `go-task run`, verify `/status` and `/docs` work
- [ ] Add `docker-compose.yml`: postgres, redis, temporal (server + UI + admin tools), **keycloak**, worker placeholder
- [ ] Keycloak realm import (`deploy/keycloak/realm-export.json`) → admin console reachable, realm `tenantflow` imported
- [ ] Add Go deps: `go.temporal.io/sdk`, `coreos/go-oidc/v3`, `Nerzal/gocloak/v14`
- [ ] Read through every boilerplate layer once (config → database → middleware → handler → service → repository) and note questions

### Phase 1 — Identity & Access with Keycloak

- [ ] Swap `internal/middleware/auth.go`: Clerk → OIDC middleware (go-oidc verifier, cached JWKS)
- [ ] New `internal/auth`: provider init, claims extraction, `RequireRole` middleware
- [ ] Update `internal/config`: Keycloak settings (URL, realm, client IDs, admin secret)
- [ ] Remove Clerk deps (`clerk-sdk-go`), remove `service/auth.go` Clerk code → Keycloak admin client (gocloak)
- [ ] Seed users/roles in realm import: `platform-admin`, `platform-operator` + two test users
- [ ] Protect `/api/v1/*` with auth; `/status` stays public
- [ ] **Manual test:** get token via Keycloak login, `curl -H "Authorization: Bearer ..."`, verify 200 + 401 cases + role rejection (403)
- [ ] Write down the OIDC flow from memory (login → code → token → API verification) — this is the interview answer

### Phase 2 — Control plane data model

- [ ] Migration `0002_tenants.sql`: `tenants` table
- [ ] Migration `0003_audit_events.sql`: `audit_events` table (+ index)
- [ ] Migration `0004_workflow_instances.sql`: `workflow_instances` table (+ index)
- [ ] Go models in `internal/model`: Tenant, AuditEvent, WorkflowInstance, enums as typed strings
- [ ] Repositories: `internal/repository/tenant.go`, `audit.go`, `workflow_instance.go`
- [ ] Unit/integration tests for repository layer against local Postgres

### Phase 3 — Cloud provider abstraction

- [ ] `internal/cloud/provider.go`: `CloudProvider` interface (CreateDatabase, DropDatabase, RunMigration, CreateRedis, CreateApp, HealthCheck…)
- [ ] `internal/cloud/docker.go`: DockerProvider using Docker SDK
- [ ] Helper: generate tenant DB name safely (`tenant_<id>` — Postgres identifier rules)
- [ ] Test manually: create/drop a tenant database via a small CLI or test script

### Phase 4 — Provision Saga (MVP core)

- [ ] `internal/activity/provision.go`: all forward activities + compensation activities
- [ ] `internal/activity/identity.go`: CreateTenantIdentity / DeleteTenantIdentity via gocloak
- [ ] `internal/workflow/provision.go`: `TenantProvisionWorkflow` using `workflow.NewCompensator`
- [ ] `internal/temporal/client.go` + `internal/temporal/worker.go`
- [ ] Start worker inside `cmd/api` (or a separate `cmd/worker`)
- [ ] `internal/service/tenant.go` + `internal/handler/tenant.go` + v1 routes: `POST /tenants`, `GET /tenants`, `GET /tenants/{id}`
- [ ] Audit events written from every activity (including actor from token context)
- [ ] **Demo test:** kill Docker mid-provision → verify compensation runs and no orphan containers/DBs/clients
- [ ] **Minimal chaos hook:** config flag that fails the Nth activity → verify saga

### Phase 5 — Fullstack dashboard

- [x] Scaffold Next.js app in `web/` (TypeScript, strict)
- [x] Auth.js + Keycloak provider login
- [x] Tenants list page (calls GET /tenants with Bearer token)
- [x] Create tenant form (calls POST /tenants)
- [ ] Tenant detail page with SSE live progress (`/workflows/{id}/events`)
- [x] Audit timeline component
- [x] Role-aware UI (hide admin actions for non-admins)
- [x] Users page (list/delete/role management)
- [ ] User create form
- [ ] Tenant delete button
- [ ] shadcn/ui + tokyonight color scheme
- [ ] Dark/light mode toggle
- [ ] JetBrains Mono + Inter fonts

### Phase 6 — Upgrade Saga + Shared isolation mode

- [ ] Migration for shared-schema tenant data (e.g. `shared_users` with `tenant_id`)
- [ ] Tenant creation supports `isolation_mode: shared`
- [ ] `TenantUpgradeWorkflow`: verify → raise quotas → enable features → update billing; compensation = roll quotas back
- [ ] Upgrade endpoint + UI button
- [ ] Write the "dedicated vs shared" trade-off notes for interviews

### Phase 7 — Migrate / Backup / Restore / Delete

- [ ] `TenantMigrateWorkflow` (the realistic ops flow): lock writes → snapshot → restore to new host → sync changes → switch traffic → unlock
- [ ] `TenantBackupWorkflow`: freeze writes (PG) → snapshot → **restore to temp → run validation → drop temp DB → mark backup verified** (production-grade backup verification, not just "backup done")
- [ ] `TenantRestoreWorkflow`: create new DB → restore → validate → switch traffic
- [ ] `TenantDeleteWorkflow`: disable login → notify → **wait 30 days (Temporal timer)** → drop resources + Keycloak client → purge row
- [ ] Endpoints + UI actions

### Phase 8 — Resilience, chaos & DLQ

- [ ] Configurable activity failure injection (rate + which activity) via env or API
- [ ] **DLQ:** failed runs recorded in `workflow_instances` (status = failed + error) → "failed runs" view + retry endpoint that restarts the workflow → audit event records the manual replay
- [ ] `audit_events` query view for the UI ("compensation history")
- [ ] Test matrix: fail every activity at least once; assert clean end state
- [ ] **Workflow versioning demo:** add a step via `workflow.GetVersion` while an old run is in-flight → verify both paths complete

### Phase 9 — Observability, cost, docs, ship

- [ ] Prometheus metrics from API + worker (`temporal_*` SDK metrics + custom `tenantflow_*`)
- [ ] Grafana dashboard: provision time, success %, compensation count, worker utilization
- [ ] **Per-tenant cost view (stretch):** estimate from resource metadata (DB size, container CPU/mem, workflow count) — the "cost dashboard" real SaaS platforms have
- [ ] README: architecture diagram, quickstart, screenshots, demo GIF
- [ ] Record 2–3 min demo video (happy path + migration + backup verification + chaos failure)
- [ ] Blog posts (draft list in Showable Artifacts)
- [ ] Deploy demo somewhere reachable (Render/Railway/Fly or homelab) + link from portfolio

---

## 12. Implementation Progress Tracker

> Update this table as we go. Each row becomes "done" only when it actually works.

### Phase 0 — Foundation

| #   | Task                                                       | Status |
| --- | ---------------------------------------------------------- | ------ |
| 0.1 | Project created from boilerplate (module renamed)          | ☑      |
| 0.2 | Env prefix renamed to `TENANTFLOW_`                        | ☑      |
| 0.3 | App runs: `/status` healthy, `/docs` serves                | ☑      |
| 0.4 | docker-compose stack (postgres/redis/temporal/keycloak) up | ☑      |
| 0.5 | Keycloak realm imported + admin console reachable          | ☑      |
| 0.6 | Go deps added (temporal, oidc, gocloak)                    | ☑      |
| 0.7 | Boilerplate layers read + questions listed                 | ☑      |

### Phase 1 — Identity & Access (Keycloak)

| #   | Task                                              | Status |
| --- | ------------------------------------------------- | ------ |
| 1.1 | Clerk → OIDC middleware swap                      | ☑      |
| 1.2 | `internal/auth` provider + verifier + RequireRole | ☑      |
| 1.3 | Keycloak config added                             | ☑      |
| 1.4 | Clerk deps removed; gocloak admin client wired    | ☐      |
| 1.5 | Roles + test users seeded                         | ☑      |
| 1.6 | API protected; `/status` public                   | ☑      |
| 1.7 | curl tests: 200 / 401 / 403 pass                  | ☑      |
| 1.8 | OIDC flow written down from memory                | ☐      |

### Phase 2 — Data model

| #   | Task                           | Status |
| --- | ------------------------------ | ------ |
| 2.1 | `tenants` migration            | ☑      |
| 2.2 | `audit_events` migration       | ☑      |
| 2.3 | `workflow_instances` migration | ☑      |
| 2.4 | Go models                      | ☑      |
| 2.5 | Repositories                   | ☑      |
| 2.6 | Repo tests                     | ☑      |

### Phase 3 — Cloud provider

| #   | Task                            | Status |
| --- | ------------------------------- | ------ |
| 3.1 | `CloudProvider` interface       | ☑      |
| 3.2 | `DockerProvider` implementation | ☑      |
| 3.3 | Manual create/drop verification | ☑      |

### Phase 4 — Provision Saga

| #   | Task                                 | Status |
| --- | ------------------------------------ | ------ |
| 4.1 | Provision activities + compensations | ☑      |
| 4.2 | Identity activities (gocloak)        | ☑      |
| 4.3 | `TenantProvisionWorkflow`            | ☑      |
| 4.4 | Temporal client + worker             | ☑      |
| 4.5 | Tenant API (POST/GET)                | ☑      |
| 4.6 | Audit events from activities         | ☑      |
| 4.7 | Failure demo: compensation verified  | ☑      |
| 4.8 | Minimal chaos hook                   | ☑      |
| 4.9 | Audit timeline endpoint (GET events) | ☑      |

### Phase 5 — Dashboard

| #   | Task                     | Status |
| --- | ------------------------ | ------ |
| 5.1 | Next.js scaffold         | ☑      |
| 5.2 | Keycloak login (Auth.js) | ☑      |
| 5.3 | Tenants list             | ☑      |
| 5.4 | Create tenant form       | ☑      |
| 5.5 | Live progress (SSE)      | ☐      |
| 5.6 | Audit timeline           | ☑      |
| 5.7 | Role-aware UI            | ☑      |
| 5.8 | Users page (list/delete) | ☑      |
| 5.9 | User create form         | ☐      |
| 5.10| Tenant delete button     | ☐      |
| 5.11| shadcn/ui + tokyonight   | ☑      |
| 5.12| Dark/light mode toggle   | ☑      |
| 5.13| JetBrains Mono + Inter   | ☑      |

### Phase 6 — Upgrade + shared mode

| #   | Task                             | Status |
| --- | -------------------------------- | ------ |
| 6.1 | Shared-schema migration          | ☑      |
| 6.2 | Shared isolation tenant creation | ☑      |
| 6.3 | Upgrade workflow + compensation  | ☑      |
| 6.4 | Upgrade endpoint + UI            | ☑      |
| 6.5 | Trade-off notes written          | ☑      |

### Phase 7 — Migrate/Backup/Restore/Delete

| #   | Task                                                           | Status |
| --- | -------------------------------------------------------------- | ------ |
| 7.1 | Migrate workflow (lock→snapshot→sync→switch→unlock)            | ☐      |
| 7.2 | Backup workflow with verification (restore→validate→drop temp) | ☐      |
| 7.3 | Restore workflow                                               | ☐      |
| 7.4 | Delete workflow (with 30-day timer)                            | ☐      |
| 7.5 | Endpoints + UI                                                 | ☐      |

### Phase 8 — Chaos & DLQ

| #   | Task                                   | Status |
| --- | -------------------------------------- | ------ |
| 8.1 | Failure injection config               | ☐      |
| 8.2 | DLQ: failed-runs view + retry endpoint | ☐      |
| 8.3 | Compensation history view              | ☐      |
| 8.4 | Fail-every-activity test matrix        | ☐      |
| 8.5 | Workflow versioning demo               | ☐      |

### Phase 9 — Ship

| #   | Task                             | Status |
| --- | -------------------------------- | ------ |
| 9.1 | Metrics + Grafana                | ☐      |
| 9.2 | Per-tenant cost view (stretch)   | ☐      |
| 9.3 | README + screenshots             | ☐      |
| 9.4 | Demo video                       | ☐      |
| 9.5 | Blog posts                       | ☐      |
| 9.6 | Live deployment + portfolio link | ☐      |

---

## 13. Learning Goals Checklist

> Every item here is an interview-answer you should be able to give from _this_ project, not from a tutorial.

### Keycloak / OIDC / OAuth2

- [ ] Realm vs client vs user vs role — explain each in one sentence with an analogy
- [ ] Public vs confidential clients; when to use PKCE (Authorization Code + PKCE for SPAs)
- [ ] Access token vs ID token vs refresh token — who sees each, what they're for
- [ ] Why the API verifies tokens offline via JWKS instead of calling Keycloak per request
- [ ] What `iss`, `aud`, `exp`, `sub` claims mean and why we check them
- [ ] Realm roles vs client roles; how `realm_access.roles` lands in the JWT
- [ ] Client Credentials grant (service accounts) for automation — gocloak admin API
- [ ] Keycloak admin REST API: create client/user/role; delete; error handling
- [ ] Realm import/export JSON structure (`deploy/keycloak/realm-export.json`)
- [ ] Keycloak 26 container: `start-dev`, `--import-realm`, bootstrap admin env vars
- [ ] How our API's OIDC middleware maps token → `user_id`/`user_role` context (the Clerk swap)
- [ ] Token refresh flow in the dashboard; 401 handling in the frontend

### Multitenancy

- [ ] What database-per-tenant buys you (isolation, noise, restore isolation) and costs you (connection bloat, migrations ×N, management)
- [ ] What shared-schema buys you (cheap, easy analytics) and costs you (noisy neighbor, RLS needs)
- [ ] How `tenant_id` scoping must be enforced at the repository layer (one missed `WHERE tenant_id = ?` = cross-tenant leak)
- [ ] When to use Postgres Row-Level Security (RLS) instead of app-level filtering
- [ ] How a control plane _itself_ is a multi-tenant system (our API + workflow instances)
- [ ] Realm-per-customer vs client-per-customer IAM — when you'd pick each

### Temporal

- [x] Workflows are deterministic; Activities are where side effects live
- [x] Temporal replay / event history — why a workflow survives restarts
- [x] Retries vs compensation: when each applies
- [x] `workflow.NewCompensator` — the saga helper; why compensation order is reverse
- [ ] Signals, queries, and updates — and when to use each
- [ ] Workflow versioning (changing a workflow that has running instances)
- [x] Why `time.Now()`, `rand`, and direct I/O are forbidden in workflow code
- [x] Official Temporal Go saga pattern: named return `(err error)` + plain `defer` + LIFO `errors.Join`
- [x] `testsuite.WorkflowTestSuite`: register → mock → assert (OnActivity is override, not register)
- [x] Test env auto-skips timers for fast tests; retry policy still respected

### SAGA / distributed transactions

- [x] Saga = local transactions + compensation, not a 2PC distributed transaction
- [x] Orchestration (central coordinator) vs choreography (event-driven) sagas — and why Temporal = orchestration
- [ ] Idempotency keys: making retries and compensations safe
- [ ] The outbox pattern: why we persist "intent" (audit events) before/with side effects
- [ ] Exactly-once is a lie; aim for at-least-once + idempotent handlers
- [x] The failure ladder: retries → compensation → DLQ (failed state + manual replay)

### PostgreSQL

- [ ] Writing versioned migrations with tern; why `schema_version` table exists
- [ ] UUID keys, timestamptz, JSONB payloads for audit
- [ ] Unique constraints as a _feature_ (slug uniqueness → friendly 400 via sqlerr)
- [x] Creating/dropping databases at runtime (for tenant provisioning) — connection pooling gotchas
- [x] Indexing the audit trail (tenant_id, created_at DESC) for the timeline UI
- [ ] Backup verification: restore to a temp DB → validate → drop temp → mark verified
- [ ] Live migration flow: lock → snapshot → sync → switch traffic → unlock (and its failure modes)

### Platform engineering / fullstack

- [x] Provider abstraction: why the `CloudProvider` interface is the platform boundary
- [ ] Async API design: 202 Accepted + workflowId + status polling/SSE
- [ ] Observability of a control plane: provision time, success rate, compensation count
- [ ] React + SSE: rendering a live event stream without polling the DB
- [ ] Role-aware UI driven by token claims, not by a second lookup

---

## 14. Showable Artifacts

- **README** with ASCII architecture diagram, quickstart (`docker compose up`), screenshots
- **Demo video (2–3 min):** login via Keycloak → happy-path provision → flip chaos to 50% → watch the saga compensate → show clean state + audit trail
- **Blog posts** (draft topics — 1 per phase, post as we finish):
  1. "Saga compensation with Temporal: retries vs rollback" (Phase 4)
  2. "Securing a Go control plane with Keycloak OIDC" (Phase 1) — realm design, JWKS verification, RBAC
  3. "Database-per-tenant vs shared schema: implementing both" (Phase 6)
  4. "Docker as a cloud provider: the abstraction that makes control planes testable" (Phase 3)
  5. "Chaos-testing a workflow engine: retries, compensation, and the DLQ" (Phase 8)
  6. "Migrating a live tenant with zero downtime" (Phase 7)
- **Live demo URL** linked from portfolio (Phase 9)
- **This file** — the roadmap itself is a showable artifact (shows planning discipline)

---

## 15. Interview Talking Points

Questions this project lets you answer with authority:

1. "Tell me about a time you handled distributed systems failure." → Saga compensation, chaos mode.
2. "How would you design a multi-tenant SaaS?" → Both isolation models + control plane + IAM.
3. "How does OIDC/OAuth2 actually work?" → Realms, clients, PKCE, JWKS verification — with code from this project.
4. "Why Keycloak instead of rolling your own auth?" → Standards, identity lifecycle, admin API, enterprise expectations.
5. "Why Temporal instead of a message queue / manual scripts?" → Durability, retries, visibility, long timers.
6. "How do you guarantee no orphaned resources?" → Compensation + idempotency + audit trail.
7. "What's your approach to migrations at scale?" → tern, per-tenant migrations, versioning.
8. "How do you observe a control plane?" → Metrics (provision time, success %, compensation count) + structured logs + audit events.
9. "Walk me through your architecture." → The diagram in section 3, plus the provider + IAM abstractions.
10. "How do you migrate a live tenant without downtime?" → `TenantMigrateWorkflow`: lock → snapshot → sync → switch → unlock.
11. "What happens when retries are exhausted?" → Compensation, then the DLQ state (failed run + audit + manual replay endpoint).

---

## 15.5 Design Notes & Trade-offs (Phase 6)

Deep-dive notes on the decisions made in Phase 6. These are the "why" behind the code, written to hold up under interview questioning.

### Dedicated vs shared isolation

|                       | Dedicated (db-per-tenant) | Shared (schema-per-tenant) |
| --------------------- | ------------------------- | -------------------------- |
| Isolation             | Strongest (separate DB)   | Logical only (`tenant_id`) |
| Cost at scale         | High (many small DBs)     | Low (shared capacity)      |
| Tenant migration      | Independent (per DB)      | Linked to shared schema    |
| Cross-tenant blast radius | Rows in one DB          | A bad query can hit all    |
| Upgrade path          | Upgrade each DB           | One schema change, low risk|

**Why we implemented both:** a real control plane must let operators pick per tenant (compliance/major tenants → dedicated; long-tail → shared). The `CloudProvider` interface is the seam that makes both modes plain data, and the create saga branches on `isolation_mode`.

### Compensation vs retry decision (upgrade saga)

- **Retry first, compensate last.** Some failures are transient (a directory call, a network blip). Temporal retries those for free. Only when retries are exhausted does the saga's compensation need to run. This is the "failure ladder": retries → compensation → DLQ.
- **Guard flags keep compensation idempotent.** The workflow tracks `quotaRaised`; compensation only rolls back quotas if we actually raised them. Without the guard, a rollback after the forward activity never ran would double-count or error (rolling back a quota that was never raised).
- **Compensation must be able to undo a forward step.** `VerifyTenantActive` returns the tenant's old quota precisely so `RollbackQuotas` can restore it exactly. This is the rule: *a mutating activity's result payload is what its compensation needs*.

### In-memory quota store (a deliberate MVP simplification) — the headline trade-off

- The quota store lives **only in worker memory** and is seeded with `DefaultQuota` for every tenant. This is a stopgap for the MVP demo, not production.
- **Consequence:** quotas reset to `DefaultQuota` on worker restart; there is no persistence and no cross-worker visibility. Coordinated reads/writes across multiple workers, or durable per-tenant quota, need the store backed by the control-plane Postgres (or a dedicated service).
- This is called out explicitly because it violates the "long-term architecture" rule until Phase 7+ — we know it, and the `QuotaStore` interface is the seam to swap the in-memory impl for a durable one without touching the workflow.

### Workflow ID reuse limitation

- Every operation uses a fixed workflow ID (`upgrade-<id>`, `provision-<id>`, `delete-<id>`) with `REJECT_DUPLICATE` policy. Temporal **never re-runs a completed/failed workflow under the same ID**, so a second upgrade of a tenant whose previous upgrade completed returns **409**.
- **Net effect:** a tenant can only be upgraded once (or retried only after the failed instance is properly superseded). This is safe but not ergonomic.
- **Future work:** use `ALLOW_DUPLICATE_FAILED_ONLY` so a *failed* operation can be retried by re-posting while a *completed* one still rejects — preserving idempotency while unblocking recovery. (Noted, not yet implemented.)

### VerifyTenantActive guards non-active tenants

- The upgrade can only proceed from a tenant in `active` state. Missing → 404; not active → 409. This prevents upgrading a tenant that is provisioning, deleted, or failed, which would leave inconsistent state. The tenant status is the control plane's single source of truth for lifecycle transitions.

### Compensation writes are audit-visible

- `RollbackQuotas` writes a `TENANT_QUOTA_ROLLED_BACK` audit event. The dashboard timeline therefore shows the saga *reason* — you can see a rollback happened, not just that "things failed." This makes the compensation path demonstrable in the UI and in demos.

---

## 16. Open Decisions

Decisions to make _when we reach them_ (not now):

- [ ] **Keycloak auth mode (DECIDED):** OIDC bearer-token validation in the Go API; Auth.js (Keycloak provider) in the dashboard
- [ ] **Token validation library:** go-oidc verifier (recommended) vs raw go-jose (we may peek at go-jose internals for learning)
- [ ] **Per-tenant IAM:** one client per tenant created by the saga (recommended) vs per-tenant realm (heavier, more isolation)
- [ ] **gocloak vs raw HTTP** for admin API calls in activities (gocloak v14 recommended; raw HTTP is a good learning exercise)
- [x] **Provider depth:** Docker CLI vs Docker SDK (`docker/docker` Go client)? → Docker SDK chosen
- [ ] **Frontend framework:** Next.js App Router (recommended) vs Vite + React?
- [ ] **Worker placement:** same binary with `cmd/api` + `cmd/worker` (recommended) vs separate repo?
- [ ] **Shared-schema enforcement:** app-level `tenant_id` filters vs PostgreSQL RLS?
- [ ] **Metrics library:** Prometheus client + `temporal_*` SDK metrics exporter?
- [ ] **Deployment target:** homelab server vs Fly/Railway for the live demo?
- [ ] **Multi-region:** simulate with multiple Docker hosts/contexts? (stretch — likely beyond MVP; the `CloudProvider` interface leaves room for it later)
- [ ] **Cost dashboard depth:** estimate from resource metadata (DB size, container CPU/mem) vs real billing data? (stretch)

---

_This file is the living source of truth. Update the progress tracker (Section 12) at the end of every session._
