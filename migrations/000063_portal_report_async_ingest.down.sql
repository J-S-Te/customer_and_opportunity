-- Destructive rollback: remove only after every ingest job has completed or
-- been exported for controlled recovery.
DROP TABLE IF EXISTS portal_report_ingest_jobs;
