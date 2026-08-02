-- Controlled CRM rollback. Snapshot data is discarded; export it first if the
-- report has already been used for a closed accounting period.
DROP INDEX idx_opportunity_report_scope ON crm_opportunities;
DROP INDEX idx_presale_report_completion ON crm_presale_status_logs;
DROP INDEX idx_presale_report_request ON crm_presale_requests;
DROP INDEX idx_presale_report_worklog ON crm_presale_worklogs;
ALTER TABLE crm_presale_assignments DROP COLUMN assignee_department_snapshot;
