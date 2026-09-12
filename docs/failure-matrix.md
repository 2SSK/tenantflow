# Failure Matrix

Every failure point in TenantFlow, the expected system behavior, and the test
or tool that proves it. Gaps found by this audit are listed in §6 and
automated in this phase.

Companion document: [`idempotency.md`](./idempotency.md) — what happens when a
failed activity is *retried*. This document is about what happens when an
activity *fails at all*.

---

## 1. Where failures come from

The worker depends on three external systems. Any of them can die; that is
the ordinary failure of this system, not an edge case.

```
Temporal (durable execution)  ── activity calls ──▶  worker process
                                                        │
              ┌─────────────────────────────────────────┼─────────────────────────┐
              ▼                                         ▼                         ▼
        Postgres (tenant data,           Docker engine          Keycloak (identity
        audit_events, workflow_instances) (per-tenant DBs)      users, roles)
```

| Source | Failure mode | What the worker sees |
|---|---|---|
| Postgres | Connection refused, query timeout, deadlock | activity returns an error |
| Docker engine | Daemon down, container create times out | activity returns an error |
| Keycloak | 5xx, token endpoint down | activity returns an error |
| Temporal | Worker killed (`kill -9`, OOM, deploy) | **no error at all** — activity result is lost |
| The worker itself | A bug throws an unexpected error | activity returns an error |

The fifth row (worker death) is the reason for the whole idempotency audit:
Temporal retries the activity, but the *world* (a database, a role, a Keycloak
user) may already reflect the first attempt.

---

## 2. The defense stack

Failures are handled in five layers. A failure that escapes one layer is
caught by the next.

```
1  Chaos injection (opt-in)      — every activity may fail on purpose
        ↓
2  Temporal retry (default)      — exponential backoff, up to max attempts
        ↓
3  Saga compensation             — the workflow reverses what it already did
        ↓
4  DLQ + manual retry            — failed run recorded; retry endpoint re-starts
        ↓
5  Reconciliation                — drift probe + repair script, converges later
```

**Layer 1 — chaos injection.** `internal/chaos/` implements a worker
interceptor that fails an activity *before it runs*, per a controller policy:

- `TENANTFLOW_CHAOS_RATE` — probability 0..1 that an eligible call fails
- `TENANTFLOW_CHAOS_ACTIVITIES` — which activity types to target
  (empty / `*` = all)

The activity never executes; the worker and the instance recorder see a
normal activity error, so the whole retry/compensation/DLQ path is exercised
with real Temporal. Unit-tested by `internal/chaos/chaos_test.go`; the actual
end-to-end chaos run is a manual demo (§5).

**Layer 2 — Temporal retry.** Every activity call is retried by the worker
with backoff. Retries must be **idempotent** — proven by the retry-window
integration tests (`internal/activities/migrate_integration_test.go`,
`identity_integration_test.go`, see §4).

**Layer 3 — saga compensation.** Each workflow tracks progress with guard
flags (`dbCreated`, `switched`, `preSnapshotTaken`, …) so compensation runs
*only* for steps whose side effects could already exist. The
fail-every-activity matrix (`internal/workflow/failmatrix_test.go`) asserts
each compensation rule fires exactly for the failure positions it must
(§3).

**Layer 4 — DLQ.** `internal/instance/` records every run
(`workflow_instances`), marks it failed on terminal failure, and
`POST /api/v1/tenants/{id}/retry` re-starts it. Recording is best-effort:
a repo failure is logged and swallowed, never turned into a workflow error
(`TestRecorder_BestEffortOnRepoError`).

**Layer 5 — reconciliation.** `ReconcileTenantWorkflow` probes actual state
vs desired state and repairs drift. It is the plane that reconverges whatever
the sagas left behind (`internal/activities/reconcile.go`,
`internal/workflow/reconcile.go`).

---

## 3. The matrix: workflow × activity × failure

`TestFailEveryActivity` in `internal/workflow/failmatrix_test.go` drives each
table below: for every activity of every workflow it forces that exact
activity to fail and asserts

- the workflow ends with an error;
- the terminal failure audit activity always runs;
- each compensation activity runs **exactly** when the failure position calls
  for it (index inside `[min, max]`) and never otherwise;
- the success terminal never runs unless it was the failed step itself.

Compensation ranges encode the guard flags per workflow:

```
provision  DropTenantDatabase   [2,3]   drop only once ProvisionTenant ran (db exists)
           DeleteTenantIdentity [3,3]   delete only once identity creation likely ran
migrate    DropTenantAuxDatabase [0,2]  drop only while the live DB is untouched
restore    RestoreRollback       [2,3]  roll back only once the pre-restore snapshot exists
upgrade    RollbackQuotas        [3,5]  roll back only once quotas were raised
backup     (none)  backup never mutates the live DB — nothing to roll back
delete     (none)  reviving a half-torn-down tenant would be a lie — failure
                  only audits TENANT_DELETE_FAILED
```

### 3.1 Provision

