--- 0006: control-plane backup records
-- Each row tracks a point-in-time pg_dump taken for a tenant. The artifact
-- lives in the postgres container; this row is metadata so the UI can list
-- history and Restore can reference a specific backup by ID.
CREATE TABLE IF NOT EXISTS backups (
  id           BIGSERIAL PRIMARY KEY,
  tenant_id    TEXT   NOT NULL REFERENCES tenants(tenant_id),
  filename     TEXT   NOT NULL UNIQUE,
  status       TEXT   NOT NULL DEFAULT 'pending',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_backups_tenant ON backups(tenant_id, created_at DESC);
