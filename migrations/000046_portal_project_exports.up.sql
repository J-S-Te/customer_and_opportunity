CREATE TABLE portal_project_exports (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  public_id VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  project_id VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  snapshot_json JSON NOT NULL,
  source_updated_at DATETIME(3) NOT NULL,
  status VARCHAR(16) NOT NULL,
  file_name VARCHAR(255) NOT NULL DEFAULT '',
  file_hash CHAR(64) NOT NULL DEFAULT '',
  file_size BIGINT NOT NULL DEFAULT 0,
  file_bytes MEDIUMBLOB NULL,
  failure_code VARCHAR(64) NOT NULL DEFAULT '',
  locked_by VARCHAR(128) NOT NULL DEFAULT '',
  locked_until DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  completed_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project_export_public (public_id),
  UNIQUE KEY uq_portal_project_export_key (tenant_id,customer_id,account_id,idempotency_key),
  KEY idx_portal_project_export_project (tenant_id,customer_id,project_id,created_at),
  KEY idx_portal_project_export_claim (status,locked_until,created_at),
  CONSTRAINT chk_portal_project_export_size CHECK (file_size >= 0 AND file_size <= 2097152),
  CONSTRAINT chk_portal_project_export_status CHECK (status IN ('PENDING','GENERATING','READY','FAILED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_project_export_grants (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  public_id VARCHAR(64) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  export_id BIGINT UNSIGNED NOT NULL,
  token_hash CHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  used_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project_export_grant_public (public_id),
  UNIQUE KEY uq_portal_project_export_grant_token (token_hash),
  KEY idx_portal_project_export_grant_owner (tenant_id,customer_id,account_id,export_id),
  KEY idx_portal_project_export_grant_expiry (expires_at,status),
  CONSTRAINT chk_portal_project_export_grant_status CHECK (status IN ('ACTIVE','USED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE portal_project_export_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  export_id BIGINT UNSIGNED NOT NULL,
  event_type VARCHAR(64) NOT NULL,
  result VARCHAR(16) NOT NULL,
  reason_code VARCHAR(64) NOT NULL DEFAULT '',
  request_trace VARCHAR(128) NOT NULL DEFAULT '',
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  KEY idx_portal_project_export_event (tenant_id,customer_id,account_id,export_id,occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Deployment: apply before portal-server exposes project export routes and before the render worker starts.
-- Backfill: none. Historical project views do not imply historical export requests.
-- Risk: online DDL creates only new empty tables; no existing business row is rewritten.
