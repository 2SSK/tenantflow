--- 0007: workflow_instances gains a unique (workflow_id, run_id) key.
--- The instance recorder upserts idempotently with ON CONFLICT DO NOTHING,
--- guaranteeing "one row per workflow execution" (the DLQ contract).
--- Safe to add: the table is currently empty (recorder lands in this phase).
ALTER TABLE workflow_instances
  ADD CONSTRAINT workflow_instances_workflow_run_key UNIQUE (workflow_id, run_id);