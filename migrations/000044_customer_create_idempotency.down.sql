-- Controlled empty/test-environment rollback only. Once production writes
-- exist, dropping this replay evidence can duplicate customer numbers, records
-- and audit events on a client retry; retain it and use a forward fix instead.
DROP TABLE IF EXISTS crm_customer_create_idempotency;
