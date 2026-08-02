-- Stop every presale-worker and CRM write instance before rollback.
DROP TABLE IF EXISTS crm_worker_heartbeats;
