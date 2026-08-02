-- Target schema: CRM. Apply after 000032.
-- CM-001 customer profile children. Phone/email plaintext is never stored.
-- The composite parent key makes the child foreign keys enforce tenant/customer
-- consistency in the database, rather than relying only on application filters.
ALTER TABLE crm_customers
  ADD UNIQUE KEY uq_customer_tenant_id (tenant_id,id);

CREATE TABLE crm_customer_stakeholders (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(100) NOT NULL,
  role_title VARCHAR(100) NOT NULL,
  influence VARCHAR(16) NOT NULL,
  relationship_summary VARCHAR(500) NOT NULL DEFAULT '',
  phone_cipher VARBINARY(512) NULL,
  phone_masked VARCHAR(32) NOT NULL DEFAULT '',
  email_cipher VARBINARY(512) NULL,
  email_masked VARCHAR(200) NOT NULL DEFAULT '',
  sort_order INT NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  KEY idx_customer_stakeholder_parent (tenant_id,customer_id,deleted_at,sort_order,id),
  CONSTRAINT chk_customer_stakeholder_influence CHECK (influence IN ('LOW','MEDIUM','HIGH')),
  CONSTRAINT fk_customer_stakeholder_parent FOREIGN KEY (tenant_id,customer_id) REFERENCES crm_customers(tenant_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_customer_systems (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(200) NOT NULL,
  protection_level VARCHAR(16) NOT NULL,
  application_scenario VARCHAR(500) NOT NULL DEFAULT '',
  filing_no VARCHAR(100) NOT NULL DEFAULT '',
  grading_date DATE NULL,
  filing_status VARCHAR(16) NOT NULL,
  sort_order INT NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY (id),
  KEY idx_customer_system_parent (tenant_id,customer_id,deleted_at,sort_order,id),
  CONSTRAINT chk_customer_system_level CHECK (protection_level IN ('LEVEL_1','LEVEL_2','LEVEL_3','LEVEL_4','LEVEL_5')),
  CONSTRAINT chk_customer_system_filing_status CHECK (filing_status IN ('NOT_FILED','FILING','FILED')),
  CONSTRAINT fk_customer_system_parent FOREIGN KEY (tenant_id,customer_id) REFERENCES crm_customers(tenant_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- There is no history backfill: the former values exist only in the non-production
-- HTML prototype and must not be invented in production customer records.
-- The parent index ALTER can acquire a metadata lock. Use the release platform's
-- approved online-schema-change procedure for a large crm_customers table.
