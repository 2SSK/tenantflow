# ADR-0007: Soft delete with a durable, cancelable grace period

- Status: accepted
- Date: 2026
- Deciders: project owner

## Context

Tenant deletion is destructive: database, role, Keycloak identity, config.
Operators need a window to undo an accidental delete, but the deletion must
eventually complete *even if the cluster dies mid-way* — no stuck
"half-deleted" tenants.

## Decision

`DeleteTenantWorkflow` implements **soft delete with a durable grace period**:

```
DELETE request → mark tenant "deleting" (ISBN status in control plane)
      ↓
wait GracePeriod (cancelable by API: POST /tenants/{id}/cancel-delete)
      ↓
BackupTenantData  ← the safety net: a deleted tenant always has a backup
      ↓
DeprovisionTenant (drop DB + role, delete Keycloak user, records)
      ↓
CompensationBookkeeping on failure → failed-run row for operator retry
```

The grace period is durable because Temporal persists the timer — restarting
the worker does not reset it. Within the window, cancellation (`CancelDelete`)
restores the tenant and leaves no side effects. After the window, deletion
proceeds with per-activity retries and the failure matrix guarantees every
activity is individually safe to retry (`docs/failure-matrix.md` §3.6).
A live chaos test proved the original supervision bug: a worker kill during
the grace period used to strand deletion silently — now the recorder writes a
failed row so the operator can act.

## Consequences

Positive:

- Accidental deletes are reversible during the grace window.
- Deletes are *durable*: no operator babysitting after the window expires.
- The backup-before-deprovision rule means restore always has an artifact.
- Shared-schema tenants have no dedicated database, so the delete saga skips
  the backup step for them (nothing to snapshot; dumping a nonexistent DB
  would strand their deletion in the DLQ — found in the 12.2 load run).

Negative:

- A deleted-but-not-yet-deprovisioned tenant keeps its backup file and
  WorkflowInstance history — intentional, but it is retained storage.
- Cancel requires the workflow to still be open; once deprovision starts,
  cancellation is no longer offered (documented in the API).

## References

- `internal/workflow/delete.go` — the saga + grace period
- `internal/handler/tenant.go` — GET/POST cancel-delete endpoints
- `internal/activities/delete.go` — compensation + bookkeeping
- `internal/activities/deprovision.go`
- `docs/failure-matrix.md` §3.6 — delete failure table