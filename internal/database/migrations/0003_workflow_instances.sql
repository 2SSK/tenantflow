--- 0003: Temporal workflow metadata for the control plane
--- one row per workflow execution; status mirrors Temporal's state

CREATE TABLE IF NOT EXISTS workflow_instances (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id text REFERENCES tenants(tenant_id),
  workflow_type text NOT NULL,
  workflow_id text NOT NULL,
  run_id text NOT NULL,
  status text NOT NULL,
  error jsonb,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);

-- Look up by Temporal workflow ID (for status queries)
CREATE INDEX IF NOT EXISTS idx_workflow_instances_workflow ON workflow_instances(workflow_id);

-- Look up all workflows for a tenant
CREATE INDEX IF NOT EXISTS idx_workflow_instances_tenant ON workflow_instances(tenant_id, started_at DESC);
