-- Controlled empty/test-environment rollback. Production must archive the
-- append-only read evidence and use a forward migration.
DROP TABLE IF EXISTS portal_report_notification_read_events;
DROP TABLE IF EXISTS portal_report_notifications;
