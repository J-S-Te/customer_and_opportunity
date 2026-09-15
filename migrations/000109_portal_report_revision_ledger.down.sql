-- Destructive after any corrected report exists; production rollback requires an explicit data
-- retention/export plan and must not be executed automatically.
DROP TABLE portal_report_revision_events;
ALTER TABLE portal_report_grants DROP INDEX idx_portal_report_grant_revision, DROP COLUMN report_revision;
ALTER TABLE portal_report_ingest_jobs
  DROP INDEX uq_portal_report_ingest_revision,
  DROP COLUMN report_revision,
  ADD UNIQUE KEY uq_portal_report_ingest_request (tenant_id, request_id);
ALTER TABLE portal_report_files
  DROP INDEX idx_portal_report_file_current,
  DROP INDEX uq_portal_report_file_revision,
  DROP COLUMN void_notice,
  DROP COLUMN validity_status,
  DROP COLUMN report_revision,
  ADD UNIQUE KEY uq_portal_report_file (tenant_id, request_id);
ALTER TABLE portal_report_requests
  DROP COLUMN void_notice,
  DROP COLUMN report_validity_status,
  DROP COLUMN current_report_revision;
