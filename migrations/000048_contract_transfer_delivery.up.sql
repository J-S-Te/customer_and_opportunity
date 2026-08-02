-- Target schema: CRM. Run after 000047_opportunity_external_edges.up.sql.
CREATE TABLE crm_contract_transfer_attempts (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  source_event_id VARCHAR(64) NOT NULL,
  attempt_no TINYINT UNSIGNED NOT NULL,
  result VARCHAR(16) NOT NULL,
  contract_intake_id VARCHAR(64) NOT NULL DEFAULT '',
  response_code VARCHAR(32) NOT NULL DEFAULT '',
  error_summary VARCHAR(500) NOT NULL DEFAULT '',
  attempted_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_contract_transfer_attempt (tenant_id,source_event_id,attempt_no),
  KEY idx_contract_transfer_attempt_time (tenant_id,attempted_at),
  CONSTRAINT chk_contract_transfer_attempt_result CHECK (result IN ('RETRY_WAIT','SENT','DEAD_LETTER'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- Existing OPPORTUNITY_SIGNED rows remain PENDING and are picked up after the
-- worker is enabled. No historical delivery success is synthesized.
