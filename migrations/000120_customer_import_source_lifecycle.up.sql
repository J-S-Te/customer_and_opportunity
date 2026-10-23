-- Target schema: CRM. Apply after 000117 (118/119 belong to Portal).
-- A durable source intent is committed with the preview before any gateway I/O.
-- source ciphertext is AES-256-GCM, bounded by the 10 MiB import input limit;
-- it is cleared only after an idempotent exact-tuple release is acknowledged.
CREATE TABLE crm_customer_import_sources (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  job_id BIGINT UNSIGNED NOT NULL,
  job_no VARCHAR(64) NOT NULL,
  actor_id VARCHAR(64) NOT NULL,
  request_id VARCHAR(128) NOT NULL,
  filename VARCHAR(100) NOT NULL,
  content_cipher MEDIUMBLOB NULL,
  file_id VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
  attempts INT UNSIGNED NOT NULL DEFAULT 0,
  next_attempt_at DATETIME(3) NOT NULL,
  locked_by VARCHAR(64) NOT NULL DEFAULT '',
  locked_until DATETIME(3) NULL,
  last_error_code VARCHAR(32) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_customer_import_source_job (tenant_id, job_id),
  UNIQUE KEY uq_customer_import_source_request (tenant_id, request_id),
  KEY idx_customer_import_source_due (tenant_id, status, next_attempt_at, locked_until),
  CONSTRAINT fk_customer_import_source_job FOREIGN KEY (tenant_id, job_id) REFERENCES crm_customer_import_jobs(tenant_id, id),
  CONSTRAINT chk_customer_import_source_status CHECK (status IN ('PENDING', 'UPLOADED', 'RELEASED')),
  CONSTRAINT chk_customer_import_source_content CHECK (status <> 'RELEASED' OR content_cipher IS NULL)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
