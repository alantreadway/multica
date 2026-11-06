CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ag_overview_forecast_day_idx ON ag_overview_forecast (workspace_id, kind, ref, day);
