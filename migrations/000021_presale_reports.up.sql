-- CRM schema: TS-009 historical reporting snapshots and bounded-query indexes.
ALTER TABLE crm_presale_assignments
  ADD COLUMN assignee_department_snapshot VARCHAR(128) NOT NULL DEFAULT '' AFTER assignee_name_snapshot;

UPDATE crm_presale_assignments a
JOIN crm_presale_engineers e
  ON e.tenant_id = a.tenant_id AND e.person_id = a.assignee_id AND e.deleted_at IS NULL
SET a.assignee_department_snapshot = e.department
WHERE a.assignee_department_snapshot = '';

UPDATE crm_presale_worklogs w
JOIN crm_presale_engineers e
  ON e.tenant_id = w.tenant_id AND e.person_id = w.person_id AND e.deleted_at IS NULL
SET w.department_snapshot = e.department
WHERE w.department_snapshot = '';

CREATE INDEX idx_presale_report_worklog
  ON crm_presale_worklogs (tenant_id, work_start, voided_at, person_id, request_id);
CREATE INDEX idx_presale_report_request
  ON crm_presale_requests (tenant_id, opportunity_id, status, created_at, expected_end);
CREATE INDEX idx_presale_report_completion
  ON crm_presale_status_logs (tenant_id, `trigger`, occurred_at, request_id);
CREATE INDEX idx_opportunity_report_scope
  ON crm_opportunities (tenant_id, owner_org_id, opp_status, stage_changed_at, created_at);

-- Backfill uses the current engineer directory only for legacy rows. New
-- assignments and worklogs persist the department snapshot at write time.
-- On a large upgraded table, run each CREATE INDEX as online DDL in a separate
-- release step and monitor replication lag before enabling report traffic.
