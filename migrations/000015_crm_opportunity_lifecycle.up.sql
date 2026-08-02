-- CRM schema: adds BM-001 master-data and reversible lifecycle columns.
ALTER TABLE crm_opportunities
  ADD COLUMN system_count INT UNSIGNED NOT NULL DEFAULT 0 AFTER requirement_summary,
  ADD COLUMN pain_points TEXT NULL AFTER system_count,
  ADD COLUMN competitor_info TEXT NULL AFTER pain_points,
  ADD COLUMN end_date DATE NULL AFTER external_status_changed_at,
  ADD COLUMN status_before_void VARCHAR(32) NULL AFTER end_date;

CREATE INDEX idx_opportunity_lifecycle
  ON crm_opportunities (tenant_id, opp_status, end_date, id);

-- Production recovery is forward-only after lifecycle writes. Do not drop these
-- columns until all VOID rows have been restored or archived.
