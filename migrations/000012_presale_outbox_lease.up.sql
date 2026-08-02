ALTER TABLE crm_outbox_events
  ADD COLUMN locked_by VARCHAR(128) NOT NULL DEFAULT '' AFTER next_retry_at,
  ADD COLUMN locked_until DATETIME(3) NULL AFTER locked_by,
  ADD COLUMN last_error_summary VARCHAR(1000) NOT NULL DEFAULT '' AFTER locked_until,
  ADD KEY idx_crm_outbox_lease (status, locked_until, next_retry_at, created_at);

-- Deploy this compatible migration before enabling PRESALE_WORKER_ENABLED.
-- Expired PROCESSING rows are deliberately claimable by another worker.
