--- 0001: control-plane tenants table
-- status lifecycle: pending -> provisioning -> active | failed | deleting | deleted
CREATE TABLE IF NOT EXISTS tenants (
  tenant_id   text PRIMARY KEY,
  status      text NOT NULL DEFAULT 'pending',
  workflow_id text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
