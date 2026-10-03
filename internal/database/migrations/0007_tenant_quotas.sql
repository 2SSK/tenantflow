--- 0007: control-plane tenant quotas
-- Quotas previously lived only in the worker process's in-memory store. They
-- are persisted here so the API process (a separate binary) can read them
-- back for display, and so upgraded quotas survive worker restarts.
-- Get() in the quota store falls back to the default plan when no row exists.
CREATE TABLE IF NOT EXISTS tenant_quotas (
  tenant_id      TEXT PRIMARY KEY REFERENCES tenants(tenant_id) ON DELETE CASCADE,
  max_users      INTEGER NOT NULL,
  max_storage_gb INTEGER NOT NULL,
  max_seats      INTEGER NOT NULL,
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);