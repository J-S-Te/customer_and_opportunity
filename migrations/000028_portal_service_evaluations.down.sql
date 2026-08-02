-- customer_portal schema only. Disable evaluation routes and export immutable
-- source/audit data before a controlled rollback; prefer forward repair.
DROP TABLE IF EXISTS portal_evaluation_outbox;
DROP TABLE IF EXISTS portal_evaluation_notifications;
DROP TABLE IF EXISTS portal_evaluation_alerts;
DROP TABLE IF EXISTS portal_evaluation_audit_logs;
DROP TABLE IF EXISTS portal_service_evaluations;
