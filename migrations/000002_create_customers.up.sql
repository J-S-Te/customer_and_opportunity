CREATE TABLE crm_customers (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, customer_no VARCHAR(32) NOT NULL,
  name VARCHAR(200) NOT NULL, normalized_name VARCHAR(200) NOT NULL, unified_credit_code_cipher VARBINARY(512) NULL,
  unified_credit_code_hmac CHAR(64) NULL, customer_type VARCHAR(64) NOT NULL, industry VARCHAR(64) NOT NULL,
  region VARCHAR(64) NOT NULL, owner_user_id VARCHAR(64) NOT NULL, owner_org_id VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL, end_date DATE NULL, merged_into_id BIGINT UNSIGNED NULL, created_by VARCHAR(64) NOT NULL,
  updated_by VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1, PRIMARY KEY(id),
  UNIQUE KEY uk_customer_no(tenant_id,customer_no), UNIQUE KEY uk_customer_credit(tenant_id,unified_credit_code_hmac),
  KEY idx_customer_scope(tenant_id,status,owner_user_id,updated_at), KEY idx_customer_industry_region(tenant_id,industry,region),
  KEY idx_customer_name(tenant_id,normalized_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE crm_customer_contacts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(100) NOT NULL, phone_cipher VARBINARY(512) NOT NULL, phone_masked VARCHAR(32) NOT NULL,
  email_cipher VARBINARY(512) NULL, email_masked VARCHAR(200) NOT NULL DEFAULT '', is_registration BOOLEAN NOT NULL,
  sort_order INT NOT NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL, deleted_at DATETIME(3) NULL, version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  PRIMARY KEY(id), KEY idx_contact_customer(tenant_id,customer_id,is_registration)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE crm_customer_followups (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, tenant_id VARCHAR(64) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL,
  type VARCHAR(32) NOT NULL, content TEXT NOT NULL, followed_at DATETIME(3) NOT NULL, followed_by VARCHAR(64) NOT NULL,
  next_follow_at DATETIME(3) NULL, created_by VARCHAR(64) NOT NULL, updated_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL, deleted_at DATETIME(3) NULL,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1, PRIMARY KEY(id), KEY idx_followup(tenant_id,customer_id,followed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- Recovery: do not drop after customer traffic. Disable writes and restore from the pre-migration backup.
