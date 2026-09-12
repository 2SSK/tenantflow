# ADR-0001: Temporal as the workflow engine

- Status: accepted
- Date: 2026
- Deciders: project owner

## Context

Tenant lifecycle operations (provision, upgrade, migrate, backup, restore,
delete, reconcile) are multi-step, long-running, and frequently interrupted.
We needed durable, resumable execution with built-in retries, plus a way to
reason honestly about failures.

## Decision

Use Temporal (self-hosted Temporal Server via `docker-compose.yml`) with
per-activity retry policies and workflow tests that run in-memory
(`go.temporal.io/sdk/testsuite`). Rejected alternatives:

- **AWS Step Functions**: excellent, but couples the whole control plane to
  AWS and uses JSON state machines instead of normal Go code.
- **Airflow / batch schedulers**: tuned for scheduled DAGs, not event/API
  driven, long-running sagas with cancel semantics.
- **Homegrown state machine**: we already have global failure modes;
  reimplementing durable execution is exactly the "reimplement common
  functionality" anti-pattern this project avoids.

## Consequences

Positive:

- Workflows survive worker crashes: the engine replays history, so
  `workflow.ExecuteActivity(...).Get()` handles lost results.
- Retry policies are declarative (`MaximumAttempts: 3`) and unit-testable;
  the failure matrix (`docs/failure-matrix.md`) exploits this to prove
  every activity fails safely.
- The test suite needs no Temporal server for workflow logic.

Negative / obligations:

- **Activity idempotency is mandatory**: because an activity may run to
  completion but report nowhere, every activity must converge on retry.
  This obligation is audited in `docs/idempotency.md` and backed by live
  retry-window tests (e.g. `TestProvisionTenantGetOrCreateLive` for Keycloak).
- Temporal becomes core infrastructure (port 7233; UI on :8080).

## References

- `internal/workflow/*.go` — the six sagas + reconcile
- `internal/temporal/client.go` — client wiring
- `docs/idempotency.md`, `docs/failure-matrix.md` — the audit trail this
  decision forced us to write