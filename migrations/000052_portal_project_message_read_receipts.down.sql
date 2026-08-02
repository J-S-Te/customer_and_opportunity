-- Destructive recovery for test/empty environments only. This removes read
-- evidence and must not be used as a routine production rollback.
DROP TABLE IF EXISTS portal_project_message_read_receipts;
ALTER TABLE portal_project_messages
  DROP KEY uq_portal_project_message_cursor,
  DROP COLUMN message_cursor;
