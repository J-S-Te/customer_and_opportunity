-- Controlled empty/test-environment rollback only. Once writes exist, dropping
-- replay coordinates re-opens duplicate numbering/audit risk; prefer forward repair.
DROP TABLE IF EXISTS crm_opportunity_create_idempotency;

ALTER TABLE crm_opportunities
  DROP INDEX uq_opportunity_tenant_id;
