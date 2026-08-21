--- 0004: add workflow_id to audit_events (missing from 0002)
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS workflow_id TEXT;
