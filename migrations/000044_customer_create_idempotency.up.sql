-- CRM schema only. Apply after CRM migration 000042 (000043 belongs only to
-- the separate customer_portal schema). CM-001 interactive customer creation
-- gets an append-only, actor-bound replay record committed with the customer,
-- generated number and audit event. Sensitive request values are never stored:
-- the application first applies the deployment HMAC, then stores only the final
-- canonical SHA-256 digest and the already-masked public response.
CREATE TABLE crm_customer_create_idempotency (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  actor_id VARCHAR(64) COLLATE utf8mb4_0900_bin NOT NULL,
  idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  request_hash CHAR(64) NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  status VARCHAR(16) COLLATE utf8mb4_0900_bin NOT NULL,
  response_json JSON NOT NULL,
  response_hash CHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_customer_create_idempotency (tenant_id,actor_id,idempotency_key),
  UNIQUE KEY uq_customer_create_resource (tenant_id,customer_id),
  KEY idx_customer_create_actor (tenant_id,actor_id,created_at,id),
  CONSTRAINT chk_customer_create_key CHECK (CHAR_LENGTH(TRIM(idempotency_key)) BETWEEN 1 AND 128),
  CONSTRAINT chk_customer_create_hash CHECK (request_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT chk_customer_create_status CHECK (status = 'COMPLETED'),
  CONSTRAINT chk_customer_create_response_hash CHECK (response_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT fk_customer_create_resource FOREIGN KEY (tenant_id,customer_id)
    REFERENCES crm_customers(tenant_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- No historical rows are synthesized: old requests have no trustworthy
-- Idempotency-Key or canonical body. Deploy this empty table before the
-- application version that starts requiring the header. Creation is low-volume;
-- no existing business table is rebuilt by this migration. Replay evidence is
-- retained until a separately approved archive/retention protocol exists;
-- silently expiring it would re-open duplicate creation after a delayed retry.
