ALTER TABLE crm_portal_invites MODIFY COLUMN platform_user_id VARCHAR(128) NOT NULL;
ALTER TABLE crm_portal_identity_links MODIFY COLUMN platform_user_id VARCHAR(128) NOT NULL;

CREATE TABLE crm_portal_compensation_tasks (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  task_no VARCHAR(32) NOT NULL,
  task_type VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  contact_id BIGINT UNSIGNED NOT NULL,
  platform_user_id VARCHAR(128) NOT NULL,
  status VARCHAR(16) NOT NULL,
  attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
  next_retry_at DATETIME(3) NULL,
  last_error_code VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_compensation_task_no (tenant_id, task_no),
  KEY idx_portal_compensation_claim (tenant_id, status, next_retry_at, created_at),
  KEY idx_portal_compensation_customer (tenant_id, customer_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Existing invite/link rows are preserved. Widening platform_user_id is
-- compatible and prevents truncation of standards-compliant OIDC subject IDs.
