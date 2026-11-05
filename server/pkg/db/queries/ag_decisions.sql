-- =====================
-- AG patch: Decision Desk. See ag-patches/DECISION-DESK-DESIGN.md.
-- =====================

-- name: UpsertAGDecisionCard :one
-- Revision and updated_at move only when something shown changed, and a card
-- that comes back after being resolved starts its waiting time again. Inside
-- one transaction now() is constant, so `changed` means this write moved
-- updated_at (or inserted the row).
INSERT INTO ag_decision_card (workspace_id, dedupe_key, issue_id, waiting_on, card_type, head_sha, content)
VALUES (
    sqlc.arg('workspace_id'), sqlc.arg('dedupe_key'), sqlc.narg('issue_id'),
    sqlc.arg('waiting_on'), sqlc.arg('card_type'), sqlc.narg('head_sha'), sqlc.arg('content')
)
ON CONFLICT (workspace_id, dedupe_key) DO UPDATE SET
    issue_id = EXCLUDED.issue_id,
    waiting_on = EXCLUDED.waiting_on,
    card_type = EXCLUDED.card_type,
    head_sha = EXCLUDED.head_sha,
    content = EXCLUDED.content,
    resolved_at = NULL,
    first_seen_at = CASE WHEN ag_decision_card.resolved_at IS NOT NULL THEN now() ELSE ag_decision_card.first_seen_at END,
    revision = CASE
        WHEN ag_decision_card.waiting_on IS DISTINCT FROM EXCLUDED.waiting_on
          OR ag_decision_card.card_type IS DISTINCT FROM EXCLUDED.card_type
          OR ag_decision_card.head_sha IS DISTINCT FROM EXCLUDED.head_sha
          OR ag_decision_card.content IS DISTINCT FROM EXCLUDED.content
          OR ag_decision_card.resolved_at IS NOT NULL
        THEN ag_decision_card.revision + 1 ELSE ag_decision_card.revision END,
    updated_at = CASE
        WHEN ag_decision_card.waiting_on IS DISTINCT FROM EXCLUDED.waiting_on
          OR ag_decision_card.card_type IS DISTINCT FROM EXCLUDED.card_type
          OR ag_decision_card.head_sha IS DISTINCT FROM EXCLUDED.head_sha
          OR ag_decision_card.content IS DISTINCT FROM EXCLUDED.content
          OR ag_decision_card.resolved_at IS NOT NULL
        THEN now() ELSE ag_decision_card.updated_at END
RETURNING revision, (updated_at = now())::boolean AS changed;

-- name: ResolveAGDecisionCardsNotIn :execrows
-- Everything the engine did not send this time has stopped waiting.
UPDATE ag_decision_card
SET resolved_at = now(), updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')
  AND resolved_at IS NULL
  AND NOT (dedupe_key = ANY(sqlc.arg('dedupe_keys')::text[]));

-- name: ListOpenAGDecisionCards :many
SELECT * FROM ag_decision_card
WHERE workspace_id = $1 AND resolved_at IS NULL
ORDER BY first_seen_at, id;

-- name: CountAGDecisionCardsWaitingOn :one
SELECT count(*) FROM ag_decision_card
WHERE workspace_id = $1 AND waiting_on = $2 AND resolved_at IS NULL;

-- name: ListIssueIDsInWorkspace :many
-- Which of these issues belong to the workspace, to validate the engine's references.
SELECT id FROM issue
WHERE workspace_id = sqlc.arg('workspace_id') AND id = ANY(sqlc.arg('issue_ids')::uuid[]);

-- name: TouchAGDecisionEngine :exec
INSERT INTO ag_decision_engine (workspace_id, last_sync_at)
VALUES ($1, now())
ON CONFLICT (workspace_id) DO UPDATE SET last_sync_at = now();

-- name: GetAGDecisionEngine :one
SELECT last_sync_at, worker_seen_at FROM ag_decision_engine WHERE workspace_id = $1;

-- name: TouchAGDecisionWorker :exec
-- Only ever follows a first sync: without one there are no cards to act on.
UPDATE ag_decision_engine SET worker_seen_at = now() WHERE workspace_id = $1;

-- name: InsertAGDecisionRecord :execrows
-- A retried write hits the primary key and logs nothing new.
INSERT INTO ag_decision_record (id, workspace_id, at, actor, action, record)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO NOTHING;

-- name: ListAGDecisionRecords :many
SELECT * FROM ag_decision_record
WHERE workspace_id = $1
ORDER BY at DESC, id DESC
LIMIT $2;

-- name: GetAGDecisionCard :one
SELECT * FROM ag_decision_card WHERE id = $1 AND workspace_id = $2;

-- name: CreateAGDecisionIntent :one
-- The partial unique index refuses a second active intent on a card.
INSERT INTO ag_decision_intent (workspace_id, card_id, card_revision, action_id, note, override_reason, actor_user_id, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now() + interval '15 minutes')
RETURNING *;

-- name: ListLatestAGDecisionIntents :many
-- The newest intent of each open card, whatever its status.
SELECT DISTINCT ON (i.card_id) i.*
FROM ag_decision_intent i
JOIN ag_decision_card c ON c.id = i.card_id
WHERE i.workspace_id = $1 AND c.resolved_at IS NULL
ORDER BY i.card_id, i.created_at DESC, i.id DESC;

-- name: CancelAGDecisionIntent :execrows
UPDATE ag_decision_intent
SET status = 'cancelled', finished_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'pending';

-- name: ExpireAGDecisionIntents :execrows
UPDATE ag_decision_intent
SET status = 'expired', finished_at = now(), message = 'Not picked up in time, so it did not run.'
WHERE workspace_id = $1 AND status = 'pending' AND expires_at <= now();

-- name: FailLostAGDecisionIntents :execrows
-- Never retried: a worker that died part way may already have merged.
UPDATE ag_decision_intent
SET status = 'failed', finished_at = now(), message = 'The worker stopped reporting. It may have run part of this, so check before retrying.'
WHERE workspace_id = $1 AND status = 'running' AND claimed_at <= now() - interval '10 minutes';

-- name: ClaimAGDecisionIntent :one
UPDATE ag_decision_intent
SET status = 'running', claimed_at = now()
WHERE id = (
    SELECT candidate.id FROM ag_decision_intent candidate
    WHERE candidate.workspace_id = $1 AND candidate.status = 'pending' AND candidate.expires_at > now()
    ORDER BY candidate.created_at, candidate.id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: FinishAGDecisionIntent :execrows
UPDATE ag_decision_intent
SET status = sqlc.arg('status'), message = sqlc.arg('message'), finished_at = now()
WHERE id = sqlc.arg('id') AND workspace_id = sqlc.arg('workspace_id') AND status = 'running';
