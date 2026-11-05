-- AG patch: Decision Desk (see ag-patches/DECISION-DESK-DESIGN.md).
-- One row per thing waiting on someone. ag-jira-sync owns the contents; a card
-- it stops sending gets resolved_at. Additive: the stock backend image never
-- reads these tables, so switching the image back keeps working.

CREATE TABLE ag_decision_card (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    -- Stable identity across polls, chosen by the engine, e.g. `issue:<uuid>`.
    dedupe_key TEXT NOT NULL,
    issue_id UUID,
    -- alan | author | colleague | agent | nobody. Kept as text so the engine
    -- can add a value without a migration.
    waiting_on TEXT NOT NULL,
    card_type TEXT NOT NULL,
    -- Bumped when anything shown changes, so an action can pin what was on screen.
    revision INTEGER NOT NULL DEFAULT 1,
    head_sha TEXT,
    content JSONB NOT NULL DEFAULT '{}',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ
);
