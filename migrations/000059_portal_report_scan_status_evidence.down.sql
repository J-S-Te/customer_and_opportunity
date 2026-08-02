-- Controlled rollback only. Dropping scan_status removes explicit evidence
-- needed to distinguish a clean scan from an opaque scan reference. Stop Portal
-- report ingestion/downloads and confirm affected files will be re-ingested
-- before applying this rollback.
ALTER TABLE portal_report_files
  DROP CONSTRAINT chk_portal_report_file_scan_status,
  DROP COLUMN scan_status;
