-- Development/test rollback only. Never drop a live audit table in production.
DROP TABLE IF EXISTS crm_customer_change_logs;
