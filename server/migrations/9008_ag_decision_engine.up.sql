-- AG patch: Decision Desk. When the engine last reported, whether or not any
-- card changed, so the page can warn that its cards may be out of date.

CREATE TABLE ag_decision_engine (
    workspace_id UUID PRIMARY KEY,
    last_sync_at TIMESTAMPTZ NOT NULL
);
