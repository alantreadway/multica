-- AG patch: Overview dashboard. One row per pinned tree per day, written by the
-- first GET /api/ag/overview of the day, so the ETA can be scored later.

CREATE TABLE ag_overview_forecast (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    kind TEXT NOT NULL,
    ref TEXT NOT NULL,
    day DATE NOT NULL,
    -- NULL when the ETA was hidden (too few samples, or nothing open).
    p50_at TIMESTAMPTZ,
    p85_at TIMESTAMPTZ,
    known_units INTEGER NOT NULL,
    done_units INTEGER NOT NULL,
    scope_open BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
