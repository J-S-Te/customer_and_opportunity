-- Empty-environment rollback only. In production, retain import result and
-- audit evidence according to policy and use a forward migration.
DROP TABLE IF EXISTS crm_customer_import_idempotency;
DROP TABLE IF EXISTS crm_customer_import_rows;
DROP TABLE IF EXISTS crm_customer_import_jobs;
