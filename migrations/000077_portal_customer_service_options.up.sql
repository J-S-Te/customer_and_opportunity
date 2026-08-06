-- Target schema: customer_portal. 按客户生效的门户服务项；缺失行视为全部开通，
-- 避免存量客户在升级后能力被意外收窄。权限判定为“角色权限 ∩ 客户服务项”。
CREATE TABLE portal_customer_service_options (
  tenant_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  capabilities_json JSON NOT NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (tenant_id, customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
