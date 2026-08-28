--- 0005: add isolation_mode to tenants + shared-schema tables
--- dedicated = each tenant gets its own database (current default)
--- shared = tenant data lives in shared tables with tenant_idcolumn

ALTER TABLE tenants
  ADD COLUMN isolation_mode text NOT NULL DEFAULT 'dedicated';

-- Shared-schema sample: a shared users table that all shared tenants write into.
-- In production, this would have Row-Level Security (RLS) to enforce tenant isolation.
-- We keep it simple for now: the app layer is responsible for scoping queries by tenant_id.

CREATE TABLE IF NOT EXISTS shared_users (
  id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id text NOT NULL REFERENCES tenants(tenant_id),
  username  text NOT NULL,
  email     text NOT NULL,
  role      text NOT NULL DEFAULT 'member',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  -- Ensure username is unique per tenant, not globally
  UNIQUE (tenant_id, username)
);

-- Index for fast tenant-scoped queries
CREATE INDEX IF NOT EXISTS idx_shared_users_tenant ON shared_users(tenant_id);

-- Shared-schema sample: orders table
CREATE TABLE IF NOT EXISTS shared_orders (
  id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id text NOT NULL REFERENCES tenants(tenant_id),
  user_id   uuid NOT NULL REFERENCES shared_users(id),
  amount    numeric(12,2) NOT NULL DEFAULT 0,
  status    text NOT NULL DEFAULT 'pending',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_shared_orders_tenant ON shared_orders(tenant_id, created_at desc);
