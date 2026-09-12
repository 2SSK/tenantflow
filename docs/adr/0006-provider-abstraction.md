# ADR-0006: Cloud provider abstraction

- Status: accepted
- Date: 2026
- Deciders: project owner

## Context

The control plane needs a small, stable surface for infrastructure
operations: create/drop/rename databases, backup, restore. Real deployments
may target managed Postgres (RDS/Cloud SQL); the project needed to stay
testable locally and in CI without any cloud credentials.

## Decision

Define a `cloud.Provider` interface and implement exactly one backend:
`DockerProvider` (`internal/cloud/docker.go`), which operates on Postgres
containers. The provider is the *only* package that talks to the database
engine for lifecycle operations — workflow activities depend on the
interface, never on Docker. Rejected alternatives:

- **Direct SQL from activities**: couples orchestration to one engine and
  makes provider swaps impossible.
- **Shelling out to `psql` from Go at the call site**: un-discoverable,
  duplicate of safety logic.

## Consequences

Positive:

- Integration tests run the *same* provider code against real Postgres on
  a dev machine and in CI (Docker is present on GitHub runners).
- CI proved the design: the workflow starts a `postgres` container that the
  provider finds by name and `docker exec`s `psql` into —
  `.github/workflows/integration.yml` documents this non-obvious contract.
- A managed-Postgres provider (e.g. `cloudsql` or `rds`) can be added
  without touching workflows or activities.

Negative:

- `DockerProvider` deliberately execs inside a container; it cannot be used
  against `localhost:5432` services not running in Docker.
- Safety net (identifier validation, ownership, idempotent create) lives in
  the provider — it must be duplicated for any future backend.

## References

- `internal/cloud/provider.go` — the interface + naming contract
- `internal/cloud/docker.go` — the implementation
- `internal/activities/*_integration_test.go` — provider-backed proofs
- ADR-0002, ADR-0003, ADR-0005 — the contracts the provider implements