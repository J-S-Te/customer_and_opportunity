-- CRM schema only. PMS engineer sync queue, per-tenant schedule and safe cache metadata.
CREATE TABLE crm_presale_engineer_sync_states (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3), deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  last_attempt_at DATETIME(3) NULL, last_successful_at DATETIME(3) NULL, last_source_revision DATETIME(3) NULL,
  next_sync_at DATETIME(3) NOT NULL, last_job_no VARCHAR(64) NOT NULL DEFAULT '', last_person_count INT UNSIGNED NOT NULL DEFAULT 0,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_engineer_sync_state (tenant_id),
  KEY idx_presale_engineer_sync_due (next_sync_at,tenant_id),
  CONSTRAINT chk_presale_engineer_sync_state_version CHECK (version >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_engineer_sync_jobs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3), deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  job_no VARCHAR(64) NOT NULL, trigger_type VARCHAR(16) NOT NULL, requested_by VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL, request_hash VARCHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL, retry_count TINYINT UNSIGNED NOT NULL DEFAULT 0, next_retry_at DATETIME(3) NULL,
  locked_by VARCHAR(128) NOT NULL DEFAULT '', locked_until DATETIME(3) NULL, last_error VARCHAR(1000) NOT NULL DEFAULT '',
  source_revision DATETIME(3) NULL, person_count INT UNSIGNED NOT NULL DEFAULT 0, started_at DATETIME(3) NULL, finished_at DATETIME(3) NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_engineer_sync_job_no (tenant_id,job_no),
  UNIQUE KEY uq_presale_engineer_sync_idem (tenant_id,requested_by,idempotency_key),
  KEY idx_presale_engineer_sync_claim (status,next_retry_at,locked_until,id),
  CONSTRAINT chk_presale_engineer_sync_trigger CHECK (trigger_type IN ('MANUAL','SCHEDULED')),
  CONSTRAINT chk_presale_engineer_sync_status CHECK (status IN ('PENDING','PROCESSING','RETRY_WAIT','SUCCEEDED','DEAD_LETTER')),
  CONSTRAINT chk_presale_engineer_sync_version CHECK (version >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_engineer_sync_requests (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3), deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  requested_by VARCHAR(64) NOT NULL, idempotency_key VARCHAR(128) NOT NULL, request_hash VARCHAR(64) NOT NULL,
  job_id BIGINT UNSIGNED NOT NULL, job_no VARCHAR(64) NOT NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_engineer_sync_request (tenant_id,requested_by,idempotency_key),
  KEY idx_presale_engineer_sync_request_job (tenant_id,job_id),
  CONSTRAINT chk_presale_engineer_sync_request_version CHECK (version >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
