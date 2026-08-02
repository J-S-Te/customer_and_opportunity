-- Controlled rollback only. Removing indexes can block concurrent writes on
-- large tables; prefer a forward fix in production.
ALTER TABLE crm_presale_worklogs
  DROP INDEX idx_presale_worklog_timeline;

ALTER TABLE crm_presale_assignments
  DROP INDEX idx_presale_assignment_end_timeline;
