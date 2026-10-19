ALTER TABLE github_pull_request_check_run
    DROP COLUMN IF EXISTS is_required;

ALTER TABLE github_pull_request
    DROP COLUMN IF EXISTS review_decision,
    DROP COLUMN IF EXISTS latest_reviews,
    DROP COLUMN IF EXISTS base_ref_name;
