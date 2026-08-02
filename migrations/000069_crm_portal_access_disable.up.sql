-- CRM schema only. Forward-only access disable saga: Portal mapping/session
-- revocation is completed before the platform application role is revoked.
CREATE TABLE crm_portal_access_disable_operations (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  operation_no VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  actor_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  identity_link_id BIGINT UNSIGNED NOT NULL,
  identity_link_version BIGINT UNSIGNED NOT NULL,
  contact_id BIGINT UNSIGNED NOT NULL,
  platform_user_id VARCHAR(128) NOT NULL,
  portal_account_id VARCHAR(64) NOT NULL,
  reason VARCHAR(500) NOT NULL,
  stage VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempts SMALLINT UNSIGNED NOT NULL DEFAULT 0,
  last_error_code VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  last_error_summary VARCHAR(500) NOT NULL DEFAULT '',
  next_retry_at DATETIME(3) NULL,
  locked_by VARCHAR(128) NOT NULL DEFAULT '',
  locked_until DATETIME(3) NULL,
  completed_at DATETIME(3) NULL,
  created_by VARCHAR(128) NOT NULL,
  updated_by VARCHAR(128) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_access_disable_no (tenant_id, operation_no),
  UNIQUE KEY uq_portal_access_disable_idempotency (tenant_id, actor_id, idempotency_key),
  KEY idx_portal_access_disable_customer (tenant_id, customer_id, created_at),
  KEY idx_portal_access_disable_link (tenant_id, identity_link_id),
  KEY idx_portal_access_disable_recovery (status, next_retry_at, locked_until, updated_at),
  CONSTRAINT fk_portal_access_disable_customer FOREIGN KEY (tenant_id, customer_id) REFERENCES crm_customers (tenant_id, id) ON DELETE RESTRICT,
  CONSTRAINT fk_portal_access_disable_link FOREIGN KEY (identity_link_id) REFERENCES crm_portal_identity_links (id) ON DELETE RESTRICT,
  CONSTRAINT chk_portal_access_disable_hash CHECK (request_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT chk_portal_access_disable_stage CHECK (stage IN ('PREPARED','MAPPING_DISABLED','COMPLETED')),
  CONSTRAINT chk_portal_access_disable_status CHECK (status IN ('PROCESSING','RETRY_WAIT','COMPLETED','DEAD_LETTER')),
  CONSTRAINT chk_portal_access_disable_completion CHECK (
    (stage = 'COMPLETED' AND status = 'COMPLETED' AND completed_at IS NOT NULL)
    OR (stage <> 'COMPLETED' AND status <> 'COMPLETED' AND completed_at IS NULL)
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Existing links remain enabled. A migration cannot infer operator intent or
-- a revocation reason and must never disable historical access automatically.
