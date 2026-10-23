-- =====================
-- AG patch: drain badge. The daemon's drain report lives in
-- agent_runtime.metadata.ag_drain. Kept apart from runtime.sql so the patch
-- rebases cleanly. See ag-patches/PATCHES.md.
-- =====================

-- name: SetAgentRuntimeAGDrain :execrows
-- Rows affected is 0 when the stored report is unchanged.
UPDATE agent_runtime
SET metadata = metadata || jsonb_build_object('ag_drain', @drain::jsonb),
    updated_at = now()
WHERE id = $1
  AND (metadata -> 'ag_drain') IS DISTINCT FROM @drain::jsonb;

-- name: ClearAgentRuntimeAGDrain :execrows
-- Rows affected is 0 when no report is stored.
UPDATE agent_runtime
SET metadata = metadata - 'ag_drain',
    updated_at = now()
WHERE id = $1
  AND (metadata -> 'ag_drain') IS NOT NULL;
