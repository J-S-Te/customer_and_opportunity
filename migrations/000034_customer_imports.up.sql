-- Target schema: CRM. Apply after 000033.
-- CM-001 import preview/commit coordination. Uploaded workbook bytes are never
-- persisted. command_cipher is application-layer AES-256-GCM ciphertext.
CREATE TABLE crm_customer_import_jobs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  job_no VARCHAR(64) NOT NULL,
  actor_id VARCHAR(64) NOT NULL,
  status VARCHAR(32) NOT NULL,
  reason VARCHAR(500) NOT NULL,
  total_rows INT UNSIGNED NOT NULL DEFAULT 0,
  importable_rows INT UNSIGNED NOT NULL DEFAULT 0,
  warning_rows INT UNSIGNED NOT NULL DEFAULT 0,
  error_rows INT UNSIGNED NOT NULL DEFAULT 0,
  succeeded_rows INT UNSIGNED NOT NULL DEFAULT 0,
  failed_rows INT UNSIGNED NOT NULL DEFAULT 0,
  expires_at DATETIME(3) NOT NULL,
  completed_at DATETIME(3) NULL,
  commit_request_version BIGINT UNSIGNED NOT NULL DEFAULT 0,
  commit_idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
  locked_by VARCHAR(128) NOT NULL DEFAULT '',
  locked_until DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_customer_import_tenant_id (tenant_id,id),
  UNIQUE KEY uq_customer_import_job_no (tenant_id,job_no),
  KEY idx_customer_import_owner_status (tenant_id,actor_id,status,expires_at),
  CONSTRAINT chk_customer_import_status CHECK (status IN ('PREVIEWED','COMMITTING','COMPLETED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_customer_import_rows (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  job_id BIGINT UNSIGNED NOT NULL,
  row_no INT UNSIGNED NOT NULL,
  status VARCHAR(32) NOT NULL,
  command_cipher VARBINARY(4096) NULL,
  error_column VARCHAR(100) NOT NULL DEFAULT '',
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  error_message VARCHAR(500) NOT NULL DEFAULT '',
  customer_id BIGINT UNSIGNED NULL,
  customer_no VARCHAR(32) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_customer_import_row (tenant_id,job_id,row_no),
  KEY idx_customer_import_row_status (tenant_id,job_id,status,row_no),
  CONSTRAINT fk_customer_import_row_job FOREIGN KEY (tenant_id,job_id) REFERENCES crm_customer_import_jobs(tenant_id,id),
  CONSTRAINT chk_customer_import_row_status CHECK (status IN ('READY','WARNING','ERROR','IMPORTED','FAILED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_customer_import_idempotency (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  actor_id VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  response_json JSON NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_customer_import_idempotency (tenant_id,actor_id,idempotency_key),
  KEY idx_customer_import_idempotency_created (tenant_id,created_at),
  CONSTRAINT chk_customer_import_idempotency_status CHECK (status IN ('PROCESSING','COMPLETED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Large installations should create the new tables before enabling the routes.
-- No historical backfill is required because imports are new coordination data.
