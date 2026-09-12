# ADR-0003: Shared schema inside each tenant database

- Status: accepted
- Date: 2026
- Deciders: project owner

## Context

Each tenant database (`tenant_<id>`) must hold the application schema.
The open question was how tenants evolve their schema independently while
the control plane stays simple.

## Decision

All tenants share the same application schema in the default (`public`)
schema of their own database. Schema *changes* are managed by the migrate
saga as a full, explicit operation: build a `tenant_<id>_new` database,
copy data, rename `_new` → production name, drop `_temp` copies. On retry
the operation pre-drops leftovers so the happy path is deterministic
(`docs/idempotency.md` §4.2–4.4). Rejected alternatives:

- **Per-tenant DDL versioning (Liquibase/Flyway per tenant)**: N independent
  migration histories to reconcile, with no shared guarantee — overkill for
  identical tenants.
- **Migrate-in-place with ALTER TABLE**: no safe cutover story, and a failed
  mid-migration leaves a half-applied schema.

## Consequences

Positive:

- Schema is uniform; upgrade contracts (backup → migrate → switch) are the
  only schema-changing path.
- `_new`-first cutover gives a cheap rollback point (the old DB is intact
  until rename).
- The migrate path is exercised by integration tests with a real provider and
  a retry window.

Negative:

- Copy-based migration is heavier than ALTER TABLE for wide tables —
  acceptable at this scale and consistent with DB-per-tenant backup/restore.
- Leftover `_new`/`_temp` databases from a hard crash are removed
  idempotently on the next retry (see idempotency doc).

## References

- `internal/cloud/docker.go` — `RenameDatabase`, `DropDatabaseNamed`
- `internal/workflow/migrate.go`, `internal/activities/migrate.go`
- `internal/activities/migrate_integration_test.go` — retry-window proofs