CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ag_overview_pin_workspace_ref_idx ON ag_overview_pin (workspace_id, kind, ref);
