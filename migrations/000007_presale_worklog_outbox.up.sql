CREATE TABLE crm_presale_worklogs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3), deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  worklog_no VARCHAR(32) NOT NULL, request_id BIGINT UNSIGNED NOT NULL, person_id VARCHAR(64) NOT NULL, department_snapshot VARCHAR(128) NOT NULL DEFAULT '', person_name_snapshot VARCHAR(128) NOT NULL,
  work_start DATETIME(3) NOT NULL, work_end DATETIME(3) NOT NULL, raw_unit VARCHAR(16) NOT NULL, raw_value DECIMAL(10,2) NOT NULL, conversion_factor DECIMAL(10,2) NOT NULL, work_hours DECIMAL(10,2) NOT NULL, unit VARCHAR(16) NOT NULL DEFAULT 'HOUR',
  work_site_address VARCHAR(500) NOT NULL, work_content VARCHAR(32) NOT NULL, remark VARCHAR(1000) NOT NULL DEFAULT '', push_status VARCHAR(16) NOT NULL, push_attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
  next_retry_at DATETIME(3) NULL, last_error_summary VARCHAR(1000) NOT NULL DEFAULT '', idempotency_key VARCHAR(128) NOT NULL, request_hash CHAR(64) NOT NULL, completed_task BOOLEAN NOT NULL DEFAULT FALSE, voided_at DATETIME(3) NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_worklog_no (tenant_id, worklog_no), UNIQUE KEY uq_presale_worklog_key (tenant_id, idempotency_key),
  KEY idx_presale_worklog_request (tenant_id, request_id, work_start), KEY idx_presale_worklog_person (tenant_id, request_id, person_id, voided_at), KEY idx_presale_worklog_delivery (tenant_id, push_status, next_retry_at),
  CONSTRAINT chk_presale_worklog_time CHECK (work_end > work_start), CONSTRAINT chk_presale_worklog_values CHECK (raw_value > 0 AND work_hours > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_outbox_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, event_id VARCHAR(64) NOT NULL, tenant_id VARCHAR(64) NOT NULL, event_type VARCHAR(64) NOT NULL,
  aggregate_type VARCHAR(64) NOT NULL, aggregate_id VARCHAR(64) NOT NULL, payload JSON NOT NULL, status VARCHAR(16) NOT NULL, retry_count TINYINT UNSIGNED NOT NULL DEFAULT 0,
  next_retry_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, sent_at DATETIME(3) NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_crm_outbox_event (event_id), KEY idx_crm_outbox_aggregate_event (tenant_id, aggregate_type, aggregate_id, event_type), KEY idx_crm_outbox_claim (status, next_retry_at, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_integration_attempts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, worklog_id BIGINT UNSIGNED NOT NULL, attempt_no TINYINT UNSIGNED NOT NULL,
  result VARCHAR(16) NOT NULL, error_summary VARCHAR(1000) NOT NULL DEFAULT '', response_code VARCHAR(32) NOT NULL DEFAULT '', attempted_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_delivery_attempt (tenant_id, worklog_id, attempt_no), KEY idx_presale_attempt_time (tenant_id, attempted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Large-table risk: indexes are created with the new empty tables. If adding
-- these structures to existing tables later, use online DDL and monitor lag.
