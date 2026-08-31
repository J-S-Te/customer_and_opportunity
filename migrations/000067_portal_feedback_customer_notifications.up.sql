ALTER TABLE portal_feedback_notifications
  ADD COLUMN account_id VARCHAR(128) NULL AFTER tenant_id,
  ADD COLUMN event_id VARCHAR(64) NULL AFTER feedback_id,
  ADD COLUMN title VARCHAR(200) NOT NULL DEFAULT '' AFTER kind,
  ADD COLUMN body VARCHAR(500) NOT NULL DEFAULT '' AFTER title,
  ADD COLUMN target_path VARCHAR(500) NOT NULL DEFAULT '/customer-portal/feedback' AFTER body;
ALTER TABLE portal_feedback_notifications DROP INDEX uq_portal_feedback_notice;
UPDATE portal_feedback_notifications n JOIN portal_feedbacks f ON f.id=n.feedback_id AND f.tenant_id=n.tenant_id
  SET n.account_id=f.account_id, n.event_id=CONCAT('legacy-', n.feedback_id, '-', n.kind)
  WHERE n.account_id IS NULL OR n.event_id IS NULL;
ALTER TABLE portal_feedback_notifications MODIFY account_id VARCHAR(128) NOT NULL, MODIFY event_id VARCHAR(64) NOT NULL;
ALTER TABLE portal_feedback_notifications ADD UNIQUE KEY uq_portal_feedback_notice (tenant_id,event_id);
ALTER TABLE portal_feedback_notifications ADD KEY idx_portal_feedback_notice_account (tenant_id,account_id,status,created_at);
