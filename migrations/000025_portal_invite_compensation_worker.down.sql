-- Stop every compensation worker before rollback. SUCCEEDED/DEAD_LETTER rows
-- are intentionally preserved in the original table for operator audit.
ALTER TABLE crm_portal_compensation_tasks
  DROP KEY idx_portal_compensation_lease,
  DROP COLUMN last_error_summary,
  DROP COLUMN completed_at,
  DROP COLUMN last_attempt_at,
  DROP COLUMN locked_until,
  DROP COLUMN locked_by,
  DROP COLUMN account_no;

-- Recovery note: 000014 cannot resume processing. Roll forward to 000025 to
-- restart the worker; never report external completion from local state alone.
