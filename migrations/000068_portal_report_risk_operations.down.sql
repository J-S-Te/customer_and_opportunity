-- Destructive test/empty-environment rollback only. Production environments
-- with risk-review evidence must use a forward migration instead.
DROP TABLE IF EXISTS portal_report_risk_review_events;
DROP TABLE IF EXISTS portal_report_risk_alerts;
ALTER TABLE portal_report_grants
  DROP INDEX uq_portal_report_grant_scope_id;
