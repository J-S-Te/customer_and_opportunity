-- Development rollback only. Production rollback must retain read/audit data
-- and be performed through an approved forward migration.
DROP TABLE IF EXISTS portal_project_message_reads;

ALTER TABLE portal_project_messages
  DROP KEY uq_portal_project_message_tenant_conversation_id;

ALTER TABLE portal_project_conversations
  DROP KEY uq_portal_project_conversation_tenant_id;
