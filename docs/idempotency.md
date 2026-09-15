# Idempotency

*What happens when an operation runs twice — and why it is safe.*

Temporal retries activities. Every activity boundary is therefore a place
where a side effect may be executed **more than once**: the worker runs an
activity, the side effect commits, the worker dies before reporting success,
and the retry executes the side effect again. Idempotency is what makes those
retries safe.

This document answers the reviewer question *"what happens if you run X
twice?"* for every operation in TenantFlow, records the audit that verified
the answers, and states the rules new operations must follow to stay safe.

---

## 1. The execution model

```
workflow execution          activity          side effect
(wf-<tenant>)               (retries on crash)
     │                           │
     ├── CreateTenantRecord ─────┼── INSERT tenants (ON CONFLICT DO NOTHING)
     ├── ProvisionTenant ────────┼── CREATE DATABASE + ownership
     │                           │        │
     │                           │        └── worker dies here ──► retry runs
     │                           │            ProvisionTenant AGAIN
     │                           │
     ├── MarkTenantActive ───────┼── UPDATE tenants SET status
     └── ...                     │
```

Two properties protect us:

1. **Workflow-ID dedupe** — provision/migrate/backup/restore/upgrade/delete
   all start with `workflow-<tenantID>` and `WorkflowIDReusePolicy=REJECT_DUPLICATE`,
   so an open (or completed) saga can never be double-started. Reconcile uses
   `ALLOW_DUPLICATE` deliberately: re-running after closure is its whole point,
   while an in-flight run still errors with `AlreadyStarted`.
2. **Activity-level idempotency** — even WITHIN one workflow, a single activity
   can be retried after a crash. That is where the interesting bugs live (see
   §3).

The audit below therefore distinguishes "safe because it can only run once"
from "safe even if it runs again".

---

## 2. The inventory

Glossary of the primitives and what they do, so the table in §3 stays honest:

| Provider primitive | Behavior |
|---|---|
| `CreateDatabase` / `CreateDatabaseNamed` | `CREATE DATABASE` + ownership statements. Fails if the DB already exists. **Not idempotent on its own** — callers must guard (check-then-act). |
| `DropDatabase` / `DropDatabaseNamed` | Terminates connections, then `DROP DATABASE IF EXISTS`. Idempotent. |
| `DropTenantRole` | `DROP ROLE IF EXISTS`. Idempotent. |
| `EnsureDatabaseOwnership` | `DO $$ … IF NOT EXISTS CREATE ROLE`, `ALTER DATABASE … OWNER TO`, `REVOKE CONNECT … FROM PUBLIC`. All three statements are idempotent. |
| `InspectDatabase` / `RoleExists` / `ValidateDatabase` | Read-only probes. Trivially idempotent. |
| `SnapshotDatabase` | `pg_dump` into a uniquely-named artifact. Re-running creates a second, equally valid dump (benign duplicate artifact). |
| `RestoreDatabaseFromBackup` | Plain `psql -f` restore. **Not safe to re-run** after a partial restore ("relation already exists"); safe when the target DB was freshly created/emptied. |
| `RenameDatabase` | `ALTER DATABASE … RENAME`. Fails if the source is missing (needs a forward-progress guard at the caller). |

Temporal workflow-ID policies:

| Workflow | Workflow ID | Reuse policy | Effect of starting again |
|---|---|---|---|
| Provision | `provision-<id>` | `REJECT_DUPLICATE` | Refused — both while open and after close. One saga per tenant, ever. |
| Migrate | `migrate-<id>` | `REJECT_DUPLICATE` | Same. |
| Backup / Restore / Upgrade / Delete | `<verb>-<id>` | `REJECT_DUPLICATE` | Same. |
| Reconcile | `reconcile-<id>` | `ALLOW_DUPLICATE` | In-flight run → `AlreadyStarted` (409). *Completed* run → new run starts. Re-runnable by design. |

---

## 3. Operation × repeated execution

Meaning of the columns:

- **Retry-safe?** — can the operation be executed a second time after a crash
  mid-way and still reach the correct end state?
- **Mechanism** — *workflow* = protected by workflow-ID dedupe; *provider* =
  the primitive itself is idempotent; *caller guard* = check/guard logic in
  the activity.

