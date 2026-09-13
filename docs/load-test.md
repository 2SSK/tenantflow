# Load Test Report

Measured period: 2026-09-13, against the local development stack at the
commit this Phase-12 report was written from. Every number below is a real
measurement from the run in question; nothing is estimated or extrapolated.

## Environment

| Component | Version / Mode |
|---|---|
| Host | single dev machine (the same one running this repo) |
| PostgreSQL | 16.14 (`postgres:16`, container `tenantflow-postgres`) |
| Temporal | 1.31.0 (`temporalio/server:1.31.0`) |
| Keycloak | 26.7.0 (`quay.io/keycloak/keycloak:26.7.0`) |
| API / worker | binaries built from this repo at the tested commit |
| Delete grace period | `TENANTFLOW_DELETE_GRACE_PERIOD=2s` (soft delete window) |
| Load generator | `cmd/loadtest` against `http://localhost:9090` |
| Concurrency / volume | 10 workers, n = 100 tenants per scenario |

Both scenarios were run with a fresh tenant-ID prefix, so DBs, roles, and
Keycloak identities created by a scenario are attributable to that scenario
exactly.

## Scenario A — shared isolation

`cmd/loadtest -mode shared -prefix shared3 -n 100 -c 10 -delete`

| Phase | Result | Wall | Throughput | Latency (s) |
|---|---|---|---|---|
| Provision `provision -> active` | 100/100 ok, 0 errored | 10.1s | 592.0/min | p50 1.02, p95 1.26, p99 1.46, mean 0.96 (min 0.43, max 1.46) |
| Soft delete `delete -> deleted` | 100/100 ok, 0 errored | 37.0s | 162.3/min | p50 3.47, p95 4.54, p99 4.96, mean 3.57 (min 2.84, max 5.19) |

Teardown leftovers after deletion: **0 databases, 0 roles, 0 Keycloak
identities** (checked across `pg_database`, `pg_roles`, Keycloak users).
Failed-runs / DLQ rows attributable to this scenario: **0**.

## Scenario B — dedicated isolation

`cmd/loadtest -mode dedicated -prefix dedicated3 -n 100 -c 10 -delete`

| Phase | Result | Wall | Throughput | Latency (s) |
|---|---|---|---|---|
| Provision `provision -> active` | 100/100 ok, 0 errored | 22.1s | 271.5/min | p50 2.08, p95 2.75, p99 2.91, mean 2.14 (min 1.45, max 2.94) |
| Soft delete `delete -> deleted` | 100/100 ok, 0 errored | 1m24.9s | 70.7/min | p50 7.34, p95 18.54, p99 18.55, mean 8.47 (min 6.12, max 18.74) |

Teardown leftovers after deletion: **0 databases, 0 roles, 0 Keycloak
identities**. Failed-runs / DLQ rows attributable to this scenario: **0**.

Interpretation notes (real observations, not measurements):

- Dedicated provision is ~2x slower than shared because every tenant gets a
  dedicated PostgreSQL role in addition to the database. This is the intended
  isolation-versus-cost trade-off.
- Dedicated delete's p95/p99 (~18.5s) is a spike, not a plateau: the
  pre-delete backup runs `pg_dump` inside the postgres container, and with
  10 concurrent deletes the container's serialized dump work occasionally
  queues. The mean (8.47s) vs shared (3.57s) is the durability cost of
  capturing a backup before teardown. max 18.74s stayed inside the 4-minute
  per-tenant poll timeout with 0 failed deletes.

## What the load test found (real bugs, discovered by these runs)

1. **Shared-mode delete phase reported success ~500ms after the API call.**
   The harness treated the tenant's still-`active` status (during the 2s
   grace/soft-delete window) as terminal for the delete phase, so the first
   poll returned instantly: "delete ended at active" rather than `deleted`.
   Fixed in the harness by making the delete poll wait for `deleted`
   (`d0ead58`). The correct delete path then measured 100/100 on both
   scenarios above.

2. **The delete saga was a simulation, not a teardown.** Before the fix, the
   run with the corrected harness (prefix `dedicated`) deleted 100/100
   tenants, reported 0 DLQ rows — and left **all 100 tenant databases behind**.
   `DeprovisionTenant` slept 2 seconds and wrote an audit event with
   `"infra": "simulated teardown"`; it never dropped the database, the owner
   role, or the Keycloak identity. The fix replaced the simulation with real
   teardown: `DROP DATABASE ... IF EXISTS` + `DROP ROLE ... IF EXISTS` +
   Keycloak `DeleteUser`, and added `DeleteTenantIdentityByTenant` to the
   delete workflow (`0ba69b9`). The dedicated3 numbers above are measured
   AFTER this fix; the leftover check (0/0/0) proves the teardown now runs
   for real.

3. **Worker-version skew between a new activity and a running worker.** The
   dedicated rerun immediately after fixing the saga (prefix `dedicated2`)
   stalled: every delete workflow failed with `unable to find activityType=
   DeleteTenantIdentityByTenant` because the worker binary had not been
   restarted with the new activity registered (`edf892d`). Two follow-ups
   this uncovered:
   - An unregistered activity fails before the run-mirror interceptor runs,
     so no failed row is recorded and `/retry` legally refuses. The observed
     state: 30 tenants stuck in `deleting` with no recoverable failed run.
   - Replaying a delete whose database was already dropped re-ran the
     pre-delete backup against the missing DB: `pg_dump ... tenant_<id>`
     exited 1 and the resumed run re-failed. The observed state: 60 failed
     delete records, 30 tenants still `deleting`, databases they owned
     already gone — an eternal DLQ loop.

## The DLQ healing that the load test drove (real numbers)

After the fixes above were loaded onto the worker, the 30 stuck tenants
(prefix `dedicated2`) were recovered:

- `POST /api/v1/tenants/{id}/retry` on each stuck tenant: 30/30 accepted.
- Result: **30/30 reached `deleted`**, 0/30 errors, 0 new DLQ rows.
- Keycloak identities for the healed tenants: 0 remaining.
- 60 stale failed-run artifacts (the two failure waves above) were flushed;
  DLQ view after healing: **0 failed rows**.

The convergence fix the replay loop needed: `BackupTenantData` now probes the
provider (`InspectDatabase`) before `pg_dump` and skips the backup with a
`(skipped: database absent)` marker when the database is already gone, so a
replay after teardown converges instead of re-failing forever. This is the
change the final runs in this report were measured with.

## Scope

- Workload: this report measures HTTP-visible tenant lifecycle behavior
  (provision + soft-delete with a 2s grace period, 10 concurrent workers).
- Not measured here: sustained soak, multi-host scale-out, and the migrate
  workflow (its own durability checks cover it). Those belong to a follow-up
  before production hardening.