-- customer_portal schema only. Production rollback must first stop the worker
-- and callbacks and retain/report all RETRY_WAIT, PROCESSING and DEAD_LETTER
-- rows; prefer a forward fix after delivery has begun.
ALTER TABLE portal_report_requests
  DROP COLUMN last_callback_hash,
  DROP COLUMN last_callback_key;

ALTER TABLE portal_report_outbox
  DROP KEY idx_portal_report_outbox_lease,
  ADD KEY idx_portal_report_outbox (status, created_at),
  DROP COLUMN sent_at,
  DROP COLUMN last_error_summary,
  DROP COLUMN locked_until,
  DROP COLUMN locked_by,
  DROP COLUMN next_retry_at,
  DROP COLUMN retry_count;
