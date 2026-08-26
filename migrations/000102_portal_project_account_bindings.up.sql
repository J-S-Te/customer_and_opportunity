-- Portal 账号到项目的显式授权关系。SYNC 与 MANUAL 来源独立维护，避免同步覆盖运营人员的手工授权。
CREATE TABLE portal_project_account_bindings (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  project_id VARCHAR(64) NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  source VARCHAR(16) NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
  source_version VARCHAR(64) NOT NULL DEFAULT '',
  created_by VARCHAR(128) NOT NULL,
  updated_by VARCHAR(128) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project_account_binding
    (tenant_id, customer_id, project_id, account_id, source),
  KEY idx_portal_project_account_active
    (tenant_id, customer_id, account_id, status, deleted_at, project_id),
  KEY idx_portal_project_account_project
    (tenant_id, customer_id, project_id, source, status, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
