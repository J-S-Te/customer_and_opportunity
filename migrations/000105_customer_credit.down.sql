DROP TABLE IF EXISTS crm_customer_credit_applications;
DROP TABLE IF EXISTS crm_customer_credit_logs;
DROP TABLE IF EXISTS crm_customer_credit_payment_records;
DROP TABLE IF EXISTS crm_credit_rule_settings;
ALTER TABLE crm_customers
    DROP INDEX idx_customer_credit_level,
    DROP COLUMN last_payment_eval_at,
    DROP COLUMN consecutive_late_count,
    DROP COLUMN consecutive_ontime_count,
    DROP COLUMN credit_change_source,
    DROP COLUMN credit_updated_at,
    DROP COLUMN credit_level;
