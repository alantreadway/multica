CREATE INDEX CONCURRENTLY IF NOT EXISTS ag_decision_record_workspace_at_idx ON ag_decision_record (workspace_id, at DESC);