| # | Activity | On failure, before step ran | On failure, after this step | Idempotent retry proof |
|---|---|---|---|---|
| 0 | `CreateTenantRecord` | — | — | — |
| 1 | `ProvisionTenant` | nothing to undo | `DropTenantDatabase` | `TestProvisionTenantResumesAfterPartialCreate` — a retry inspects first, creates only if missing, re-asserts ownership |
| 2 | `ProvisionTenantIdentity` | `DropTenantDatabase` | `DropTenantDatabase` + `DeleteTenantIdentity` | `TestProvisionTenantIdentityCreatesWhenAbsent` / `ReusesExisting` / `GetOrCreateLive` — probe-first get-or-create |
| 3 | `MarkTenantActive` | + identity delete | + identity delete | — |

### 3.2 Migrate

| # | Activity | On failure, before step ran | On failure, after this step | Idempotent retry proof |
|---|---|---|---|---|
| 0 | `MarkTenantMigrating` | — | — | — |
| 1 | `MigrateData` | nothing to undo | `DropTenantAuxDatabase` (`_new` pre-dropped on retry) | `TestMigrateDataResumesAfterStaleNewDB` — stale `_new` DB from a crash does not block the retry |
| 2 | `SwitchTraffic` | `DropTenantAuxDatabase` | nothing to undo (live DB already switched; no aux DB left) | `TestSwitchTrafficRetryIsIdempotent` / `CompletesInterruptedSwitch` — `_new`-existence sentinel tells a retry "already done" |
| 3 | `MarkTenantMigrated` | — | — | — |

### 3.3 Backup

| # | Activity | On failure | Idempotent retry proof |
|---|---|---|---|
| 0 | `MarkTenantBackingUp` | nothing (read-only op) | — |
| 1 | `BackupTenantData` | nothing; `_temp` verification DB is pre-dropped on retry | identical pre-drop pattern to migrate, covered by it |
| 2 | `MarkTenantBackedUp` | — | — |

### 3.4 Restore

| # | Activity | On failure, before step ran | On failure, after this step | Idempotent retry proof |
|---|---|---|---|---|
| 0 | `MarkTenantRestoring` | — | — | — |
| 1 | `PreRestoreSnapshot` | nothing | nothing (snapshot is the safety net, not the tenant) | — |
| 2 | `RestoreData` | nothing (snapshot exists, live DB untouched) | `RestoreRollback(preBackupName)` | ⚠️ **documented gap** — plain `psql -f` restore is not retry-idempotent (§6, idempotency.md §4.2) |
| 3 | `MarkTenantRestored` | `RestoreRollback` | `RestoreRollback` | — |

### 3.5 Upgrade

| # | Activity | On failure, before step ran | On failure, after this step | Idempotent retry proof |
|---|---|---|---|---|
| 0 | `MarkTenantUpgrading` | — | — | — |
| 1 | `VerifyTenantActive` | — | — | — |
| 2 | `RaiseQuotas` | nothing | `RollbackQuotas(oldQuota)` | — |
| 3 | `EnableFeatures` | — | `RollbackQuotas` | — |
| 4 | `UpdateBilling` | — | `RollbackQuotas` | — |
| 5 | `MarkTenantUpgraded` | — | — | — |

### 3.6 Delete

| # | Activity | On failure | Notes |
|---|---|---|---|
| 0 | `MarkTenantDeleting` | nothing | grace-period timer + cancel window are workflow tests (`DeleteWorkflow_TimerExpirySucceeds`, `CancelSignalDuringGracePeriod`, `ResumeSkipsTransitionAndGrace`, `CancelRestoreFails`) |
| 1 | `BackupTenantData` | nothing | version-gated pre-delete backup; skipped for shared-schema tenants — they own no dedicated DB, so there is nothing to snapshot (found in the 12.2 load run: shared deletes stranded in the DLQ) |
| 2 | `DeprovisionTenant` | nothing (deliberate — see §3) | teardown has no compensation |
| 3 | `MarkTenantDeleted` | — | — |

### 3.7 Reconcile

Not a linear saga: it loops `resolve → probe → detect drift → repair → re-probe
→ converge` (or `skip` for non-active tenants), and it has **no
compensation** — repairs are idempotent and the re-probe is the proof, not
rollback. That changes its failure contract: a mid-run crash must fail the
run and must NOT audit `MarkReconcileFailed` (reserved for the
detected-unconverged branch).

Coverage is now full and position-exact:

- **Fast path** (already-converged): in the fail-every-activity matrix as
  `reconcile-converged` — `ResolveTenantSpec / ProbeTenantActualState /
  MarkReconcileConverged` each failing, any order.
- **Repair path** (drift → repair → re-probe → converge):
  `TestReconcileRepairPathActivityFailures` drives the branchy sequence
  `Resolve → Probe → RecordReconcileDrift → EnsureTenantDatabase →
  BackupTenantData → re-Probe (clean) → MarkReconcileConverged` as a
  fail-every-activity loop with exact composition assertions: drift logging
  runs only from position 2, ensure only from 3, backup only from 4, converged
  only when it is itself the failure, `MarkReconcileFailed` **never** on a
  mid-run crash. Failing mocks are persistent across retry attempts, so the
  assertions hold under the workflow's `MaximumAttempts: 3` policy.
