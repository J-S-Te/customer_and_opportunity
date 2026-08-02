-- Target schema: customer_portal. Apply after 000050. Number 000052 is shared
-- only by filename ordering; 000051 belongs to the independent CRM schema.
-- This creates only forward read cursors; historical reads are not fabricated.
ALTER TABLE portal_project_messages
  ADD COLUMN message_cursor VARCHAR(64) COLLATE utf8mb4_0900_bin NULL AFTER id;

-- Existing messages receive a one-time opaque cursor. It does not claim those
-- messages were read and does not expose their numeric primary keys.
UPDATE portal_project_messages
SET message_cursor = SHA2(CONCAT(UUID(), ':', tenant_id, ':', conversation_id, ':', id, ':', request_hash), 256)
WHERE message_cursor IS NULL;

ALTER TABLE portal_project_messages
  MODIFY COLUMN message_cursor VARCHAR(64) COLLATE utf8mb4_0900_bin NOT NULL,
  ADD UNIQUE KEY uq_portal_project_message_cursor (message_cursor);

CREATE TABLE portal_project_message_read_receipts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  conversation_id BIGINT UNSIGNED NOT NULL,
  reader_type VARCHAR(16) NOT NULL,
  reader_account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  last_read_message_id BIGINT UNSIGNED NOT NULL,
  last_read_cursor VARCHAR(64) COLLATE utf8mb4_0900_bin NOT NULL,
  read_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project_message_receipt (tenant_id,conversation_id,reader_type,reader_account_id),
  KEY idx_portal_project_message_receipt_conversation (tenant_id,conversation_id,last_read_message_id),
  KEY idx_portal_project_message_receipt_message (last_read_message_id),
  CONSTRAINT fk_portal_project_message_receipt_conversation FOREIGN KEY (conversation_id) REFERENCES portal_project_conversations(id),
  CONSTRAINT fk_portal_project_message_receipt_message FOREIGN KEY (last_read_message_id) REFERENCES portal_project_messages(id),
  CONSTRAINT chk_portal_project_message_receipt_reader CHECK (reader_type IN ('CUSTOMER','MANAGER')),
  CONSTRAINT chk_portal_project_message_receipt_account CHECK (reader_account_id <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Production rollback must use a forward migration after retaining receipt and
-- audit evidence. This table intentionally has no historical backfill.
