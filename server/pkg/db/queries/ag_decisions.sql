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

-- name: GetAGDecisionEngineLastSync :one
SELECT last_sync_at FROM ag_decision_engine WHERE workspace_id = $1;

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
