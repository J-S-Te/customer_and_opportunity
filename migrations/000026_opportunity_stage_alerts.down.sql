-- Target schema: CRM. Stop every opportunity-alert-worker instance before this
-- destructive rollback. Production recovery should disable the worker and use
-- forward repair after notifications have been delivered.
DELETE FROM crm_outbox_events WHERE event_type='OPPORTUNITY_STAGE_ALERT_SITE_MESSAGE';
DROP INDEX idx_opportunity_stage_alert_scan ON crm_opportunities;
DROP TABLE IF EXISTS crm_opportunity_alert_job_leases;
DROP TABLE IF EXISTS crm_opportunity_stage_alerts;
DROP TABLE IF EXISTS crm_opportunity_stage_alert_rule_versions;
DROP TABLE IF EXISTS crm_opportunity_stage_alert_rules;
