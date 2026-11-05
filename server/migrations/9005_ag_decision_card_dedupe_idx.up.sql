CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ag_decision_card_workspace_dedupe_idx ON ag_decision_card (workspace_id, dedupe_key);
