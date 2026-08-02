-- CRM schema only. Apply after CRM migration 000044. Durable actor-bound
-- BM-001 creation replays. No request
-- plaintext is stored. The response snapshot is the same public DTO
-- returned by BM-001 and makes an exact retry independent of later edits.
ALTER TABLE crm_opportunities
  ADD UNIQUE KEY uq_opportunity_tenant_id (tenant_id,id);

CREATE TABLE crm_opportunity_create_idempotency (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  actor_id VARCHAR(64) COLLATE utf8mb4_0900_bin NOT NULL,
  idempotency_key VARCHAR(128) COLLATE utf8mb4_0900_bin NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  request_hash CHAR(64) NOT NULL,
  response_hash CHAR(64) NOT NULL,
  response_json JSON NOT NULL,
  request_id_trace VARCHAR(64) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_opportunity_create_idem (tenant_id,actor_id,idempotency_key),
  UNIQUE KEY uq_opportunity_create_resource (tenant_id,opportunity_id),
  KEY idx_opportunity_create_customer (tenant_id,customer_id,created_at,id),
  CONSTRAINT chk_opportunity_create_key CHECK (CHAR_LENGTH(TRIM(idempotency_key)) BETWEEN 1 AND 128),
  CONSTRAINT chk_opportunity_create_hashes CHECK (request_hash REGEXP '^[0-9a-f]{64}$' AND response_hash REGEXP '^[0-9a-f]{64}$'),
  CONSTRAINT fk_opportunity_create_customer FOREIGN KEY (tenant_id,customer_id)
    REFERENCES crm_customers(tenant_id,id),
  CONSTRAINT fk_opportunity_create_resource FOREIGN KEY (tenant_id,opportunity_id)
    REFERENCES crm_opportunities(tenant_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Existing creations cannot be reconstructed safely because their original
-- Idempotency-Key and exact canonical body are unavailable. Deploy this empty
-- table before the application version that requires the header. Adding the
-- parent composite unique index can take a metadata lock; use the approved
-- online-schema-change procedure for a large opportunity table.
