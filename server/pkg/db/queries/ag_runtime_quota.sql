-- =====================
-- AG patch: Claude quota report and runtime backup setting. The report lives in
-- agent_runtime.metadata.ag_quota and the setting in metadata.ag_settings; the
-- upserts in runtime.sql keep ag_settings across re-registration. Kept apart
-- from runtime.sql so the patch rebases cleanly. See ag-patches/PATCHES.md.
-- =====================

-- name: SetAgentRuntimeAGQuota :one
-- changed is 1 when the stored report was replaced and 0 when it already
-- matched. ag_settings is the runtime's stored setting (NULL when none), read
-- in the same statement so a WebSocket lease can refresh its copy for free.
WITH upd AS (
    UPDATE agent_runtime AS u
    SET metadata = u.metadata || jsonb_build_object('ag_quota', @quota::jsonb),
        updated_at = now()
    WHERE u.id = $1
      AND (u.metadata -> 'ag_quota') IS DISTINCT FROM @quota::jsonb
    RETURNING 1
)
SELECT (SELECT count(*) FROM upd)::bigint AS changed,
       (SELECT r.metadata -> 'ag_settings' FROM agent_runtime AS r WHERE r.id = $1)::jsonb AS ag_settings;

-- name: SetAgentRuntimeAGSettings :execrows
-- Runtime backup setting. Rows affected is 0 when it is unchanged.
UPDATE agent_runtime
SET metadata = metadata || jsonb_build_object('ag_settings', @settings::jsonb),
    updated_at = now()
WHERE id = $1
  AND (metadata -> 'ag_settings') IS DISTINCT FROM @settings::jsonb;
