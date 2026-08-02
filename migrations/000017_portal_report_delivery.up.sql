-- customer_portal schema only. Deploy this compatible migration before
-- starting portal-report-worker or enabling project-service callbacks.
ALTER TABLE portal_report_outbox
  ADD COLUMN retry_count TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER status,
  ADD COLUMN next_retry_at DATETIME(3) NULL AFTER retry_count,
  ADD COLUMN locked_by VARCHAR(128) NOT NULL DEFAULT '' AFTER next_retry_at,
  ADD COLUMN locked_until DATETIME(3) NULL AFTER locked_by,
  ADD COLUMN last_error_summary VARCHAR(1000) NOT NULL DEFAULT '' AFTER locked_until,
  ADD COLUMN sent_at DATETIME(3) NULL AFTER created_at,
  DROP KEY idx_portal_report_outbox,
  ADD KEY idx_portal_report_outbox_lease (event_type, status, locked_until, next_retry_at, created_at);

ALTER TABLE portal_report_requests
  ADD COLUMN last_callback_key VARCHAR(128) NOT NULL DEFAULT '' AFTER last_callback_version,
  ADD COLUMN last_callback_hash VARCHAR(64) NOT NULL DEFAULT '' AFTER last_callback_key;
