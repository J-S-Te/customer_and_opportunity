CREATE TABLE crm_opportunity_followups (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  type VARCHAR(32) NOT NULL,
  content TEXT NOT NULL,
  followed_at DATETIME(3) NOT NULL,
  followed_by VARCHAR(64) NOT NULL,
  next_follow_at DATETIME(3) NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  KEY idx_opportunity_followup (tenant_id, opportunity_id, followed_at, id),
  KEY idx_opportunity_next_follow (tenant_id, next_follow_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Rollback is safe only before follow-up traffic. Once used, archive the append-only
-- records and use a forward migration instead of dropping this table.
