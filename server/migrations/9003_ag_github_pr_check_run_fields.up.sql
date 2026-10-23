-- AG patch: per-run fields on the GitHub PR snapshot's check rows (see
-- ag-patches/PATCHES.md).
--
-- Additive and nullable on purpose, like 9001: the stock backend image never
-- reads or writes these columns, so switching the backend image back keeps
-- working against a migrated database. NULL means "the snapshot did not carry
-- the field" (a snapshot written before this migration, a commit status, a
-- check run outside Actions, or the extras query did not answer), which the
-- API reports as null rather than guessing.
--
-- No index: lookups are by pr_id, already covered by
-- github_pull_request_check_run_pr_ordinal_idx.

ALTER TABLE github_pull_request_check_run
    -- GraphQL `checkSuite.workflowRun.workflow.name`.
    ADD COLUMN workflow_name TEXT,
    -- GraphQL `checkSuite.workflowRun.databaseId`: the Actions run id.
    ADD COLUMN run_id BIGINT,
    -- GraphQL `startedAt` of the check run.
    ADD COLUMN started_at TIMESTAMPTZ,
    -- GraphQL `checkSuite.status` / `checkSuite.conclusion`, raw enums.
    ADD COLUMN suite_status TEXT,
    ADD COLUMN suite_conclusion TEXT;
