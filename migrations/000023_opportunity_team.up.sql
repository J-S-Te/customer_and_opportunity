-- Target schema: CRM. Run after 000021_presale_reports.up.sql.
CREATE TABLE crm_opportunity_members (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  opportunity_id BIGINT UNSIGNED NOT NULL, user_id VARCHAR(64) NOT NULL,
  role VARCHAR(32) NOT NULL, is_active BOOLEAN NOT NULL DEFAULT TRUE, ended_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_opportunity_member (tenant_id,opportunity_id,user_id),
  KEY idx_opportunity_member_current (tenant_id,opportunity_id,is_active,role),
  KEY idx_opportunity_member_user (tenant_id,user_id,is_active),
  CONSTRAINT chk_opportunity_member_role CHECK (role IN ('SALES_SUPPORT','TECHNICAL_SUPPORT','BUSINESS_SUPPORT','OTHER'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_opportunity_change_idempotency (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL, opportunity_id BIGINT UNSIGNED NOT NULL,
  operation VARCHAR(32) NOT NULL, actor_id VARCHAR(64) NOT NULL, idempotency_key VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL, response_json JSON NOT NULL, created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_opportunity_change_idem (tenant_id,opportunity_id,operation,actor_id,idempotency_key),
  KEY idx_opportunity_change_idem_created (tenant_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Existing opportunities intentionally start with an empty auxiliary team;
-- owner_user_id remains the single authoritative owner. There is no synthetic
-- backfill because an owner is not implicitly a team member.
-- Large-table risk: a new empty table is created, so no existing opportunity
-- rows are locked or rewritten. The application must be deployed only after
-- this compatible migration succeeds.
