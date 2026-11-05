CREATE INDEX CONCURRENTLY IF NOT EXISTS ag_decision_intent_claim_idx ON ag_decision_intent (workspace_id, created_at, id) WHERE status = 'pending';
