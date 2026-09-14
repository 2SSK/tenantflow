# TenantFlow — case study

*This is the engineering story behind the project: what it actually does, why
it is shaped the way it is, and what the numbers show. Companion documents:
[ROADMAP](../ROADMAP.md) for the build narrative, [failure matrix](failure-matrix.md)
for the testing philosophy, [load test report](load-test.md) for measured
behavior, [ADRs](adr/) for decisions.*

## 1. The problem

A platform that rents out "tenants" has a boring-but-deadly job at its core:
**administering tenant infrastructure reliably**. Provision a database,
isolate it, back it up, migrate it, delete it — and any of those steps can
die halfway. Killed worker, dropped connection, DB that's already gone,
concurrent operator clicking the wrong button. A control plane that scrambles
or corrupts tenant resources loses trust instantly, and a control plane that
*blocks forever* on half-done work loses operations teams instantly.

TenantFlow is a small, honest control plane (Go API + worker, Temporal,
Postgres, Keycloak) built to make one guarantee visible and provable:
**every lifecycle operation either completes exactly, or fails
recoverably with a recorded, replayable artifact** — never "lost" and never
silently fake.

The stack is deliberately modest: one machine in dev, real Postgres, real
Keycloak, real Temporal. "Simulated" appears nowhere in the control plane
except the pluggable provider's default (the cloud provider abstraction —
see §3.4 — makes that one line).

## 2. Architecture at a glance

```
Operators / API clients
      │  HTTPS + bearer (JWT)
      ▼
┌────────────────────────┐        ┌──────────────────────────────┐
│ API (Go, net/http)     │        │ Worker (Go, Temporal client)  │
│  OIDC middleware       │  starts│  registers:                   │
│  handler layer         │───────▶│   Provision/Delete/Migrate/   │
│  repository (Postgres) │        │   Upgrade/Backup/Reconcile    │
└──────────┬─────────────┘        └──────────────┬───────────────┘
           │                                     │ activities
           ▼                                     ▼
   ┌───────────────┐   ┌──────────────┐   ┌──────────────┐
   │ tenantflow DB │   │ Temporal     │   │ Keycloak     │
   │ tenant ledger,│   │ (durable     │   │ (identity/   │
   │ backups,      │   │  history)    │   │  roles)      │
   │ audit events  │   └──────────────┘   └──────────────┘
   └───────────────┘             │
                                 ▼
   ┌─────────────────────────────────────────────┐
   │ Provider (cloud.Provider interface)         │
   │  simulate  │  aws/rds  │ (future)           │
   │  CreateDatabase, BackupDatabase, Restore...  │
   └─────────────────────────────────────────────┘
```

The relationship that matters: **the API only starts workflows; the workflows
do the work.** No handler directly mutates tenant infrastructure. History
lives in Temporal; the tenant row in Postgres is a projection with a strict
status state machine; the actual resources (databases, roles, Keycloak
users) are effects performed by activities.

## 3. Engineering decisions and their why

### 3.1 Workflows are deterministic; side effects are activities

Temporal replays workflow history on restart. Any in-workflow branch that
depends on wall-clock time, random values, or ambient state would replay to a
different decision. So **every decision input is pinned in history
(deterministic code + activity results)**, and every side effect is a
retryable activity. This is the basis of the failure matrix's wildest
guarantee: kill the worker mid-repair and restart it; the run converges
identically (ADR-0008 documents the deployment consequence: the worker is a
stateless, versioned binary, rebuild-from-HEAD-and-restart).

### 3.2 Database-per-tenant with owner roles

Tenant isolation is physical: each dedicated tenant gets its own database and
a dedicated owner role, and `CONNECT` is revoked from `PUBLIC` (the single
most dangerous multitenancy drift — now auto-repaired, §3.6). Shared-schema
tenants use one database with `tenant_id` scoping enforced at the repository
layer. Server-side timestamps (`updated_at`) and `ON CONFLICT DO NOTHING` make
retries idempotent. The role lives independently of the database (survives
migrations; dropped only at terminal teardown) — see
[ADR-0005](adr/0005-owner-roles.md).

### 3.3 Every lifecycle is a saga; delete is soft and cancelable

Each operation is a saga with per-activity retries and a recorded failed-run
artifact for anything that exhausts retries. Delete first enters a durable,
cancelable grace window (Temporal timer survives worker restarts) and only
then runs backup → deprovision → cleanup, so an accidental delete is
reversible for GracePeriod and every deleted tenant still has a backup
([ADR-0007](adr/0007-soft-delete.md)).

### 3.4 The tenant status is a real state machine (CAS)

All transitions go through one guarded UPDATE
(`UpdateTenantStatusFrom`), so concurrent workflows physically cannot
clobber each other: the loser of a race gets `ErrStatusConflict` → 409 or a
failed run, never corruption (Phase 13.4 proved DELETE×UPGRADE, DELETE×BACKUP,
MIGRATE×BACKUP, RECONCILE×MIGRATE, RECONCILE×DELETE live, one winner each).
Before this rule, a create/delete race could strand a half-state; that class
of bug is now unreachable at the storage layer.

### 3.5 The reconciler's job has a scope

Reconcile is a control loop: resolve desired state → probe actual → detect
drift → repair → re-probe → converge. Its scope is explicitly **database
infrastructure + backup policy** — database exists, isolation shape, owner
role, a completed backup — not application data or identity
([ADR note; reconciler doc comment](adr/0008-worker-deployment.md) and
`internal/workflow/reconcile.go`). Drifts found in the wild so far: missing
database, missing backup, `PUBLIC` CONNECT granted, wrong owner.

