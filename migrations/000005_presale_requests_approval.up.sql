CREATE TABLE crm_presale_number_sequences (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  sequence_type VARCHAR(16) NOT NULL,
  sequence_date CHAR(8) NOT NULL,
  `last_value` INT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_presale_sequence (tenant_id, sequence_type, sequence_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_requests (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  request_no VARCHAR(32) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  opportunity_no_snapshot VARCHAR(64) NOT NULL,
  applicant_id VARCHAR(64) NOT NULL,
  applicant_name_snapshot VARCHAR(128) NOT NULL,
  venue VARCHAR(16) NOT NULL,
  service_address VARCHAR(500) NOT NULL DEFAULT '',
  contact_name VARCHAR(128) NOT NULL,
  contact_phone_cipher VARBINARY(1024) NOT NULL,
  contact_phone_masked VARCHAR(64) NOT NULL,
  description TEXT NOT NULL,
  expected_start DATETIME(3) NOT NULL,
  expected_end DATETIME(3) NOT NULL,
  urgency VARCHAR(16) NOT NULL,
  status VARCHAR(32) NOT NULL,
  current_approval_node TINYINT UNSIGNED NOT NULL DEFAULT 0,
  reject_reason VARCHAR(2000) NOT NULL DEFAULT '',
  completed_at DATETIME(3) NULL,
  cancelled_at DATETIME(3) NULL,
  create_idempotency_key VARCHAR(128) NOT NULL,
  create_request_hash CHAR(64) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_presale_request_no (tenant_id, request_no),
  UNIQUE KEY uq_presale_create_key (tenant_id, create_idempotency_key),
  KEY idx_presale_opportunity (tenant_id, opportunity_id),
  KEY idx_presale_applicant_status (tenant_id, applicant_id, status, created_at),
  KEY idx_presale_status_created (tenant_id, status, created_at),
  CONSTRAINT chk_presale_expected_time CHECK (expected_end >= expected_start)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_approval_instances (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  request_id BIGINT UNSIGNED NOT NULL,
  engine_instance_id VARCHAR(128) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL,
  current_node TINYINT UNSIGNED NOT NULL DEFAULT 0,
  last_event_seq BIGINT UNSIGNED NOT NULL DEFAULT 0,
  started_at DATETIME(3) NULL,
  finished_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_presale_approval_request (tenant_id, request_id),
  KEY idx_presale_engine_instance (tenant_id, engine_instance_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_approval_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  node TINYINT UNSIGNED NOT NULL,
  approver_id VARCHAR(64) NOT NULL,
  approver_name_snapshot VARCHAR(128) NOT NULL,
  result VARCHAR(16) NOT NULL,
  comment VARCHAR(2000) NOT NULL DEFAULT '',
  approved_at DATETIME(3) NOT NULL,
  engine_task_id VARCHAR(128) NOT NULL,
  request_id_trace VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uq_presale_engine_task (tenant_id, engine_task_id),
  KEY idx_presale_approval_timeline (tenant_id, request_id, approved_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Recovery: stop application writes, export rows created after deployment, then
-- drop in reverse dependency order. Production rollback should normally retain
-- these tables because prior binaries do not read them.
