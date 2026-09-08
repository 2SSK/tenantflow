# TenantFlow

A multi-tenant database platform where every lifecycle operation — provision, upgrade, migrate, backup, restore, delete — runs as a durable **Temporal workflow** with saga compensation, an auditable event log, chaos-injection testing, and a per-tenant cost model.

The interesting part: **deletions that fail mid-flight can be resumed from a DLQ instead of being abandoned.**

```
tenant lifecycle event
      ↓
Temporal workflow (durable, retryable)
      ↓
saga of activities (provision DB, create aux DBs, set quotas, provision identity…)
      ↓
on failure → compensation rollback        on success → audit event + status transition
      ↓
failed runs mirrored to a DLQ table → POST /retry resumes them
```

## Why this exists

SaaS platforms quietly get away with "it works on my machine" because their state is ephemeral. Tenant provisioning is **stateful and long-running**: a database must exist before the app connects, a dropped DB must be cleaned up, a half-created tenant must be rolled back. TenantFlow models every one of those operations as a durable workflow, and treats "the worker died at 3 AM" as a **normal input**, not an edge case.

The delete path is the heart of the project: after a grace period, a tenant's database is backed up, deprovisioned, and its identity dropped. If any activity fails (say, the database host is unreachable), the workflow retries — but if it exhausts retries, the run is mirrored to a **DLQ** (`workflow_instances` with status `failed`) and the tenant is left in `deleting`. An operator can then `POST /api/v1/tenants/{tenantID}/retry` and the system decides *intelligently* what to do next:

| Tenant status | Retry behavior |
|---|---|
| `deleted` | 409 — nothing to retry |
| `failed` | re-provision (new workflow) |
| `deleting` | resume the delete workflow, skipping the already-satisfied transition and grace period (`DELETE` executed with `{"Resume": true}`) |
| any other active state | 409 — only failed tenants or stuck deletions can be retried |

The screenshot below is the *real* failure moment: a chaos-injected worker killed the `DeprovisionTenant` activity on attempt 3.

![Delete workflow failing under chaos injection](docs/screenshots/temporal-delete-failed.png)

That run landed in the DLQ, and the operator's one-call resume (`POST /retry`) completed the deletion in seconds.

## Stack

- **Go 1.22+** — API (`cmd/api`) and worker (`cmd/worker`)
- **Temporal** — durable workflow engine
- **PostgreSQL** — platform state + one database per tenant
- **Keycloak** — OIDC authentication + role-based access control
- **Prometheus + Grafana** — metrics and dashboards

## Quick start

```bash
# 1. Configure (cp from template, then edit secrets if needed)
cp .env.example .env

# 2. Start the platform: PostgreSQL, Temporal (+ UI), Keycloak
docker compose up -d

# 3. The API and worker load .env automatically (via godotenv)
go run ./cmd/api      # listens on TENANTFLOW_HTTP_PORT (default 9090)
go run ./cmd/worker   # polls the tenantflow task queue
```

| Component | Address |
|---|---|
| API | http://localhost:9090 (status: `GET /status`) |
| Worker metrics | http://localhost:9091/metrics |
| Temporal UI | http://localhost:8080 |
| Keycloak | http://localhost:8081 |
| Prometheus | http://localhost:9092 |
| Grafana | http://localhost:3000 (admin / admin) |

### Optional: observability stack

```bash
docker run -d --name tf-prometheus --network=host \
  -v "$PWD/deploy/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml:ro" \
  prom/prometheus --config.file=/etc/prometheus/prometheus.yml --web.listen-address=:9092

docker run -d --name tf-grafana --network=host \
  -v "$PWD/deploy/grafana/provisioning:/etc/grafana/provisioning:ro" \
  -v "$PWD/deploy/grafana/dashboards:/var/lib/grafana/dashboards:ro" \
  grafana/grafana:latest
```

The dashboard (`TenantFlow`, uid `tenantflow-overview`) is provisioned from `deploy/grafana/dashboards/tenantflow.json` and shows workflow latency/outcomes, activity results, compensation rollbacks, HTTP status, worker pollers, and the per-tenant cost view.

![TenantFlow Grafana dashboard](docs/screenshots/grafana-overview.png)

## API

Read-only routes need no auth; mutating routes require a Keycloak bearer token with the `platform-admin` role:

```bash
TOKEN=$(curl -s -X POST http://localhost:8081/realms/tenantflow/protocol/openid-connect/token \
  -d 'grant_type=password&client_id=tenantflow-api&client_secret=<secret>&username=<user>&password=<pass>' \
  | jq -r .access_token)
```

| Method & path | Auth | Purpose |
|---|---|---|
| `GET /status` | – | liveness |
| `GET /api/v1/tenants` | – | list tenants |
| `GET /api/v1/tenants/{id}` | – | tenant detail |
| `GET /api/v1/tenants/{id}/events` | – | audit events (newest first) |
| `GET /api/v1/tenants/{id}/backups` | – | backup history |
| `GET /api/v1/tenants/{id}/cost` | – | monthly cost estimate |
| `POST /api/v1/tenants` | admin | provision (`{"tenantID","isolationMode":"dedicated\|shared"}`) |
| `DELETE /api/v1/tenants/{id}` | admin | start delete workflow (grace period, then deprovision) |
| `POST /api/v1/tenants/{id}/cancel-delete` | admin | cancel a pending deletion |
| `POST /api/v1/tenants/{id}/upgrade` | admin | toggle isolation tier |
| `POST /api/v1/tenants/{id}/migrate` | admin | migrate tenant database |
| `POST /api/v1/tenants/{id}/backup` | admin | snapshot tenant database |
| `POST /api/v1/tenants/{id}/restore` | admin | restore from snapshot |
| `GET /api/v1/failed-runs` | admin | DLQ: failed workflow runs |
| `POST /api/v1/tenants/{id}/retry` | admin | re-provision or resume a stuck deletion |

