-- Target schema: customer_portal. Apply after 000046. The nullable-compatible
-- source field is deployed before project sync starts sending recipient IDs.
ALTER TABLE portal_project_snapshots
  ADD COLUMN manager_portal_account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL DEFAULT '' AFTER manager_contact_masked,
  ADD KEY idx_portal_project_manager_account (tenant_id,manager_portal_account_id,project_id);

CREATE TABLE portal_project_conversations (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  public_id VARCHAR(64) COLLATE utf8mb4_0900_bin NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  project_id VARCHAR(64) NOT NULL,
  customer_account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  manager_account_id_snapshot VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  manager_name_snapshot VARCHAR(128) NOT NULL DEFAULT '',
  create_idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  create_request_hash CHAR(64) NOT NULL,
  last_message_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project_conversation_public (public_id),
  UNIQUE KEY uq_portal_project_conversation (tenant_id,customer_id,project_id,customer_account_id,manager_account_id_snapshot),
  UNIQUE KEY uq_portal_project_conversation_create (tenant_id,customer_account_id,create_idempotency_key),
  KEY idx_portal_project_conversation_customer (tenant_id,customer_id,project_id,customer_account_id,last_message_at),
  KEY idx_portal_project_conversation_manager (tenant_id,manager_account_id_snapshot,last_message_at),
  CONSTRAINT chk_portal_project_conversation_accounts CHECK (customer_account_id <> '' AND manager_account_id_snapshot <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_project_messages (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  conversation_id BIGINT UNSIGNED NOT NULL,
  sender_type VARCHAR(16) NOT NULL,
  sender_account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  recipient_account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  content TEXT NOT NULL,
  idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  request_hash CHAR(64) NOT NULL,
  accepted_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project_message_key (tenant_id,sender_type,sender_account_id,idempotency_key),
  KEY idx_portal_project_message_timeline (tenant_id,conversation_id,accepted_at,id),
  KEY idx_portal_project_message_rate (tenant_id,conversation_id,sender_type,sender_account_id,accepted_at),
  CONSTRAINT fk_portal_project_message_conversation FOREIGN KEY (conversation_id) REFERENCES portal_project_conversations(id),
  CONSTRAINT chk_portal_project_message_sender CHECK (sender_type IN ('CUSTOMER','MANAGER')),
  CONSTRAINT chk_portal_project_message_accounts CHECK (sender_account_id <> '' AND recipient_account_id <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_project_message_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  conversation_id BIGINT UNSIGNED NOT NULL,
  message_id BIGINT UNSIGNED NULL,
  operation VARCHAR(64) NOT NULL,
  actor_type VARCHAR(16) NOT NULL,
  actor_account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  recipient_account_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL DEFAULT '',
  request_id VARCHAR(64) NOT NULL DEFAULT '',
  result VARCHAR(16) NOT NULL,
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  KEY idx_portal_project_message_event (tenant_id,conversation_id,occurred_at,id),
  KEY idx_portal_project_message_event_message (message_id),
  CONSTRAINT fk_portal_project_message_event_conversation FOREIGN KEY (conversation_id) REFERENCES portal_project_conversations(id),
  CONSTRAINT fk_portal_project_message_event_message FOREIGN KEY (message_id) REFERENCES portal_project_messages(id),
  CONSTRAINT chk_portal_project_message_event_actor CHECK (actor_type IN ('CUSTOMER','MANAGER')),
  CONSTRAINT chk_portal_project_message_event_result CHECK (result IN ('SUCCEEDED','FAILED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Backfill: none. Existing manager_name/person_ref/contact values are not
-- delivery identities and must not be converted into account IDs.
-- Deployment: migrate first, deploy Portal/worker, then let the project source
-- begin sending manager_portal_account_id. Missing values fail closed.
-- Risk: the ALTER may acquire a metadata lock; use the approved online DDL
-- mechanism on large snapshot tables and monitor replication lag.
