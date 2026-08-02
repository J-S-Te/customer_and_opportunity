-- Target schema: CRM. Run after 000027_customer_merge.up.sql. Migration 000028
-- belongs to the independent customer_portal schema and is intentionally not
-- a CRM predecessor.
CREATE TABLE crm_notifications (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  source_event_id VARCHAR(64) NOT NULL, type VARCHAR(64) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL, opportunity_version BIGINT UNSIGNED NOT NULL,
  opportunity_no VARCHAR(32) NOT NULL,
  opportunity_name VARCHAR(200) NOT NULL, recipient_id VARCHAR(64) NOT NULL,
  recipient_kind VARCHAR(32) NOT NULL, title VARCHAR(200) NOT NULL,
  body VARCHAR(500) NOT NULL, target_path VARCHAR(500) NOT NULL,
  status VARCHAR(16) NOT NULL, read_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_crm_notification_source (tenant_id,source_event_id),
  KEY idx_crm_notification_inbox (tenant_id,recipient_id,status,created_at,id),
  KEY idx_crm_notification_opportunity (tenant_id,opportunity_id,created_at),
  CONSTRAINT chk_crm_notification_status CHECK (status IN ('UNREAD','READ','CANCELLED')),
  CONSTRAINT chk_crm_notification_recipient_kind CHECK (recipient_kind IN ('PREVIOUS_OWNER','NEW_OWNER'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- No historical owner-change notifications are synthesized. Their outbox
-- payloads are untrusted and may already have been superseded; the worker only
-- projects still-PENDING/RETRY_WAIT events after checking the current row and
-- the immutable OWNER_CHANGE audit trail. Creating this empty table does not
-- rewrite existing business rows.
