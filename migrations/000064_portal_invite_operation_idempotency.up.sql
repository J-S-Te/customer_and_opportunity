-- CRM schema only. This durable saga is created before the first external
-- identity write, so an ambiguous response can resume with the same remote
-- idempotency keys instead of provisioning a second user or mapping.
CREATE TABLE crm_portal_provision_operations (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  operation_no VARCHAR(32) COLLATE utf8mb4_0900_bin NOT NULL,
  actor_id VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  contact_id BIGINT UNSIGNED NOT NULL,
  contact_snapshot_cipher VARBINARY(4096) NOT NULL,
  stage VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  platform_user_id VARCHAR(128) NOT NULL DEFAULT '',
  account_no VARCHAR(64) NOT NULL DEFAULT '',
  portal_account_id VARCHAR(64) NOT NULL DEFAULT '',
  invite_id BIGINT UNSIGNED NULL,
  token_cipher VARBINARY(1024) NULL,
  attempts SMALLINT UNSIGNED NOT NULL DEFAULT 0,
  last_error_code VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  last_error_summary VARCHAR(500) NOT NULL DEFAULT '',
  completed_at DATETIME(3) NULL,
  created_by VARCHAR(128) NOT NULL,
  updated_by VARCHAR(128) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_provision_operation_no (tenant_id, operation_no),
  UNIQUE KEY uq_portal_provision_idempotency (tenant_id, actor_id, idempotency_key),
  UNIQUE KEY uq_portal_provision_invite (tenant_id, invite_id),
  KEY idx_portal_provision_customer (tenant_id, customer_id, created_at),
  KEY idx_portal_provision_recovery (status, stage, updated_at),
  CONSTRAINT chk_portal_provision_request_hash CHECK (request_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT chk_portal_provision_stage CHECK (stage IN ('PREPARED','USER_PROVISIONED','ROLE_ASSIGNED','MAPPING_READY','COMPLETED')),
  CONSTRAINT chk_portal_provision_status CHECK (status IN ('PROCESSING','RETRY_WAIT','COMPLETED')),
  CONSTRAINT chk_portal_provision_completion CHECK (
    (stage = 'COMPLETED' AND status = 'COMPLETED' AND invite_id IS NOT NULL AND token_cipher IS NOT NULL AND completed_at IS NOT NULL)
    OR
    (stage <> 'COMPLETED' AND status <> 'COMPLETED' AND invite_id IS NULL AND token_cipher IS NULL AND completed_at IS NULL)
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- No historical operations are inferred: older remote writes do not carry a
-- trustworthy operation number or replay token and must be reconciled as-is.
