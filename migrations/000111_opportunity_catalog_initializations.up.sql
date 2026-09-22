CREATE TABLE crm_opportunity_catalog_initializations (
  tenant_id VARCHAR(64) NOT NULL,
  initialized_at DATETIME(3) NOT NULL,
  PRIMARY KEY (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT IGNORE INTO crm_opportunity_catalog_initializations (tenant_id, initialized_at)
SELECT tenant_id, UTC_TIMESTAMP(3)
FROM (
  SELECT tenant_id FROM crm_opportunity_catalog_items
  UNION SELECT tenant_id FROM crm_customers
  UNION SELECT tenant_id FROM crm_opportunities
  UNION SELECT tenant_id FROM crm_oidc_sessions
) AS existing_tenants;
