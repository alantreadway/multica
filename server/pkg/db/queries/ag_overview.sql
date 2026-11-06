-- =====================
-- AG patch: Overview dashboard. See ag-patches/OVERVIEW-DASHBOARD-DESIGN.md.
-- =====================

-- name: AGListOverviewPins :many
SELECT * FROM ag_overview_pin
WHERE workspace_id = $1
ORDER BY created_at, id;

-- name: AGInsertOverviewPin :one
-- Pinning the same tree twice returns the existing pin.
INSERT INTO ag_overview_pin (workspace_id, kind, ref, created_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (workspace_id, kind, ref) DO UPDATE SET kind = EXCLUDED.kind
RETURNING *;

-- name: AGDeleteOverviewPin :execrows
DELETE FROM ag_overview_pin WHERE id = $1 AND workspace_id = $2;

-- name: AGOverviewTreeIssues :many
-- The work tree rule: the seeds (one root issue, or every issue whose metadata
-- names the JIRA key) plus their descendants, minus descendants that name a
-- different key. With no tree_key nothing is cut.
WITH RECURSIVE tree AS (
    SELECT i.id
    FROM issue i
    WHERE i.workspace_id = sqlc.arg('workspace_id')
      AND (
        i.id = sqlc.narg('root_id')::uuid
        OR (
          sqlc.narg('seed_key')::text IS NOT NULL
          AND (
            i.metadata @> jsonb_build_object('jira_key', sqlc.narg('seed_key')::text)
            OR i.metadata @> jsonb_build_object('review_jira', sqlc.narg('seed_key')::text)
            OR i.metadata @> jsonb_build_object('source_key', sqlc.narg('seed_key')::text)
          )
        )
      )
    UNION
    SELECT c.id
    FROM issue c
    JOIN tree t ON c.parent_issue_id = t.id
    WHERE c.workspace_id = sqlc.arg('workspace_id')
      AND (
        sqlc.narg('tree_key')::text IS NULL
        OR NOT (
          COALESCE(NULLIF(c.metadata->>'jira_key', ''), sqlc.narg('tree_key')::text) <> sqlc.narg('tree_key')::text
          OR COALESCE(NULLIF(c.metadata->>'review_jira', ''), sqlc.narg('tree_key')::text) <> sqlc.narg('tree_key')::text
          OR COALESCE(NULLIF(c.metadata->>'source_key', ''), sqlc.narg('tree_key')::text) <> sqlc.narg('tree_key')::text
        )
      )
)
SELECT
    i.id,
    i.number,
    i.title,
    i.status,
    i.parent_issue_id,
    i.created_at,
    i.updated_at,
    COALESCE(i.metadata->>'unit_flow', '')::text AS unit_flow,
    (CASE WHEN jsonb_typeof(i.metadata->'review_round') = 'number'
          THEN (i.metadata->>'review_round')::numeric::int ELSE -1 END)::int AS review_round,
    (i.metadata ? 'pr_url'
        OR EXISTS (SELECT 1 FROM issue_pull_request ipr WHERE ipr.issue_id = i.id))::boolean AS has_pr,
    (NOT EXISTS (SELECT 1 FROM issue k WHERE k.parent_issue_id = i.id))::boolean AS is_leaf
FROM tree
JOIN issue i ON i.id = tree.id
ORDER BY i.created_at, i.number;

-- name: AGOverviewOpenUnits :many
-- Every open unit in the workspace that sits in a working state, for ageing.
-- A unit has a unit_flow, or is a leaf that owns a PR.
SELECT
    i.id,
    i.number,
    i.title,
    i.status,
    i.parent_issue_id,
    i.created_at,
    i.updated_at,
    COALESCE(i.metadata->>'unit_flow', '')::text AS unit_flow,
    (CASE WHEN jsonb_typeof(i.metadata->'review_round') = 'number'
          THEN (i.metadata->>'review_round')::numeric::int ELSE -1 END)::int AS review_round
FROM issue i
WHERE i.workspace_id = $1
  AND i.status IN ('in_progress', 'in_review', 'handoff', 'blocked')
  AND (
    i.metadata ? 'unit_flow'
    OR (
      (i.metadata ? 'pr_url' OR EXISTS (SELECT 1 FROM issue_pull_request ipr WHERE ipr.issue_id = i.id))
      AND NOT EXISTS (SELECT 1 FROM issue k WHERE k.parent_issue_id = i.id)
    )
  )
ORDER BY i.number;

-- name: AGOverviewLatestStatusChanges :many
-- When each issue entered its current status, and the status it left.
SELECT DISTINCT ON (a.issue_id)
    a.issue_id,
    a.created_at,
    COALESCE(a.details->>'from', '')::text AS from_status,
    COALESCE(a.details->>'to', '')::text AS to_status
FROM activity_log a
WHERE a.issue_id = ANY(sqlc.arg('issue_ids')::uuid[])
  AND a.action = 'status_changed'
ORDER BY a.issue_id, a.created_at DESC, a.id DESC;

-- name: AGOverviewUnitTransitions :many
-- Status changes of every unit since a cut-off, oldest first per unit, for the
-- state p90s and the ETA samples.
SELECT
    a.issue_id,
    i.created_at AS issue_created_at,
    a.created_at,
    COALESCE(a.details->>'from', '')::text AS from_status,
    COALESCE(a.details->>'to', '')::text AS to_status
FROM activity_log a
JOIN issue i ON i.id = a.issue_id
WHERE i.workspace_id = sqlc.arg('workspace_id')
  AND a.action = 'status_changed'
  AND a.created_at >= sqlc.arg('since')
  AND (
    i.metadata ? 'unit_flow'
    OR (
      (i.metadata ? 'pr_url' OR EXISTS (SELECT 1 FROM issue_pull_request ipr WHERE ipr.issue_id = i.id))
      AND NOT EXISTS (SELECT 1 FROM issue k WHERE k.parent_issue_id = i.id)
    )
  )
ORDER BY a.issue_id, a.created_at, a.id;

-- name: AGOverviewIssuePullRequests :many
SELECT ipr.issue_id, pr.pr_number, pr.state, pr.checks_rollup_state, pr.review_decision, pr.pr_updated_at
FROM issue_pull_request ipr
JOIN github_pull_request pr ON pr.id = ipr.pull_request_id
WHERE ipr.issue_id = ANY(sqlc.arg('issue_ids')::uuid[])
ORDER BY ipr.issue_id, pr.pr_updated_at DESC;

-- name: AGOverviewOpenDeskCards :many
SELECT c.id, c.issue_id, c.waiting_on, c.card_type, i.number AS issue_number, i.title AS issue_title
FROM ag_decision_card c
LEFT JOIN issue i ON i.id = c.issue_id AND i.workspace_id = c.workspace_id
WHERE c.workspace_id = $1 AND c.resolved_at IS NULL
ORDER BY c.first_seen_at, c.id;

-- name: AGOverviewActiveTasks :many
SELECT
    t.id,
    t.status,
    t.agent_id,
    a.name AS agent_name,
    t.issue_id,
    i.number AS issue_number,
    i.title AS issue_title,
    (CASE WHEN jsonb_typeof(i.metadata->'review_round') = 'number'
          THEN (i.metadata->>'review_round')::numeric::int ELSE -1 END)::int AS review_round,
    t.created_at,
    t.started_at
FROM agent_task_queue t
JOIN agent a ON a.id = t.agent_id
LEFT JOIN issue i ON i.id = t.issue_id
WHERE a.workspace_id = $1
  AND t.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')
ORDER BY t.created_at, t.id;

-- name: AGOverviewRunSeconds :one
-- Agent run time spent on these issues, finished runs only.
SELECT COALESCE(SUM(EXTRACT(EPOCH FROM (t.completed_at - t.started_at))), 0)::float8 AS seconds
FROM agent_task_queue t
WHERE t.issue_id = ANY(sqlc.arg('issue_ids')::uuid[])
  AND t.started_at IS NOT NULL
  AND t.completed_at IS NOT NULL;

-- name: AGInsertOverviewForecast :execrows
-- The first write of the day wins, so the log keeps the morning forecast.
INSERT INTO ag_overview_forecast (workspace_id, kind, ref, day, p50_at, p85_at, known_units, done_units, scope_open)
VALUES (
    sqlc.arg('workspace_id'), sqlc.arg('kind'), sqlc.arg('ref'), sqlc.arg('day'),
    sqlc.narg('p50_at'), sqlc.narg('p85_at'), sqlc.arg('known_units'), sqlc.arg('done_units'), sqlc.arg('scope_open')
)
ON CONFLICT (workspace_id, kind, ref, day) DO NOTHING;
