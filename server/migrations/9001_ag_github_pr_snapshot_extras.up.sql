-- AG patch: extra GitHub PR snapshot fields (see ag-patches/PATCHES.md).
--
-- Additive and nullable on purpose: the stock backend image never reads or
-- writes these columns, so switching the backend image back keeps working
-- against a migrated database. NULL means "the snapshot did not carry the
-- field" (older snapshot, or GitHub / the token did not return it), which the
-- API reports as null rather than guessing.
--
-- No index: lookups are by pr_id, already covered by
-- github_pull_request_check_run_pr_ordinal_idx and the table's primary key.

ALTER TABLE github_pull_request
    -- GraphQL `reviewDecision`: APPROVED / CHANGES_REQUESTED / REVIEW_REQUIRED.
    -- NULL when GitHub returns null (no review requirement) or no snapshot.
    ADD COLUMN review_decision TEXT,
    -- GraphQL `latestReviews`: [{"author": "...", "state": "APPROVED",
    -- "submitted_at": "..."}], one entry per reviewer's latest review.
    ADD COLUMN latest_reviews JSONB,
    -- GraphQL `baseRefName`: the branch the PR targets.
    ADD COLUMN base_ref_name TEXT;

ALTER TABLE github_pull_request_check_run
    -- GraphQL `isRequired(pullRequestNumber:)` on CheckRun / StatusContext:
    -- TRUE if a branch protection rule or ruleset requires this check for the
    -- PR's base branch. NULL when the snapshot did not carry it.
    ADD COLUMN is_required BOOLEAN;
