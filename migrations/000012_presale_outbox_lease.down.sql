ALTER TABLE crm_outbox_events
  DROP KEY idx_crm_outbox_lease,
  DROP COLUMN last_error_summary,
  DROP COLUMN locked_until,
  DROP COLUMN locked_by;
