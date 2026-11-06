-- Target schema: CRM. Manual win evidence is distinct from a verified contract.
-- Additive migration: existing contractual wins are not backfilled or relabeled.
CREATE TABLE crm_opportunity_win_confirmations (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  opportunity_version BIGINT UNSIGNED NOT NULL,
  mode VARCHAR(32) NOT NULL,
  evidence_type VARCHAR(32) NOT NULL,
  evidence_reference VARCHAR(200) NOT NULL,
  confirmed_on DATE NOT NULL,
  confirmed_amount DECIMAL(18,2) NOT NULL,
  evidence_note VARCHAR(1000) NOT NULL,
  confirmed_by VARCHAR(64) NOT NULL,
  confirmed_at DATETIME(3) NOT NULL,
  request_id VARCHAR(64) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_opportunity_win_confirmation (tenant_id, opportunity_id),
  CONSTRAINT fk_opportunity_win_confirmation FOREIGN KEY (tenant_id, opportunity_id)
    REFERENCES crm_opportunities (tenant_id, id),
  CONSTRAINT chk_opportunity_win_mode CHECK (mode = 'local_evidence'),
  CONSTRAINT chk_opportunity_win_type CHECK (
    evidence_type IN ('PURCHASE_ORDER', 'AWARD_NOTICE', 'CUSTOMER_CONFIRMATION', 'OTHER')
  ),
  CONSTRAINT chk_opportunity_win_amount CHECK (confirmed_amount >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- Recovery is forward-only once evidence exists; do not drop or relabel evidence.
