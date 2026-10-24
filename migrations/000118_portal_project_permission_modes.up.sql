-- 持久化门户账号的项目权限模式，避免用“当前是否还有 ACTIVE 绑定”推断。
-- 没有该账号行 = 尚未迁移，保留按客户查看的兼容行为；
-- 写入 ISOLATED 后即使最后一条授权被撤销，也只返回空项目集。
CREATE TABLE portal_account_project_permission_modes (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  account_id VARCHAR(128) NOT NULL,
  mode VARCHAR(16) NOT NULL,
  created_by VARCHAR(128) NOT NULL,
  updated_by VARCHAR(128) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  UNIQUE KEY uq_portal_project_permission_mode (tenant_id, customer_id, account_id),
  KEY idx_portal_project_permission_mode (tenant_id, customer_id, mode)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- 历史已有任何项目绑定（包括已撤销）的账号必须进入隔离模式：
-- 迁移后若最后一条授权恰好被撤销，不能重新获得客户全部项目。
INSERT INTO portal_account_project_permission_modes
  (tenant_id, customer_id, account_id, mode, created_by, updated_by, created_at, updated_at, version)
SELECT tenant_id, customer_id, account_id, 'ISOLATED', 'migration-000118', 'migration-000118', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 1
FROM portal_project_account_bindings
GROUP BY tenant_id, customer_id, account_id
ON DUPLICATE KEY UPDATE mode = 'ISOLATED', updated_by = 'migration-000118', updated_at = UTC_TIMESTAMP(3);