## Cost model

`GET /api/v1/tenants/{id}/cost` and the Grafana cost panels estimate monthly cost from **measured resource metadata** (the same shape a real SaaS billing system would use):

| Component | Rate | Measured from |
|---|---|---|
| Storage | $0.10 / GB / month | live `pg_database_size` of `tenant_<id>` |
| Compute (dedicated) | 1 vCPU × $0.02 / vCPU-hour × 730h | isolation tier |
| Compute (shared) | 0.25 vCPU × $0.02 / vCPU-hour × 730h | isolation tier |
| Memory (dedicated) | 2 GB × $0.002 / GB-hour × 730h | isolation tier |
| Memory (shared) | 0.5 GB × $0.002 / GB-hour × 730h | isolation tier |
| Workflows | $0.0001 / run | workflow instances started in the last 30 days |
| Backups | storage × backup count | completed backups per tenant |

A dedicated tenant pays the compute/memory floor (~$17.52/mo) whether or not it stores anything — like a reserved instance; a shared tenant floors at ~$4.38/mo. The Prometheus collector (`internal/cost`) measures every tenant on each scrape, so a deleted tenant can never leave a stale cost series behind.

## Database ownership

Every tenant database is born with a **dedicated owner role** and locked against PUBLIC — the isolation shape a real database-per-tenant SaaS hands off to the tenant application:

| Database | Owner | PUBLIC connect |
|---|---|---|
| `tenant_demo-b` | `tenant_demo-b` | denied |
| `tenant_owner-demo` | `tenant_owner-demo` | denied |
| `tenantflow` (platform) | `temporal` | allowed |

- `CreateDatabase` ensures role `tenant_<id>` (idempotent), makes it the database `OWNER`, and runs `REVOKE CONNECT ... FROM PUBLIC`. The role has no password (`NOLOGIN` by default) — credential delivery to tenant apps is deliberately deferred; this commit establishes the *ownership* contract.
- **Auxiliary databases reuse the role**: migrate (`_new`) and backup verification (`_temp`) borrow the tenant's own role instead of minting throwaway ones, so the count of tenant roles is always one per tenant.
- **Migration preserves ownership**: `_new` is born owned by `tenant_<id>`, so `ALTER DATABASE ... RENAME` (which keeps the owner) promotes it into the live name with the isolation shape intact.
- **Terminal teardown drops the role**: the provision saga's `DropTenantDatabase` compensation drops the database *then* `DropTenantRole`. Migrate's switch does NOT drop the role — it is not a terminal teardown.

## Reliability & recovery design

- **Durable workflows**: every operation survives worker crashes and restarts (Temporal replays history).
- **Saga compensation**: deprovision/delete paths roll back half-done work (drop created databases, restore quotas, restore identity) when a later step fails.
- **Grace period for deletion**: `DeleteTenantWorkflow` transitions to `deleting`, waits `TENANTFLOW_DELETE_GRACE_PERIOD` (default 30 days), then deprovisions. `cancel-delete` can interrupt it safely.
- **DLQ for failed runs**: the worker mirrors failed workflows into `workflow_instances` (`status='failed'`) — the source of `GET /api/v1/failed-runs`.
- **Workflow-aware retry**: the retry handler distinguishes *a failed tenant* from *a stuck deletion*, and resumes the exact delete workflow with `Resume: true` instead of blindly re-running it — the grace period and state transition are never repeated.
- **Chaos injection**: set `TENANTFLOW_CHAOS_ACTIVITIES` + `TENANTFLOW_CHAOS_RATE` to make the worker fail real activities at runtime — how the stuck-deletion scenario above was produced and verified.

## Observability

The API exposes Prometheus metrics at `/metrics` (`tenantflow_http_requests_total`, `tenantflow_activity_executions_total`, plus the `tenantflow_tenant_*` cost gauges); the worker exposes its own metrics on `TENANTFLOW_WORKER_METRICS_ADDR`. Temporal's own metrics (`temporal_*`) are scraped by the same Prometheus. The Grafana dashboard (8 panels) aggregates: provision latency p95, workflow outcomes, activity results, rollbacks, HTTP status codes, worker pollers, and per-tenant cost.

## Repository layout

```
cmd/api           API server (auth middleware, routes, cost collector wiring)
cmd/worker        Temporal worker (registers workflows + activities)
internal/
  activities      one activity per side effect (provision, deprovision, backup…)
  app             composition root (Postgres, Temporal client, repos, keycloak hook)
  auth            OIDC middleware + role checks
  billing         resource quotas per tenant
  chaos           runtime fault injection
  cloud           compute provider abstraction (Docker implementation)
  config          env-driven configuration
  cost            estimation model + Prometheus collector
  database        Postgres bootstrap + migrations (tenants, audit, workflow instances)
  handler         HTTP handlers
  identity        Keycloak identity provider
  instance        workflow-instance (DLQ) repository + recorder
  metrics         Prometheus registry helpers
  middleware      request logging + metrics
  model           entities (Tenant, AuditEvent, Backup, IsolationMode…)
  repository      Postgres repositories
  router          ServeMux route table (flat, method-prefixed)
  temporal        Temporal client wrapper
  worker          worker bootstrap
  workflow        workflow definitions (provision, delete, upgrade, migrate…)
deploy/           compose overlay, Postgres init, Keycloak realm, Prometheus, Grafana
web/              project docs site
ROADMAP.md        build plan with completion tracking
```

## Quality gates

```bash
gofmt -l internal/ cmd/
go build ./...
go vet ./...
go test ./...
go test -tags integration ./internal/repository/ ./internal/instance/
```

## License

MIT