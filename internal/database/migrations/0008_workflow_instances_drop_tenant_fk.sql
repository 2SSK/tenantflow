-- workflow_instances is a durable mirror / dead letter queue. Runs can refer
-- to tenants that do not (yet) exist: the recorder fires before the first
-- activity creates the tenant row, and if that activity itself fails, the
-- tenant never exists at all. The foreign key made the DLQ blind to exactly
-- the failures it exists to surface, so it is removed.
ALTER TABLE workflow_instances DROP CONSTRAINT workflow_instances_tenant_id_fkey;