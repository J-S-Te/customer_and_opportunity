-- CRM schema only. Destructive rollback; export job history before use outside disposable environments.
DROP TABLE IF EXISTS crm_presale_engineer_sync_requests;
DROP TABLE IF EXISTS crm_presale_engineer_sync_jobs;
DROP TABLE IF EXISTS crm_presale_engineer_sync_states;
