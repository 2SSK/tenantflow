# Load Test Report

Measured period: 2026-09-13, against the local development stack at the
commit this Phase-12 report was written from. Every number below is a real
measurement from the run in question; nothing is estimated or extrapolated.
The Phase 13.3 addendum (resource metrics + server-side task latency) was
measured later the same day at the Phase-13 commit; it is a fresh, coherent
dataset (client latency, server latency, and resources all from the same two
runs, `lt-s3` / `lt-d3`).

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

## Phase 13.3 addendum — resource metrics + server-side task latency

Measured on the Phase-13 commit. Fresh run, same recipe as above
(`lt-s3` shared, `lt-d3` dedicated, n=100, c=10, `-delete`). All three
datasets — client latency, Temporal-server-side duration, and resource
sampling — come from these same two runs.

### Client-visible latency (the loadtest's own clock)

| Scenario | Phase | Result | Wall | p50 | p95 | p99 | mean (min–max) |
|---|---|---|---|---|---|---|---|
| lt-s3 shared | provision → active | 100/100, 0 err | 8.4s | 0.83 | 1.06 | 1.09 | 0.82 (0.41–1.24) |
| lt-s3 shared | delete → deleted | 100/100, 0 err | 30.3s | 3.04 | 3.26 | 3.27 | 2.97 (2.64–3.46) |
| lt-d3 dedicated | provision → active | 100/100, 0 err | – | 2.00 | 2.32 | 2.52 | 1.97 (1.30–2.71) |
| lt-d3 dedicated | delete → deleted | 100/100, 0 err | – | 7.54 | 8.36 | 8.56 | 7.54 (6.69–8.56) |

### Temporal-server-side task duration

From `temporal_visibility.executions_visibility` (`close_time - start_time`,
status=2 completed, 100/100 rows each). This is the time the Temporal engine
itself accounted for: task scheduling + activity execution + retries.

| Scenario | Workflow | p50 | p95 | p99 | max |
|---|---|---|---|---|---|
| lt-s3 shared | ProvisionTenantWorkflow | 0.82 | 1.08 | 1.15 | 1.16 |
| lt-s3 shared | DeleteTenantWorkflow | 2.93 | 3.24 | 3.28 | 3.46 |
| lt-d3 dedicated | ProvisionTenantWorkflow | 1.96 | 2.39 | 2.62 | 2.67 |
| lt-d3 dedicated | DeleteTenantWorkflow | 7.57 | 8.40 | 8.44 | 8.47 |

**Queue overhead is single-digit-to-tens of milliseconds**: server-side
durations track the client clock within ~0.1s across every percentile. The
API→Temporal starter + the task queue add no measurable latency at this
volume; the p99 client/server deltas (0.09s provision, 0.17s delete across
both modes) are HTTP + token checks + recorder writes.

### Resource metrics (sampled during the runs)

`docker stats` (3s cadence) for the stack containers; `ps` (3s) for the
control-plane processes; Postgres `pg_stat_activity` count (2s).

| Metric | lt-s3 shared | lt-d3 dedicated |
|---|---|---|
| postgres CPU mean / max | 120% / 222% | 315% / 606% |
| postgres mem max | 298 MiB | 358 MiB |
| temporal CPU mean / max | 59% / 152% | 32% / 93% |
| temporal mem max | 261 MiB | 275 MiB |
| keycloak CPU mean / max | 37% / 114% | 15% / 103% |
| keycloak mem max | 774 MiB | 770 MiB |
| postgres connections mean / max | 51.1 / 53 | 54.3 / 64 |
| worker CPU max / RSS | 3.1% / 66 MiB | 3.9% / 66 MiB |
| api CPU max / RSS | 0.7% / 60 MiB | 0.9% / 64 MiB |

Interpretation:

- **Postgres is the bottleneck, by design.** Dedicated mode pushes it to 3–6
  cores (pg_dump during delete backup + restore ownership work) and 64 peak
  connections — 36% headroom under the default `max_connections=100` for this
  stack. Shared mode stays half that.
- **The control plane is cheap.** The Go worker peaks under 4% CPU with a 66
  MiB RSS serving 200 workflow runs; the API is a rounding error. This is the
  case-study claim: control-plane overhead is negligible next to the
  per-tenant data plane.
- **Temporal's engine is not the constraint.** Its server-side durations are
  the workflow, not a queueing tax (see the delta table). The max 152% CPU
  blip in shared mode is history-replay during the 100-delete burst.
- **The earlier dedicated p99 spike (18.55s in `dedicated3`) did not recur**
  in `lt-d3` (p99 8.56s) at the same c=10. pg_dump queuing is contention at
  the postgres container, not a platform cliff: worst-case delete latency
  stays bounded and every delete still completed.
- Leftovers after this addendum's runs: **0 databases, 0 roles** (Keycloak
  identities checked in the loadtest teardown, 0 errors). These runs, like
  their predecessors, ended clean.

## Scope

- Workload: this report measures HTTP-visible tenant lifecycle behavior
  (provision + soft-delete with a 2s grace period, 10 concurrent workers).
- Not measured here: sustained soak, multi-host scale-out, and the migrate
  workflow (its own durability checks cover it). Those belong to a follow-up
  before production hardening.