| Operation | Layer | Retry-safe? | Mechanism / notes |
|---|---|---|---|
| CreateTenantRecord | repo | ✅ | `INSERT … ON CONFLICT (tenant_id) DO NOTHING`. Re-run is a no-op. Verified by `TestCreateTenantIsIdempotent` (integration). |
| ProvisionTenant (dedicated) | activity | ✅ | **Fixed in this audit** (§3.1): inspect-first, create only if missing, re-assert ownership when present. Previously a retry died with "database already exists". |
| ProvisionTenant (shared) | activity | ✅ | No infrastructure created; only an idempotent audit write. |
| MarkTenantActive / MarkTenantFailed / MarkTenantMigrated / … | activity | ✅ | Idempotent `UPDATE` / audit `WriteEvent`. |
| DropTenantDatabase (compensation) | activity | ✅ | `DROP DATABASE IF EXISTS`. |
| ProvisionTenantIdentity | activity | ✅ | **Fixed in this audit** (§3.5): get-or-create — probe `GetUserByUsername` first, `POST /users` only when absent. A retried activity reuses the existing user instead of 409ing. Proven by `TestProvisionTenantIdentityGetOrCreateLive` against real Keycloak. |
| AssignRole (identity) | activity | ✅ | Keycloak role-mappings are a set operation; re-assigning converges. |
| MigrateData | activity | ✅ | **Fixed in this audit** (§3.2): pre-drops the fixed-name `_new` DB before building, so a retry after a mid-way crash resumes cleanly. Snapshot artifact is new per run. |
| SwitchTraffic | activity | ✅ | **Fixed in this audit** (§3.3): `_new` existence is the forward-progress sentinel. If `_new` is gone, the switch already happened → no-op success. Previously a retry re-dropped the live DB and destroyed a *completed* promotion (then self-healed from backup — data recovered, migration lost). |
| DropTenantAuxDatabase (compensation) | activity | ✅ | `DROP … IF EXISTS` + idempotent audit write. |
| BackupTenantData | activity | ✅ | **Fixed in this audit** (§3.4): pre-drops the fixed-name `_temp` verification DB. A lost-result retry may create a second, equally valid backup row — benign (immutable artifacts). |
| DeleteTenantWorkflow (shared tenant) | workflow | ✅ | Skips `BackupTenantData` entirely: a shared tenant has no `tenant_<id>` database to snapshot, and dumping a nonexistent DB strands the deletion in the DLQ (found by the 12.2 load run — 100/100 shared deletes stuck). Covered by `TestDeleteWorkflow_SharedTenantSkipsPreDeleteBackup`. |
| RestoreData | activity | ⚠️ **Known gap** (§4.2) | Plain restore into the live DB. Retry after a partial restore hits "relation already exists". Mitigation: pre-restore snapshot exists for rollback; remediation = rollback, not re-run. |
| PreRestoreSnapshot | activity | ✅ | New artifact per run. |
| RestoreRollback | activity | ⚠️ Same as RestoreData | Same mitigation. |
| RestoreTenantFromBackup (reconcile) | activity | ✅ | **Fixed in this audit** (§4.1): pre-drops the recreated `tenant_<id>` before restoring, so a retried attempt re-does the restore into a fresh DB instead of dying with "database already exists". Proven by `TestRestoreTenantFromBackup_PreDropsResidue` (unit) + `TestRestoreTenantFromBackup_DropsResidueBeforeCreate` (integration). |
| Reconcile: ProbeTenantActualState | activity | ✅ | Read-only probes. |
| Reconcile: EnsureTenantDatabase | activity | ✅ | Check-then-act: create only if missing, then re-assert ownership. |
| Reconcile: RecordDrift / MarkConverged / MarkSkipped / MarkFailed | activity | ✅ | Idempotent. |
| Reconcile workflow re-run | workflow | ✅ | `ALLOW_DUPLICATE`; converged runs re-run as fast-path no-ops. |
| Docker-provider Read APIs | provider | ✅ | Read-only. |
| Snapshot Database | provider | ✅ | New artifact each run (benign duplicate). |
| CreateDatabase (raw) | provider | ❌ by itself | Must be guarded by callers. All callers guard today (§5 check-then-act). |

---

## 4. The audit findings

The table above contains six fixes and one accepted gap.

### 4.1 Fixed in this audit

**ProvisionTenant** (`internal/activities/provision.go`):
the database created by a crashed attempt blocked the retry.
Now: inspect → create if missing → re-assert ownership either way.
Integration proof: `TestProvisionTenantResumesAfterPartialCreate`.

**MigrateData** (`internal/activities/migrate.go`):
the fixed-name `_new` database left behind by a crashed attempt blocked the
retry. Now: pre-drop `_new` before building (it is a disposable artifact;
`DROP … IF EXISTS` is idempotent).
Integration proof: `TestMigrateDataResumesAfterStaleNewDB`.

