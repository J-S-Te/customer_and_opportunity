-- Destructive recovery for test/empty environments only. In production first
-- disable project.message.send, preserve message/audit evidence, and use a
-- forward migration. Never roll back after customer messages exist.
DROP TABLE IF EXISTS portal_project_message_events;
DROP TABLE IF EXISTS portal_project_messages;
DROP TABLE IF EXISTS portal_project_conversations;

ALTER TABLE portal_project_snapshots
  DROP KEY idx_portal_project_manager_account,
  DROP COLUMN manager_portal_account_id;
