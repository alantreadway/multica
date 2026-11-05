-- AG patch: Decision Desk. When the desk worker last asked for work, so the page
-- can warn that a button will not do anything until it is running again.
ALTER TABLE ag_decision_engine ADD COLUMN IF NOT EXISTS worker_seen_at TIMESTAMPTZ;
