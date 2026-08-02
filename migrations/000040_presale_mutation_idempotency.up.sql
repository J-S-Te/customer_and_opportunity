-- CRM schema only. Durable actor-bound idempotency for TS approval commands,
-- assignment replacements and cancellations. The table is append-only and
-- stores only canonical request digests; it does not duplicate business data.
ALTER TABLE crm_presale_requests
  ADD UNIQUE KEY uq_presale_request_tenant_id (tenant_id,id);

CREATE TABLE crm_presale_mutation_replays (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  request_id BIGINT UNSIGNED NOT NULL,
  operation VARCHAR(32) NOT NULL,
  action VARCHAR(32) NOT NULL,
  actor_id VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  response_version BIGINT UNSIGNED NOT NULL,
  request_id_trace VARCHAR(64) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uq_presale_mutation_key (tenant_id,idempotency_key),
  KEY idx_presale_mutation_request (tenant_id,request_id,created_at,id),
  KEY idx_presale_mutation_actor (tenant_id,actor_id,created_at,id),
  CONSTRAINT chk_presale_mutation_operation CHECK (operation IN ('APPROVAL_ACTION','REPLACE_ASSIGNMENTS','CANCEL')),
  CONSTRAINT chk_presale_mutation_action CHECK (action IN ('NODE_1_PASS','NODE_1_REJECT','NODE_2_PASS','NODE_2_REJECT','REPLACE','CANCEL')),
  CONSTRAINT chk_presale_mutation_operation_action CHECK (
    (operation = 'APPROVAL_ACTION' AND action IN ('NODE_1_PASS','NODE_1_REJECT','NODE_2_PASS','NODE_2_REJECT')) OR
    (operation = 'REPLACE_ASSIGNMENTS' AND action = 'REPLACE') OR
    (operation = 'CANCEL' AND action = 'CANCEL')
  ),
  CONSTRAINT fk_presale_mutation_request FOREIGN KEY (tenant_id,request_id)
    REFERENCES crm_presale_requests(tenant_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- No historical rows are synthesized: prior approval commands, assignment
-- changes and cancellations do not have trustworthy original request bodies or
-- keys. Deploy the table before the application version that requires keys.
-- The new empty table does not synthesize TS business or audit rows. Adding the
-- composite parent index can take a metadata lock and must use the release
-- platform's approved online-schema-change procedure on a large request table.
