-- =====================
-- AG patch: label ordering. Reorder writes for issue_label.position; the
-- position-aware ORDER BY and CreateLabel stay in issue_label.sql. Kept apart
-- so the patch rebases cleanly. See ag-patches/PATCHES.md.
-- =====================

-- name: LockLabelIDsForReorder :many
-- Row-locks one namespace so a reorder sees a stable set of labels and a
-- concurrent create/delete waits for it.
SELECT id FROM issue_label
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
  AND resource_type = sqlc.arg('resource_type')::text
ORDER BY id
FOR UPDATE;

-- name: SetLabelPositions :exec
-- Rewrites position to the 1-based array ordinality minus one. Callers have
-- already verified label_ids is exactly the namespace's current set.
UPDATE issue_label l
SET position = o.ord - 1
FROM unnest(sqlc.arg('label_ids')::uuid[]) WITH ORDINALITY AS o(id, ord)
WHERE l.id = o.id
  AND l.workspace_id = sqlc.arg('workspace_id')::uuid
  AND l.resource_type = sqlc.arg('resource_type')::text;
