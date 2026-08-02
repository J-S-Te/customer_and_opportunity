CREATE TABLE crm_presale_alert_rules (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3), deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  type VARCHAR(40) NOT NULL, threshold_hours INT UNSIGNED NOT NULL, enabled BOOLEAN NOT NULL,
  config_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_alert_rule (tenant_id,type),
  CONSTRAINT chk_presale_alert_rule_threshold CHECK (threshold_hours <= 8760)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_alert_rule_versions (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  rule_id BIGINT UNSIGNED NOT NULL, type VARCHAR(40) NOT NULL, threshold_hours INT UNSIGNED NOT NULL,
  enabled BOOLEAN NOT NULL, config_version BIGINT UNSIGNED NOT NULL, changed_by VARCHAR(64) NOT NULL,
  request_id VARCHAR(64) NOT NULL DEFAULT '', changed_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id), UNIQUE KEY uq_presale_alert_rule_version (tenant_id,type,config_version),
  KEY idx_presale_alert_rule_history (tenant_id,rule_id,changed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_alerts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3), updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3), deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  request_id BIGINT UNSIGNED NOT NULL, alert_type VARCHAR(40) NOT NULL, rule_version BIGINT UNSIGNED NOT NULL,
  basis_at DATETIME(3) NOT NULL, due_at DATETIME(3) NOT NULL, status VARCHAR(16) NOT NULL,
  recipient_id VARCHAR(64) NOT NULL, sent_at DATETIME(3) NULL, read_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_presale_alert_dedupe (tenant_id,request_id,alert_type,rule_version,recipient_id),
  KEY idx_presale_alert_recipient (tenant_id,recipient_id,status,created_at),
  KEY idx_presale_alert_request (tenant_id,request_id,status,due_at),
  CONSTRAINT chk_presale_alert_status CHECK (status IN ('PENDING','UNREAD','READ','CANCELLED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_presale_job_leases (
  job_name VARCHAR(64) NOT NULL, owner_id VARCHAR(128) NOT NULL, lease_until DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL, PRIMARY KEY (job_name), KEY idx_presale_job_lease (lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Existing requests are intentionally evaluated by the first scanner run using
-- the then-current tenant configuration. Sent alerts are never rewritten when
-- a later configuration version is published.
-- Large-table risk: crm_presale_alerts starts empty. The request scan uses the
-- existing tenant/status/expected_end indexes and bounded batches.
