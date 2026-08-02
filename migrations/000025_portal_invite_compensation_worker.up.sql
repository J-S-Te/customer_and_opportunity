-- Apply to the CRM database after 000014. Existing rows are retained. Rows
-- created before this migration do not have replay-safe AccountNo snapshots;
-- they fail closed as INVALID_TASK and remain observable for reconciliation.
ALTER TABLE crm_portal_compensation_tasks
  ADD COLUMN account_no VARCHAR(64) NOT NULL DEFAULT '' AFTER platform_user_id,
  ADD COLUMN locked_by VARCHAR(128) NOT NULL DEFAULT '' AFTER next_retry_at,
  ADD COLUMN locked_until DATETIME(3) NULL AFTER locked_by,
  ADD COLUMN last_attempt_at DATETIME(3) NULL AFTER locked_until,
  ADD COLUMN completed_at DATETIME(3) NULL AFTER last_attempt_at,
  ADD COLUMN last_error_summary VARCHAR(500) NOT NULL DEFAULT '' AFTER last_error_code,
  ADD KEY idx_portal_compensation_lease (status, next_retry_at, locked_until, created_at);

-- Keep legacy PENDING work immediately eligible. PROCESSING did not exist in
-- 000014, so no historical in-flight ownership needs to be inferred.
UPDATE crm_portal_compensation_tasks
SET next_retry_at = COALESCE(next_retry_at, CURRENT_TIMESTAMP(3))
WHERE status = 'PENDING';
