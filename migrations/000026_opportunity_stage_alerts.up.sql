-- Target schema: CRM. Run after 000025_portal_invite_compensation_worker.up.sql.
CREATE TABLE crm_opportunity_stage_alert_rules (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  stage VARCHAR(32) NOT NULL, threshold_hours INT UNSIGNED NOT NULL,
  enabled BOOLEAN NOT NULL, config_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_opportunity_stage_alert_rule (tenant_id,stage),
  CONSTRAINT chk_opportunity_stage_alert_rule_stage CHECK (stage IN ('初步接触','需求沟通','方案制定','报价','投标')),
  CONSTRAINT chk_opportunity_stage_alert_rule_threshold CHECK (threshold_hours BETWEEN 1 AND 8760)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_opportunity_stage_alert_rule_versions (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL,
  rule_id BIGINT UNSIGNED NOT NULL, stage VARCHAR(32) NOT NULL,
  threshold_hours INT UNSIGNED NOT NULL, enabled BOOLEAN NOT NULL,
  config_version BIGINT UNSIGNED NOT NULL, changed_by VARCHAR(64) NOT NULL,
  request_id VARCHAR(64) NOT NULL DEFAULT '', changed_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_opportunity_stage_alert_rule_version (tenant_id,stage,config_version),
  KEY idx_opportunity_stage_alert_rule_history (tenant_id,rule_id,changed_at),
  CONSTRAINT chk_opportunity_stage_alert_rule_version_stage CHECK (stage IN ('初步接触','需求沟通','方案制定','报价','投标')),
  CONSTRAINT chk_opportunity_stage_alert_rule_version_threshold CHECK (threshold_hours BETWEEN 1 AND 8760)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_opportunity_stage_alerts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  opportunity_id BIGINT UNSIGNED NOT NULL, stage VARCHAR(32) NOT NULL,
  threshold_version BIGINT UNSIGNED NOT NULL, basis_at DATETIME(3) NOT NULL,
  due_at DATETIME(3) NOT NULL, status VARCHAR(16) NOT NULL,
  recipient_id VARCHAR(64) NOT NULL, sent_at DATETIME(3) NULL, read_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_opportunity_stage_alert_dedupe (tenant_id,opportunity_id,stage,threshold_version,recipient_id),
  KEY idx_opportunity_stage_alert_recipient (tenant_id,recipient_id,status,created_at),
  KEY idx_opportunity_stage_alert_opportunity (tenant_id,opportunity_id,status,due_at),
  CONSTRAINT chk_opportunity_stage_alert_stage CHECK (stage IN ('初步接触','需求沟通','方案制定','报价','投标')),
  CONSTRAINT chk_opportunity_stage_alert_status CHECK (status IN ('PENDING','UNREAD','READ','CANCELLED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_opportunity_alert_job_leases (
  job_name VARCHAR(64) NOT NULL, owner_id VARCHAR(128) NOT NULL,
  lease_until DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (job_name), KEY idx_opportunity_alert_job_lease (lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Existing opportunities are evaluated only after a tenant explicitly creates
-- an enabled rule. Rules are never synthesized because their thresholds are a
-- tenant business decision. Large-table risk is limited to a compatible scan
-- index on the existing opportunity table; deploy it with online DDL controls.
CREATE INDEX idx_opportunity_stage_alert_scan
  ON crm_opportunities (opp_status,current_stage,id,tenant_id,stage_changed_at);
