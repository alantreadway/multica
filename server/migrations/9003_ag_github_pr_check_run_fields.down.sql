ALTER TABLE github_pull_request_check_run
    DROP COLUMN IF EXISTS workflow_name,
    DROP COLUMN IF EXISTS run_id,
    DROP COLUMN IF EXISTS started_at,
    DROP COLUMN IF EXISTS suite_status,
    DROP COLUMN IF EXISTS suite_conclusion;
