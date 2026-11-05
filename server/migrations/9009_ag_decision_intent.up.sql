-- AG patch: Decision Desk. An action Alan asked for on a card. The page creates
-- it, the desk worker claims it and reports what happened. card_revision pins
-- what was on screen, and expires_at stops a sleeping Mac acting hours later.

CREATE TABLE ag_decision_intent (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    card_id UUID NOT NULL,
    card_revision INTEGER NOT NULL,
    action_id TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    override_reason TEXT NOT NULL DEFAULT '',
    actor_user_id UUID NOT NULL,
    -- pending | running | succeeded | failed | refused | expired | cancelled
    status TEXT NOT NULL DEFAULT 'pending',
    message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    claimed_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
