ALTER TABLE crm_opportunities DROP CONSTRAINT chk_opportunity_contract_link_status;
ALTER TABLE crm_opportunities
  DROP INDEX uq_opportunity_contract_link_event,
  DROP INDEX idx_opportunity_contract_link,
  DROP COLUMN contract_link_event_id,
  DROP COLUMN contract_sync_version,
  DROP COLUMN contract_linked_at,
  DROP COLUMN contract_link_status,
  DROP COLUMN contract_intake_id,
  DROP COLUMN contract_id;
