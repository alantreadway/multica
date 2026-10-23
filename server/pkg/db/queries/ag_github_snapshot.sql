-- =====================
-- AG patch: extra GitHub PR snapshot fields (review decision, latest reviews,
-- base ref, per-check required-ness). Kept apart from github_snapshot.sql so
-- the patch rebases cleanly. See ag-patches/PATCHES.md.
-- =====================

-- name: AGUpdateGitHubPRSnapshotExtras :exec
-- Second half of the snapshot's guarded UPDATE, run in the same transaction
-- once UpdateGitHubPRSnapshot has matched the head SHA. NULL clears a value the
-- new snapshot did not carry, so a stale review decision never outlives its
-- snapshot.
UPDATE github_pull_request
SET review_decision = sqlc.narg('review_decision'),
    latest_reviews  = sqlc.narg('latest_reviews')::jsonb,
    base_ref_name   = sqlc.narg('base_ref_name')
WHERE id = sqlc.arg('pr_id');

-- name: AGSetGitHubPRCheckRunRequired :exec
-- Sets is_required for the checks (by ordinal) whose required-ness the snapshot
-- carried, once for the required ones and once for the rest; checks the
-- snapshot said nothing about keep their inserted NULL.
UPDATE github_pull_request_check_run
SET is_required = sqlc.arg('required')::boolean
WHERE pr_id = sqlc.arg('pr_id') AND ordinal = ANY(sqlc.arg('ordinals')::int[]);

-- name: AGSetGitHubPRCheckRunRunFields :exec
-- Sets the per-run fields of one check row (by ordinal). Rows the extras query
-- said nothing about are never written, so they keep their inserted NULLs.
UPDATE github_pull_request_check_run
SET workflow_name    = sqlc.narg('workflow_name'),
    run_id           = sqlc.narg('run_id'),
    started_at       = sqlc.narg('started_at'),
    suite_status     = sqlc.narg('suite_status'),
    suite_conclusion = sqlc.narg('suite_conclusion')
WHERE pr_id = sqlc.arg('pr_id') AND ordinal = sqlc.arg('ordinal');

-- name: AGListPullRequestSnapshotExtras :many
SELECT pr.id, pr.review_decision, pr.latest_reviews, pr.base_ref_name
FROM github_pull_request pr
JOIN issue_pull_request ipr ON ipr.pull_request_id = pr.id
WHERE ipr.issue_id = sqlc.arg('issue_id');

-- name: AGListPullRequestChecksForIssue :many
-- Per-check rows for the CURRENT snapshot head of each of the issue's PRs.
SELECT cr.pr_id, cr.name, cr.status, cr.conclusion, cr.details_url, cr.is_required,
       cr.workflow_name, cr.run_id, cr.started_at, cr.suite_status, cr.suite_conclusion
FROM github_pull_request_check_run cr
JOIN github_pull_request pr ON pr.id = cr.pr_id
JOIN issue_pull_request ipr ON ipr.pull_request_id = pr.id
WHERE ipr.issue_id = sqlc.arg('issue_id')
  AND pr.snapshot_head_sha <> ''
  AND cr.head_sha = pr.snapshot_head_sha
ORDER BY cr.pr_id, cr.ordinal;
