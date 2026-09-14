# Demo video — shot list (60–120s, one take)

The demo is run by `sh scripts/demo/run-demo.sh` against the local stack
(`make dev-up` + API/worker built from the repo). Every beat is
deterministic and prints its own title, so a single terminal session in a
screencast is all that is needed. Suggested total: ~1:40.

Recommended recording: `script` or `asciinema` for the terminal, or a
full-screen recorder at 1080p. Zoom the terminal font so it is readable.

---

## Beat 1 — Happy path (0:00–0:20)

**Narration:** "Multitenancy at the touch of a button. One API call
provisions a fully isolated database — dedicated owner role, `PUBLIC`
connect revoked, a Keycloak identity, an audit trail."

On screen (from `run-demo.sh`):

1. `POST /api/v1/tenants` with `tenantID=demo-good-…` → `202 Accepted`,
   workflow `provision-demo-good-…` starts.
2. Poll → `active` (a few seconds).
3. Event timeline: `TENANT_CREATED → TENANT_PROVISIONED → TENANT_ACTIVATED`.

**Cut point:** after the timeline prints.

---

## Beat 2 — Failure and the dead-letter queue (0:20–0:50)

**Narration:** "Now an operator kills a delete mid-flight. This is the
moment the control plane has to be honest: no hanging, no loops — the run
fails fast and lands somewhere an operator can see it."

On screen:

1. `DELETE /api/v1/tenants/demo-fail-…` → `202`, status `deleting`.
2. The mischievous step the video shows: while the saga is busy with its
   pre-delete backup, the tenant's status is flipped underneath it
   (`UPDATE tenants SET status='failed'`). The saga's final transition
   `deleting → deleted` now conflicts.
3. Tenant shows `failed`.
4. DLQ: the failed run appears with
   `mark tenant demo-fail-… deleted (type: StatusConflict, retryable: false)`.
   Note the non-retryable marker — a CAS conflict is a programming/operator
   error, not a transient outage, so retrying forever would be wrong.
5. The world is consistent: database and role are gone (`0|0`) because the
   teardown completed before the final bookkeeping step failed.

**Cut point:** after the `0|0` print.

---

## Beat 3 — One-click replay (0:50–1:10)

**Narration:** "Replay is one API call. The same saga re-runs against a
half-torn-down world, and because every teardown step is idempotent, it
converges: fresh database, fresh role, tenant active again — proved, not
assumed, by the `1|1`."

On screen:

1. `POST /api/v1/tenants/demo-fail-…/retry` → the provisioning workflow is
   restarted (`provision-demo-fail-…`, new run).
2. Poll → `active`.
3. Database and role recreated (`1|1`).

**Cut point:** after `1|1`.

---

## Beat 4 — Reconcile, the drift detective (1:10–1:40, optional last 10s)

**Narration:** "And the self-healing loop. Someone grants `PUBLIC` connect
on a production tenant database — the single most dangerous multitenancy
drift. Reconcile spots it, records it, and repairs it: connect revoked,
backup verified."

On screen:

1. `demo-recon-…` provisioned; a manual `GRANT CONNECT … TO PUBLIC` sneaks
   the drift in.
2. `POST /api/v1/tenants/demo-recon-…/reconcile` → `reconciling`.
3. Timeline shows `TENANT_DRIFT_DETECTED` with
   `["public_connect","missing_backup"]` — note the second drift: this
   dedicated tenant had not yet been backed up, which is also "drift" for a
   production tenant — then `TENANT_RECONCILE_CONVERGED` with
   `["public_connect","missing_backup"]` repaired.

**Expected quirk to narrate confidently:** the fresh dedicated tenant shows
BOTH drifts; `missing_backup` is included because a dedicated tenant that
has never been backed up is not yet delete-safe. That is real behavior, and
the reconcile repairs both in one pass.

> **Live note:** The operator doesn't need to remember to POST manually —
> since Phase 14, `ReconcileSweepWorkflow` runs on a configurable interval
> (`TENANTFLOW_RECONCILE_SWEEP_INTERVAL`, default 10m), picks up every
> active tenant, and starts a `reconcile-<id>` child. A collision with the
> manual call above is harmless: both share the same workflow ID, so the
> second start is `AlreadyStarted` → skipped.

---

## Beat 5 — Data safety: external DROP DATABASE → restore from backup (1:40–2:00)

**Narration:** "The strongest guarantee. Someone — an attacker, a runaway
script, a careless engineer — drops the tenant's whole database. The
reconciler's job is not guesswork: it restores the tenant from its latest
verified backup, with zero data loss. Notice the proof: the marker row is
still there, so this is a real restore, not a brand-new empty database."

On screen:

1. `demo-safe-…` provisioned; a `marker` table seeded with `alive` —
   "this is what must survive".
2. `POST …/reconcile` → the reconciler detects `missing_backup` and captures
   a completed backup of the live database (poll shows the row flip to
   `completed`).
3. External disaster: `DROP DATABASE "tenant_demo-safe-…"
   WITH (FORCE);` — the whole database disappears out of band.
4. `POST …/reconcile` again → timeline shows `TENANT_DRIFT_DETECTED`
   `["missing_database"]` → **`TENANT_RECONCILE_RESTORED`** →
   `TENANT_RECONCILE_CONVERGED` `["missing_database"]`.
5. Query the marker again: `alive` — restored from backup, not recreated
   empty.

**Why this is worth a beat:** it is the operational failure a multitenant
platform can't talk its way out of for free. The counterpart — no verified
backup exists → the reconciler refuses to recreate an empty database and
escalates to the DLQ with `TENANT_RECONCILE_UNRECOVERABLE` — is real but
dramatically dark for the video; it is covered in tests and the failure
matrix.

---

## What NOT to say (it's not measured)

- No throughput claims in the video — those live in
  [`docs/load-test.md`](./load-test.md).
- Do not claim exactly-once semantics. The correct phrase is
  "at-least-once, with idempotent steps that make retries converge".

## Optional B-roll

- Temporal UI (localhost:8080) showing the failed delete workflow's event
  history — matches `docs/screenshots/temporal-delete-failed.png`.
- Grafana overview — `docs/screenshots/grafana-overview.png`; note Grafana
  is not part of the default dev-up compose, so record this before/after
  rather than mid-take.

## Cleanup

`sh scripts/demo/run-demo.sh --cleanup` soft-deletes the three tenants the
run created. Demo tenants from earlier runs can be removed the same way.