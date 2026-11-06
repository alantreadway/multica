-- AG patch: Overview dashboard (see ag-patches/OVERVIEW-DASHBOARD-DESIGN.md).
-- The work trees the Overview page follows. Additive: the stock backend image
-- never reads this table.

CREATE TABLE ag_overview_pin (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    -- jira: ref is a JIRA key such as AG-9003. issue: ref is a root issue's id.
    kind TEXT NOT NULL,
    ref TEXT NOT NULL,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
