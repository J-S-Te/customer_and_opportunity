ALTER TABLE portal_report_download_events
  DROP CHECK chk_portal_report_tracking_digest,
  DROP INDEX idx_portal_report_tracking_digest,
  DROP COLUMN tracking_digest;