### 3.6 The data-safety rule (the hardest lesson)

Active dedicated tenants have *something to lose*. When reconcile finds a
missing database, restoring from the latest **verified** backup is
unambiguous; but when there is no verified backup, the tempting "fix" is to
recreate an empty database and call it converged. That would silently destroy
the tenant's data and the operator's ability to notice. TenantFlow's rule,
proved live (Phase 13.1): **never synthesize an empty database, escalate
instead** — audit `TENANT_RECONCILE_UNRECOVERABLE`, fail non-retryable into
the DLQ with a readable reason. External `DROP DATABASE` on a backed-up
tenant converged with marker data restored from the backup; the same drop on
an unbacked-up tenant left the database absent and the operator in control.

### 3.7 Every read is authenticated

The control plane is a REST API in front of a durable engine — reads are
cheap, but they reveal platform topology (which tenants exist, their state,
events, backups, cost). Phase 15.4 removed the last unauthenticated surface:
only `GET /status` stays public, and only because a load balancer must probe
liveness before anyone has logged in. Every tenant read requires a valid
Keycloak bearer token; the realm's two roles map onto a deliberately simple
model — **any authenticated platform user may read, `platform-admin` may
write**. `platform-operator` is therefore the honest read-only tier (inspect
tenants, events, backups, cost), while every mutation and the failed-runs DLQ
demand the admin role. The boundaries are proven at the router, not just the
handler: `internal/router/router_test.go` drives real HTTP requests through
the mux and asserts 401 (no/invalid token), 403 (operator on a write), and
200/202 (authorized), so a regression in the middleware wiring fails a test
instead of leaking a tenant list.

## 4. Failure handling done for real

The failure matrix is not a table of hypotheticals: the repo drives each
activity as the failing actor at every position in every saga and asserts the
workflow's exact composition (which activities run, which are skipped, which
audit lands). It found real bugs — a delete saga that reported success while
leaving 100 databases behind (fixed to real teardown), a replay that failed
forever against an already-dropped database (fixed with probe-before-dump),
an unregistered activity stranding 30 tenants in `deleting` with no
replayable artifact. Each became a regression test. The DLQ (`failed-runs`)
plus `/retry` is the operator's recovery lane: replay re-runs the workflow
and converges the world — proven in the demo's beat 2→3 (sabotaged delete →
one-click restore) and in the load run's healing of 30 stuck tenants.

## 5. What the numbers say

Full run details in [load-test.md](load-test.md). Highlights (n=100, c=10,
soft-delete with 2s grace):

| | shared | dedicated |
|---|---|---|
| provision p50/p99 | 0.83 / 1.09 s | 2.00 / 2.52 s |
| delete p50/p99 | 3.04 / 3.27 s | 7.54 / 8.56 s |
| postgres CPU max | 222% | 606% |
| peak connections | 53 | 64 (36% headroom under default 100) |
| control plane | worker ≤4% CPU, 66 MiB RSS; API ≤1% | same |
| leftovers | 0 DB / 0 roles / 0 identities, 0 DLQ rows | same |

- Temporal-server-side durations track the client clock to within ~0.1s at
  every percentile — the workflow engine is not a queueing tax at this scale.
- Dedicated delete costs ~2.5x shared because every teardown captures a real
  pre-delete backup (`pg_dump` in the postgres container). That is the
  durability cost of the safety net, and it still bounded every delete inside
  the poll timeout.
- The control plane is almost free next to the data plane: this is the
  architectural claim, measured.

## 6. What I'd do at production scale

- Real provider parity (AWS RDS / GCP Cloud SQL) behind the same activity
  interfaces; the provider abstraction exists precisely so substitution is a
  reimplementation, not a redesign.
- Soak + multi-worker scale-out (already-scoped, measured in the load-test
  "not measured" note).
- Scheduled reconcile — implemented in Phase 14 as `ReconcileSweepWorkflow`
  (fixed workflow ID `reconcile-sweep`): every `TENANTFLOW_RECONCILE_SWEEP_INTERVAL`
  (default 10m) it enumerates ACTIVE tenants, starts one `reconcile-<id>`
  child per tenant under the SAME workflow ID the manual endpoint uses, so an
  in-flight collision (manual call, or a second worker's tick) surfaces as
  `WorkflowExecutionAlreadyStarted` and is skipped — never a second workflow.
  `ContinueAsNew` every 100 sweeps bounds history on the forever loop. Honest
  remaining gap: per-tenant jitter (all tenants sweep on the same interval)
  is still future work.
- Alerting on DLQ depth and `TENANT_RECONCILE_UNRECOVERABLE` (the
  observability surface — audit events, metrics, recorder — is already there).
- Keycloak high availability and token-refresh hardening (the admin-token
  refresh bug found in Phase 12 was a real-world lesson: caches that hold
  credentials need invalidation hooks).

## 7. Honest limitations

- Shared-schema tenant isolation rests on repository-layer `tenant_id`
  discipline; a production deployment should evaluate Postgres RLS as a
  defense-in-depth layer.
- "Verified backup" means the backup row completed; there is not yet
  automated restore-on-restore validation. The restore path itself is
  exercised (live 13.1) but a scheduled restore check is future work.
- The load test is lifecycle-throughput, not sustained soak.
- The reconcile loop is interval-driven (Phase 14 sweep) plus on-demand; all
  active tenants tick on the same cadence (no per-tenant jitter), so "drift
  detected within N minutes" is bounded by the interval but not yet a
  measured SLO.