**SwitchTraffic** (`internal/activities/migrate.go`):
a retry after the rename had already completed re-ran "drop live" and
destroyed the freshly promoted database (self-healing then restored the
*old* backup — data recovered, migration lost). Now: if `_new` no longer
exists, the promotion already happened → no-op success.
Integration proofs: `TestSwitchTrafficRetryIsIdempotent` (completed-switch
retry) and `TestSwitchTrafficCompletesInterruptedSwitch` (mid-switch retry).

**BackupTenantData** (`internal/activities/backup.go`):
the fixed-name `_temp` verification database left behind by a crashed attempt
blocked the retry. Now: pre-drop `_temp` before restoring into it.
(The provider-level symmetric case is covered by the migrate procedure; no
separate integration test because BackupTenantData needs a real repo — the
pre-drop line is the identical pattern.)

**ProvisionTenantIdentity** (`internal/activities/identity.go`):
Keycloak's `POST /users` returns 409 when the username already exists, so a
retried activity failed and dragged the provision saga into the DLQ until a
human deleted the user by hand. Now: probe `GetUserByUsername` first and
create only when absent; role assignment re-runs and converges because
Keycloak role-mappings are a set operation. `DeleteUser` already treated 404
as success, so deletes were safe.
Proofs: `TestProvisionTenantIdentityCreatesWhenAbsent`,
`TestProvisionTenantIdentityReusesExisting` (fast unit tests with a recording
fake), and `TestProvisionTenantIdentityGetOrCreateLive` (real Keycloak,
double-call converges to the same user ID).

**RestoreTenantFromBackup** (`internal/activities/reconcile.go`):
the reconcile restore leg invoked on `DriftMissingDatabase` recreated the
tenant database and restored into it — but a crash between `CreateDatabase`
and the restore's completion left a partially-restored DB, and the activity
retry died at the raw `CreateDatabase` with "database already exists" (the
same bug class §3.1 fixed for ProvisionTenant). Now: pre-drop `tenant_<id>`
before creating (`DROP DATABASE IF EXISTS` is idempotent), so every retry
restores into a freshly created database. This is safe because the drift that
invoked the leg was missing-database: the tenant's previous data was already
gone, and the only trustworthy state is the verified backup artifact — the
residue is our own crashed attempt's partial work. It follows the audit's own
Rule 2 — "fixed-name disposable databases must be pre-dropped before use" —
the identical pattern MigrateData and BackupTenantData use for `_new`/`_temp`.
Proofs: `TestRestoreTenantFromBackup_PreDropsResidue` (unit: asserts
drop-before-create ordering) and the integration twin
`TestRestoreTenantFromBackup_DropsResidueBeforeCreate` (real postgres:
proof table restored from the verified backup, residue table gone).

### 4.2 Accepted gaps (documented, not silent)

The Keycloak gap described in earlier drafts is fixed (§3.5). The remaining
accepted gap: `RestoreData` restores a plain dump into
the live DB; a retry after a partial restore fails with "relation already
exists". This is mitigated structurally: every restore is preceded by
`PreRestoreSnapshot`, and the documented remediation for a broken restore is
rollback to that snapshot — not re-running the restore. Making the restore
itself idempotent (drop-all-then-restore inside a transaction, or archive-format
dumps with `pg_restore --clean --if-exists`) is a future improvement.

---

## 5. Rules for new operations

1. **Never write a raw `CREATE`** without a guard. Pattern: `InspectDatabase`
   → create only if missing; or pre-drop disposable fixed-name artifacts.
2. **Fixed-name disposable databases must be pre-dropped** before use
   (`DROP … IF EXISTS`). Timestamped names need no pre-drop but leak garbage
   on crashes.
3. **Use a forward-progress sentinel for destructive multi-step operations.**
   The `_new`-exists check in `SwitchTraffic` is the template: the sentinel
   tells a retry "this step already completed — do not redo it".
4. **Prefer `IF EXISTS` / `ON CONFLICT DO NOTHING` / `IF NOT EXISTS`** in every
   destructive or insert-side effect.
5. **Read APIs are your friends in retry paths** — probing before acting is
   cheap and converges.
6. **When an operation cannot be made idempotent**, say so in this document
   and provide the manual remediation. A documented, narrow gap beaten by an
   undocumented one.
7. **Prove it.** New activity-level retry behavior gets an integration test
   that simulates the crash residue and asserts convergence
   (`internal/activities/migrate_integration_test.go` is the template).