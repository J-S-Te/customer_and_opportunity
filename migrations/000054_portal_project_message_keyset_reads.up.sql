-- Target schema: customer_portal. Apply after 000052. CRM migration numbers
-- are an independent sequence even when they share this directory.
--
-- The former participant high-water mark could acknowledge messages that a
-- bounded client page never displayed. Keep that table as immutable legacy
-- evidence and record all new reads one message at a time.
ALTER TABLE portal_project_conversations
  ADD UNIQUE KEY uq_portal_project_conversation_tenant_id (tenant_id,id);

ALTER TABLE portal_project_messages
  ADD UNIQUE KEY uq_portal_project_message_tenant_conversation_id (tenant_id,conversation_id,id);

CREATE TABLE portal_project_message_reads (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  conversation_id BIGINT UNSIGNED NOT NULL,
  message_id BIGINT UNSIGNED NOT NULL,
  reader_type VARCHAR(16) NOT NULL,
  reader_account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  read_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project_message_read (tenant_id,conversation_id,reader_type,reader_account_id,message_id),
  KEY idx_portal_project_message_read_conversation (tenant_id,conversation_id,reader_account_id,read_at),
  KEY idx_portal_project_message_read_message (tenant_id,conversation_id,message_id),
  CONSTRAINT fk_portal_project_message_read_conversation FOREIGN KEY (tenant_id,conversation_id)
    REFERENCES portal_project_conversations(tenant_id,id),
  CONSTRAINT fk_portal_project_message_read_message FOREIGN KEY (tenant_id,conversation_id,message_id)
    REFERENCES portal_project_messages(tenant_id,conversation_id,id),
  CONSTRAINT chk_portal_project_message_read_reader CHECK (reader_type IN ('CUSTOMER','MANAGER')),
  CONSTRAINT chk_portal_project_message_read_account CHECK (reader_account_id <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Conservatively retain only the exact legacy cursor target as an explicit
-- read. Older messages are intentionally made unread again instead of carrying
-- forward a potentially over-broad high-water inference.
INSERT INTO portal_project_message_reads (
  tenant_id,conversation_id,message_id,reader_type,reader_account_id,read_at,created_at
)
SELECT r.tenant_id,r.conversation_id,r.last_read_message_id,r.reader_type,r.reader_account_id,r.read_at,r.created_at
FROM portal_project_message_read_receipts r
JOIN portal_project_messages m
  ON m.tenant_id=r.tenant_id
 AND m.conversation_id=r.conversation_id
 AND m.id=r.last_read_message_id
 AND m.message_cursor=r.last_read_cursor
 AND m.recipient_account_id=r.reader_account_id;

