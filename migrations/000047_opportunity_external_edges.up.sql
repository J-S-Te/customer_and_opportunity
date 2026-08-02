-- Target schema: CRM. Run after 000045_opportunity_create_idempotency.up.sql.
-- CRM owns only trusted external status snapshots; quotation/bid documents and
-- their contents remain in the originating system.
CREATE TABLE crm_opportunity_external_links (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  opportunity_id BIGINT UNSIGNED NOT NULL,
  type VARCHAR(16) NOT NULL,
  source_id VARCHAR(64) NOT NULL,
  status VARCHAR(32) NOT NULL,
  amount DECIMAL(18,2) NULL,
  changed_at DATETIME(3) NOT NULL,
  snapshot_json JSON NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_opportunity_external_status (tenant_id,opportunity_id,source_id,status),
  KEY idx_opportunity_external_latest (tenant_id,opportunity_id,changed_at,id),
  CONSTRAINT fk_opportunity_external_parent FOREIGN KEY (tenant_id,opportunity_id)
    REFERENCES crm_opportunities (tenant_id,id),
  CONSTRAINT chk_opportunity_external_type CHECK (type IN ('报价','投标')),
  CONSTRAINT chk_opportunity_external_amount CHECK (amount IS NULL OR amount >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- No existing stage log is converted into a snapshot because old callbacks do
-- not retain the complete authoritative type/amount payload. A new trusted
-- callback establishes the first readable snapshot without inventing history.
