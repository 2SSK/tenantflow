# ADR-0005: Dedicated owner roles per tenant database

- Status: accepted
- Date: 2026
- Deciders: project owner

## Context

The data plane needs least-privilege, ownership-isolated access. A tenant's
application role must be able to use *its* database but never another
tenant's, and tenant DBs must not be connectable by the public role.

## Decision

For each tenant database, `CreateDatabase`:

1. creates the database `tenant_<id>`,
2. derives a dedicated role `tenant_<id>`,
3. makes the role the database **OWNER**,
4. runs `REVOKE CONNECT ON DATABASE "tenant_<id>" FROM PUBLIC`.

The owner role is `NOLOGIN` by default — credential delivery to tenant apps
is deliberately deferred; this commit of the design establishes the
ownership contract and the blast-radius boundary. All database identifiers
pass `safeID` validation (letters, digits, `_`, `-`; anchored) which doubles
as SQL-injection protection. On retry the create path inspects first and
is idempotent (see `docs/idempotency.md` §4.1).

## Consequences

Positive:

- One tenant's app cannot connect to another tenant's DB even if it can
  guess the name (PUBLIC revoked).
- Ownership follows the tenant role, so cleanup (`DropDatabase`) is a clean
  `DROP ROLE` + `DROP DATABASE` with no orphans.
- Migrate's `_new`/switch cutover reassigns ownership so the tenant role
  owns the final database.

Negative:

- Credential delivery is out of scope for now — a tenant integration task
  for a future phase (log it in ROADMAP/known gaps, not in this ADR).
- `NOLOGIN` means the control plane's `temporal` superuser is the only
  connector — correct at this stage, revisit when tenant apps exist.

## References

- `internal/cloud/provider.go`, `internal/cloud/docker.go`
- `internal/activities/provision.go`
- `internal/activities/migrate_integration_test.go` (ownership assertions)
- `docs/idempotency.md` §4.1