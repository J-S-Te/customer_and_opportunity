CREATE TABLE crm_opportunities (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, opportunity_no VARCHAR(32) NOT NULL,
  name VARCHAR(200) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL, type VARCHAR(64) NOT NULL, source VARCHAR(64) NOT NULL,
  expected_amount DECIMAL(18,2) NOT NULL, expected_sign_date DATE NOT NULL, requirement_summary TEXT NOT NULL,
  owner_user_id VARCHAR(64) NOT NULL, owner_org_id VARCHAR(64) NOT NULL DEFAULT '', current_stage VARCHAR(32) NOT NULL,
  opp_status VARCHAR(32) NOT NULL, contract_ref VARCHAR(64) NULL, lost_reason VARCHAR(64) NULL,
  terminal_pending_type VARCHAR(32) NOT NULL DEFAULT 'NONE', stage_changed_at DATETIME(3) NOT NULL,
  external_status_changed_at DATETIME(3) NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL, deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1, PRIMARY KEY(id), UNIQUE KEY uk_opportunity_no(tenant_id,opportunity_no),
  KEY idx_opportunity_scope(tenant_id,opp_status,owner_user_id,updated_at), KEY idx_opportunity_customer(tenant_id,customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE crm_opportunity_stage_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, opportunity_id BIGINT UNSIGNED NOT NULL,
  from_stage VARCHAR(32) NOT NULL, to_stage VARCHAR(32) NOT NULL, source VARCHAR(32) NOT NULL, source_id VARCHAR(64) NOT NULL,
  reason VARCHAR(500) NOT NULL DEFAULT '', contract_ref VARCHAR(64) NULL, lost_reason VARCHAR(64) NULL,
  pending_type VARCHAR(32) NOT NULL, operator_id VARCHAR(64) NOT NULL, changed_at DATETIME(3) NOT NULL,
  request_id VARCHAR(64) NOT NULL, PRIMARY KEY(id), UNIQUE KEY uk_stage_source(tenant_id,source,source_id),
  KEY idx_stage_history(tenant_id,opportunity_id,changed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- Recovery: terminal transitions are auditable business records; production rollback must preserve both tables and use forward repair.
