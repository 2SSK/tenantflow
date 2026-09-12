# ADR-0002: Database per tenant

- Status: accepted
- Date: 2026
- Deciders: project owner

## Context

Tenants run mutable Postgres state. The core value of TenantFlow is data
isolation: a tenant's migration, corruption, or backup/restore must never
affect another tenant's data plane.

## Decision

Give every tenant its own Postgres database named `tenant_<id>` on the
control-plane Postgres instance. Rejected alternatives:

- **Single database + `tenant_id` column**: cheapest to operate, but a
  single bad restore, runaway query, or per-tenant migration breaks everyone
  and makes per-tenant ownership meaningless.
- **Schema-per-tenant in one database**: better isolation but leaky
  ownership (one set of roles/databases), and `pg_restore` of a single
  schema is more awkward than a full database.

## Consequences

Positive:

- Operations are per-tenant by construction: backup/restore/migrate operate
  on one database without touching neighbours.
- Restore is a full `pg_restore` of a `pg_dump` artifact — the cleanest
  Postgres rollback primitive we have.
- Cross-tenant blast radius is one database.

Negative:

- More databases to monitor (mitigated by Grafana dashboards in `deploy/`).
- Cross-tenant queries are impossible by design — acceptable, since tenants
  must never share data.

## References

- `internal/cloud/provider.go` — `TenantDatabaseName` naming rule
- `internal/activities/migrate_integration_test.go`, `backup.go`, `restore.go`
- `docs/idempotency.md` §4 (per-ops idempotency proof on real databases)