- Scenarios: `ConvergedWhenNoDrift`, `RepairsDriftAndConverges`,
  `UnconvergedAfterRepairFails` (the only legitimate `MarkReconcileFailed`
  path), `SkipsNonActiveTenant`, `SharedTenantConvergesWithoutProbe`.

---

## 4. Non-activity failure points

| Failure point | Expected behavior | Proven by |
|---|---|---|
| Duplicate `POST /api/v1/tenants` | 409 conflict, no second workflow | `CreateTenatConflict` (handler) |
| Duplicate reconcile while in flight | 409, no duplicate workflow | `ReconcileTenantAlreadyInFlight` (handler) |
| Same run executing many activities → recorder dedupe | Only one `workflow_instances` row per run | `TestRecorder_RunningIsIdempotent` |
| Run fails after several activities | Row transitions running → failed with the error message | `TestRecorder_RunningThenFailed` |
| Recorder's repo is down | Failure is logged and swallowed — never breaks the workflow | `TestRecorder_BestEffortOnRepoError` |
| Retry endpoint called for a failed run | Workflow re-started; audit event records the replay | `RetryTenant` (handler) |
| Failed runs viewed in UI | Filtered by `status = failed` | `ListFailedRuns` (handler) |
| Invalid tenant ID / bad JSON on every write endpoint | 400, rejected before any workflow starts | `CreateTenantRejectsInvalidTenantID`, `CreateTenantBadJSON`, `ReconcileTenantRejectsInvalidTenantID`, … |
| Delete canceled during grace period | Cancel workflow instead of teardown | `DeleteWorkflow_CancelSignalDuringGracePeriod` |
| Cancel signal after teardown started | `CancelRestoreFails` — cancels on dead workflows are not errors | `DeleteWorkflow_CancelRestoreFails` |
| Delete workflow resumed after restart | Skips transition + grace period already elapsed | `DeleteWorkflow_ResumeSkipsTransitionAndGrace` |

---

## 5. The live chaos demo (manual)

With the stack running, chaos is an ops switch, not a test flag:

```
TENANTFLOW_CHAOS_RATE=0.5 TENANTFLOW_CHAOS_ACTIVITIES=ProvisionTenant go run ./cmd/worker
```

Watch in the UI: provision fails mid-saga → compensation drops the half-built
database → `workflow_instances` row goes failed → `POST /api/v1/tenants/acme-01/retry`
re-runs → tenant converges. `docs/screenshots/` shows the audit trail and
failed-runs view from the recorded demo.

---

## 6. Gaps found by this audit

Automated coverage is strong on the linear sagas and weak on two edges:

| # | Gap | Impact | Status |
|---|---|---|---|
| G1 | Reconcile workflow not in `TestFailEveryActivity`; its activities (mid-repair failure, failure between probe and converge) were only scenario-covered | A reconcile that dies mid-repair is exactly the "plane crashed on the way to the gate" case the system exists for | ✅ **Closed** — fast path added to the matrix (`reconcile-converged`, harness gained `noTerminalOnFailure`: reconcile must NOT audit failure on a mid-run crash); branchy repair path driven by `TestReconcileRepairPathActivityFailures` with position-exact composition + retry-persistent failing mocks |
| G2 | No automated **live** chaos test: the controller is unit-tested and the demo is manual, but nothing in CI proves a chaos-injected failure lands as a failed `workflow_instances` row and that retry heals it | Regression risk on the interceptor/recorder/retry interaction (order of interceptors, error propagation) | Open — integration test: start real worker + Temporal with chaos rate 1 targeting one activity → assert row `status=failed` → retry via the API surface → assert row converges |
| G3 | No full **retry-heals end-to-end** proof across the stack (handler → workflow → activities → real DB); resume is proven at the activity level only | The DLQ's promise ("retry converges") is demo-proven, not gate-proven | Open — pair with G2's live test: the retry leg makes it a single test |

Documented, accepted (not automating — see idempotency.md §4.2):
RestoreData is not retry-idempotent (plain `psql -f`); mitigated by the
pre-restore rollback snapshot; future fix is archive-format dumps +
`pg_restore --clean --if-exists`.

---

## 7. Rules for new operations

1. **Every new workflow ships with a matrix entry.** If it has a guard flag,
   add the workflow (activities + `compRule` ranges) to `matrices` in
   `failmatrix_test.go`, or the fail-every-activity guarantee silently stops
   covering it.
2. **Compensation ranges must encode guard flags**, never "intuition about
   order": the range is `[index-of-flag-setting-step, last-index]`.
3. **A step with no side effects needs no compensation** (backup); a step
   whose side effects must *not* be rolled back needs a comment saying why
   (delete teardown).
4. **Any new activity that creates or destroys named resources** gets a
   retry-window integration test per idempotency.md §5 rule 7.
5. When a behavior can't be automated, **say so here with the same framing
   as idempotency.md** (injected how → expected → proven by → why not).