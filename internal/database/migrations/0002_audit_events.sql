--- 0002: audit trail for every tenant lifecycle event
-- append-only: rows are never updated or deleted

CREATE TABLE IF NOT EXISTS audit_events (
  id         BIGSERIAL PRIMARY KEY,
  tenant_id  TEXT   NOT NULL REFERENCES tenants(tenant_id),
  event_type TEXT        NOT NULL,
  actor      TEXT        NOT NULL DEFAULT 'system',
  payload    jsonb       NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
