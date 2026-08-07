CREATE TABLE crm_presale_approval_rules (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  rule_key VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL,
  priority INT NOT NULL DEFAULT 0,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  expression_json JSON NOT NULL,
  nodes_json JSON NOT NULL,
  UNIQUE KEY uq_presale_approval_rule_key (tenant_id, rule_key),
  KEY idx_presale_approval_rule_match (tenant_id, enabled, priority),
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

ALTER TABLE crm_presale_approval_instances
  ADD COLUMN rule_id VARCHAR(64) NOT NULL DEFAULT '' AFTER finished_at,
  ADD COLUMN rule_version BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER rule_id,
  ADD COLUMN nodes_json JSON NULL AFTER rule_version;

ALTER TABLE crm_presale_requests
  ADD COLUMN execution_department_id VARCHAR(64) NOT NULL DEFAULT '' AFTER current_approval_node,
  ADD COLUMN execution_department VARCHAR(128) NOT NULL DEFAULT '' AFTER execution_department_id;
