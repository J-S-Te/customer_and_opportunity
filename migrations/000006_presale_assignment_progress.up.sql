CREATE TABLE crm_presale_engineers (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3), deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  person_id VARCHAR(64) NOT NULL, person_name VARCHAR(128) NOT NULL, department VARCHAR(128) NOT NULL DEFAULT '', role VARCHAR(32) NOT NULL,
  skill_tags_json JSON NULL, contact_cipher VARBINARY(1024) NULL, valid_flag BOOLEAN NOT NULL, source_updated_at DATETIME(3) NOT NULL, synced_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_engineer (tenant_id, person_id), KEY idx_presale_engineer_valid (tenant_id, valid_flag, person_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_assignments (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3), deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  request_id BIGINT UNSIGNED NOT NULL, assignee_id VARCHAR(64) NOT NULL, assignee_name_snapshot VARCHAR(128) NOT NULL, assignee_role VARCHAR(32) NOT NULL,
  assigned_by VARCHAR(64) NOT NULL, assigned_at DATETIME(3) NOT NULL, ended_at DATETIME(3) NULL, is_current BOOLEAN NOT NULL, batch_no BIGINT UNSIGNED NOT NULL, change_reason VARCHAR(1000) NOT NULL,
  PRIMARY KEY (id), KEY idx_presale_assignment_request (tenant_id, request_id, assigned_at), KEY idx_presale_assignment_person (tenant_id, assignee_id, is_current, request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_progress_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, request_id BIGINT UNSIGNED NOT NULL, author_id VARCHAR(64) NOT NULL,
  content TEXT NOT NULL, link_url VARCHAR(1000) NOT NULL DEFAULT '', progress_pct TINYINT UNSIGNED NULL, created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id), KEY idx_presale_progress_timeline (tenant_id, request_id, created_at, id), CONSTRAINT chk_presale_progress_pct CHECK (progress_pct IS NULL OR progress_pct <= 100)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_status_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, request_id BIGINT UNSIGNED NOT NULL,
  from_status VARCHAR(32) NOT NULL, to_status VARCHAR(32) NOT NULL, `trigger` VARCHAR(64) NOT NULL, reason VARCHAR(2000) NOT NULL DEFAULT '', operator_id VARCHAR(64) NOT NULL,
  occurred_at DATETIME(3) NOT NULL, request_id_trace VARCHAR(64) NOT NULL DEFAULT '', PRIMARY KEY (id), KEY idx_presale_status_timeline (tenant_id, request_id, occurred_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
