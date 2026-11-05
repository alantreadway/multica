CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ag_decision_intent_one_active_idx ON ag_decision_intent (card_id) WHERE status IN ('pending', 'running');
