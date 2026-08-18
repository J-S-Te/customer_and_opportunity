-- CRM stores only the contract linkage projection; the contract system remains authoritative.
ALTER TABLE crm_opportunities
  ADD COLUMN contract_id VARCHAR(26) NULL,
  ADD COLUMN contract_intake_id VARCHAR(64) NULL,
  ADD COLUMN contract_link_status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
  ADD COLUMN contract_linked_at DATETIME(3) NULL,
  ADD COLUMN contract_sync_version BIGINT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN contract_link_event_id VARCHAR(128) NULL,
  ADD KEY idx_opportunity_contract_link (tenant_id, contract_id),
  ADD UNIQUE KEY uq_opportunity_contract_link_event (tenant_id, contract_link_event_id);

ALTER TABLE crm_opportunities
  ADD CONSTRAINT chk_opportunity_contract_link_status CHECK (contract_link_status IN ('PENDING','LINK_CONFIRMED','LINK_EXCEPTION'));
