ALTER TABLE portal_feedback_notifications DROP INDEX idx_portal_feedback_notice_account;
ALTER TABLE portal_feedback_notifications DROP INDEX uq_portal_feedback_notice;
ALTER TABLE portal_feedback_notifications ADD UNIQUE KEY uq_portal_feedback_notice (feedback_id,kind);
ALTER TABLE portal_feedback_notifications DROP COLUMN target_path, DROP COLUMN body, DROP COLUMN title, DROP COLUMN event_id, DROP COLUMN account_id;
