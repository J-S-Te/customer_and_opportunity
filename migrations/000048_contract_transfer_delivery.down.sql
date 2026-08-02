-- Dropping this table removes local delivery diagnostics only. It deliberately
-- does not delete OPPORTUNITY_SIGNED outbox rows or downstream inbox records.
DROP TABLE IF EXISTS crm_contract_transfer_attempts;
