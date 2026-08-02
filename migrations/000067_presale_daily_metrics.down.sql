-- Stop every presale-report-aggregate-worker before rollback. This projection
-- is rebuildable from retained worklogs, requests, opportunities and outbox.
DROP TABLE IF EXISTS crm_presale_daily_metric_runs;
DROP TABLE IF EXISTS crm_presale_daily_metrics;
