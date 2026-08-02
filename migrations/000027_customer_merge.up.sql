-- Target schema: CRM. Apply after 000026. This migration creates only new
-- append-only merge coordination tables; existing customer rows are untouched.
CREATE TABLE crm_customer_merge_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  source_customer_id BIGINT UNSIGNED NOT NULL,
  target_customer_id BIGINT UNSIGNED NOT NULL,
  source_version BIGINT UNSIGNED NOT NULL,
  target_version BIGINT UNSIGNED NOT NULL,
  migrated_counts_json JSON NOT NULL,
  reason VARCHAR(500) NOT NULL,
  operator_id VARCHAR(64) NOT NULL,
  request_id VARCHAR(64) NOT NULL,
  occurred_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  KEY idx_customer_merge_source (tenant_id,source_customer_id,occurred_at),
  KEY idx_customer_merge_target (tenant_id,target_customer_id,occurred_at),
  KEY idx_customer_merge_request (tenant_id,request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE crm_customer_merge_idempotency (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  actor_id VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  response_json JSON NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_customer_merge_idempotency (tenant_id,actor_id,idempotency_key),
  KEY idx_customer_merge_idempotency_created (tenant_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- A real foreign key is intentionally not added to crm_customers.merged_into_id:
-- this repository's existing schema uses application-level tenant keys and the
-- merge transaction enforces same-tenant scope under row locks.
