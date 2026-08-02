-- Controlled empty/test-environment rollback only. Dropping this append-only
-- coordination record permits duplicate manual commands after a retry. Prefer
-- a forward fix once production writes exist.
DROP TABLE IF EXISTS crm_presale_mutation_replays;

ALTER TABLE crm_presale_requests
  DROP INDEX uq_presale_request_tenant_id;
