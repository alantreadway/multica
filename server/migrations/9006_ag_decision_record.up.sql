-- AG patch: Decision Desk. Append-only log of decisions, human or policy. `id`
-- is chosen by the writer, so a retried write cannot log a decision twice.

CREATE TABLE ag_decision_record (
    id TEXT PRIMARY KEY,
    workspace_id UUID NOT NULL,
    at TIMESTAMPTZ NOT NULL,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    record JSONB NOT NULL
);
