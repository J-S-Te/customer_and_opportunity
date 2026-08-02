-- Stop all opportunity-owner-notification-worker instances before rollback.
-- Existing CRM notifications should be archived according to the retention
-- policy before this destructive development-only rollback.
DROP TABLE IF EXISTS crm_notifications;
