-- customer_portal schema only. Stop portal-feedback-worker and disable the
-- feature before rollback. Export records first; prefer forward repair.
DROP TABLE IF EXISTS portal_feedback_job_leases;
DROP TABLE IF EXISTS portal_feedback_outbox;
DROP TABLE IF EXISTS portal_feedback_notifications;
DROP TABLE IF EXISTS portal_feedback_escalations;
DROP TABLE IF EXISTS portal_feedback_status_logs;
DROP TABLE IF EXISTS portal_feedback_messages;
DROP TABLE IF EXISTS portal_feedbacks;
