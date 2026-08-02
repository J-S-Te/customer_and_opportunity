-- Stop every presale-alert-worker instance before rollback. Pending
-- PRESALE_ALERT_SITE_MESSAGE outbox events reference rows in these tables and
-- must be archived/cancelled before the destructive drop below.
DELETE FROM crm_outbox_events WHERE event_type='PRESALE_ALERT_SITE_MESSAGE';
DROP TABLE IF EXISTS crm_presale_job_leases;
DROP TABLE IF EXISTS crm_presale_alerts;
DROP TABLE IF EXISTS crm_presale_alert_rule_versions;
DROP TABLE IF EXISTS crm_presale_alert_rules;
