-- User-defined label order. `position` is per (workspace_id, resource_type),
-- so reordering issue labels never touches agent or skill labels. Existing
-- labels are backfilled in their current alphabetical order, so nothing moves
-- until someone reorders. New labels are appended by CreateLabel.
--
-- No index: label lists are small and already filtered by workspace_id.

ALTER TABLE issue_label
    ADD COLUMN position INTEGER NOT NULL DEFAULT 0;

UPDATE issue_label l
SET position = ranked.pos
FROM (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY workspace_id, resource_type
               ORDER BY LOWER(name), id
           ) - 1 AS pos
    FROM issue_label
) ranked
WHERE l.id = ranked.id;
