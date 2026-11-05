CREATE INDEX CONCURRENTLY IF NOT EXISTS ag_decision_intent_card_idx ON ag_decision_intent (card_id, created_at DESC, id DESC);
